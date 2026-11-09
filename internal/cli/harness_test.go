package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/term"
	"github.com/samhvw8/termctl/internal/termtest"
)

type env struct {
	b   *termtest.Fake
	app *App
	out *bytes.Buffer
	err *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Setenv("TERMCTL_STATE_DIR", t.TempDir())
	target := termtest.MakePane(1, "", "target", "/w/t", "python3")
	dupA := termtest.MakePane(2, "", "dup", "/w/a", "")
	dupB := termtest.MakePane(3, "", "dup", "/w/b", "")
	me := termtest.MakePane(9, "me", "", "/w/me", "claude")
	me.IsSelf = true
	b := termtest.New([]*term.Pane{target, dupA, dupB, me}, me)
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	app := &App{Out: out, Err: errb, Clock: &termtest.Clock{},
		OpenBackend: func(*Options, string) (term.Backend, error) { return b, nil },
		OpenSender:  func(*Options, term.Backend) (*term.Pane, error) { return me, nil },
		Inbox:       t.TempDir(),
	}
	return &env{b, app, out, errb}
}

func (e *env) run(args ...string) (int, string, string) {
	e.out.Reset()
	e.err.Reset()
	code := e.app.Harness(args)
	return code, e.out.String(), e.err.String()
}

func TestSendAliasMatchesMsg(t *testing.T) {
	e := newEnv(t)
	code, out, _ := e.run("send", "target", "hello", "world")
	if code != 0 || !strings.Contains(out, "sent to") || !strings.HasSuffix(e.b.Texts("fake:1")[0], " hello world") {
		t.Fatal(code, out, e.b.Texts("fake:1"))
	}
}

func TestMsgTextMayLookLikeFlags(t *testing.T) {
	e := newEnv(t)
	code, _, stderr := e.run("msg", "target", "--", "--force", "is", "text")
	if code != 0 || !strings.HasSuffix(e.b.Texts("fake:1")[0], " --force is text") {
		t.Fatal(code, stderr, e.b.Texts("fake:1"))
	}
}

func TestDoubleDashMidMessageStaysInText(t *testing.T) {
	e := newEnv(t)
	e.run("msg", "target", "a", "--", "b")
	if !strings.HasSuffix(e.b.Texts("fake:1")[0], " a -- b") {
		t.Fatal(e.b.Texts("fake:1"))
	}
}

func TestFlagAfterTargetIsRefused(t *testing.T) {
	e := newEnv(t)
	code, _, stderr := e.run("msg", "target", "hi", "--clear-first")
	if code != 2 || !strings.Contains(stderr, "before the target") || len(e.b.Texts("fake:1")) != 0 {
		t.Fatal(code, stderr)
	}
}

func TestSlashFlagAfterCommandIsRefused(t *testing.T) {
	e := newEnv(t)
	code, _, stderr := e.run("slash", "target", "/help", "--wait")
	if code != 2 || !strings.Contains(stderr, "before the target") || len(e.b.Texts("fake:1")) != 0 {
		t.Fatal(code, stderr)
	}
}

func TestSlashKeepsItsArguments(t *testing.T) {
	e := newEnv(t)
	code, _, _ := e.run("slash", "target", "/compact", "focus", "on", "tests")
	if code != 0 || e.b.Texts("fake:1")[0] != "/compact focus on tests" {
		t.Fatal(code, e.b.Texts("fake:1"))
	}
}

func TestExitCodes(t *testing.T) {
	e := newEnv(t)
	if code, _, stderr := e.run("msg", "dup", "x"); code != 2 || !strings.Contains(stderr, "matches 2") {
		t.Fatal(code, stderr)
	}
	if code, _, stderr := e.run("msg", "me", "x"); code != 2 || !strings.Contains(stderr, "own pane") {
		t.Fatal(code, stderr)
	}
	e.b.State["fake:1"].Input = "draft"
	if code, _, stderr := e.run("msg", "target", "x"); code != 4 || !strings.Contains(stderr, "draft") {
		t.Fatal(code, stderr)
	}
	if code, _, _ := e.run("msg", "--force", "target", "x"); code != 0 {
		t.Fatal(code)
	}
	if code, _, _ := e.run("nonsense"); code != 2 {
		t.Fatal(code)
	}
	if code, _, _ := e.run(); code != 2 {
		t.Fatal(code)
	}
	if code, out, _ := e.run("--help"); code != 0 || !strings.Contains(out, "Exit codes") {
		t.Fatal(code, out)
	}
}

func TestClearFirstAndClearAndSlash(t *testing.T) {
	for _, argv := range [][]string{{"msg", "--clear-first", "target", "x"}, {"clear", "target"}} {
		e := newEnv(t)
		code, _, stderr := e.run(argv...)
		if code != 0 || e.b.Texts("fake:1")[0] != "/clear" {
			t.Fatal(code, stderr)
		}
	}
	e := newEnv(t)
	if code, _, _ := e.run("slash", "target", "/compact"); code != 0 || e.b.Texts("fake:1")[0] != "/compact" {
		t.Fatal(code)
	}
}

