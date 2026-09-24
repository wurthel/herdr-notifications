// Package transcript extracts the last user prompt and agent response from
// the session logs that Claude Code and Codex keep on disk.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Turn is the latest exchange in a session.
type Turn struct {
	Prompt string
	Output string
	// Pending describes a tool call still waiting for a result, e.g. the
	// approval or question a blocked agent is showing.
	Pending string
}

func (t Turn) Empty() bool {
	return t.Prompt == "" && t.Output == "" && t.Pending == ""
}

// Dirs are the directories that hold session logs.
type Dirs struct {
	Claude []string // e.g. ~/.claude/projects
	Codex  []string // e.g. ~/.codex/sessions
}

// DefaultDirs honours CLAUDE_CONFIG_DIR and CODEX_HOME, falling back to the
// standard locations under home.
func DefaultDirs(getenv func(string) string, home string) Dirs {
	var d Dirs
	if v := getenv("CLAUDE_CONFIG_DIR"); v != "" {
		d.Claude = append(d.Claude, filepath.Join(v, "projects"))
	}
	if v := getenv("CODEX_HOME"); v != "" {
		d.Codex = append(d.Codex, filepath.Join(v, "sessions"))
	}
	if home != "" {
		d.Claude = append(d.Claude, filepath.Join(home, ".claude", "projects"))
		d.Codex = append(d.Codex, filepath.Join(home, ".codex", "sessions"))
	}
	return d
}

var sessionIDRE = regexp.MustCompile(`^[A-Za-z0-9-]{8,128}$`)

// Locate returns the log file for an agent session, or "" if none is found.
func Locate(agent, sessionID string, dirs Dirs) string {
	if !sessionIDRE.MatchString(sessionID) {
		return ""
	}
	var patterns []string
	switch agent {
	case "claude":
		for _, d := range dirs.Claude {
			patterns = append(patterns, filepath.Join(d, "*", sessionID+".jsonl"))
		}
	case "codex":
		for _, d := range dirs.Codex {
			patterns = append(patterns, filepath.Join(d, "*", "*", "*", "rollout-*-"+sessionID+".jsonl"))
		}
	}
	for _, p := range patterns {
		matches, _ := filepath.Glob(p)
		if len(matches) > 0 {
			sort.Strings(matches)
			return matches[len(matches)-1]
		}
	}
	return ""
}

// Read parses the log at path in the format of the given agent.
func Read(agent, path string) (Turn, error) {
	lines, err := tailLines(path, maxTailBytes)
	if err != nil {
		return Turn{}, err
	}
	switch agent {
	case "claude":
		return parseClaude(lines), nil
	case "codex":
		return parseCodex(lines), nil
	}
	return Turn{}, fmt.Errorf("unsupported agent %q", agent)
}

const maxTailBytes = 8 << 20

// tailLines returns the complete JSONL lines within the last max bytes.
func tailLines(path string, max int64) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// Start one byte early so a window that begins exactly on a line start
	// skips only the preceding newline, not a complete line.
	start := info.Size() - max - 1
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	if start > 0 {
		// Skip the partial first line.
		if _, err := r.ReadBytes('\n'); err != nil {
			return nil, nil
		}
	}
	var lines [][]byte
	for {
		line, err := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			lines = append(lines, line)
		}
		if err == io.EOF {
			return lines, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// wrappedRE matches messages that are entirely a single XML-like block, which
// agents use for injected context (<environment_context>, <task-notification>…).
var wrappedRE = regexp.MustCompile(`(?s)^<([A-Za-z][\w-]*)[^>]*>.*</([A-Za-z][\w-]*)>$`)

func isInjected(text string) bool {
	text = strings.TrimSpace(text)
	return wrappedRE.MatchString(text)
}

// pendingCalls tracks tool calls that have no result yet, in call order, so
// parallel calls resolve independently.
type pendingCalls struct {
	ids  []string
	text map[string]string
}

func (p *pendingCalls) add(id, text string) {
	if p.text == nil {
		p.text = map[string]string{}
	}
	if _, ok := p.text[id]; !ok {
		p.ids = append(p.ids, id)
	}
	p.text[id] = text
}

func (p *pendingCalls) resolve(id string) {
	if _, ok := p.text[id]; !ok {
		return
	}
	delete(p.text, id)
	for i, v := range p.ids {
		if v == id {
			p.ids = append(p.ids[:i], p.ids[i+1:]...)
			break
		}
	}
}

func (p *pendingCalls) reset() { *p = pendingCalls{} }

// last returns the most recent unresolved call.
func (p *pendingCalls) last() string {
	if len(p.ids) == 0 {
		return ""
	}
	return p.text[p.ids[len(p.ids)-1]]
}

func summarizeTool(name string, input json.RawMessage) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	str := func(k string) string {
		s, _ := in[k].(string)
		return strings.TrimSpace(s)
	}
	switch name {
	case "AskUserQuestion":
		var b strings.Builder
		qs, _ := in["questions"].([]any)
		for _, q := range qs {
			qm, _ := q.(map[string]any)
			text, _ := qm["question"].(string)
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(text)
			opts, _ := qm["options"].([]any)
			for _, o := range opts {
				om, _ := o.(map[string]any)
				if label, _ := om["label"].(string); label != "" {
					b.WriteString("\n• " + label)
				}
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	case "ExitPlanMode":
		return "Plan approval"
	}
	for _, k := range []string{"command", "cmd", "file_path", "path", "url", "pattern", "description"} {
		if s := str(k); s != "" {
			return name + ": " + s
		}
	}
	if s, ok := in["command"].([]any); ok && len(s) > 0 {
		parts := make([]string, 0, len(s))
		for _, p := range s {
			parts = append(parts, fmt.Sprint(p))
		}
		return name + ": " + strings.Join(parts, " ")
	}
	return name
}
