package notify

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

var (
	mdFenceRE    = regexp.MustCompile("^\\s*```")
	mdHeadingRE  = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*$`)
	mdBulletRE   = regexp.MustCompile(`^(\s*)[-*+]\s+`)
	mdRuleRE     = regexp.MustCompile(`^\s*(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$`)
	mdCodeRE     = regexp.MustCompile("`([^`]+)`")
	mdBoldRE     = regexp.MustCompile(`\*\*([^*\n]+?)\*\*`)
	mdLinkRE     = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
	mdLinkSlotRE = regexp.MustCompile("\x00([0-9]+)\x00")
)

// markdownToHTML converts the common subset of Markdown that agents write
// (fences, inline code, bold, headings, bullets, links) into Telegram HTML.
// Every tag it opens is closed on the same line or at the end of the input,
// so any prefix of the source converts to valid markup.
func markdownToHTML(src string) string {
	var out []string
	inFence := false
	for _, line := range strings.Split(src, "\n") {
		if mdFenceRE.MatchString(line) {
			if inFence {
				out = append(out, "</pre>")
			} else {
				out = append(out, "<pre>")
			}
			inFence = !inFence
			continue
		}
		if inFence {
			out = append(out, html.EscapeString(line))
			continue
		}
		out = append(out, markdownLine(line))
	}
	if inFence {
		out = append(out, "</pre>")
	}
	// Tags were added as separate lines; glue them to their content so
	// Telegram does not render extra blank lines around code blocks.
	s := strings.Join(out, "\n")
	s = strings.ReplaceAll(s, "<pre>\n", "<pre>")
	s = strings.ReplaceAll(s, "\n</pre>", "</pre>")
	return s
}

func markdownLine(line string) string {
	if mdRuleRE.MatchString(line) {
		return "──────────"
	}
	if m := mdHeadingRE.FindStringSubmatch(line); m != nil {
		return "<b>" + inlineMarkdown(m[1]) + "</b>"
	}
	if m := mdBulletRE.FindStringSubmatch(line); m != nil {
		return m[1] + "• " + inlineMarkdown(line[len(m[0]):])
	}
	return inlineMarkdown(line)
}

// inlineMarkdown escapes s and converts inline code, bold and http(s) links.
// Code spans are cut out first so their contents are never reformatted.
func inlineMarkdown(s string) string {
	var b strings.Builder
	for {
		loc := mdCodeRE.FindStringSubmatchIndex(s)
		if loc == nil {
			b.WriteString(inlineText(s))
			return b.String()
		}
		b.WriteString(inlineText(s[:loc[0]]))
		b.WriteString("<code>" + html.EscapeString(s[loc[2]:loc[3]]) + "</code>")
		s = s[loc[1]:]
	}
}

// inlineText converts links and bold. Each link is rendered separately and
// replaced by an opaque placeholder during the bold pass, so a bold span can
// wrap a whole link but never cross a link boundary or reach into an href.
func inlineText(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	var (
		b     strings.Builder
		links []string
	)
	for {
		loc := mdLinkRE.FindStringSubmatchIndex(s)
		if loc == nil {
			b.WriteString(html.EscapeString(s))
			break
		}
		b.WriteString(html.EscapeString(s[:loc[0]]))
		text, href := boldText(html.EscapeString(s[loc[2]:loc[3]])), s[loc[4]:loc[5]]
		if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
			text = `<a href="` + html.EscapeString(href) + `">` + text + "</a>"
		}
		b.WriteString("\x00" + strconv.Itoa(len(links)) + "\x00")
		links = append(links, text)
		s = s[loc[1]:]
	}
	return mdLinkSlotRE.ReplaceAllStringFunc(boldText(b.String()), func(m string) string {
		i, _ := strconv.Atoi(m[1 : len(m)-1])
		return links[i]
	})
}

func boldText(s string) string {
	return mdBoldRE.ReplaceAllString(s, "<b>${1}</b>")
}
