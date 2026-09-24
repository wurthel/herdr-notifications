package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type obj = map[string]any

// writeJSONL writes each entry as one line; string entries are written raw so
// tests can inject malformed lines.
func writeJSONL(t *testing.T, path string, entries ...any) {
	t.Helper()
	var b strings.Builder
	for _, e := range entries {
		if s, ok := e.(string); ok {
			b.WriteString(s)
		} else {
			line, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			b.Write(line)
		}
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFixture(t *testing.T, agent string, entries ...any) Turn {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeJSONL(t, path, entries...)
	turn, err := Read(agent, path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return turn
}

func checkTurn(t *testing.T, got, want Turn) {
	t.Helper()
	if got.Prompt != want.Prompt {
		t.Errorf("Prompt = %q, want %q", got.Prompt, want.Prompt)
	}
	if got.Output != want.Output {
		t.Errorf("Output = %q, want %q", got.Output, want.Output)
	}
	if got.Pending != want.Pending {
		t.Errorf("Pending = %q, want %q", got.Pending, want.Pending)
	}
}

func touch(t *testing.T, path string) string {
	t.Helper()
	writeJSONL(t, path, obj{"type": "user"})
	return path
}

const testID = "0f7c1c9e-3b2a-4d8e-9f10-123456789abc"

func TestLocateClaude(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	root := t.TempDir()
	touch(t, filepath.Join(root, "-Users-me-other", "11111111-2222-3333-4444-555555555555.jsonl"))
	want := touch(t, filepath.Join(root, "-Users-me-proj", testID+".jsonl"))
	dirs := Dirs{Claude: []string{missing, root}}

	if got := Locate("claude", testID, dirs); got != want {
		t.Errorf("Locate = %q, want %q", got, want)
	}
	if got := Locate("claude", "99999999-0000-0000-0000-000000000000", dirs); got != "" {
		t.Errorf("Locate for unknown id = %q, want empty", got)
	}
	if got := Locate("codex", testID, dirs); got != "" {
		t.Errorf("Locate with no codex dirs = %q, want empty", got)
	}
}

func TestLocateClaudeFirstDirWins(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	want := touch(t, filepath.Join(a, "p", testID+".jsonl"))
	touch(t, filepath.Join(b, "p", testID+".jsonl"))
	if got := Locate("claude", testID, Dirs{Claude: []string{a, b}}); got != want {
		t.Errorf("Locate = %q, want %q", got, want)
	}
}

func TestLocateCodex(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "2026", "09", "20", "rollout-2026-09-20T10-00-00-"+testID+".jsonl"))
	want := touch(t, filepath.Join(root, "2026", "09", "24", "rollout-2026-09-24T08-15-30-"+testID+".jsonl"))
	touch(t, filepath.Join(root, "2026", "09", "24", "rollout-2026-09-24T09-00-00-11111111-2222-3333-4444-555555555555.jsonl"))
	// Wrong depth must not match.
	touch(t, filepath.Join(root, "2026", "09", "rollout-2026-09-30T00-00-00-"+testID+".jsonl"))
	dirs := Dirs{Codex: []string{root}}

	if got := Locate("codex", testID, dirs); got != want {
		t.Errorf("Locate = %q, want latest %q", got, want)
	}
	if got := Locate("claude", testID, dirs); got != "" {
		t.Errorf("Locate with no claude dirs = %q, want empty", got)
	}
}

func TestLocateRejectsBadInput(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "p", testID+".jsonl"))
	// A file whose name is a glob that would match if the id were not validated.
	touch(t, filepath.Join(root, "p", "abcdefgh.jsonl"))
	dirs := Dirs{Claude: []string{root}, Codex: []string{root}}

	for _, id := range []string{"", "short", "../x", "../p/" + testID, "abcd*efgh", "a*b", "abcdefg?", "abcdefgh/..", "abc def gh", strings.Repeat("a", 129)} {
		if got := Locate("claude", id, dirs); got != "" {
			t.Errorf("Locate(claude, %q) = %q, want empty", id, got)
		}
	}
	if got := Locate("gemini", testID, dirs); got != "" {
		t.Errorf("Locate(unknown agent) = %q, want empty", got)
	}
	if got := Locate("", testID, dirs); got != "" {
		t.Errorf("Locate(empty agent) = %q, want empty", got)
	}
}

