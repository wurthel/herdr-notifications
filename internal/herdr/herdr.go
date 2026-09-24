package herdr

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const DefaultTimeout = 3 * time.Second

type CLI struct {
	Bin     string
	Timeout time.Duration
}

// ReadTail returns the last lines of a pane's scrollback as plain text.
func (c CLI) ReadTail(ctx context.Context, paneID string, lines int) (string, error) {
	out, err := c.run(ctx, "pane", "read", paneID, "--source", "recent-unwrapped", "--lines", strconv.Itoa(lines))
	return string(out), err
}

// Toast shows a herdr notification; delivery depends on the user's [ui.toast] config.
func (c CLI) Toast(ctx context.Context, title, body string) error {
	_, err := c.run(ctx, "notification", "show", title, "--body", body)
	return err
}

func (c CLI) run(ctx context.Context, args ...string) ([]byte, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, c.Bin, args...).Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if msg := strings.TrimSpace(string(exitErr.Stderr)); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
	}
	return out, err
}
