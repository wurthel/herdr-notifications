// Command herdr-notifications is a herdr plugin that sends Telegram messages
// when an agent finishes or needs input.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wurthel/herdr-notifications/internal/config"
	"github.com/wurthel/herdr-notifications/internal/event"
	"github.com/wurthel/herdr-notifications/internal/herdr"
	"github.com/wurthel/herdr-notifications/internal/notify"
	"github.com/wurthel/herdr-notifications/internal/state"
	"github.com/wurthel/herdr-notifications/internal/telegram"
	"github.com/wurthel/herdr-notifications/internal/transcript"
)

const usage = "usage: herdr-notifications [notify|test|toggle]"

type app struct {
	getenv func(string) string
	now    func() time.Time
	stdout io.Writer
	stderr io.Writer
	http   *http.Client
	// sleepFn replaces time.Sleep in tests.
	sleepFn func(time.Duration)
}

func main() {
	a := app{getenv: os.Getenv, now: time.Now, stdout: os.Stdout, stderr: os.Stderr}
	os.Exit(a.run(os.Args[1:]))
}

func (a app) run(args []string) int {
	cmd := "notify"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "notify":
		// Event hooks always exit 0; failures go to stderr, which herdr keeps in `herdr plugin log list`.
		if err := a.notify(context.Background()); err != nil {
			fmt.Fprintln(a.stderr, "herdr-notifications:", err)
		}
		return 0
	case "test":
		if err := a.test(context.Background()); err != nil {
			fmt.Fprintln(a.stderr, "herdr-notifications:", err)
			return 1
		}
		return 0
	case "toggle":
		if err := a.toggle(context.Background()); err != nil {
			fmt.Fprintln(a.stderr, "herdr-notifications:", err)
			return 1
		}
		return 0
	case "-h", "--help", "help":
		fmt.Fprintln(a.stdout, usage)
		return 0
	default:
		fmt.Fprintln(a.stderr, usage)
		return 2
	}
}

func (a app) notify(ctx context.Context) error {
	cfg, err := config.Load(a.getenv)
	if err != nil {
		return err
	}
	a.warn(cfg)
	eventJSON := a.getenv("HERDR_PLUGIN_EVENT_JSON")
	contextJSON := a.getenv("HERDR_PLUGIN_CONTEXT_JSON")
	if cfg.Debug {
		if err := dumpDebug(cfg.StateDir, eventJSON, contextJSON); err != nil {
			fmt.Fprintln(a.stderr, "herdr-notifications: debug dump:", err)
		}
	}

	info, err := event.Parse(eventJSON, contextJSON, a.getenv("HERDR_PANE_ID"))
	if err != nil {
		return err
	}
	if info.PaneID == "" || info.Status == "" {
		return nil
	}

	store := state.Store{Dir: cfg.StateDir}
	if store.Disabled() {
		return nil
	}
	// Checked before touching state so a fixed config still notifies on the next transition.
	if err := cfg.ValidateTelegram(); err != nil {
		return err
	}

	cli := herdr.CLI{Bin: cfg.HerdrBin}
	rules := notify.Rules{NotifyOn: cfg.NotifyOn, Debounce: cfg.Debounce, IdleAfterWorking: cfg.IdleAfterWorking}
	var (
		pane     herdr.Pane
		paneErr  error
		havePane bool
	)
	// herdr can report idle for a moment when a tool call starts. Waiting
	// outside the state lock lets the following "working" event land first,
	// and the re-read status then shows the idle was not a real finish.
	if cfg.Settle > 0 && mayFinish(info.Status, rules) {
		a.sleep(cfg.Settle)
		pane, paneErr = cli.PaneInfo(ctx, info.PaneID)
		havePane = true
		if paneErr == nil && busy(pane.AgentStatus) {
			return nil
		}
	}

	var (
		notifyAs   string
		prevStatus string
		now        = a.now()
	)
	err = store.Update(info.PaneID, func(prev state.PaneState) state.PaneState {
		next, as := notify.Decide(prev, info.Status, now, rules)
		notifyAs, prevStatus = as, prev.Status
		return next
	})
	if err != nil {
		return fmt.Errorf("update state: %w", err)
	}
	if notifyAs == "" {
		return nil
	}

	if !havePane {
		pane, paneErr = cli.PaneInfo(ctx, info.PaneID)
	}
	if paneErr != nil {
		fmt.Fprintln(a.stderr, "herdr-notifications: pane info:", paneErr)
	}
	msg := notify.Message{
		Status:    notifyAs,
		Agent:     info.Agent,
		Title:     info.Title,
		Workspace: info.WorkspaceLabel,
		Tab:       info.TabLabel,
	}
	a.addContext(ctx, cfg, cli, info.PaneID, pane, &msg)

	c := claim{paneID: info.PaneID, status: info.Status, as: notifyAs, prevStatus: prevStatus, at: now}
	if notifyAs == notify.StatusDone {
		dup, err := c.recordTurn(store, msg.Fingerprint())
		if err != nil {
			fmt.Fprintln(a.stderr, "herdr-notifications: record turn:", err)
		}
		if dup {
			if err := c.undo(store, false); err != nil {
				fmt.Fprintln(a.stderr, "herdr-notifications: undo duplicate:", err)
			}
			return nil
		}
	}
	sendErr := a.telegram(cfg).SendHTML(ctx, cfg.ChatID, msg.HTML(), msg.Plain())
	if sendErr != nil {
		if err := c.undo(store, true); err != nil {
			fmt.Fprintln(a.stderr, "herdr-notifications: rollback state:", err)
		}
	}
	return sendErr
}

