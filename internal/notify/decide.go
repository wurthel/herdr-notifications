package notify

import (
	"time"

	"github.com/vusalsalmanov/herdr-notifications/internal/state"
)

const (
	StatusUnknown = "unknown"
	StatusWorking = "working"
	StatusIdle    = "idle"
	StatusDone    = "done"
)

type Rules struct {
	NotifyOn map[string]bool
	Debounce time.Duration
	// IdleAfterWorking treats working→idle as "done". herdr reports idle
	// instead of done when the pane is visible as the agent finishes.
	IdleAfterWorking bool
}

// Decide returns the pane's next state and the status to notify about, or ""
// for none. herdr does not send the previous status and may repeat an event
// with the same status, so only real transitions count, throttled per status
// by debounce. "unknown" is never recorded so a flicker through it cannot
// re-trigger the previous status.
func Decide(prev state.PaneState, status string, now time.Time, r Rules) (state.PaneState, string) {
	if status == "" || status == StatusUnknown {
		return prev, ""
	}

	next := state.PaneState{Status: status, LastNotified: prev.LastNotified}
	if status == prev.Status {
		return next, ""
	}
	notifyAs := status
	if r.IdleAfterWorking && status == StatusIdle && prev.Status == StatusWorking {
		notifyAs = StatusDone
	}
	if !r.NotifyOn[notifyAs] {
		return next, ""
	}
	if last, ok := prev.LastNotified[notifyAs]; ok && now.Sub(last) < r.Debounce {
		return next, ""
	}

	next.LastNotified = make(map[string]time.Time, len(prev.LastNotified)+1)
	for k, v := range prev.LastNotified {
		next.LastNotified[k] = v
	}
	next.LastNotified[notifyAs] = now
	return next, notifyAs
}
