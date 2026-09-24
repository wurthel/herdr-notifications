package state

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSafeName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"w1:p2", "w1_p2"},
		{"abc-XYZ_09", "abc-XYZ_09"},
		{".", "_"},
		{"..", "__"},
		{"", "_"},
		{"../../etc/passwd", "______etc_passwd"},
		{"a b/c\\d", "a_b_c_d"},
		{"пане", "____"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := SafeName(tt.in); got != tt.want {
				t.Errorf("SafeName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestUpdateRoundTrip(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "nested", "state")}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	var seen []PaneState
	steps := []PaneState{
		{Status: "working"},
		{Status: "done", LastNotified: map[string]time.Time{"done": t0}},
		{Status: "idle", LastNotified: map[string]time.Time{"done": t0}},
	}
	for _, next := range steps {
		if err := s.Update("w1:p1", func(prev PaneState) PaneState {
			seen = append(seen, prev)
			return next
		}); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	want := []PaneState{{}, steps[0], steps[1]}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("prev states = %+v, want %+v", seen, want)
	}

	if _, err := os.Stat(filepath.Join(s.Dir, "panes", "w1_p1.json")); err != nil {
		t.Errorf("state file missing: %v", err)
	}

	if err := s.Update("w1:p2", func(prev PaneState) PaneState {
		if !reflect.DeepEqual(prev, PaneState{}) {
			t.Errorf("other pane prev = %+v, want empty", prev)
		}
		return prev
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateCorruptState(t *testing.T) {
	tests := []struct{ name, content string }{
		{"garbage", "{not json"},
		{"empty", ""},
		{"wrong types", `{"status":5,"last_notified":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Store{Dir: t.TempDir()}
			if err := os.MkdirAll(filepath.Join(s.Dir, "panes"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(s.Dir, "panes", "p.json"), []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			err := s.Update("p", func(prev PaneState) PaneState {
				if prev.Status != "" || len(prev.LastNotified) != 0 {
					t.Errorf("prev = %+v, want empty", prev)
				}
				return PaneState{Status: "done"}
			})
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			_ = s.Update("p", func(prev PaneState) PaneState {
				if prev.Status != "done" {
					t.Errorf("after rewrite prev = %+v", prev)
				}
				return prev
			})
		})
	}
}

func TestUpdateConcurrent(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.Update("w1:p1", func(prev PaneState) PaneState {
				c, _ := strconv.Atoi(prev.Status)
				return PaneState{Status: strconv.Itoa(c + 1)}
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	var final PaneState
	if err := s.Update("w1:p1", func(prev PaneState) PaneState { final = prev; return prev }); err != nil {
		t.Fatal(err)
	}
	if final.Status != strconv.Itoa(n) {
		t.Errorf("counter = %q, want %d (lost updates)", final.Status, n)
	}
}

func TestUpdateLeftoverLockFileDoesNotBlock(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	lock := filepath.Join(s.Dir, "panes", "w1_p1.lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	called := false
	if err := s.Update("w1:p1", func(prev PaneState) PaneState { called = true; return prev }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !called {
		t.Error("fn not called")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("unheld lock file delayed Update by %v", d)
	}
}

func TestUpdateHeldLockTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the lock timeout")
	}
	s := Store{Dir: t.TempDir()}
	lock := filepath.Join(s.Dir, "panes", "w1_p1.lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := acquire(lock)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	err = s.Update("w1:p1", func(prev PaneState) PaneState {
		t.Error("fn called while lock held")
		return prev
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %v, want lock timeout", err)
	}
}

func TestDisabled(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "state")}
	if s.Disabled() {
		t.Fatal("new store is disabled")
	}
	steps := []bool{false, true, true, false, false, true}
	for i, want := range steps {
		if err := s.SetDisabled(want); err != nil {
			t.Fatalf("step %d SetDisabled(%v): %v", i, want, err)
		}
		if got := s.Disabled(); got != want {
			t.Fatalf("step %d Disabled() = %v, want %v", i, got, want)
		}
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	for _, content := range []string{"first", "second"} {
		if err := WriteFileAtomic(path, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFileAtomic: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != content {
			t.Errorf("content = %q, want %q", got, content)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %v, want 0600", perm)
	}

	// Renaming onto a non-empty directory fails; the temp file must still be cleaned up.
	blocked := filepath.Join(dir, "blocked")
	if err := os.MkdirAll(filepath.Join(blocked, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(blocked, []byte("x"), 0o600); err == nil {
		t.Error("expected error writing over a non-empty directory")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"blocked", "f.json"}; !reflect.DeepEqual(names, want) {
		t.Errorf("dir entries = %v, want %v", names, want)
	}
}
