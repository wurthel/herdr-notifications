package transcript

import (
	"encoding/json"
	"testing"
)

func xMessage(role string, texts ...string) obj {
	kind := "input_text"
	if role == "assistant" {
		kind = "output_text"
	}
	content := make([]obj, 0, len(texts))
	for _, s := range texts {
		content = append(content, obj{"type": kind, "text": s})
	}
	return obj{"type": "response_item", "payload": obj{"type": "message", "role": role, "content": content}}
}

func xCall(t *testing.T, name, callID string, args obj) obj {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return obj{"type": "response_item", "payload": obj{
		"type": "function_call", "name": name, "arguments": string(raw), "call_id": callID,
	}}
}

func xCallOutput(callID string) obj {
	return obj{"type": "response_item", "payload": obj{
		"type": "function_call_output", "call_id": callID, "output": "ok",
	}}
}

func xTaskComplete(msg string) obj {
	return obj{"type": "event_msg", "payload": obj{"type": "task_complete", "last_agent_message": msg}}
}

var codexNoise = []any{
	obj{"type": "session_meta", "payload": obj{"id": "abc", "cwd": "/w"}},
	obj{"type": "turn_context", "payload": obj{"cwd": "/w", "model": "gpt"}},
	obj{"type": "event_msg", "payload": obj{"type": "token_count"}},
	obj{"type": "event_msg", "payload": obj{"type": "user_message", "message": "ignored"}},
	obj{"type": "response_item", "payload": obj{"type": "reasoning", "summary": []obj{}}},
}

func TestCodexTurn(t *testing.T) {
	entries := append([]any{}, codexNoise...)
	entries = append(entries,
		xMessage("developer", "<permissions instructions>be careful</permissions instructions>"),
		xMessage("user", "<environment_context>\n  <cwd>/w</cwd>\n</environment_context>"),
		xMessage("user", "first prompt"),
		xMessage("assistant", "first answer"),
		xMessage("user", "second", "prompt"),
		xMessage("developer", "developer note"),
		xMessage("assistant", "thinking out loud"),
		xCall(t, "shell", "c1", obj{"command": []string{"ls", "-la"}}),
		xCallOutput("c1"),
		xMessage("assistant", "Here is the result."),
	)
	entries = append(entries, codexNoise...)
	checkTurn(t, readFixture(t, "codex", entries...), Turn{
		Prompt: "second\nprompt",
		Output: "Here is the result.",
	})
}

func TestCodexTaskCompleteOverridesOutput(t *testing.T) {
	checkTurn(t, readFixture(t, "codex",
		xMessage("user", "prompt"),
		xMessage("assistant", "streamed"),
		xTaskComplete("  final  "),
	), Turn{Prompt: "prompt", Output: "final"})
}

func TestCodexEmptyTaskCompleteKeepsOutput(t *testing.T) {
	checkTurn(t, readFixture(t, "codex",
		xMessage("user", "prompt"),
		xMessage("assistant", "streamed"),
		xTaskComplete(""),
	), Turn{Prompt: "prompt", Output: "streamed"})
}

func TestCodexInjectedUserMessageKeepsTurn(t *testing.T) {
	checkTurn(t, readFixture(t, "codex",
		xMessage("user", "prompt"),
		xMessage("assistant", "answer"),
		xMessage("user", "<environment_context>\n  <cwd>/other</cwd>\n</environment_context>"),
		xMessage("user", "   "),
	), Turn{Prompt: "prompt", Output: "answer"})
}

func TestCodexPending(t *testing.T) {
	tests := []struct {
		name    string
		entries func(t *testing.T) []any
		want    string
	}{
		{
			name: "array command",
			entries: func(t *testing.T) []any {
				return []any{xCall(t, "shell", "c1", obj{"command": []string{"ls", "-la"}, "workdir": "/w"})}
			},
			want: "shell: ls -la",
		},
		{
			name: "string cmd",
			entries: func(t *testing.T) []any {
				return []any{xCall(t, "exec_command", "c1", obj{"cmd": "cargo test"})}
			},
			want: "exec_command: cargo test",
		},
		{
			name: "cleared by output",
			entries: func(t *testing.T) []any {
				return []any{xCall(t, "shell", "c1", obj{"command": []string{"ls"}}), xCallOutput("c1")}
			},
		},
		{
			name: "output for other call keeps pending",
			entries: func(t *testing.T) []any {
				return []any{xCall(t, "shell", "c2", obj{"command": []string{"make"}}), xCallOutput("c1")}
			},
			want: "shell: make",
		},
		{
			name: "custom tool call",
			entries: func(t *testing.T) []any {
				return []any{
					obj{"type": "response_item", "payload": obj{"type": "custom_tool_call", "name": "apply_patch", "call_id": "c3", "arguments": "*** Begin Patch"}},
				}
			},
			want: "apply_patch",
		},
		{
			name: "custom tool call output clears",
			entries: func(t *testing.T) []any {
				return []any{
					obj{"type": "response_item", "payload": obj{"type": "custom_tool_call", "name": "apply_patch", "call_id": "c3"}},
					obj{"type": "response_item", "payload": obj{"type": "custom_tool_call_output", "call_id": "c3"}},
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := append([]any{xMessage("user", "prompt")}, tt.entries(t)...)
			checkTurn(t, readFixture(t, "codex", entries...), Turn{Prompt: "prompt", Pending: tt.want})
		})
	}
}

func TestCodexNewPromptClearsPending(t *testing.T) {
	checkTurn(t, readFixture(t, "codex",
		xMessage("user", "first"),
		xMessage("assistant", "answer"),
		xCall(t, "shell", "c1", obj{"command": []string{"rm", "x"}}),
		xMessage("user", "second"),
	), Turn{Prompt: "second"})
}

func TestCodexMalformedLinesIgnored(t *testing.T) {
	checkTurn(t, readFixture(t, "codex",
		`{"type":"response_item","payload":`,
		xMessage("user", "prompt"),
		`nope`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":"not an array"}}`,
		xMessage("assistant", "answer"),
	), Turn{Prompt: "prompt", Output: "answer"})
}
