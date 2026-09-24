package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseEnv(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[string]string
	}{
		{"empty", "", map[string]string{}},
		{"comments and blanks", "# comment\n\n   \n  # indented comment\nA=1\n", map[string]string{"A": "1"}},
		{"export prefix", "export A=1\nexport   B = 2", map[string]string{"A": "1", "B": "2"}},
		{"double quotes", `A="hello world"`, map[string]string{"A": "hello world"}},
		{"single quotes", `A='x#y'`, map[string]string{"A": "x#y"}},
		{"mismatched quotes kept", `A="x'`, map[string]string{"A": `"x'`}},
		{"lone quote kept", `A="`, map[string]string{"A": `"`}},
		{"empty quoted", `A=""`, map[string]string{"A": ""}},
		{"equals in value", "URL=https://x.test/?a=b&c=d", map[string]string{"URL": "https://x.test/?a=b&c=d"}},
		{"token with colon", "TELEGRAM_BOT_TOKEN=123:ABC-def", map[string]string{"TELEGRAM_BOT_TOKEN": "123:ABC-def"}},
		{"whitespace around", "  A  =  v  \r\n", map[string]string{"A": "v"}},
		{"no equals ignored", "garbage\nA=1", map[string]string{"A": "1"}},
		{"empty key ignored", "=v\nA=1", map[string]string{"A": "1"}},
		{"empty value", "A=", map[string]string{"A": ""}},
		{"last wins", "A=1\nA=2", map[string]string{"A": "2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseEnv(strings.NewReader(tt.in))
			if err != nil {
				t.Fatalf("ParseEnv error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseEnv = %v, want %v", got, tt.want)
			}
		})
	}
}

func mapEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, EnvFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(mapEnv(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	want := Config{
		NotifyOn:         map[string]bool{"done": true, "blocked": true},
		PaneTailLines:    DefaultPaneTailLines,
		IdleAfterWorking: true,
		Debounce:         DefaultDebounce,
		APIBase:          DefaultAPIBase,
		StateDir:         filepath.Join(cacheDir, "herdr-notifications"),
		HerdrBin:         "herdr",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load defaults = %+v, want %+v", cfg, want)
	}
}

func TestLoad(t *testing.T) {
	fileDir := writeEnvFile(t, strings.Join([]string{
		"# telegram",
		"export TELEGRAM_BOT_TOKEN=file-token",
		`TELEGRAM_CHAT_ID="1234"`,
		"NOTIFY_ON= Done , BLOCKED,,idle ",
		"PANE_TAIL_LINES=5",
		"DEBOUNCE_SECONDS=30",
		"DEBUG=yes",
		"TELEGRAM_API_BASE=http://file.test///",
	}, "\n"))

	tests := []struct {
		name  string
		env   map[string]string
		check func(t *testing.T, c Config)
	}{
		{
			name: "values from .env",
			env:  map[string]string{"HERDR_PLUGIN_CONFIG_DIR": fileDir, "HERDR_PLUGIN_STATE_DIR": "/s", "HERDR_BIN_PATH": "/bin/herdr"},
			check: func(t *testing.T, c Config) {
				want := Config{
					BotToken:         "file-token",
					ChatID:           "1234",
					NotifyOn:         map[string]bool{"done": true, "blocked": true, "idle": true},
					PaneTailLines:    5,
					IdleAfterWorking: true,
					Debounce:         30 * time.Second,
					Debug:            true,
					APIBase:          "http://file.test",
					ConfigDir:        fileDir,
					StateDir:         "/s",
					HerdrBin:         "/bin/herdr",
				}
				if !reflect.DeepEqual(c, want) {
					t.Errorf("got %+v\nwant %+v", c, want)
				}
			},
		},
		{
			name: "process env overrides .env",
			env: map[string]string{
				"HERDR_PLUGIN_CONFIG_DIR": fileDir,
				"TELEGRAM_BOT_TOKEN":      "env-token",
				"NOTIFY_ON":               "Working",
				"PANE_TAIL_LINES":         "0",
				"DEBOUNCE_SECONDS":        "0",
				"DEBUG":                   "off",
				"TELEGRAM_API_BASE":       "http://env.test/",
			},
			check: func(t *testing.T, c Config) {
				if c.BotToken != "env-token" || c.ChatID != "1234" {
					t.Errorf("token/chat = %q/%q", c.BotToken, c.ChatID)
				}
				if !reflect.DeepEqual(c.NotifyOn, map[string]bool{"working": true}) {
					t.Errorf("NotifyOn = %v", c.NotifyOn)
				}
				if c.PaneTailLines != 0 || c.Debounce != 0 || c.Debug {
					t.Errorf("tail=%d debounce=%v debug=%v", c.PaneTailLines, c.Debounce, c.Debug)
				}
				if c.APIBase != "http://env.test" {
					t.Errorf("APIBase = %q", c.APIBase)
				}
			},
		},
		{
			name: "missing .env is fine",
			env:  map[string]string{"HERDR_PLUGIN_CONFIG_DIR": t.TempDir(), "TELEGRAM_BOT_TOKEN": "t"},
			check: func(t *testing.T, c Config) {
				if c.BotToken != "t" || c.PaneTailLines != DefaultPaneTailLines {
					t.Errorf("got %+v", c)
				}
			},
		},
		{
			name: "blank NOTIFY_ON uses default",
			env:  map[string]string{"NOTIFY_ON": "  "},
			check: func(t *testing.T, c Config) {
				if !reflect.DeepEqual(c.NotifyOn, map[string]bool{"done": true, "blocked": true}) {
					t.Errorf("NotifyOn = %v", c.NotifyOn)
				}
			},
		},
		{
			name: "debug truthy values",
			env:  map[string]string{"DEBUG": " TRUE "},
			check: func(t *testing.T, c Config) {
				if !c.Debug {
					t.Error("Debug = false")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(mapEnv(tt.env))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			tt.check(t, c)
		})
	}
}

func TestLoadErrors(t *testing.T) {
	dirEnv := t.TempDir()
	if err := os.Mkdir(filepath.Join(dirEnv, EnvFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"tail lines not a number", map[string]string{"PANE_TAIL_LINES": "abc"}, "PANE_TAIL_LINES"},
		{"tail lines negative", map[string]string{"PANE_TAIL_LINES": "-1"}, "PANE_TAIL_LINES: must be >= 0"},
		{"debounce not a number", map[string]string{"DEBOUNCE_SECONDS": "1.5"}, "DEBOUNCE_SECONDS"},
		{"debounce negative", map[string]string{"DEBOUNCE_SECONDS": "-3"}, "DEBOUNCE_SECONDS: must be >= 0"},
		{"invalid in .env", map[string]string{"HERDR_PLUGIN_CONFIG_DIR": writeEnvFile(t, "PANE_TAIL_LINES=x")}, "PANE_TAIL_LINES"},
		{".env is a directory", map[string]string{"HERDR_PLUGIN_CONFIG_DIR": dirEnv}, ".env"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(mapEnv(tt.env))
			if err == nil {
				t.Fatalf("Load succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateTelegram(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"ok", Config{BotToken: "t", ChatID: "c"}, ""},
		{"both missing", Config{}, "missing TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID (set in the environment)"},
		{"token missing", Config{ChatID: "c"}, "missing TELEGRAM_BOT_TOKEN (set in the environment)"},
		{"chat missing with config dir", Config{BotToken: "t", ConfigDir: "/cfg"}, "missing TELEGRAM_CHAT_ID (set in " + filepath.Join("/cfg", ".env") + ")"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.ValidateTelegram()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Errorf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadWarningsAndClamps(t *testing.T) {
	env := map[string]string{
		"NOTIFY_ON":        "done,finished",
		"PANE_TAIL_LINES":  "5000",
		"DEBOUNCE_SECONDS": "999999999999",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PaneTailLines != maxPaneTailLines {
		t.Errorf("PaneTailLines = %d, want %d", cfg.PaneTailLines, maxPaneTailLines)
	}
	if cfg.Debounce != maxDebounceSeconds*time.Second {
		t.Errorf("Debounce = %v, want %v", cfg.Debounce, maxDebounceSeconds*time.Second)
	}
	joined := strings.Join(cfg.Warnings, "\n")
	for _, want := range []string{`"finished"`, "PANE_TAIL_LINES", "DEBOUNCE_SECONDS"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q missing %s", cfg.Warnings, want)
		}
	}
}

func TestParseEnvInlineComment(t *testing.T) {
	got, err := ParseEnv(strings.NewReader("A=abc # note\nB=\"x # kept\"\nC=a#b\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "abc", "B": "x # kept", "C": "a#b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseEnv = %v, want %v", got, want)
	}
}

func TestLoadIdleAfterWorking(t *testing.T) {
	for raw, want := range map[string]bool{"": true, "1": true, "true": true, "0": false, "false": false, "off": false} {
		cfg, err := Load(func(k string) string {
			if k == "NOTIFY_IDLE_AFTER_WORKING" {
				return raw
			}
			return ""
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.IdleAfterWorking != want {
			t.Errorf("NOTIFY_IDLE_AFTER_WORKING=%q: IdleAfterWorking = %v, want %v", raw, cfg.IdleAfterWorking, want)
		}
	}
}
