package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vusalsalmanov/herdr-notifications/internal/state"
)

type sentMessage struct {
	Path      string `json:"-"`
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

type fakeTelegram struct {
	mu   sync.Mutex
	sent []sentMessage
	fail int
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var m sentMessage
	_ = json.Unmarshal(raw, &m)
	m.Path = r.URL.Path
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail > 0 {
		f.fail--
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":500,"description":"Internal Server Error"}`)
		return
	}
	f.sent = append(f.sent, m)
	_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
}

func (f *fakeTelegram) messages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

type harness struct {
	t        *testing.T
	env      map[string]string
	tg       *fakeTelegram
	argsFile string
	clock    time.Time
	stdout   bytes.Buffer
	stderr   bytes.Buffer
}

const fakeHerdrScript = `#!/bin/sh
printf '%%s\n' "$*" >> '%s'
case "$1" in
pane) printf '\033[32mcompiling\033[0m\nall <tests> & checks passed\r\n\n' ;;
esac
exit %d
`

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{
		t:        t,
		tg:       &fakeTelegram{},
		argsFile: filepath.Join(dir, "herdr-args.log"),
		clock:    time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	srv := httptest.NewServer(h.tg)
	t.Cleanup(srv.Close)

	h.env = map[string]string{
		"HERDR_PLUGIN_CONFIG_DIR":   filepath.Join(dir, "config"),
		"HERDR_PLUGIN_STATE_DIR":    filepath.Join(dir, "state"),
		"HERDR_BIN_PATH":            h.writeHerdr(dir, 0),
		"TELEGRAM_BOT_TOKEN":        "123:tok",
		"TELEGRAM_CHAT_ID":          "777",
		"TELEGRAM_API_BASE":         srv.URL,
		"HERDR_PLUGIN_CONTEXT_JSON": `{"workspace_label":"api","tab_label":"main","focused_pane_id":"w1:p1"}`,
	}
	if err := os.MkdirAll(h.env["HERDR_PLUGIN_CONFIG_DIR"], 0o700); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) writeHerdr(dir string, exitCode int) string {
	h.t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("herdr-%d", exitCode))
	script := fmt.Sprintf(fakeHerdrScript, h.argsFile, exitCode)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		h.t.Fatal(err)
	}
	return path
}

func (h *harness) run(args ...string) int {
	h.t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	a := app{
		getenv: func(k string) string { return h.env[k] },
		now:    func() time.Time { return h.clock },
		stdout: &h.stdout,
		stderr: &h.stderr,
	}
	return a.run(args)
}

func (h *harness) notify(status string) {
	h.t.Helper()
	h.env["HERDR_PLUGIN_EVENT_JSON"] = fmt.Sprintf(`{"event":"pane_agent_status_changed","data":{"type":"pane_agent_status_changed",`+
		`"pane_id":"w1:p1","workspace_id":"w1","agent_status":%q,"agent":"claude","display_agent":"Claude Code","title":"fix bug"}}`, status)
	if code := h.run("notify"); code != 0 {
		h.t.Fatalf("notify exit code = %d, want 0", code)
	}
}

func (h *harness) herdrCalls() []string {
	h.t.Helper()
	raw, err := os.ReadFile(h.argsFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

func TestNotifyDoneSendsMessage(t *testing.T) {
	h := newHarness(t)
	h.notify("working")
	if n := len(h.tg.messages()); n != 0 {
		t.Fatalf("working sent %d messages", n)
	}
	h.notify("done")
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q", h.stderr.String())
	}
	msgs := h.tg.messages()
	if len(msgs) != 1 {
		t.Fatalf("sent %d messages, want 1", len(msgs))
	}
	m := msgs[0]
	if m.Path != "/bot123:tok/sendMessage" || m.ChatID != "777" || m.ParseMode != "HTML" {
		t.Errorf("request = %+v", m)
	}
	for _, want := range []string{
		"✅ <b>Claude Code</b> finished",
		"📁 api › main",
		"<i>fix bug</i>",
		"<pre>compiling\nall &lt;tests&gt; &amp; checks passed</pre>",
	} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("message missing %q:\n%s", want, m.Text)
		}
	}
	calls := h.herdrCalls()
	if want := []string{"pane read w1:p1 --source recent-unwrapped --lines 15"}; strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Errorf("herdr calls = %q, want %q", calls, want)
	}
}

