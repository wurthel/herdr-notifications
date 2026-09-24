package notify

import (
	"fmt"
	"html"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMessageHTML(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want string
	}{
		{
			name: "done with everything",
			msg:  Message{Status: "done", Agent: "Claude Code", Title: "fix bug", Workspace: "api", Tab: "main", Tail: "ok\n"},
			want: "✅ <b>Claude Code</b> finished\n📁 api › main\n<i>fix bug</i>\n\n<pre>ok</pre>",
		},
		{
			name: "blocked",
			msg:  Message{Status: "blocked", Agent: "Codex"},
			want: "⏸ <b>Codex</b> needs input",
		},
		{
			name: "other status",
			msg:  Message{Status: "idle", Agent: "Codex"},
			want: "ℹ️ <b>Codex</b>: idle",
		},
		{
			name: "escapes everything",
			msg: Message{
				Status: "done", Agent: "a<b>&c", Title: `t "x" > 'y'`,
				Workspace: "w&<1>", Tab: "t>2", Tail: "if a < b && c > d {",
			},
			want: "✅ <b>a&lt;b&gt;&amp;c</b> finished\n📁 w&amp;&lt;1&gt; › t&gt;2\n" +
				"<i>t &#34;x&#34; &gt; &#39;y&#39;</i>\n\n<pre>if a &lt; b &amp;&amp; c &gt; d {</pre>",
		},
		{
			name: "escapes unknown status",
			msg:  Message{Status: "<x>", Agent: "A"},
			want: "ℹ️ <b>A</b>: &lt;x&gt;",
		},
		{
			name: "title equal to agent omitted",
			msg:  Message{Status: "done", Agent: "Claude Code", Title: "  claude code "},
			want: "✅ <b>Claude Code</b> finished",
		},
		{
			name: "only workspace",
			msg:  Message{Status: "done", Agent: "A", Workspace: "api", Tab: "  "},
			want: "✅ <b>A</b> finished\n📁 api",
		},
		{
			name: "only tab",
			msg:  Message{Status: "done", Agent: "A", Tab: "main"},
			want: "✅ <b>A</b> finished\n📁 main",
		},
		{
			name: "blank tail has no pre",
			msg:  Message{Status: "done", Agent: "A", Tail: " \n\x1b[0m\n\r\n"},
			want: "✅ <b>A</b> finished",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.msg.HTML(); got != tt.want {
				t.Errorf("HTML() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestMessagePlain(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want string
	}{
		{
			name: "done unescaped",
			msg:  Message{Status: "done", Agent: "a<b>&c", Title: "x & y", Workspace: "api", Tab: "main", Tail: "a < b\n"},
			want: "✅ a<b>&c finished\n📁 api › main\nx & y\n\na < b",
		},
		{
			name: "blocked",
			msg:  Message{Status: "blocked", Agent: "Codex"},
			want: "⏸ Codex needs input",
		},
		{
			name: "other",
			msg:  Message{Status: "working", Agent: "Codex", Title: "codex"},
			want: "ℹ️ Codex: working",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.msg.Plain(); got != tt.want {
				t.Errorf("Plain() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestCleanTail(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "hello\nworld", "hello\nworld"},
		{"csi color", "\x1b[1;31mred\x1b[0m text", "red text"},
		{"csi private mode", "\x1b[?25lhidden\x1b[?25h", "hidden"},
		{"csi cursor", "a\x1b[2Kb\x1b[10;20Hc", "abc"},
		{"osc bel", "\x1b]0;window title\x07prompt", "prompt"},
		{"osc st", "\x1b]8;;https://x.test\x1b\\link\x1b]8;;\x1b\\", "link"},
		{"two byte escape", "a\x1bMb", "ab"},
		{"crlf", "a\r\nb\r\n", "a\nb"},
		{"lone cr keeps last redraw", "progress 10%\rprogress 100%", "progress 100%"},
		{"csi modifyOtherKeys", "\x1b[>4;2ma\x1b[<ub", "ab"},
		{"csi colon truecolor", "\x1b[38:2:255:0:0mred", "red"},
		{"dcs payload", "a\x1bPq#0;2;0;0;0\x1b\\b", "ab"},
		{"apc kitty graphics", "a\x1b_Gf=100;AAAA\x1b\\b", "ab"},
		{"unterminated osc", "a\x1b]0;title", "a"},
		{"c1 controls", "a\u009b31mb\u0085c", "a31mbc"},
		{"control chars", "a\x00b\x07c\x08d\x7fe", "abcde"},
		{"tab kept", "a\tb", "a\tb"},
		{"trailing spaces", "a   \nb\t \n", "a\nb"},
		{"leading and trailing blank lines", "\n\n  \n\ta\n\nb\n \n\n", "\ta\n\nb"},
		{"only blanks", "  \n\t\n\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CleanTail(tt.in); got != tt.want {
				t.Errorf("CleanTail(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func preBody(t *testing.T, s string) string {
	t.Helper()
	start := strings.Index(s, "<pre>")
	end := strings.LastIndex(s, "</pre>")
	if start < 0 || end < start || !strings.HasSuffix(s, "</pre>") {
		t.Fatalf("no <pre> block in message (len %d): %.200q", utf8.RuneCountInString(s), s)
	}
	return s[start+len("<pre>") : end]
}

func TestMessageTruncation(t *testing.T) {
	var many strings.Builder
	for i := 1; i <= 400; i++ {
		fmt.Fprintf(&many, "line %05d <tag> & stuff\n", i)
	}
	tests := []struct {
		name     string
		tail     string
		wantEnd  string
		notWant  string
		fitsFull bool
	}{
		{name: "many lines", tail: many.String(), wantEnd: "line 00400 &lt;tag&gt; &amp; stuff", notWant: "line 00001"},
		{name: "single huge line", tail: strings.Repeat("x", 10000) + "END", wantEnd: "xxxEND"},
		{name: "single huge line of ampersands", tail: strings.Repeat("&", 5000), wantEnd: "&amp;"},
		{name: "single huge line of angle brackets", tail: strings.Repeat("<>", 3000) + "!", wantEnd: "&lt;&gt;!"},
		{name: "many lines ending in entity-heavy line", tail: "first\nsecond\n" + strings.Repeat("&", 4000), wantEnd: "&amp;", notWant: "first"},
		{name: "multibyte", tail: strings.Repeat("жэ", 5000) + "Ω", wantEnd: "жэΩ"},
		{name: "just fits", tail: strings.Repeat("y", 3000), wantEnd: "yyy", fitsFull: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Message{Status: "done", Agent: "Claude <Code>", Title: "t & t", Workspace: "api", Tab: "main", Tail: tt.tail}
			got := m.HTML()
			if n := utf8.RuneCountInString(got); n > MaxMessageLen {
				t.Fatalf("HTML rune count = %d, want <= %d", n, MaxMessageLen)
			}
			body := preBody(t, got)
			if tt.fitsFull {
				if body != html.EscapeString(tt.tail) {
					t.Fatalf("expected untruncated tail")
				}
			} else if !strings.HasPrefix(body, "…\n") {
				t.Errorf("body does not start with truncation marker: %.40q", body)
			}
			if !strings.HasSuffix(body, tt.wantEnd) {
				t.Errorf("body does not end with %q: ...%q", tt.wantEnd, body[max(0, len(body)-60):])
			}
			if tt.notWant != "" && strings.Contains(body, tt.notWant) {
				t.Errorf("body unexpectedly contains %q", tt.notWant)
			}
			if html.EscapeString(html.UnescapeString(body)) != body {
				t.Errorf("body contains a split or unbalanced HTML entity")
			}
			if strings.Count(body, "&") != strings.Count(body, "&amp;")+strings.Count(body, "&lt;")+strings.Count(body, "&gt;") {
				t.Errorf("body contains a dangling '&'")
			}
			// Use most of the budget, so truncation is not overly aggressive.
			if n := utf8.RuneCountInString(got); !tt.fitsFull && n < MaxMessageLen-64 {
				t.Errorf("HTML rune count = %d, expected close to %d", n, MaxMessageLen)
			}

			plain := m.Plain()
			if n := utf8.RuneCountInString(plain); n > MaxMessageLen {
				t.Fatalf("Plain rune count = %d, want <= %d", n, MaxMessageLen)
			}
			if wantPlainEnd := html.UnescapeString(tt.wantEnd); !strings.HasSuffix(plain, wantPlainEnd) {
				t.Errorf("Plain does not end with %q", wantPlainEnd)
			}
		})
	}
}

func TestMessageHugeHeaderFieldsAreClipped(t *testing.T) {
	m := Message{
		Status:    "done",
		Agent:     strings.Repeat("a", 500),
		Workspace: strings.Repeat("&", 500),
		Tab:       strings.Repeat("<", 500),
		Title:     strings.Repeat("t", 5000),
		Tail:      "tail",
	}
	got := m.HTML()
	if n := utf8.RuneCountInString(got); n > MaxMessageLen {
		t.Fatalf("rune count = %d, want <= %d", n, MaxMessageLen)
	}
	if !strings.Contains(got, "<pre>tail</pre>") {
		t.Errorf("tail should survive clipped header fields:\n%s", got)
	}
	if !strings.Contains(got, strings.Repeat("t", maxTitleLen-1)+"…</i>") {
		t.Errorf("title not clipped to %d runes", maxTitleLen)
	}
	if strings.Contains(got, strings.Repeat("a", maxAgentLen)) {
		t.Errorf("agent not clipped to %d runes", maxAgentLen)
	}
}

func TestMessageTruncationCountsUTF16(t *testing.T) {
	tail := strings.Repeat("🔥", 5000)
	for _, render := range []func(Message) string{Message.HTML, Message.Plain} {
		got := render(Message{Status: "done", Agent: "A", Tail: tail})
		if n := textLen(got); n > MaxMessageLen {
			t.Errorf("UTF-16 length = %d, want <= %d", n, MaxMessageLen)
		}
		if !strings.Contains(got, "🔥🔥") {
			t.Errorf("tail dropped entirely")
		}
	}
}
