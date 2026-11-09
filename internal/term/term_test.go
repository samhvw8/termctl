package term

import (
	"errors"
	"strings"
	"testing"
)

func p(n, label, title, folder string) *Pane {
	return &Pane{ID: "fake:" + n, Backend: "fake", Native: n, Label: label, Title: title, Folder: folder}
}

func sample() []*Pane {
	return []*Pane{
		p("1", "alpha", "Build things", "~/w/one"),
		p("2", "", "Docs writer", "~/w/two"),
		p("3", "", "Docs reader", "~/w/three"),
		p("4", "", "alpha", "~/w/four"),
	}
}

func code(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return -1
}

func TestResolveRules(t *testing.T) {
	cases := map[string]string{"alpha": "1", "docs writer": "2", "three": "3", "writ": "2", "fake:3": "3"}
	for target, want := range cases {
		got, err := Resolve(sample(), target)
		if err != nil || got.Native != want {
			t.Errorf("%s: got %v %v, want %s", target, got, err, want)
		}
	}
}

func TestResolveAmbiguousListsCandidates(t *testing.T) {
	_, err := Resolve(sample(), "docs")
	if code(err) != 2 || !strings.Contains(err.Error(), "matches 2") || !strings.Contains(err.Error(), "Docs writer") {
		t.Fatal(err)
	}
}

func TestResolveNoMatch(t *testing.T) {
	if _, err := Resolve(sample(), "nope"); code(err) != 2 {
		t.Fatal(err)
	}
}

func TestResolveIDPrefixNeedsFourChars(t *testing.T) {
	a, b := p("ABCDEF12", "", "", ""), p("ZZZZ9999", "", "", "")
	got, err := Resolve([]*Pane{a, b}, "abcd")
	if err != nil || got != a {
		t.Fatal(got, err)
	}
	if _, err := Resolve([]*Pane{a, b}, "abc"); code(err) != 2 {
		t.Fatal(err)
	}
}

func TestResolveTitleContainsBeforeIDPrefix(t *testing.T) {
	a, b := p("1", "", "abcd tools", ""), p("abcd9999", "", "", "")
	got, err := Resolve([]*Pane{a, b}, "abcd")
	if err != nil || got != a {
		t.Fatal(got, err)
	}
}

func TestAddress(t *testing.T) {
	it := &Pane{ID: "iterm2:UUID", Backend: "iterm2", Native: "UUID"}
	tm := &Pane{ID: "tmux:%3", Backend: "tmux", Native: "%3"}
	if it.Address() != "UUID" || tm.Address() != "tmux:%3" {
		t.Fatal(it.Address(), tm.Address())
	}
}

func TestDetectPriority(t *testing.T) {
	env := map[string]string{"ITERM_SESSION_ID": "w0:ABC", "TMUX": "/tmp/s,1,0", "REX_SESSION": "r"}
	get := func(k string) string { return env[k] }
	if Detect(get, false) != "tmux" {
		t.Fatal("tmux first")
	}
	delete(env, "TMUX")
	if Detect(get, false) != "rex" {
		t.Fatal("rex next")
	}
	delete(env, "REX_SESSION")
	if Detect(get, false) != "iterm2" {
		t.Fatal("iterm2 last")
	}
	none := func(string) string { return "" }
	if Detect(none, false) != "" || Detect(none, true) != "iterm2" {
		t.Fatal("fallback")
	}
}

func TestGhosttySaysWhy(t *testing.T) {
	b, err := Make("ghostty", "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.ListPanes()
	if code(err) != 1 || !strings.Contains(err.Error(), "not supported") || !strings.Contains(err.Error(), "Ghostty") {
		t.Fatal(err)
	}
}

func TestUnknownBackend(t *testing.T) {
	if _, err := Make("nope", "", ""); code(err) != 2 {
		t.Fatal(err)
	}
}

func TestITerm2BackendIsOnePerProcess(t *testing.T) {
	a, _ := Make("iterm2", "", "")
	b, _ := Make("iterm2", "", "")
	if a != b {
		t.Fatal("two iterm2 backends")
	}
}

func TestCheckTextRejectsLineBreaks(t *testing.T) {
	if code(CheckText("a\nb")) != 2 || code(CheckText("a\rb")) != 2 || CheckText("plain") != nil {
		t.Fatal("line breaks must be refused")
	}
}

func TestNormalizeKey(t *testing.T) {
	if k, err := NormalizeKey("Ctrl-C"); k != "ctrl-c" || err != nil {
		t.Fatal(k, err)
	}
	if _, err := NormalizeKey("nonsense-key"); code(err) != 2 {
		t.Fatal(err)
	}
}

func TestTrimScreenTurnsNulIntoSpaces(t *testing.T) {
	got := TrimScreen([]string{"a\x00b  ", "", "\x00\x00"}, 5)
	if len(got) != 1 || got[0] != "a b" {
		t.Fatalf("%q", got)
	}
}

func TestCleanTitle(t *testing.T) {
	if got := CleanTitle("⠧ π > hello   there"); got != "hello there" {
		t.Fatalf("%q", got)
	}
}

func TestShellQuote(t *testing.T) {
	if got := ShellQuote("a'b c"); got != `'a'"'"'b c'` {
		t.Fatal(got)
	}
	if got := ShellQuote("/bin/zsh"); got != "/bin/zsh" {
		t.Fatal(got)
	}
}

func TestITermKeyBytes(t *testing.T) {
	for key, want := range map[string]string{"enter": "\r", "escape": "\x1b", "ctrl-u": "\x15", "ctrl-c": "\x03"} {
		if got, err := iterm2KeyBytes(key); got != want || err != nil {
			t.Errorf("%s: %q %v", key, got, err)
		}
	}
}

func TestITermCommandKeepsAFailedSessionOpen(t *testing.T) {
	got := iterm2Command("/bin/zsh", "claude --model haiku")
	want := "/bin/zsh -lic " + ShellQuote("claude --model haiku; s=$?; [ $s -eq 0 ] || { echo \"termctl: command exited with status $s\"; sleep 8; }")
	if got != want {
		t.Fatalf("%s\nwant %s", got, want)
	}
}