func TestNotifySequence(t *testing.T) {
	tests := []struct {
		name     string
		steps    []string
		advance  time.Duration
		wantSent int
	}{
		{"repeated done", []string{"working", "done", "done", "done"}, time.Second, 1},
		{"done then idle", []string{"working", "done", "idle"}, time.Second, 1},
		{"done idle done within debounce", []string{"done", "idle", "done"}, time.Second, 1},
		{"done idle done after debounce", []string{"done", "idle", "done"}, 11 * time.Second, 2},
		{"blocked then done", []string{"blocked", "working", "done"}, time.Second, 2},
		{"unknown and empty ignored", []string{"done", "unknown", "", "done"}, time.Minute, 1},
		{"working to idle counts as done", []string{"idle", "working", "idle"}, time.Minute, 1},
		{"idle to working only", []string{"idle", "working"}, time.Minute, 0},
		{"blocked to idle silent", []string{"working", "blocked", "idle"}, time.Minute, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.env["PANE_TAIL_LINES"] = "0"
			for _, s := range tt.steps {
				h.notify(s)
				h.clock = h.clock.Add(tt.advance)
			}
			if n := len(h.tg.messages()); n != tt.wantSent {
				t.Errorf("sent %d messages, want %d", n, tt.wantSent)
			}
		})
	}
}

func TestNotifySendFailureIsRetriedOnNextEvent(t *testing.T) {
	h := newHarness(t)
	h.env["PANE_TAIL_LINES"] = "0"
	h.tg.fail = 1
	h.notify("working")
	h.notify("done")
	if n := len(h.tg.messages()); n != 0 {
		t.Fatalf("sent %d messages, want 0 after failure", n)
	}
	if !strings.Contains(h.stderr.String(), "Internal Server Error") {
		t.Errorf("stderr = %q, want send error", h.stderr.String())
	}
	h.clock = h.clock.Add(time.Second)
	h.notify("done")
	if n := len(h.tg.messages()); n != 1 {
		t.Errorf("sent %d messages, want 1 on retry", n)
	}
	h.notify("done")
	if n := len(h.tg.messages()); n != 1 {
		t.Errorf("sent %d messages, want no duplicate after successful retry", n)
	}
}

func TestNotifyIdleAfterWorking(t *testing.T) {
	h := newHarness(t)
	h.env["PANE_TAIL_LINES"] = "0"
	h.notify("working")
	h.notify("idle")
	msgs := h.tg.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0].Text, "finished") {
		t.Fatalf("messages = %+v, want one \"finished\" message", msgs)
	}

	h = newHarness(t)
	h.env["PANE_TAIL_LINES"] = "0"
	h.env["NOTIFY_IDLE_AFTER_WORKING"] = "0"
	h.notify("working")
	h.notify("idle")
	if n := len(h.tg.messages()); n != 0 {
		t.Errorf("sent %d messages with rule disabled, want 0", n)
	}
}

func TestNotifyDisabled(t *testing.T) {
	h := newHarness(t)
	if err := (state.Store{Dir: h.env["HERDR_PLUGIN_STATE_DIR"]}).SetDisabled(true); err != nil {
		t.Fatal(err)
	}
	h.notify("working")
	h.notify("done")
	if n := len(h.tg.messages()); n != 0 {
		t.Errorf("sent %d messages while disabled", n)
	}
	if calls := h.herdrCalls(); len(calls) != 0 {
		t.Errorf("herdr called while disabled: %q", calls)
	}
}

func TestNotifyMissingToken(t *testing.T) {
	h := newHarness(t)
	delete(h.env, "TELEGRAM_BOT_TOKEN")
	h.notify("done")
	want := "missing TELEGRAM_BOT_TOKEN (set in " + filepath.Join(h.env["HERDR_PLUGIN_CONFIG_DIR"], ".env") + ")"
	if !strings.Contains(h.stderr.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", h.stderr.String(), want)
	}
	if n := len(h.tg.messages()); n != 0 {
		t.Errorf("sent %d messages", n)
	}
}

