package herdr

import (
	"context"
	"encoding/json"
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

type Pane struct {
	PaneID       string `json:"pane_id"`
	Agent        string `json:"agent"`
	Cwd          string `json:"cwd"`
	AgentSession struct {
		Agent string `json:"agent"`
		Kind  string `json:"kind"`
		Value string `json:"value"`
	} `json:"agent_session"`
}

// PaneInfo returns herdr's view of a pane, including the agent session id.
func (c CLI) PaneInfo(ctx context.Context, paneID string) (Pane, error) {
	out, err := c.run(ctx, "pane", "get", paneID)
	if err != nil {
		return Pane{}, err
	}
	var resp struct {
		Result struct {
			Pane Pane `json:"pane"`
		} `json:"result"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return Pane{}, fmt.Errorf("parse pane get: %w", err)
	}
	if resp.Error != nil {
		return Pane{}, fmt.Errorf("pane get: %s: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result.Pane, nil
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
