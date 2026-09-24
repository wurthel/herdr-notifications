package herdr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeBin(t *testing.T, body string) (bin, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	bin = filepath.Join(dir, "herdr")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > '%s'\n%s\n", argsFile, body)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile
}

func readArgs(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

func TestCLI(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		call     func(c CLI) (string, error)
		wantArgs []string
		wantOut  string
		wantErr  bool
	}{
		{
			name: "read tail",
			body: `printf 'one\ntwo\n'`,
			call: func(c CLI) (string, error) {
				return c.ReadTail(context.Background(), "w1:p1", 7)
			},
			wantArgs: []string{"pane", "read", "w1:p1", "--source", "recent-unwrapped", "--lines", "7"},
			wantOut:  "one\ntwo\n",
		},
		{
			name: "read tail failure",
			body: "exit 1",
			call: func(c CLI) (string, error) {
				return c.ReadTail(context.Background(), "w1:p1", 3)
			},
			wantArgs: []string{"pane", "read", "w1:p1", "--source", "recent-unwrapped", "--lines", "3"},
			wantErr:  true,
		},
		{
			name: "toast",
			body: "exit 0",
			call: func(c CLI) (string, error) {
				return "", c.Toast(context.Background(), "title", "a body with spaces")
			},
			wantArgs: []string{"notification", "show", "title", "--body", "a body with spaces"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin, argsFile := fakeBin(t, tt.body)
			out, err := tt.call(CLI{Bin: bin})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if out != tt.wantOut {
				t.Errorf("out = %q, want %q", out, tt.wantOut)
			}
			if got := readArgs(t, argsFile); strings.Join(got, "\x00") != strings.Join(tt.wantArgs, "\x00") {
				t.Errorf("args = %q, want %q", got, tt.wantArgs)
			}
		})
	}
}

func TestCLITimeout(t *testing.T) {
	bin, _ := fakeBin(t, "exec sleep 5")
	start := time.Now()
	_, err := CLI{Bin: bin, Timeout: 100 * time.Millisecond}.ReadTail(context.Background(), "p", 1)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("timeout took %v", d)
	}
}

func TestCLIMissingBinary(t *testing.T) {
	err := CLI{Bin: filepath.Join(t.TempDir(), "nope")}.Toast(context.Background(), "t", "b")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}
