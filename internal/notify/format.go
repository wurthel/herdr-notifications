package notify

import (
	"html"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxMessageLen is Telegram's sendMessage text limit in UTF-16 code units.
const MaxMessageLen = 4096

const truncatedMarker = "…\n"

// Header fields are clipped before escaping so the header stays far below
// MaxMessageLen and truncation never has to cut through markup.
const (
	maxAgentLen = 64
	maxLabelLen = 100
	maxTitleLen = 300
)

type Message struct {
	Status    string
	Agent     string
	Title     string
	Workspace string
	Tab       string
	Tail      string
}

var ansiRE = regexp.MustCompile(
	`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\|$)` + // OSC
		`|\x1b[P_^X][^\x1b]*(?:\x1b\\|$)` + // DCS, APC, PM, SOS
		`|\x1b\[[0-?]*[ -/]*[@-~]` + // CSI
		`|\x1b[ -/]*[0-~]`) // other escapes

// HTML renders m for Telegram's HTML parse mode, truncating the pane tail
// from its start so the whole message fits in MaxMessageLen.
func (m Message) HTML() string {
	head := m.headerLines(html.EscapeString, "<b>", "</b>", "<i>", "</i>")
	return withTail(head, m.Tail, "<pre>", "</pre>", html.EscapeString)
}

// Plain renders m without markup, used when Telegram rejects the HTML.
func (m Message) Plain() string {
	id := func(s string) string { return s }
	head := m.headerLines(id, "", "", "", "")
	return withTail(head, m.Tail, "", "", id)
}

func (m Message) headerLines(esc func(string) string, bOpen, bClose, iOpen, iClose string) string {
	agent := bOpen + esc(clip(m.Agent, maxAgentLen)) + bClose
	var lines []string
	switch m.Status {
	case "done":
		lines = append(lines, "✅ "+agent+" finished")
	case "blocked":
		lines = append(lines, "⏸ "+agent+" needs input")
	default:
		lines = append(lines, "ℹ️ "+agent+": "+esc(m.Status))
	}
	var loc []string
	for _, s := range []string{m.Workspace, m.Tab} {
		if s = strings.TrimSpace(s); s != "" {
			loc = append(loc, esc(clip(s, maxLabelLen)))
		}
	}
	if len(loc) > 0 {
		lines = append(lines, "📁 "+strings.Join(loc, " › "))
	}
	if t := strings.TrimSpace(m.Title); t != "" && !strings.EqualFold(t, m.Agent) {
		lines = append(lines, iOpen+esc(clip(t, maxTitleLen))+iClose)
	}
	return strings.Join(lines, "\n")
}

func withTail(head, tail, open, close string, esc func(string) string) string {
	tail = CleanTail(tail)
	if tail == "" {
		return truncateRunes(head, MaxMessageLen)
	}
	budget := MaxMessageLen - textLen(head) - textLen(open+close) - 2
	body, ok := fitTail(tail, budget, esc)
	if !ok {
		return truncateRunes(head, MaxMessageLen)
	}
	return head + "\n\n" + open + body + close
}

// fitTail escapes tail and drops leading lines (then leading runes of the
// last line) until the escaped result fits in budget. Truncation happens on
// the raw text so an HTML entity is never split.
func fitTail(tail string, budget int, esc func(string) string) (string, bool) {
	if budget <= textLen(truncatedMarker) {
		return "", false
	}
	if s := esc(tail); textLen(s) <= budget {
		return s, true
	}
	budget -= textLen(truncatedMarker)
	lines := strings.Split(tail, "\n")
	start, used := len(lines), 0
	for start > 0 {
		w := textLen(esc(lines[start-1]))
		if start < len(lines) {
			w++
		}
		if used+w > budget {
			break
		}
		used += w
		start--
	}
	if start < len(lines) {
		return esc(truncatedMarker) + esc(strings.Join(lines[start:], "\n")), true
	}
	runes := []rune(lines[len(lines)-1])
	start, used = len(runes), 0
	for start > 0 {
		w := textLen(esc(string(runes[start-1])))
		if used+w > budget {
			break
		}
		used += w
		start--
	}
	if start == len(runes) {
		return "", false
	}
	return esc(truncatedMarker) + esc(string(runes[start:])), true
}

// CleanTail strips ANSI sequences and control characters and trims blank
// lines and trailing whitespace.
func CleanTail(s string) string {
	s = ansiRE.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		// A bare \r redraws the line (progress bars); keep only the final state.
		if j := strings.LastIndexByte(l, '\r'); j >= 0 {
			l = l[j+1:]
		}
		l = strings.Map(func(r rune) rune {
			if r == '\t' {
				return r
			}
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
				return -1
			}
			return r
		}, l)
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func truncateRunes(s string, n int) string {
	if textLen(s) <= n {
		return s
	}
	used := 0
	for i, r := range s {
		used += utf16Len(r)
		if used > n {
			return s[:i]
		}
	}
	return s
}

// textLen counts UTF-16 code units, the unit Telegram uses for text limits.
func textLen(s string) int {
	n := 0
	for _, r := range s {
		n += utf16Len(r)
	}
	return n
}

func utf16Len(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}
