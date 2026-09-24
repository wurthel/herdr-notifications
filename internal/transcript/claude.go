package transcript

import (
	"encoding/json"
	"regexp"
	"strings"
)

type claudeEntry struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Message     struct {
		ID      string          `json:"id"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
}

var (
	commandNameRE = regexp.MustCompile(`(?s)<command-name>(.*?)</command-name>`)
	commandArgsRE = regexp.MustCompile(`(?s)<command-args>(.*?)</command-args>`)
	bashInputRE   = regexp.MustCompile(`(?s)^<bash-input>(.*)</bash-input>$`)
)

func parseClaude(lines [][]byte) Turn {
	var (
		turn      Turn
		textMsgID string
		pending   pendingCalls
	)
	for _, line := range lines {
		var e claudeEntry
		if json.Unmarshal(line, &e) != nil || e.IsSidechain {
			continue
		}
		switch e.Type {
		case "user":
			text, results := claudeUserContent(e.Message.Content)
			for _, id := range results {
				pending.resolve(id)
			}
			if e.IsMeta || len(results) > 0 {
				continue
			}
			if prompt, ok := claudePrompt(text); ok {
				turn = Turn{Prompt: prompt}
				textMsgID = ""
				pending.reset()
			}
		case "assistant":
			var blocks []claudeBlock
			if json.Unmarshal(e.Message.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				switch b.Type {
				case "text":
					t := strings.TrimSpace(b.Text)
					if t == "" {
						continue
					}
					if e.Message.ID != "" && e.Message.ID == textMsgID {
						turn.Output += "\n\n" + t
					} else {
						textMsgID, turn.Output = e.Message.ID, t
					}
				case "tool_use":
					pending.add(b.ID, summarizeTool(b.Name, b.Input))
				}
			}
		}
	}
	turn.Pending = pending.last()
	return turn
}

// claudeUserContent returns the text of a user entry and the ids of any tool
// results it carries.
func claudeUserContent(raw json.RawMessage) (string, []string) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var blocks []claudeBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return "", nil
	}
	var texts, results []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "tool_result":
			results = append(results, b.ToolUseID)
		}
	}
	return strings.Join(texts, "\n"), results
}

// claudePrompt turns a user entry into what the user actually typed, and
// reports false for injected or synthetic messages.
func claudePrompt(text string) (string, bool) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return "", false
	case strings.HasPrefix(text, "[Request interrupted"):
		return "", false
	case strings.Contains(text, "<command-name>"):
		name := strings.TrimSpace(firstGroup(commandNameRE, text))
		args := strings.TrimSpace(firstGroup(commandArgsRE, text))
		if name == "" {
			return "", false
		}
		if args != "" {
			name += " " + args
		}
		return name, true
	}
	if m := bashInputRE.FindStringSubmatch(text); m != nil {
		return "! " + strings.TrimSpace(m[1]), true
	}
	if isInjected(text) {
		return "", false
	}
	return text, true
}

func firstGroup(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}