// mayFinish reports whether an event with status can be reported as done.
func mayFinish(status string, r notify.Rules) bool {
	if !r.NotifyOn[notify.StatusDone] {
		return false
	}
	return status == notify.StatusDone || (status == notify.StatusIdle && r.IdleAfterWorking)
}

func busy(status string) bool {
	switch strings.ToLower(status) {
	case notify.StatusWorking, "blocked":
		return true
	}
	return false
}

// cwdLogMaxAge is how recently a log found by working directory must have
// been written to count as the pane's current session.
const cwdLogMaxAge = 10 * time.Minute

// addContext fills msg with the last prompt and response from the agent's
// transcript, or with the pane's recent output when no transcript is found.
func (a app) addContext(ctx context.Context, cfg config.Config, cli herdr.CLI, paneID string, pane herdr.Pane, msg *notify.Message) {
	agent := pane.AgentSession.Agent
	if agent == "" {
		agent = pane.Agent
	}
	dirs := a.dirs()
	path := transcript.Locate(agent, pane.AgentSession.Value, dirs)
	if path == "" {
		path = transcript.LocateByCwd(agent, pane.Cwd, a.now().Add(-cwdLogMaxAge), dirs)
	}
	if path = transcript.Resolve(agent, path, dirs); path != "" {
		turn, err := a.readTurn(agent, path, msg.Status)
		if err != nil {
			fmt.Fprintln(a.stderr, "herdr-notifications: read transcript:", err)
		}
		msg.Prompt, msg.Output, msg.Pending = turn.Prompt, turn.Output, turn.Pending
		if msg.HasTurn() {
			return
		}
	}
	if cfg.PaneTailLines > 0 {
		tail, err := cli.ReadTail(ctx, paneID, cfg.PaneTailLines)
		if err != nil {
			fmt.Fprintln(a.stderr, "herdr-notifications: read pane tail:", err)
			tail = ""
		}
		msg.Tail = tail
	}
}

// readTurn retries briefly when a finished turn has no response yet, since
// herdr can report done a moment before the agent flushes its transcript.
func (a app) readTurn(agent, path, status string) (transcript.Turn, error) {
	for attempt := 0; ; attempt++ {
		turn, err := transcript.Read(agent, path)
		if err != nil || status != "done" || turn.Output != "" || attempt == 3 {
			return turn, err
		}
		a.sleep(250 * time.Millisecond)
	}
}

func (a app) dirs() transcript.Dirs {
	home := a.getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return transcript.DefaultDirs(a.getenv, home)
}

func (a app) sleep(d time.Duration) {
	if a.sleepFn != nil {
		a.sleepFn(d)
		return
	}
	time.Sleep(d)
}

// claim is a notification recorded in the pane state before it is sent.
type claim struct {
	paneID, status, as, prevStatus string
	at                             time.Time
	// turn is the fingerprint recorded for this notification, prevTurn the
	// one it replaced.
	turn, prevTurn string
}