func TestNotifyConfigFromEnvFile(t *testing.T) {
	h := newHarness(t)
	delete(h.env, "TELEGRAM_BOT_TOKEN")
	delete(h.env, "TELEGRAM_CHAT_ID")
	envFile := "TELEGRAM_BOT_TOKEN=999:file\nTELEGRAM_CHAT_ID='555'\nNOTIFY_ON=idle\nPANE_TAIL_LINES=0\n"
	if err := os.WriteFile(filepath.Join(h.env["HERDR_PLUGIN_CONFIG_DIR"], ".env"), []byte(envFile), 0o600); err != nil {
		t.Fatal(err)
	}
	h.notify("done")
	h.notify("idle")
	msgs := h.tg.messages()
	if len(msgs) != 1 {
		t.Fatalf("sent %d messages, want 1", len(msgs))
	}
	if msgs[0].Path != "/bot999:file/sendMessage" || msgs[0].ChatID != "555" {
		t.Errorf("request = %+v", msgs[0])
	}
	if strings.Contains(msgs[0].Text, "<pre>") {
		t.Errorf("PANE_TAIL_LINES=0 still included a tail: %q", msgs[0].Text)
	}
	if calls := h.herdrCalls(); len(calls) != 0 {
		t.Errorf("herdr called with PANE_TAIL_LINES=0: %q", calls)
	}
}