func TestDefaultDirs(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	tests := []struct {
		name string
		env  map[string]string
		home string
		want Dirs
	}{
		{
			name: "home only",
			home: "/home/u",
			want: Dirs{
				Claude: []string{"/home/u/.claude/projects"},
				Codex:  []string{"/home/u/.codex/sessions"},
			},
		},
		{
			name: "env first",
			env:  map[string]string{"CLAUDE_CONFIG_DIR": "/cfg/claude", "CODEX_HOME": "/cfg/codex"},
			home: "/home/u",
			want: Dirs{
				Claude: []string{"/cfg/claude/projects", "/home/u/.claude/projects"},
				Codex:  []string{"/cfg/codex/sessions", "/home/u/.codex/sessions"},
			},
		},
		{
			name: "env without home",
			env:  map[string]string{"CODEX_HOME": "/cfg/codex"},
			want: Dirs{Codex: []string{"/cfg/codex/sessions"}},
		},
		{name: "nothing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultDirs(env(tt.env), tt.home)
			if strings.Join(got.Claude, ",") != strings.Join(tt.want.Claude, ",") {
				t.Errorf("Claude = %q, want %q", got.Claude, tt.want.Claude)
			}
			if strings.Join(got.Codex, ",") != strings.Join(tt.want.Codex, ",") {
				t.Errorf("Codex = %q, want %q", got.Codex, tt.want.Codex)
			}
		})
	}
}

func writeRaw(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "raw.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTailLines(t *testing.T) {
	content := "aaaa\nbbbb\n\ncccc\ndd" // 18 bytes; "bbbb" starts at offset 5
	tests := []struct {
		name string
		max  int64
		want []string
	}{
		{"whole file", 100, []string{"aaaa", "bbbb", "cccc", "dd"}},
		{"exact size", 18, []string{"aaaa", "bbbb", "cccc", "dd"}},
		{"partial first line dropped", 12, []string{"cccc", "dd"}},
		{"window starts at line boundary", 13, []string{"bbbb", "cccc", "dd"}},
		{"window is exactly the last line", 2, []string{"dd"}},
		{"window inside last line", 1, nil},
	}
	path := writeRaw(t, content)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines, err := tailLines(path, tt.max)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, l := range lines {
				got = append(got, string(l))
			}
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("tailLines(max=%d) = %q, want %q", tt.max, got, tt.want)
			}
		})
	}
}

func TestReadLargeFileKeepsTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	filler := obj{"type": "user", "message": obj{"role": "user", "content": strings.Repeat("x", 1<<20)}}
	entries := make([]any, 0, 12)
	for range 9 {
		entries = append(entries, filler)
	}
	entries = append(entries,
		obj{"type": "user", "message": obj{"role": "user", "content": "last prompt"}},
		obj{"type": "assistant", "message": obj{"id": "m", "role": "assistant", "content": []obj{{"type": "text", "text": "last answer"}}}},
	)
	writeJSONL(t, path, entries...)
	turn, err := Read("claude", path)
	if err != nil {
		t.Fatal(err)
	}
	checkTurn(t, turn, Turn{Prompt: "last prompt", Output: "last answer"})
}

func TestReadErrors(t *testing.T) {
	path := writeRaw(t, `{"type":"user","message":{"content":"hi"}}`+"\n")
	if _, err := Read("gemini", path); err == nil {
		t.Error("Read with unsupported agent: expected error")
	}
	if _, err := Read("claude", filepath.Join(t.TempDir(), "missing.jsonl")); err == nil {
		t.Error("Read on missing file: expected error")
	}
}

func TestTurnEmpty(t *testing.T) {
	if !(Turn{}).Empty() {
		t.Error("zero Turn should be empty")
	}
	for _, turn := range []Turn{{Prompt: "p"}, {Output: "o"}, {Pending: "x"}} {
		if turn.Empty() {
			t.Errorf("%+v should not be empty", turn)
		}
	}
}
