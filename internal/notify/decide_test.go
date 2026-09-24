package notify

import (
	"maps"
	"reflect"
	"testing"
	"time"

	"github.com/vusalsalmanov/herdr-notifications/internal/state"
)

func TestDecide(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	notifyOn := map[string]bool{"done": true, "blocked": true}
	const debounce = 10 * time.Second

	tests := []struct {
		name       string
		prev       state.PaneState
		status     string
		now        time.Time
		wantNotify bool
		wantState  state.PaneState
	}{
		{
			name:      "empty status keeps state",
			prev:      state.PaneState{Status: "working"},
			status:    "",
			now:       t0,
			wantState: state.PaneState{Status: "working"},
		},
		{
			name:      "unknown status keeps state",
			prev:      state.PaneState{Status: "done", LastNotified: map[string]time.Time{"done": t0}},
			status:    StatusUnknown,
			now:       t0.Add(time.Minute),
			wantState: state.PaneState{Status: "done", LastNotified: map[string]time.Time{"done": t0}},
		},
		{
			name:       "working to done notifies",
			prev:       state.PaneState{Status: "working"},
			status:     "done",
			now:        t0,
			wantNotify: true,
			wantState:  state.PaneState{Status: "done", LastNotified: map[string]time.Time{"done": t0}},
		},
		{
			name:       "first event ever done notifies",
			prev:       state.PaneState{},
			status:     "done",
			now:        t0,
			wantNotify: true,
			wantState:  state.PaneState{Status: "done", LastNotified: map[string]time.Time{"done": t0}},
		},
		{
			name:       "notify preserves other statuses",
			prev:       state.PaneState{Status: "working", LastNotified: map[string]time.Time{"done": t0}},
			status:     "blocked",
			now:        t0.Add(time.Second),
			wantNotify: true,
			wantState: state.PaneState{Status: "blocked", LastNotified: map[string]time.Time{
				"done": t0, "blocked": t0.Add(time.Second),
			}},
		},
		{
			name:      "done to done is silent",
			prev:      state.PaneState{Status: "done", LastNotified: map[string]time.Time{"done": t0}},
			status:    "done",
			now:       t0.Add(time.Hour),
			wantState: state.PaneState{Status: "done", LastNotified: map[string]time.Time{"done": t0}},
		},
		{
			name:      "done to idle is silent",
			prev:      state.PaneState{Status: "done", LastNotified: map[string]time.Time{"done": t0}},
			status:    "idle",
			now:       t0.Add(time.Hour),
			wantState: state.PaneState{Status: "idle", LastNotified: map[string]time.Time{"done": t0}},
		},
		{
			name:      "idle to working is silent",
			prev:      state.PaneState{Status: "idle"},
			status:    "working",
			now:       t0,
			wantState: state.PaneState{Status: "working"},
		},
		{
			name:      "working to idle is silent",
			prev:      state.PaneState{Status: "working"},
			status:    "idle",
			now:       t0,
			wantState: state.PaneState{Status: "idle"},
		},
		{
			name:      "blocked again within debounce is silent",
			prev:      state.PaneState{Status: "working", LastNotified: map[string]time.Time{"blocked": t0}},
			status:    "blocked",
			now:       t0.Add(debounce - time.Nanosecond),
			wantState: state.PaneState{Status: "blocked", LastNotified: map[string]time.Time{"blocked": t0}},
		},
		{
			name:       "blocked again exactly at debounce notifies",
			prev:       state.PaneState{Status: "working", LastNotified: map[string]time.Time{"blocked": t0}},
			status:     "blocked",
			now:        t0.Add(debounce),
			wantNotify: true,
			wantState:  state.PaneState{Status: "blocked", LastNotified: map[string]time.Time{"blocked": t0.Add(debounce)}},
		},
		{
			name:       "blocked again after debounce notifies",
			prev:       state.PaneState{Status: "working", LastNotified: map[string]time.Time{"blocked": t0}},
			status:     "blocked",
			now:        t0.Add(time.Minute),
			wantNotify: true,
			wantState:  state.PaneState{Status: "blocked", LastNotified: map[string]time.Time{"blocked": t0.Add(time.Minute)}},
		},
		{
			name:      "status not in notifyOn is silent",
			prev:      state.PaneState{Status: "working"},
			status:    "failed",
			now:       t0,
			wantState: state.PaneState{Status: "failed"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := maps.Clone(tt.prev.LastNotified)
			got, notifyAs := Decide(tt.prev, tt.status, tt.now, Rules{NotifyOn: notifyOn, Debounce: debounce})
			if notify := notifyAs != ""; notify != tt.wantNotify {
				t.Errorf("notify = %v (%q), want %v", notify, notifyAs, tt.wantNotify)
			}
			if !reflect.DeepEqual(got, tt.wantState) {
				t.Errorf("state = %+v, want %+v", got, tt.wantState)
			}
			if !reflect.DeepEqual(tt.prev.LastNotified, before) {
				t.Errorf("prev.LastNotified mutated: %v, was %v", tt.prev.LastNotified, before)
			}
		})
	}
}