func TestNotifyTailReadFailure(t *testing.T) {
	h := newHarness(t)
	h.env["HERDR_BIN_PATH"] = h.writeHerdr(t.TempDir(), 1)
	h.notify("done")
	if !strings.Contains(h.stderr.String(), "read pane tail") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
	msgs := h.tg.messages()
	if len(msgs) != 1 {
		t.Fatalf("sent %d messages, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Text, "Claude Code") {
		t.Errorf("message = %q", msgs[0].Text)
	}
}

func TestNotifyInvalidEventJSON(t *testing.T) {
	h := newHarness(t)
	h.env["HERDR_PLUGIN_EVENT_JSON"] = "{broken"
	if code := h.run("notify"); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(h.stderr.String(), "parse event json") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

func TestNotifyDebugDump(t *testing.T) {
	tests := []struct {
		name  string
		event string
		check func(t *testing.T, got map[string]any)
	}{
		{
			name:  "valid json",
			event: `{"event":"pane_agent_status_changed","data":{"pane_id":"w1:p1","agent_status":"idle"}}`,
			check: func(t *testing.T, got map[string]any) {
				ev, ok := got["event"].(map[string]any)
				if !ok || ev["event"] != "pane_agent_status_changed" {
					t.Errorf("event = %v", got["event"])
				}
			},
		},
		{
			name:  "invalid json stored as string",
			event: "{broken",
			check: func(t *testing.T, got map[string]any) {
				if got["event"] != "{broken" {
					t.Errorf("event = %v", got["event"])
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.env["DEBUG"] = "1"
			h.env["HERDR_PLUGIN_EVENT_JSON"] = tt.event
			if code := h.run("notify"); code != 0 {
				t.Fatalf("exit code = %d", code)
			}
			raw, err := os.ReadFile(filepath.Join(h.env["HERDR_PLUGIN_STATE_DIR"], "debug", "last-event.json"))
			if err != nil {
				t.Fatalf("debug dump: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("debug dump is not json: %v\n%s", err, raw)
			}
			ctx, ok := got["context"].(map[string]any)
			if !ok || ctx["workspace_label"] != "api" {
				t.Errorf("context = %v", got["context"])
			}
			tt.check(t, got)
		})
	}
}

func TestToggle(t *testing.T) {
	h := newHarness(t)
	store := state.Store{Dir: h.env["HERDR_PLUGIN_STATE_DIR"]}
	for i, want := range []struct {
		disabled bool
		out      string
	}{
		{true, "Telegram notifications muted"},
		{false, "Telegram notifications enabled"},
		{true, "Telegram notifications muted"},
	} {
		if code := h.run("toggle"); code != 0 {
			t.Fatalf("toggle %d exit code = %d, stderr %q", i, code, h.stderr.String())
		}
		if store.Disabled() != want.disabled {
			t.Errorf("toggle %d: Disabled = %v, want %v", i, store.Disabled(), want.disabled)
		}
		if strings.TrimSpace(h.stdout.String()) != want.out {
			t.Errorf("toggle %d: stdout = %q, want %q", i, h.stdout.String(), want.out)
		}
		if h.stderr.Len() != 0 {
			t.Errorf("toggle %d: stderr = %q", i, h.stderr.String())
		}
	}
	calls := h.herdrCalls()
	if len(calls) != 3 || calls[0] != "notification show herdr-notifications --body Telegram notifications muted" {
		t.Errorf("herdr calls = %q", calls)
	}
}

func TestToggleToastFailureStillSucceeds(t *testing.T) {
	h := newHarness(t)
	h.env["HERDR_BIN_PATH"] = h.writeHerdr(t.TempDir(), 3)
	if code := h.run("toggle"); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(h.stderr.String(), "toast") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
	if !(state.Store{Dir: h.env["HERDR_PLUGIN_STATE_DIR"]}).Disabled() {
		t.Error("toggle did not mute")
	}
}

func TestTestAction(t *testing.T) {
	t.Run("sends", func(t *testing.T) {
		h := newHarness(t)
		h.env["NOTIFY_ON"] = "blocked,done"
		if code := h.run("test"); code != 0 {
			t.Fatalf("exit code = %d, stderr %q", code, h.stderr.String())
		}
		msgs := h.tg.messages()
		if len(msgs) != 1 {
			t.Fatalf("sent %d messages, want 1", len(msgs))
		}
		for _, want := range []string{"test message", "Notify on: done, blocked", "Pane tail lines: 15"} {
			if !strings.Contains(msgs[0].Text, want) {
				t.Errorf("message missing %q: %q", want, msgs[0].Text)
			}
		}
		if strings.Contains(msgs[0].Text, "muted") {
			t.Errorf("unexpected muted warning: %q", msgs[0].Text)
		}
		if !strings.Contains(h.stdout.String(), "test message sent") {
			t.Errorf("stdout = %q", h.stdout.String())
		}
	})
	t.Run("mentions muted", func(t *testing.T) {
		h := newHarness(t)
		if err := (state.Store{Dir: h.env["HERDR_PLUGIN_STATE_DIR"]}).SetDisabled(true); err != nil {
			t.Fatal(err)
		}
		if code := h.run("test"); code != 0 {
			t.Fatalf("exit code = %d", code)
		}
		msgs := h.tg.messages()
		if len(msgs) != 1 || !strings.Contains(msgs[0].Text, "muted") {
			t.Errorf("messages = %+v", msgs)
		}
	})
	t.Run("missing config", func(t *testing.T) {
		h := newHarness(t)
		delete(h.env, "TELEGRAM_BOT_TOKEN")
		delete(h.env, "TELEGRAM_CHAT_ID")
		if code := h.run("test"); code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(h.stderr.String(), "missing TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID") {
			t.Errorf("stderr = %q", h.stderr.String())
		}
		if n := len(h.tg.messages()); n != 0 {
			t.Errorf("sent %d messages", n)
		}
	})
	t.Run("invalid config", func(t *testing.T) {
		h := newHarness(t)
		h.env["PANE_TAIL_LINES"] = "-5"
		if code := h.run("test"); code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
	})
}

func TestRunSubcommands(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"unknown", []string{"bogus"}, 2, "", usage},
		{"help", []string{"--help"}, 0, usage, ""},
		{"default is notify", nil, 0, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			if code := h.run(tt.args...); code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if got := strings.TrimSpace(h.stdout.String()); got != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", got, tt.wantStdout)
			}
			if got := strings.TrimSpace(h.stderr.String()); got != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", got, tt.wantStderr)
			}
		})
	}
}
