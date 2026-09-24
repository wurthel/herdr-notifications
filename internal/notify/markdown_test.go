package notify

import (
	"regexp"
	"strings"
	"testing"
)

func TestMarkdownToHTML(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain escaped", "if a < b && c > d", "if a &lt; b &amp;&amp; c &gt; d"},
		{"quotes escaped", `say "hi" it's`, "say &#34;hi&#34; it&#39;s"},
		{"inline code", "run `go test` now", "run <code>go test</code> now"},
		{"inline code escaped and not bold", "`**x** & <y>`", "<code>**x** &amp; &lt;y&gt;</code>"},
		{"two code spans", "`a` and `b`", "<code>a</code> and <code>b</code>"},
		{"unclosed backtick", "a `b", "a `b"},
		{"bold", "**bold** text", "<b>bold</b> text"},
		{"bold escaped", "**a<b**", "<b>a&lt;b</b>"},
		{"two bolds", "**a** and **b**", "<b>a</b> and <b>b</b>"},
		{"unclosed bold", "**open", "**open"},
		{"dunder not bold", "edit __init__.py and __main__", "edit __init__.py and __main__"},
		{"single star not bold", "a *b* c", "a *b* c"},
		{"heading", "# Title", "<b>Title</b>"},
		{"heading level 3 closing hashes", "### Sub section ###", "<b>Sub section</b>"},
		{"heading with inline", "## Use `x<y>`", "<b>Use <code>x&lt;y&gt;</code></b>"},
		{"hash without space", "#tag", "#tag"},
		{"bullets", "- one\n* two\n+ three", "• one\n• two\n• three"},
		{"nested bullets keep indentation", "- a\n  - b\n    * c", "• a\n  • b\n    • c"},
		{"bullet with inline", "- **x** & `y`", "• <b>x</b> &amp; <code>y</code>"},
		{"dash without space not bullet", "-flag", "-flag"},
		{"rule dashes", "a\n---\nb", "a\n──────────\nb"},
		{"rule stars", "* * *", "──────────"},
		{"rule underscores", "___", "──────────"},
		{
			"fence",
			"before\n```go\nif a < b {\n\treturn\n}\n```\nafter",
			"before\n<pre>if a &lt; b {\n\treturn\n}</pre>\nafter",
		},
		{"fence keeps markdown literal", "```\n# not heading\n- **x** [a](https://x.test)\n```", "<pre># not heading\n- **x** [a](https://x.test)</pre>"},
		{"fence keeps inner blank lines", "```\na\n\nb\n```", "<pre>a\n\nb</pre>"},
		{"empty fence", "```\n```", "<pre></pre>"},
		{"indented fence", "  ```sh\n  ls\n  ```", "<pre>  ls</pre>"},
		{"unclosed fence", "text\n```\ncode <x>", "text\n<pre>code &lt;x&gt;</pre>"},
		{"fence at very end", "text\n```", "text\n<pre></pre>"},
		{"two fences", "```\na\n```\nmid\n```\nb\n```", "<pre>a</pre>\nmid\n<pre>b</pre>"},
		{"https link", "see [docs](https://x.test/a)", `see <a href="https://x.test/a">docs</a>`},
		{"http link", "[x](http://x.test)", `<a href="http://x.test">x</a>`},
		{"link query escaped", "[q](https://x.test/?a=1&b=2)", `<a href="https://x.test/?a=1&amp;b=2">q</a>`},
		{"link text escaped", "[a<b](https://x.test)", `<a href="https://x.test">a&lt;b</a>`},
		{"relative link text only", "open [main.go](src/main.go)", "open main.go"},
		{"absolute path link text only", "[file](/Users/me/x.go#L10)", "file"},
		{"javascript link text only", "[click](javascript:alert(1))", "click)"},
		{"bold link", "**[x](https://x.test)**", `<b><a href="https://x.test">x</a></b>`},
		{"bold inside link text", "[**x** y](https://x.test)", `<a href="https://x.test"><b>x</b> y</a>`},
		{"bold opened in link text closed after", "[**x](https://x.test)**", `<a href="https://x.test">**x</a>**`},
		{"bold opened before link closed in text", "**see [x**](https://x.test)", `**see <a href="https://x.test">x**</a>`},
		{"bold markers inside href", "**a [x](https://x.test/**b)", `**a <a href="https://x.test/**b">x</a>`},
		{"bold around non-http link", "**see [file](./a.go)**", "<b>see file</b>"},
		{"bolds around a link", "**a** [x](https://x.test) **b**", `<b>a</b> <a href="https://x.test">x</a> <b>b</b>`},
		{"two links in bold", "**[a](https://a.test) & [b](https://b.test)**", `<b><a href="https://a.test">a</a> &amp; <a href="https://b.test">b</a></b>`},
		{"nul bytes dropped", "a\x00b \x000\x00 [x](https://x.test)", `ab 0 <a href="https://x.test">x</a>`},
		{"href quote escaped", `[x](https://x.test/"a)`, `<a href="https://x.test/&#34;a">x</a>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := markdownToHTML(tt.in)
			if got != tt.want {
				t.Errorf("markdownToHTML(%q) =\n%q\nwant\n%q", tt.in, got, tt.want)
			}
			checkBalanced(t, got)
		})
	}
}

var tagRE = regexp.MustCompile(`<(/?)([a-z]+)[^>]*>`)

// checkBalanced verifies that tag counts match and that tags nest properly.
func checkBalanced(t *testing.T, s string) {
	t.Helper()
	pairs := [][2]string{
		{"<b>", "</b>"},
		{"<i>", "</i>"},
		{"<code>", "</code>"},
		{"<pre>", "</pre>"},
		{"<a ", "</a>"},
		{"<blockquote", "</blockquote>"},
	}
	for _, p := range pairs {
		if o, c := strings.Count(s, p[0]), strings.Count(s, p[1]); o != c {
			t.Errorf("%d %q vs %d %q in %q", o, p[0], c, p[1], s)
		}
	}
	var stack []string
	for _, m := range tagRE.FindAllStringSubmatch(s, -1) {
		if m[1] == "" {
			stack = append(stack, m[2])
			continue
		}
		if len(stack) == 0 || stack[len(stack)-1] != m[2] {
			t.Errorf("misnested </%s> (open: %v) in %q", m[2], stack, s)
			return
		}
		stack = stack[:len(stack)-1]
	}
	if len(stack) > 0 {
		t.Errorf("unclosed tags %v in %q", stack, s)
	}
}

const markdownSample = "# Report on **status** & <things>\n" +
	"\n" +
	"Here is `code <x>` and **bold** text with a [link](https://example.com/p?q=1&r=2) and a [file](./main.go).\n" +
	"\n" +
	"- item **one** 🎉\n" +
	"  - nested `two` & __dunder__\n" +
	"\n" +
	"```go\n" +
	"func main() {\n" +
	"\tfmt.Println(\"**not bold** <tag> [x](https://x.test)\")\n" +
	"}\n" +
	"```\n" +
	"\n" +
	"---\n" +
	"Final **words** with `a` and `b` and **c**.\n" +
	"Tricky [**x](https://a.test)** and **a [y](https://b.test/**c) [**z**](http://c.test).\n" +
	"```\n" +
	"unclosed <fence>"

func TestMarkdownToHTMLEveryPrefixBalanced(t *testing.T) {
	runes := []rune(markdownSample)
	for i := 0; i <= len(runes); i++ {
		prefix := string(runes[:i])
		got := markdownToHTML(prefix)
		checkBalanced(t, got)
		if t.Failed() {
			t.Fatalf("prefix %d: %q", i, prefix)
		}
	}
}