func TestLsJSONFields(t *testing.T) {
	e := newEnv(t)
	code, out, _ := e.run("ls", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || code != 0 {
		t.Fatal(code, err, out)
	}
	for _, k := range []string{"id", "pane_id", "harness", "title", "label", "folder", "self", "status"} {
		if _, ok := rows[0][k]; !ok {
			t.Error(k)
		}
	}
	self := false
	for _, r := range rows {
		self = self || r["self"] == true
	}
	if !self {
		t.Error("no self row")
	}
}

func TestWhichReadNameWaitIdle(t *testing.T) {
	e := newEnv(t)
	if code, out, _ := e.run("which", "target"); code != 0 || !strings.Contains(out, "target") {
		t.Fatal(code, out)
	}
	if code, out, _ := e.run("read", "target", "-n", "5"); code != 0 || !strings.Contains(out, "---") || !strings.Contains(out, "some earlier output") {
		t.Fatal(code, out)
	}
	if code, _, _ := e.run("name", "bad label!"); code != 2 {
		t.Fatal(code)
	}
	if code, out, _ := e.run("name", "good-label"); code != 0 || !strings.Contains(out, "good-label") {
		t.Fatal(code, out)
	}
	if code, out, _ := e.run("wait-idle", "target", "--timeout", "10"); code != 0 || !strings.Contains(out, "idle") {
		t.Fatal(code, out)
	}
	if code, out, _ := e.run("whoami"); code != 0 || !strings.Contains(out, "(you)") {
		t.Fatal(code, out)
	}
}

func TestCloseAndInterrupt(t *testing.T) {
	e := newEnv(t)
	if code, _, _ := e.run("close", "target"); code != 6 {
		t.Fatal(code)
	}
	if code, _, _ := e.run("close", "--force", "me"); code != 2 {
		t.Fatal(code)
	}
	if code, _, _ := e.run("close", "--force", "target"); code != 0 {
		t.Fatal(code)
	}
	e = newEnv(t)
	code, out, stderr := e.run("interrupt", "--key", "ctrl-c", "target")
	if code != 0 || !strings.Contains(out, "ctrl-c") || e.b.Typed("fake:1")[0] != "key:ctrl-c" {
		t.Fatal(code, out, stderr)
	}
}

func TestTaskCommands(t *testing.T) {
	e := newEnv(t)
	if code, _, stderr := e.run("task", "new", "generic", "--title", "t", "--cwd", "/w"); code != 2 || !strings.Contains(stderr, "brief") {
		t.Fatal(code, stderr)
	}
	if code, _, _ := e.run("task", "show", "nope"); code != 2 {
		t.Fatal(code)
	}
	if code, out, _ := e.run("task", "ls"); code != 0 || !strings.Contains(out, "STATUS") {
		t.Fatal(code, out)
	}
	if code, _, _ := e.run("task", "wait"); code != 2 {
		t.Fatal(code)
	}
	if code, _, _ := e.run("task", "done", "x", "--status", "maybe"); code != 2 {
		t.Fatal(code)
	}
	if code, _, _ := e.run("task", "nonsense"); code != 2 {
		t.Fatal(code)
	}
}

func TestUnknownFlagRefused(t *testing.T) {
	e := newEnv(t)
	if code, _, stderr := e.run("ls", "--bogus"); code != 2 || !strings.Contains(stderr, "--bogus") {
		t.Fatal(code, stderr)
	}
}

func TestParseArgs(t *testing.T) {
	p, err := parseArgs([]string{"--force", "--timeout=5", "tgt", "a", "--clear-first"}, []string{"--force", "--clear-first"}, []string{"--timeout"}, 1)
	if err != nil || !p.bools["--force"] || p.vals["--timeout"] != "5" || p.pos[0] != "tgt" || strings.Join(p.rest, " ") != "a --clear-first" {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = parseArgs([]string{"--cwd", "/w", "sleep", "--flag-of-the-command"}, nil, []string{"--cwd"}, 0)
	if err != nil || p.vals["--cwd"] != "/w" || strings.Join(p.rest, " ") != "sleep --flag-of-the-command" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := parseArgs([]string{"--timeout"}, nil, []string{"--timeout"}, -1); err == nil {
		t.Fatal("missing value accepted")
	}
}

func TestTermctlCommands(t *testing.T) {
	e := newEnv(t)
	run := func(args ...string) (int, string, string) {
		e.out.Reset()
		e.err.Reset()
		return e.app.Termctl(args), e.out.String(), e.err.String()
	}
	if code, out, _ := run("ls"); code != 0 || !strings.Contains(out, "PROCESS") {
		t.Fatal(code, out)
	}
	if code, out, _ := run("ls", "--json"); code != 0 || !strings.Contains(out, `"address"`) {
		t.Fatal(code, out)
	}
	if code, _, _ := run("send-text", "target", "hello"); code != 0 || e.b.Texts("fake:1")[0] != "hello" {
		t.Fatal(code)
	}
	if code, _, _ := run("send-key", "target", "enter"); code != 0 {
		t.Fatal(code)
	}
	if code, _, _ := run("send-text", "me", "x"); code != 2 {
		t.Fatal(code)
	}
	if code, _, _ := run("close", "me"); code != 2 {
		t.Fatal(code)
	}
	if code, out, _ := run("capture", "target", "-n", "3"); code != 0 || !strings.Contains(out, "earlier") {
		t.Fatal(code, out)
	}
	if code, _, _ := run("close", "target"); code != 0 || !e.b.Has("close", "fake:1") {
		t.Fatal(code)
	}
	if code, out, _ := run("spawn", "--where", "tab", "--cwd", "/w", "--title", "t", "sleep", "30"); code != 0 || !strings.HasPrefix(out, "fake:") {
		t.Fatal(code, out)
	}
	// Flags before the command are flags. A bad command makes iTerm2 raise a modal that stalls its API.
	last := e.b.Events[len(e.b.Events)-1]
	if last.Kind != "spawn" || last.Text != "sleep 30|/w" {
		t.Fatalf("%+v", last)
	}
	if code, _, _ := run("name", "bad label!"); code != 2 {
		t.Fatal(code)
	}
}
