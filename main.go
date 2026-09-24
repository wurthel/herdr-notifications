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

	"github.com/vusalsalmanov/herdr-notifications/internal/config"
	"github.com/vusalsalmanov/herdr-notifications/internal/event"
	"github.com/vusalsalmanov/herdr-notifications/internal/herdr"
	"github.com/vusalsalmanov/herdr-notifications/internal/notify"
	"github.com/vusalsalmanov/herdr-notifications/internal/state"
	"github.com/vusalsalmanov/herdr-notifications/internal/telegram"
)

const usage = "usage: herdr-notifications [notify|test|toggle]"

type app struct {
	getenv func(string) string
	now    func() time.Time
	stdout io.Writer
	stderr io.Writer
	http   *http.Client
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

	var (
		notifyAs   string
		prevStatus string
		now        = a.now()
	)
	rules := notify.Rules{NotifyOn: cfg.NotifyOn, Debounce: cfg.Debounce, IdleAfterWorking: cfg.IdleAfterWorking}
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

	msg := notify.Message{
		Status:    notifyAs,
		Agent:     info.Agent,
		Title:     info.Title,
		Workspace: info.WorkspaceLabel,
		Tab:       info.TabLabel,
	}
	if cfg.PaneTailLines > 0 {
		cli := herdr.CLI{Bin: cfg.HerdrBin}
		tail, err := cli.ReadTail(ctx, info.PaneID, cfg.PaneTailLines)
		if err != nil {
			fmt.Fprintln(a.stderr, "herdr-notifications: read pane tail:", err)
			tail = ""
		}
		msg.Tail = tail
	}
	sendErr := a.telegram(cfg).SendHTML(ctx, cfg.ChatID, msg.HTML(), msg.Plain())
	if sendErr != nil {
		if err := rollback(store, info.PaneID, info.Status, notifyAs, prevStatus, now); err != nil {
			fmt.Fprintln(a.stderr, "herdr-notifications: rollback state:", err)
		}
	}
	return sendErr
}

// rollback undoes a notification that failed to send so the next event with
// the same status retries it, unless another event has moved the pane on.
func rollback(store state.Store, paneID, status, notifiedAs, prevStatus string, notifiedAt time.Time) error {
	return store.Update(paneID, func(cur state.PaneState) state.PaneState {
		if cur.Status != status || !cur.LastNotified[notifiedAs].Equal(notifiedAt) {
			return cur
		}
		last := make(map[string]time.Time, len(cur.LastNotified))
		for k, v := range cur.LastNotified {
			if k != notifiedAs {
				last[k] = v
			}
		}
		if len(last) == 0 {
			last = nil
		}
		return state.PaneState{Status: prevStatus, LastNotified: last}
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