// recordTurn stores the fingerprint of the turn about to be sent and reports
// whether it is the one already sent last time.
func (c *claim) recordTurn(store state.Store, fp string) (dup bool, err error) {
	if fp == "" {
		return false, nil
	}
	err = store.Update(c.paneID, func(cur state.PaneState) state.PaneState {
		if cur.LastTurn[c.as] == fp {
			dup = true
			return cur
		}
		c.turn, c.prevTurn = fp, cur.LastTurn[c.as]
		if cur.LastTurn == nil {
			cur.LastTurn = map[string]string{}
		}
		cur.LastTurn[c.as] = fp
		return cur
	})
	return dup, err
}

// undo forgets a notification that was not delivered, unless a newer one has
// replaced it. With restoreStatus it also rewinds the pane's status, if no
// other event has moved it on, so the next event with that status retries.
func (c claim) undo(store state.Store, restoreStatus bool) error {
	return store.Update(c.paneID, func(cur state.PaneState) state.PaneState {
		if !cur.LastNotified[c.as].Equal(c.at) {
			return cur
		}
		delete(cur.LastNotified, c.as)
		if len(cur.LastNotified) == 0 {
			cur.LastNotified = nil
		}
		if c.turn != "" && cur.LastTurn[c.as] == c.turn {
			if c.prevTurn == "" {
				delete(cur.LastTurn, c.as)
			} else {
				cur.LastTurn[c.as] = c.prevTurn
			}
			if len(cur.LastTurn) == 0 {
				cur.LastTurn = nil
			}
		}
		if restoreStatus && cur.Status == c.status {
			cur.Status = c.prevStatus
		}
		return cur
	})
}

func (a app) test(ctx context.Context) error {
	cfg, err := config.Load(a.getenv)
	if err != nil {
		return err
	}
	a.warn(cfg)
	if err := cfg.ValidateTelegram(); err != nil {
		return err
	}
	statuses := make([]string, 0, len(cfg.NotifyOn))
	for _, s := range []string{"done", "blocked", "idle", "working"} {
		if cfg.NotifyOn[s] {
			statuses = append(statuses, s)
		}
	}
	muted := ""
	if (state.Store{Dir: cfg.StateDir}).Disabled() {
		muted = "\n⚠️ Notifications are currently muted (run the toggle action)."
	}
	text := fmt.Sprintf("🔔 herdr-notifications test message\nNotify on: %s\nPane tail lines: %d%s",
		strings.Join(statuses, ", "), cfg.PaneTailLines, muted)
	if err := a.telegram(cfg).SendHTML(ctx, cfg.ChatID, text, text); err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, "test message sent")
	return nil
}

func (a app) toggle(ctx context.Context) error {
	cfg, err := config.Load(a.getenv)
	if err != nil {
		return err
	}
	store := state.Store{Dir: cfg.StateDir}
	disabled := !store.Disabled()
	if err := store.SetDisabled(disabled); err != nil {
		return err
	}
	msg := "Telegram notifications enabled"
	if disabled {
		msg = "Telegram notifications muted"
	}
	fmt.Fprintln(a.stdout, msg)
	if err := (herdr.CLI{Bin: cfg.HerdrBin}).Toast(ctx, "herdr-notifications", msg); err != nil {
		fmt.Fprintln(a.stderr, "herdr-notifications: toast:", err)
	}
	return nil
}

func (a app) warn(cfg config.Config) {
	for _, w := range cfg.Warnings {
		fmt.Fprintln(a.stderr, "herdr-notifications: warning:", w)
	}
}

func (a app) telegram(cfg config.Config) *telegram.Client {
	c := telegram.New(cfg.APIBase, cfg.BotToken)
	if a.http != nil {
		c.HTTP = a.http
	}
	return c
}

func dumpDebug(stateDir, eventJSON, contextJSON string) error {
	dir := filepath.Join(stateDir, "debug")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	out, err := json.MarshalIndent(map[string]json.RawMessage{
		"event":   rawOrNull(eventJSON),
		"context": rawOrNull(contextJSON),
	}, "", "  ")
	if err != nil {
		return err
	}
	return state.WriteFileAtomic(filepath.Join(dir, "last-event.json"), out, 0o600)
}

func rawOrNull(s string) json.RawMessage {
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	b, _ := json.Marshal(s)
	return b
}