func TestDecideSequence(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	notifyOn := map[string]bool{"done": true, "blocked": true}
	steps := []struct {
		status string
		at     time.Duration
		want   bool
	}{
		{"working", 0, false},
		{"blocked", 1 * time.Second, true},
		{"working", 2 * time.Second, false},
		{"blocked", 3 * time.Second, false},
		{"working", 4 * time.Second, false},
		{"unknown", 5 * time.Second, false},
		{"working", 6 * time.Second, false},
		{"blocked", 20 * time.Second, true},
		{"unknown", 21 * time.Second, false},
		{"blocked", 40 * time.Second, false},
		{"done", 41 * time.Second, true},
		{"done", 42 * time.Second, false},
		{"idle", 43 * time.Second, false},
	}
	var st state.PaneState
	for i, s := range steps {
		var notifyAs string
		st, notifyAs = Decide(st, s.status, t0.Add(s.at), Rules{NotifyOn: notifyOn, Debounce: 10 * time.Second})
		if got := notifyAs != ""; got != s.want {
			t.Fatalf("step %d (%s at %v): notify = %v, want %v", i, s.status, s.at, got, s.want)
		}
	}
}

func TestDecideIdleAfterWorking(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rules := Rules{NotifyOn: map[string]bool{"done": true, "blocked": true}, Debounce: 10 * time.Second, IdleAfterWorking: true}
	steps := []struct {
		status string
		at     time.Duration
		want   string
	}{
		{"working", 0, ""},
		{"idle", 1 * time.Second, "done"},
		{"working", 30 * time.Second, ""},
		{"done", 31 * time.Second, "done"},
		{"idle", 32 * time.Second, ""},
		{"working", 35 * time.Second, ""},
		{"idle", 36 * time.Second, ""},
		{"blocked", 50 * time.Second, "blocked"},
		{"idle", 51 * time.Second, ""},
		{"working", 70 * time.Second, ""},
		{"idle", 80 * time.Second, "done"},
		{"idle", 81 * time.Second, ""},
	}
	var st state.PaneState
	for i, s := range steps {
		var got string
		st, got = Decide(st, s.status, t0.Add(s.at), rules)
		if got != s.want {
			t.Fatalf("step %d (%s at %v): notifyAs = %q, want %q", i, s.status, s.at, got, s.want)
		}
	}
	if st.Status != "idle" {
		t.Errorf("recorded status = %q, want idle", st.Status)
	}

	rules.NotifyOn = map[string]bool{"blocked": true}
	if _, got := Decide(state.PaneState{Status: "working"}, "idle", t0, rules); got != "" {
		t.Errorf("working→idle notified as %q with done not in NOTIFY_ON", got)
	}
	rules.NotifyOn, rules.IdleAfterWorking = map[string]bool{"done": true}, false
	if _, got := Decide(state.PaneState{Status: "working"}, "idle", t0, rules); got != "" {
		t.Errorf("working→idle notified as %q with rule disabled", got)
	}
}
