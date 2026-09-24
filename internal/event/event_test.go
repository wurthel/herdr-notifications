package event

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	const fullEvent = `{"event":"pane_agent_status_changed","data":{"type":"pane_agent_status_changed",` +
		`"pane_id":"w1:p1","workspace_id":"w1","agent_status":"done","agent":"claude",` +
		`"display_agent":"Claude Code","title":"fix bug"}}`
	const fullContext = `{"workspace_id":"w1","workspace_label":"api","tab_id":"t1","tab_label":"main",` +
		`"focused_pane_id":"w1:p1","focused_pane_status":"working","focused_pane_agent":"codex"}`

	tests := []struct {
		name     string
		event    string
		context  string
		fallback string
		want     Info
	}{
		{
			name:    "full payload",
			event:   fullEvent,
			context: fullContext,
			want: Info{PaneID: "w1:p1", Status: "done", Agent: "Claude Code", Title: "fix bug",
				WorkspaceLabel: "api", TabLabel: "main"},
		},
		{
			name:  "context describing another pane is ignored",
			event: `{"data":{"pane_id":"w1:p3","workspace_id":"w1"}}`,
			context: `{"workspace_id":"w2","workspace_label":"other","tab_label":"main",` +
				`"focused_pane_id":"w2:p1","focused_pane_status":"working","focused_pane_agent":"codex"}`,
			want: Info{PaneID: "w1:p3", Agent: "agent", WorkspaceLabel: "w1"},
		},
		{
			name:    "null optional fields",
			event:   `{"event":"pane_agent_status_changed","data":{"pane_id":"w1:p1","agent_status":"blocked","agent":null,"display_agent":null,"title":null,"workspace_id":null}}`,
			context: `{"workspace_label":null,"tab_label":null}`,
			want:    Info{PaneID: "w1:p1", Status: "blocked", Agent: "agent"},
		},
		{
			name:  "agent falls back to data.agent",
			event: `{"data":{"pane_id":"p","agent_status":"done","agent":"claude","display_agent":"  "}}`,
			want:  Info{PaneID: "p", Status: "done", Agent: "claude"},
		},
		{
			name:    "agent falls back to focused_pane_agent",
			event:   `{"data":{"pane_id":"p","agent_status":"done"}}`,
			context: `{"focused_pane_agent":"codex"}`,
			want:    Info{PaneID: "p", Status: "done", Agent: "codex"},
		},
		{
			name:     "pane id falls back to argument",
			event:    `{"data":{"agent_status":"done"}}`,
			context:  `{"focused_pane_id":"ctx"}`,
			fallback: "env-pane",
			want:     Info{PaneID: "env-pane", Status: "done", Agent: "agent"},
		},
		{
			name:    "pane id falls back to focused pane",
			event:   `{"data":{"agent_status":"done"}}`,
			context: `{"focused_pane_id":"ctx"}`,
			want:    Info{PaneID: "ctx", Status: "done", Agent: "agent"},
		},
		{
			name:    "status falls back to context and is lowercased",
			event:   `{"data":{"pane_id":"p"}}`,
			context: `{"focused_pane_status":"BLOCKED"}`,
			want:    Info{PaneID: "p", Status: "blocked", Agent: "agent"},
		},
		{
			name:  "event status lowercased and trimmed",
			event: `{"data":{"pane_id":" p ","agent_status":" Done "}}`,
			want:  Info{PaneID: "p", Status: "done", Agent: "agent"},
		},
		{
			name:  "workspace label falls back to workspace id",
			event: `{"data":{"pane_id":"p","agent_status":"done","workspace_id":"w7"}}`,
			want:  Info{PaneID: "p", Status: "done", Agent: "agent", WorkspaceLabel: "w7"},
		},
		{
			name:     "empty strings",
			fallback: "",
			want:     Info{Agent: "agent"},
		},
		{
			name:     "whitespace only",
			event:    "  \n",
			context:  "\t",
			fallback: "w1:p2",
			want:     Info{PaneID: "w1:p2", Agent: "agent"},
		},
		{
			name:  "unknown fields ignored",
			event: `{"data":{"pane_id":"p","agent_status":"idle","extra":{"nested":[1,2]}}}`,
			want:  Info{PaneID: "p", Status: "idle", Agent: "agent"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.event, tt.context, tt.fallback)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Parse =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		event   string
		context string
		wantErr string
	}{
		{"invalid event json", `{"data":`, "", "parse event json"},
		{"wrong event type", `{"data":{"pane_id":5}}`, "", "parse event json"},
		{"invalid context json", `{"data":{}}`, `not json`, "parse context json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.event, tt.context, "")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
