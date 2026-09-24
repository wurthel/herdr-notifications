package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	lockWait = 2 * time.Second
	lockPoll = 20 * time.Millisecond
)

type PaneState struct {
	Status       string               `json:"status"`
	LastNotified map[string]time.Time `json:"last_notified,omitempty"`
	// LastTurn holds a fingerprint of the transcript turn last sent per status.
	LastTurn map[string]string `json:"last_turn,omitempty"`
}

type Store struct {
	Dir string
}

// Update reads the pane's state, applies fn and persists the result while
// holding a per-pane lock, so concurrent hooks for one pane serialize.
func (s Store) Update(paneID string, fn func(prev PaneState) PaneState) error {
	dir := filepath.Join(s.Dir, "panes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	base := filepath.Join(dir, SafeName(paneID))

	unlock, err := acquire(base + ".lock")
	if err != nil {
		return err
	}
	defer unlock()

	var prev PaneState
	raw, err := os.ReadFile(base + ".json")
	switch {
	case err == nil:
		// A corrupt file is treated as empty state rather than blocking notifications.
		_ = json.Unmarshal(raw, &prev)
	case !errors.Is(err, os.ErrNotExist):
		return err
	}

	out, err := json.Marshal(fn(prev))
	if err != nil {
		return err
	}
	return WriteFileAtomic(base+".json", out, 0o600)
}

func (s Store) Disabled() bool {
	_, err := os.Stat(filepath.Join(s.Dir, "disabled"))
	return err == nil
}

func (s Store) SetDisabled(disabled bool) error {
	path := filepath.Join(s.Dir, "disabled")
	if !disabled {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return WriteFileAtomic(path, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// SafeName maps a pane id such as "w1:p2" to a portable file name.
func SafeName(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

// acquire takes an exclusive flock on path. The kernel drops the lock when
// the process exits, so a crashed hook can never leave a stale lock behind.
func acquire(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", filepath.Base(path), err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("timed out waiting for lock %s", filepath.Base(path))
		}
		time.Sleep(lockPoll)
	}
}
