package transcript

import (
	"encoding/json"
	"strings"
)

type codexEntry struct {
	Type    string `json:"type"`
	Payload struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Name             string `json:"name"`
		Arguments        string `json:"arguments"`
		CallID           string `json:"call_id"`
		LastAgentMessage string `json:"last_agent_message"`
	} `json:"payload"`
}

func parseCodex(lines [][]byte) Turn {
	var (
		turn    Turn
		pending pendingCalls
	)
	for _, line := range lines {
		var e codexEntry
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		p := e.Payload
		switch {
		case e.Type == "response_item" && p.Type == "message":
			var texts []string
			for _, c := range p.Content {
				if c.Type == "input_text" || c.Type == "output_text" {
					texts = append(texts, c.Text)
				}
			}
			text := strings.TrimSpace(strings.Join(texts, "\n"))
			if text == "" {
				continue
			}
			switch p.Role {
			case "user":
				if !isInjected(text) {
					turn = Turn{Prompt: text}
					pending.reset()
				}
			case "assistant":
				turn.Output = text
			}
		case e.Type == "response_item" && (p.Type == "function_call" || p.Type == "custom_tool_call"):
			pending.add(p.CallID, summarizeTool(p.Name, json.RawMessage(p.Arguments)))
		case e.Type == "response_item" && (p.Type == "function_call_output" || p.Type == "custom_tool_call_output"):
			pending.resolve(p.CallID)
		case e.Type == "event_msg" && p.Type == "task_complete":
			if t := strings.TrimSpace(p.LastAgentMessage); t != "" {
				turn.Output = t
			}
		}
	}
	turn.Pending = pending.last()
	return turn
}
