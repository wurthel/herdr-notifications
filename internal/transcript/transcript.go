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
	"time"
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

// maxCwdCandidates bounds how many recent logs LocateByCwd opens.
const maxCwdCandidates = 32

// LocateByCwd returns the most recently written Codex log started in cwd and
// modified after since, or "". herdr does not report a session id for Codex
// panes, so the working directory is the best available link to the log.
func LocateByCwd(agent, cwd string, since time.Time, dirs Dirs) string {
	if agent != "codex" || cwd == "" {
		return ""
	}
	cwd = filepath.Clean(cwd)
	type candidate struct {
		path string
		mod  time.Time
	}
	var cands []candidate
	for _, d := range dirs.Codex {
		matches, _ := filepath.Glob(filepath.Join(d, "*", "*", "*", "rollout-*.jsonl"))
		for _, m := range matches {
			if info, err := os.Stat(m); err == nil && info.ModTime().After(since) {
				cands = append(cands, candidate{m, info.ModTime()})
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })
	for i, c := range cands {
		if i == maxCwdCandidates {
			break
		}
		if codexCwd(c.path) == cwd {
			return c.path
		}
	}
	return ""
}

// codexCwd returns the working directory from a Codex log's session_meta
// header, or "".
func codexCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(f, 64<<10).ReadBytes('\n')
	if err != nil && err != io.EOF {
		return ""
	}
	var e struct {
		Type    string `json:"type"`
		Payload struct {
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &e) != nil || e.Type != "session_meta" || e.Payload.Cwd == "" {
		return ""
	}
	return filepath.Clean(e.Payload.Cwd)
}

const (
	maxContinuations   = 8
	continuationWindow = 64 << 10
)

// Resolve follows Claude Code's "continued-in" markers from the log at path
// to the file the session now writes to. Claude Code can move a running
// session to a new id while herdr keeps reporting the old one, which would
// otherwise freeze notifications on the last turn of the abandoned file.
func Resolve(agent, path string, dirs Dirs) string {
	if agent != "claude" || path == "" {
		return path
	}
	seen := map[string]bool{path: true}
	for range maxContinuations {
		id := continuedIn(path)
		if id == "" {
			return path
		}
		next := Locate(agent, id, Dirs{Claude: []string{filepath.Dir(filepath.Dir(path))}})
		if next == "" {
			next = Locate(agent, id, dirs)
		}
		if next == "" || seen[next] {
			return path
		}
		seen[next] = true
		path = next
	}
	return path
}

// continuedIn returns the session id from the last "continued-in" entry near
// the end of a Claude Code log, or "".
func continuedIn(path string) string {
	lines, err := tailLines(path, continuationWindow)
	if err != nil {
		return ""
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if !bytes.Contains(lines[i], []byte(`"continued-in"`)) {
			continue
		}
		var e struct {
			Type string `json:"type"`
			ID   string `json:"continuedInSessionId"`
		}
		if json.Unmarshal(lines[i], &e) == nil && e.Type == "continued-in" && e.ID != "" {
			return e.ID
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
