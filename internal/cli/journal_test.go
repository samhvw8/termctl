package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/samhvw8/termctl/internal/harness"
)

func TestJournalLogResumeNote(t *testing.T) {
	e := newEnv(t)
	if code, _, stderr := e.run("msg", "target", "short", "hello"); code != 0 {
		t.Fatal(stderr)
	}
	if code, _, stderr := e.run("msg", "target", "line one\nline two"); code != 0 {
		t.Fatal(stderr)
	}
	code, out, stderr := e.run("note", "job-plan", "ship", "the", "journal")
	if code != 0 || !strings.Contains(out, "/projects/w-me/") || !strings.HasSuffix(strings.TrimSpace(out), "-note-job-plan.md") {
		t.Fatal(code, out, stderr)
	}
	data, _ := os.ReadFile(strings.TrimSpace(out))
	if !strings.HasPrefix(string(data), "/w/me · ") || !strings.HasSuffix(string(data), "\n\nship the journal\n") {
		t.Fatalf("%q", data)
	}

	code, out, _ = e.run("log")
	if code != 0 || strings.Count(out, "\n") != 3 || !strings.Contains(out, "msg") || !strings.Contains(out, "short hello") ||
		!strings.Contains(out, "note") || !strings.Contains(out, "w-me") {
		t.Fatalf("log:\n%s", out)
	}
	code, out, _ = e.run("log", "--project", "/w/me", "--json")
	var entries []harness.Entry
	if code != 0 || json.Unmarshal([]byte(out), &entries) != nil || len(entries) != 3 || entries[1].File == "" {
		t.Fatalf("log --json:\n%s", out)
	}
	if code, out, _ = e.run("log", "--project", "/w/other"); code != 0 || !strings.Contains(out, "no journal entries") {
		t.Fatal(code, out)
	}
	if code, out, _ = e.run("log", "-n", "1"); code != 0 || strings.Count(out, "\n") != 1 || !strings.Contains(out, "note") {
		t.Fatal(code, out)
	}

	code, out, _ = e.run("resume", ".") // the caller's project (its pane is in /w/me), not the shell's folder
	if code != 0 || !strings.Contains(out, "# Project /w/me (w-me)") || !strings.Contains(out, "## Notes (1)") || !strings.Contains(out, "## Last 3 entries") {
		t.Fatalf("resume project:\n%s", out)
	}
	if code, _, stderr := e.run("resume", "t000000"); code != 2 || !strings.Contains(stderr, "no task t000000") {
		t.Fatal(code, stderr)
	}
}

func TestNoteTakesFileOnEitherSideOfWhat(t *testing.T) {
	e := newEnv(t)
	src := t.TempDir() + "/brief.md"
	if err := os.WriteFile(src, []byte("the brief\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"note", "--file", src, "a"},
		{"note", "b", "--file", src},
		{"note", "c", "--file=" + src},
	} {
		code, out, stderr := e.run(args...)
		if code != 0 {
			t.Fatal(args, code, stderr)
		}
		data, _ := os.ReadFile(strings.TrimSpace(out))
		if !strings.HasSuffix(string(data), "\n\nthe brief\n") {
			t.Fatalf("%v: %q", args, data)
		}
	}
	if code, _, stderr := e.run("note", "d", "--file", src, "more"); code != 2 || !strings.Contains(stderr, "not both") {
		t.Fatal(code, stderr)
	}
	if code, _, stderr := e.run("note", "e", "--file"); code != 2 || !strings.Contains(stderr, "needs a value") {
		t.Fatal(code, stderr)
	}
	code, out, _ := e.run("note", "f", "--", "--file", "is", "a", "flag") // after --, it is text
	data, _ := os.ReadFile(strings.TrimSpace(out))
	if code != 0 || !strings.HasSuffix(string(data), "\n\n--file is a flag\n") {
		t.Fatalf("%d %q", code, data)
	}
}

func TestResumeTaskPrintsBriefAndEveryMessage(t *testing.T) {
	e := newEnv(t)
	code, out, stderr := e.run("task", "new", "generic", "--title", "docs", "--brief", "write the docs", "--cwd", "/w", "--command", "fake-cmd", "--timeout", "60")
	if code != 0 {
		t.Fatal(out, stderr)
	}
	id := strings.Fields(out)[0]
	code, out, stderr = e.run("resume", id)
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, want := range []string{"# Task " + id + ": docs", "project /w/me", "## Brief", "write the docs", "## Timeline", "task.new", "(the brief, above:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("resume %s lacks %q:\n%s", id, want, out)
		}
	}
}

func TestParseSince(t *testing.T) {
	now := time.Unix(1794845342, 0)
	for in, want := range map[string]int64{"": 0, "30m": 1794843542, "2h": 1794838142, "1d": 1794758942, "1700000000": 1700000000} {
		if got, err := parseSince(in, now); err != nil || got != want {
			t.Errorf("%q: %d %v, want %d", in, got, err, want)
		}
	}
	if _, err := parseSince("yesterday", now); err == nil {
		t.Error("yesterday parsed")
	}
}
