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
	maxAgentLen   = 64
	maxLabelLen   = 100
	maxTitleLen   = 300
	maxPromptLen  = 400
	maxPendingLen = 600
)

type Message struct {
	Status    string
	Agent     string
	Title     string
	Workspace string
	Tab       string

	// Prompt, Output and Pending come from the agent's transcript. When all
	// are empty the message falls back to Tail, the raw pane output.
	Prompt  string
	Output  string
	Pending string
	Tail    string
}

// HasTurn reports whether there is transcript content to show; Pending only
// counts for blocked, the one status that displays it.
func (m Message) HasTurn() bool {
	if strings.TrimSpace(m.Prompt+m.Output) != "" {
		return true
	}
	return m.Status == "blocked" && strings.TrimSpace(m.Pending) != ""
}

var ansiRE = regexp.MustCompile(
	`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\|$)` + // OSC
		`|\x1b[P_^X][^\x1b]*(?:\x1b\\|$)` + // DCS, APC, PM, SOS
		`|\x1b\[[0-?]*[ -/]*[@-~]` + // CSI
		`|\x1b[ -/]*[0-~]`) // other escapes

// HTML renders m for Telegram's HTML parse mode within MaxMessageLen: the
// prompt and pending tool call are clipped, and the agent's response (as
// Markdown converted to HTML) gets the remaining space.
func (m Message) HTML() string {
	head := m.headerLines(html.EscapeString, "<b>", "</b>", "<i>", "</i>")
	if !m.HasTurn() {
		return withTail(head, m.Tail, "<pre>", "</pre>", html.EscapeString)
	}
	var before, after []string
	if p := strings.TrimSpace(m.Prompt); p != "" {
		before = append(before, "👤 <b>You</b>\n<blockquote>"+html.EscapeString(clip(p, maxPromptLen))+"</blockquote>")
	}
	if p := strings.TrimSpace(m.Pending); p != "" && m.Status == "blocked" {
		after = append(after, "❓ <b>Waiting for</b>\n<blockquote>"+html.EscapeString(clip(p, maxPendingLen))+"</blockquote>")
	}
	outHead := "🤖 <b>" + html.EscapeString(clip(m.Agent, maxAgentLen)) + "</b>\n<blockquote expandable>"
	outTail := "</blockquote>"
	return m.assemble(head, before, after, outHead, outTail, markdownToHTML)
}

// Plain renders m without markup, used when Telegram rejects the HTML.
func (m Message) Plain() string {
	id := func(s string) string { return s }
	head := m.headerLines(id, "", "", "", "")
	if !m.HasTurn() {
		return withTail(head, m.Tail, "", "", id)
	}
	var before, after []string
	if p := strings.TrimSpace(m.Prompt); p != "" {
		before = append(before, "👤 You:\n"+clip(p, maxPromptLen))
	}
	if p := strings.TrimSpace(m.Pending); p != "" && m.Status == "blocked" {
		after = append(after, "❓ Waiting for:\n"+clip(p, maxPendingLen))
	}
	return m.assemble(head, before, after, "🤖 "+clip(m.Agent, maxAgentLen)+":\n", "", id)
}

// assemble joins the sections and fits the agent output, rendered by render,
// into whatever space the other sections leave.
func (m Message) assemble(head string, before, after []string, outHead, outTail string, render func(string) string) string {
	const sep = "\n\n"
	fixed := append(append([]string{head}, before...), after...)
	used := textLen(strings.Join(fixed, sep))
	if used > MaxMessageLen {
		return truncateRunes(head, MaxMessageLen)
	}

	sections := append([]string{head}, before...)
	if out := strings.TrimSpace(m.Output); out != "" {
		budget := MaxMessageLen - used - textLen(sep+outHead+outTail)
		if body, ok := fitRendered(out, budget, render); ok {
			sections = append(sections, outHead+body+outTail)
		}
	}
	return strings.Join(append(sections, after...), sep)
}

// fitRendered returns render of the longest prefix of src (plus "…" when cut)
// whose rendering fits in budget. The cut happens on the source, so the
// renderer always sees complete input and produces balanced markup.
func fitRendered(src string, budget int, render func(string) string) (string, bool) {
	if budget <= 0 {
		return "", false
	}
	if out := render(src); textLen(out) <= budget {
		return out, true
	}
	runes := []rune(src)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if textLen(render(cutPrefix(runes, mid))) <= budget {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo == 0 {
		return "", false
	}
	return render(cutPrefix(runes, lo)), true
}

// cutPrefix returns the first n runes, backing up to the last line break or
// space in the final 20% so words stay whole, followed by "…".
func cutPrefix(runes []rune, n int) string {
	s := string(runes[:n])
	if i := strings.LastIndexAny(s, "\n "); i > len(s)*4/5 {
		s = s[:i]
	}
	s = strings.TrimRight(s, " \n")
	// A marker appended to a ``` line would be consumed as part of the fence.
	if mdFenceRE.MatchString(s[strings.LastIndexByte(s, '\n')+1:]) {
		return s + "\n…"
	}
	return s + " …"
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

// CleanTail strips ANSI sequences, control characters and decoration lines
// (box drawing, rules), collapses blank runs and trims trailing whitespace.
func CleanTail(s string) string {
	s = ansiRE.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var out []string
	for _, l := range strings.Split(s, "\n") {
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
		l = strings.TrimRight(l, " \t")
		if isDecoration(l) {
			continue
		}
		if l == "" && len(out) > 0 && out[len(out)-1] == "" {
			continue
		}
		out = append(out, l)
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}

// isDecoration reports lines made only of box-drawing, block or rule
// characters, such as TUI borders and input-box separators.
func isDecoration(l string) bool {
	n := 0
	for _, r := range l {
		switch {
		case r == ' ' || r == '\t':
			continue
		case r >= 0x2500 && r <= 0x259F, r == '_', r == '-', r == '=', r == '~', r == '·':
			n++
		default:
			return false
		}
	}
	return n >= 3
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
