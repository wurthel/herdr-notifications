package event

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Envelope struct {
	Event string `json:"event"`
	Data  Data   `json:"data"`
}

// Data mirrors the pane_agent_status_changed payload. Optional fields that
// herdr sends as null or omits decode to "".
type Data struct {
	Type         string `json:"type"`
	PaneID       string `json:"pane_id"`
	WorkspaceID  string `json:"workspace_id"`
	AgentStatus  string `json:"agent_status"`
	Agent        string `json:"agent"`
	DisplayAgent string `json:"display_agent"`
	Title        string `json:"title"`
}

type Context struct {
	WorkspaceID       string `json:"workspace_id"`
	WorkspaceLabel    string `json:"workspace_label"`
	TabID             string `json:"tab_id"`
	TabLabel          string `json:"tab_label"`
	FocusedPaneID     string `json:"focused_pane_id"`
	FocusedPaneStatus string `json:"focused_pane_status"`
	FocusedPaneAgent  string `json:"focused_pane_agent"`
}

type Info struct {
	PaneID         string
	Status         string
	Agent          string
	Title          string
	WorkspaceLabel string
	TabLabel       string
}

// Parse extracts what a notification needs from HERDR_PLUGIN_EVENT_JSON and
// HERDR_PLUGIN_CONTEXT_JSON, falling back to the context and fallbackPaneID
// (HERDR_PANE_ID) for fields the event lacks.
func Parse(eventJSON, contextJSON, fallbackPaneID string) (Info, error) {
	var env Envelope
	if strings.TrimSpace(eventJSON) != "" {
		if err := json.Unmarshal([]byte(eventJSON), &env); err != nil {
			return Info{}, fmt.Errorf("parse event json: %w", err)
		}
	}
	var ctx Context
	if strings.TrimSpace(contextJSON) != "" {
		if err := json.Unmarshal([]byte(contextJSON), &ctx); err != nil {
			return Info{}, fmt.Errorf("parse context json: %w", err)
		}
	}

	d := env.Data
	paneID := firstNonEmpty(d.PaneID, fallbackPaneID, ctx.FocusedPaneID)
	// herdr builds the context from the event's own pane, but falls back to the
	// workspace's focused pane if it cannot resolve it; ignore pane-level
	// context then so another pane's status never leaks into this one.
	if ctx.FocusedPaneID != "" && ctx.FocusedPaneID != paneID {
		ctx.TabLabel, ctx.FocusedPaneStatus, ctx.FocusedPaneAgent = "", "", ""
	}
	if d.WorkspaceID != "" && ctx.WorkspaceID != "" && ctx.WorkspaceID != d.WorkspaceID {
		ctx.WorkspaceLabel = ""
	}
	return Info{
		PaneID:         paneID,
		Status:         strings.ToLower(firstNonEmpty(d.AgentStatus, ctx.FocusedPaneStatus)),
		Agent:          firstNonEmpty(d.DisplayAgent, d.Agent, ctx.FocusedPaneAgent, "agent"),
		Title:          d.Title,
		WorkspaceLabel: firstNonEmpty(ctx.WorkspaceLabel, d.WorkspaceID, ctx.WorkspaceID),
		TabLabel:       ctx.TabLabel,
	}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
