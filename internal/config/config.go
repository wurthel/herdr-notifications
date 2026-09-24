package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAPIBase       = "https://api.telegram.org"
	DefaultPaneTailLines = 15
	DefaultDebounce      = 10 * time.Second
	EnvFileName          = ".env"

	maxPaneTailLines   = 200
	maxDebounceSeconds = 24 * 60 * 60
)

var knownStatuses = map[string]bool{"idle": true, "working": true, "blocked": true, "done": true, "unknown": true}

type Config struct {
	BotToken string
	ChatID   string
	NotifyOn map[string]bool
	// IdleAfterWorking reports working→idle as done (agent finished in a visible pane).
	IdleAfterWorking bool
	PaneTailLines    int
	Debounce         time.Duration
	Debug            bool
	APIBase          string
	ConfigDir        string
	StateDir         string
	HerdrBin         string
	// Warnings lists non-fatal problems such as unknown NOTIFY_ON statuses.
	Warnings []string
}

// Load reads $HERDR_PLUGIN_CONFIG_DIR/.env (if present) and overlays the
// process environment on top of it.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		ConfigDir: getenv("HERDR_PLUGIN_CONFIG_DIR"),
		StateDir:  getenv("HERDR_PLUGIN_STATE_DIR"),
		HerdrBin:  getenv("HERDR_BIN_PATH"),
	}
	if cfg.StateDir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		cfg.StateDir = filepath.Join(base, "herdr-notifications")
	}
	if cfg.HerdrBin == "" {
		cfg.HerdrBin = "herdr"
	}

	fileVals := map[string]string{}
	if cfg.ConfigDir != "" {
		f, err := os.Open(filepath.Join(cfg.ConfigDir, EnvFileName))
		switch {
		case err == nil:
			fileVals, err = ParseEnv(f)
			f.Close()
			if err != nil {
				return cfg, fmt.Errorf("read %s: %w", EnvFileName, err)
			}
		case !errors.Is(err, os.ErrNotExist):
			return cfg, fmt.Errorf("open %s: %w", EnvFileName, err)
		}
	}
	get := func(key string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fileVals[key]
	}

	cfg.BotToken = get("TELEGRAM_BOT_TOKEN")
	cfg.ChatID = get("TELEGRAM_CHAT_ID")
	cfg.APIBase = strings.TrimRight(get("TELEGRAM_API_BASE"), "/")
	if cfg.APIBase == "" {
		cfg.APIBase = DefaultAPIBase
	}
	cfg.NotifyOn = parseSet(get("NOTIFY_ON"), "done,blocked")
	for s := range cfg.NotifyOn {
		if !knownStatuses[s] {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("NOTIFY_ON: unknown status %q (valid: idle, working, blocked, done)", s))
		}
	}
	cfg.Debug = parseBool(get("DEBUG"))
	cfg.IdleAfterWorking = parseBoolDefault(get("NOTIFY_IDLE_AFTER_WORKING"), true)

	var err error
	if cfg.PaneTailLines, err = parseInt(get("PANE_TAIL_LINES"), DefaultPaneTailLines); err != nil {
		return cfg, fmt.Errorf("PANE_TAIL_LINES: %w", err)
	}
	if cfg.PaneTailLines > maxPaneTailLines {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("PANE_TAIL_LINES: %d capped at %d", cfg.PaneTailLines, maxPaneTailLines))
		cfg.PaneTailLines = maxPaneTailLines
	}
	secs, err := parseInt(get("DEBOUNCE_SECONDS"), int(DefaultDebounce/time.Second))
	if err != nil {
		return cfg, fmt.Errorf("DEBOUNCE_SECONDS: %w", err)
	}
	if secs > maxDebounceSeconds {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("DEBOUNCE_SECONDS: %d capped at %d", secs, maxDebounceSeconds))
		secs = maxDebounceSeconds
	}
	cfg.Debounce = time.Duration(secs) * time.Second
	return cfg, nil
}

func (c Config) ValidateTelegram() error {
	var missing []string
	if c.BotToken == "" {
		missing = append(missing, "TELEGRAM_BOT_TOKEN")
	}
	if c.ChatID == "" {
		missing = append(missing, "TELEGRAM_CHAT_ID")
	}
	if len(missing) > 0 {
		where := "the environment"
		if c.ConfigDir != "" {
			where = filepath.Join(c.ConfigDir, EnvFileName)
		}
		return fmt.Errorf("missing %s (set in %s)", strings.Join(missing, ", "), where)
	}
	return nil
}

// ParseEnv parses KEY=VALUE lines. Blank lines, # comments and an optional
// "export " prefix are allowed; matching surrounding quotes are stripped, and
// unquoted values may end with a " # comment".
func ParseEnv(r io.Reader) (map[string]string, error) {
	vals := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		} else if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		if key != "" {
			vals[key] = val
		}
	}
	return vals, sc.Err()
}

func parseSet(raw, def string) map[string]bool {
	if strings.TrimSpace(raw) == "" {
		raw = def
	}
	set := map[string]bool{}
	for _, s := range strings.Split(raw, ",") {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			set[s] = true
		}
	}
	return set
}

func parseBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func parseBoolDefault(raw string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return def
	case "0", "false", "no", "off":
		return false
	}
	return parseBool(raw)
}

func parseInt(raw string, def int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("must be >= 0, got %d", n)
	}
	return n, nil
}
