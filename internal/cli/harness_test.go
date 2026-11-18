package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/harness"
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
	if code, _, _ := e.run("task", "accept", "nope"); code != 2 {
		t.Fatal(code)
	}
	if code, _, stderr := e.run("task", "review", "nope", "--harness", "claude"); code != 2 || !strings.Contains(stderr, "blind") {
		t.Fatal(code, stderr)
	}
	if code, out, _ := e.run("--help"); code != 0 || !strings.Contains(out, "task accept") || !strings.Contains(out, "task review") {
		t.Fatal(code, out)
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

func TestStateCleanCommand(t *testing.T) {
	e := newEnv(t)
	t.Setenv("TERMCTL_STATE_DIR", "")
	err := e.app.harness([]string{"state", "clean"}) // not Harness: its pending delivery would need a state dir
	var te *term.Error
	if !errors.As(err, &te) || te.Code != 6 || !strings.Contains(err.Error(), "TERMCTL_STATE_DIR") {
		t.Fatal(err)
	}
}

// ls --mine lists only the panes this caller started, so a lead can find what it must close.
func TestLsMineListsOnlyPanesTheCallerStarted(t *testing.T) {
	e := newEnv(t)
	code, out, stderr := e.run("spawn", "generic", "--where", "tab", "--cwd", "/w/x", "--command", "fake-cmd", "--label", "worker")
	if code != 0 {
		t.Fatal(code, out, stderr)
	}
	code, out, stderr = e.run("ls", "--mine", "--json")
	if code != 0 {
		t.Fatal(code, out, stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err, out)
	}
	if len(rows) != 1 || rows[0]["label"] != "worker" {
		t.Fatalf("ls --mine = %v, want only the spawned worker", rows)
	}
}

func TestTaskDialogCommands(t *testing.T) {
	e := newEnv(t)
	var started []string
	e.app.StartWatcher = func(_ *Options, log string) error { started = append(started, log); return nil }
	e.app.Factory = func(string, map[string]string) (term.Backend, error) { return e.b, nil }
	code, out, stderr := e.run("task", "new", "generic", "--title", "t", "--brief", "b", "--cwd", "/w", "--command", "fake", "--timeout", "30")
	if code != 0 || len(started) != 1 || !strings.Contains(started[0], "watch") {
		t.Fatal(code, out, stderr, started)
	}
	id := strings.TrimSpace(out)
	worker := e.b.Panes[len(e.b.Panes)-1]
	worker.Process = "claude"
	dlg := &termtest.Dialog{Body: "touch x", Options: []string{"Yes", "Yes, and don't ask again", "No"}, Selected: 1}
	e.b.State[worker.ID].Dialog = dlg

	code, out, stderr = e.run("task", "wait", id, "--json", "--timeout", "10")
	var w struct {
		Event  string
		Task   struct{ ID, Status string }
		Dialog struct{ Fingerprint string }
	}
	if code != 0 || json.Unmarshal([]byte(out), &w) != nil || w.Event != "dialog" || w.Task.ID != id || w.Dialog.Fingerprint == "" {
		t.Fatal(code, out, stderr)
	}
	fp := w.Dialog.Fingerprint
	for _, c := range []struct {
		args []string
		code int
	}{
		{[]string{"answer", id, "2", "--expect", fp}, 6},
		{[]string{"answer", id, "1"}, 2}, // --expect is required
		{[]string{"deny", id}, 2},
		{[]string{"answer", id, "1", "--expect", "00000000"}, 7},
		{[]string{"answer", id, "x", "--expect", fp}, 2},
		{[]string{"answer", id}, 2},
	} {
		if code, _, stderr := e.run(c.args...); code != c.code {
			t.Fatal(c.args, code, stderr)
		}
	}
	if code, out, stderr := e.run("answer", id, "1", "--expect", fp); code != 0 || !strings.Contains(out, "answered dialog "+fp) || dlg.Answer != 1 {
		t.Fatal(code, out, stderr)
	}
	if code, _, stderr := e.run("answer", id, "1", "--expect", fp); code != 7 || !strings.Contains(stderr, "no dialog") {
		t.Fatal(code, stderr)
	}
	e.b.State[worker.ID].Dialog = &termtest.Dialog{Body: "rm -rf build", Options: []string{"Yes", "No"}, Selected: 1}
	code, out, _ = e.run("task", "wait", "--any", "--timeout", "10")
	if code != 0 || !strings.Contains(out, "DIALOG task "+id) ||
		!strings.Contains(out, "rm -rf build") || !strings.Contains(out, "harness-ops answer "+id+" <n> --expect ") {
		t.Fatal(code, out)
	}
	fp2 := regexp.MustCompile(`fingerprint: +([0-9a-f]{8})`).FindStringSubmatch(out)[1]
	if code, out, _ := e.run("task", "ls"); code != 0 || !strings.Contains(out, "[dialog ") {
		t.Fatal(code, out)
	}
	if code, out, stderr := e.run("deny", id, "--expect", fp2); code != 0 || !strings.Contains(out, "pressed Esc") {
		t.Fatal(code, out, stderr)
	}
	if code, out, _ := e.run("--help"); code != 0 || !strings.Contains(out, "\n  answer TARGET") || !strings.Contains(out, "7 no dialog") || !strings.Contains(out, "watch") {
		t.Fatal(out)
	}
	if code, _, _ := e.run("task", "close", "--force", id); code != 0 {
		t.Fatal(code)
	}
	if code, out, stderr := e.run("watch", "--interval", "1"); code != 0 || !strings.Contains(out, "no running task") {
		t.Fatal(code, out, stderr)
	}
}

// A blind reviewer is a running task and can stop on a dialog: task review starts a watcher as task new
// does (review F21).
func TestTaskReviewStartsAWatcher(t *testing.T) {
	e := newEnv(t)
	var started []string
	e.app.StartWatcher = func(_ *Options, log string) error { started = append(started, log); return nil }
	e.app.Factory = func(string, map[string]string) (term.Backend, error) { return e.b, nil }
	code, out, stderr := e.run("task", "new", "generic", "--title", "t", "--brief", "b", "--cwd", "/w", "--command", "fake", "--timeout", "30")
	if code != 0 {
		t.Fatal(code, out, stderr)
	}
	id := strings.TrimSpace(out)
	if code, out, stderr := e.run("task", "done", id, "--status", "ok", "--summary", "s"); code != 0 {
		t.Fatal(code, out, stderr)
	}
	started = nil
	if code, out, stderr := e.run("task", "review", id, "--blind", "--harness", "claude", "--command", "fake", "--timeout", "30"); code != 0 {
		t.Fatal(code, out, stderr)
	}
	if len(started) != 1 {
		t.Fatalf("task review started %d watchers, want 1", len(started))
	}
}

// answer and deny are top-level commands that take a pane you started (spawn, as pairs and live teams
// start members) or a task ID. Their task aliases were removed in v0.7.0: they are usage errors that name
// the new command, and press no key.
func TestAnswerAndDenyCommands(t *testing.T) {
	e := newEnv(t)
	e.app.Factory = func(string, map[string]string) (term.Backend, error) { return e.b, nil }
	code, out, stderr := e.run("spawn", "claude", "--command", "fake", "--label", "partner", "--timeout", "30")
	if code != 0 {
		t.Fatal(code, out, stderr)
	}
	paneID := strings.TrimSpace(out)
	pane := e.b.Panes[len(e.b.Panes)-1]
	pane.Process = "claude"
	ask := func(body string) (*termtest.Dialog, string) {
		dlg := &termtest.Dialog{Body: body, Options: []string{"Yes", "Yes, and don't ask again", "No"}, Selected: 1}
		e.b.State[pane.ID].Dialog = dlg
		lines, _ := e.b.Capture(pane, 60)
		return dlg, regexp.MustCompile(`^[0-9a-f]{8}$`).FindString(harnessFP(lines))
	}
	dlg, fp := ask("touch x")
	for _, c := range []struct {
		args []string
		code int
	}{
		{[]string{"answer", paneID, "2", "--expect", fp}, 6}, // persist guard
		{[]string{"answer", paneID, "1"}, 2},                 // --expect is required
		{[]string{"deny", paneID}, 2},
		{[]string{"answer", paneID, "1", "--expect", "00000000"}, 7},
		{[]string{"answer", "target", "1", "--expect", fp}, 2}, // a pane nobody started
		{[]string{"answer", "me", "1", "--expect", fp}, 2},     // your own pane
	} {
		if code, _, stderr := e.run(c.args...); code != c.code {
			t.Fatalf("%v: exit %d, want %d: %s", c.args, code, c.code, stderr)
		}
	}
	for _, cmd := range []string{"answer", "deny"} {
		code, _, stderr := e.run("task", cmd, paneID, "1", "--expect", fp)
		if code != 2 || !strings.Contains(stderr, "task "+cmd+" was removed in v0.7.0; use harness-ops "+cmd) {
			t.Fatalf("task %s: exit %d: %s", cmd, code, stderr)
		}
	}
	if dlg.Answer != 0 {
		t.Fatal("a refused answer pressed a key")
	}
	if code, out, stderr := e.run("answer", paneID, "1", "--expect", fp); code != 0 || !strings.Contains(out, "answered dialog "+fp) || dlg.Answer != 1 || stderr != "" {
		t.Fatal(code, out, stderr)
	}
	dlg, fp = ask("rm -rf build")
	if code, out, stderr := e.run("deny", "partner", "--expect", fp); code != 0 || !strings.Contains(out, "pressed Esc") || dlg.Answer != -1 {
		t.Fatal(code, out, stderr)
	}
	if code, out, _ := e.run("--help"); code != 0 || !strings.Contains(out, "\n  answer TARGET N --expect FP") || !strings.Contains(out, "\n  deny TARGET --expect FP") || strings.Contains(out, "task answer") {
		t.Fatal(out)
	}
}

// harnessFP reads the fingerprint of the Claude Code dialog on a captured screen.
func harnessFP(lines []string) string {
	if d := harness.Profiles["claude"].ReadDialog(lines); d != nil {
		return d.Fingerprint
	}
	return ""
}

// spawn with a prompt and msg (or send) to a pane you started start a watcher where work starts, and
// record the work; a spawn without a prompt, and a msg to a pane you did not start, start none. A lead
// whose harness is not typed dialog notices is warned on spawn and on msg to its own panes.
func TestSpawnWithAPromptAndMsgToYourPaneStartAWatcher(t *testing.T) {
	e := newEnv(t)
	e.app.Factory = func(string, map[string]string) (term.Backend, error) { return e.b, nil }
	var started []string
	e.app.StartWatcher = func(_ *Options, log string) error { started = append(started, log); return nil }
	if code, out, stderr := e.run("spawn", "claude", "--command", "fake", "--label", "quiet", "--timeout", "30"); code != 0 || len(started) != 0 {
		t.Fatal(code, out, stderr, started)
	}
	code, out, stderr := e.run("spawn", "claude", "--command", "fake", "--label", "partner", "--prompt", "review the plan", "--timeout", "30")
	if code != 0 || len(started) != 1 {
		t.Fatal(code, out, stderr, started)
	}
	partner := strings.TrimSpace(out)
	worked := func() float64 {
		d, err := harness.NewRegistry("").Read()
		if err != nil {
			t.Fatal(err)
		}
		return d.Panes[partner].Worked
	}
	if worked() == 0 {
		t.Fatal("the spawn's prompt was not recorded as work")
	}
	for _, cmd := range []string{"msg", "send"} {
		if code, _, stderr := e.run(cmd, "partner", "your turn"); code != 0 {
			t.Fatal(cmd, code, stderr)
		}
	}
	if len(started) != 3 {
		t.Fatalf("msg and send to your pane must start a watcher: %d starts", len(started))
	}
	if code, _, stderr := e.run("msg", "target", "hello"); code != 0 || len(started) != 3 {
		t.Fatal("a msg to a pane you did not start started a watcher", code, stderr, len(started))
	}
	e.b.Me.Process = "python3" // a lead whose input line is not calibrated
	var warned string
	old := harness.Warn
	harness.Warn = func(m string) { warned += m + "\n" }
	defer func() { harness.Warn = old }()
	note := "dialog notices are not typed to it"
	if e.run("msg", "partner", "again"); strings.Count(warned, note) != 1 || !strings.Contains(warned, "ls --mine") {
		t.Fatalf("msg to your pane must warn once: %q", warned)
	}
	warned = ""
	if e.run("msg", "target", "hi"); strings.Contains(warned, note) {
		t.Fatalf("msg to another pane warned: %q", warned)
	}
	if e.run("spawn", "claude", "--command", "fake", "--timeout", "30"); strings.Count(warned, note) != 1 {
		t.Fatalf("spawn must warn once: %q", warned)
	}
}

// ls --mine shows a pane you started with a dialog marked open as STATUS "dialog <fp>", and --json carries
// the fingerprint: the way a lead whose harness is not typed notices sees a held dialog on a spawned pane.
func TestLsMineShowsAnOpenDialog(t *testing.T) {
	e := newEnv(t)
	e.app.Factory = func(string, map[string]string) (term.Backend, error) { return e.b, nil }
	code, out, stderr := e.run("spawn", "claude", "--command", "fake", "--label", "partner", "--timeout", "30")
	if code != 0 {
		t.Fatal(code, out, stderr)
	}
	partner := strings.TrimSpace(out)
	code, out, stderr = e.run("spawn", "claude", "--command", "fake", "--label", "quiet", "--timeout", "30")
	if code != 0 {
		t.Fatal(code, out, stderr)
	}
	quiet := strings.TrimSpace(out)
	if err := harness.NewRegistry("").Update(func(d *harness.Data) error {
		d.Panes[partner].Dialog = &harness.DialogMark{Fingerprint: "abcd1234", Notified: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := e.run("ls", "--mine"); code != 0 || !regexp.MustCompile(`partner .* dialog abcd1234\n`).MatchString(out) || strings.Count(out, "dialog ") != 1 {
		t.Fatalf("ls --mine:\n%s", out)
	}
	_, out, _ = e.run("ls", "--mine", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err, out)
	}
	for _, r := range rows {
		want := map[string]string{partner: "abcd1234", quiet: ""}[r["pane_id"].(string)]
		if got, _ := r["dialog"].(string); got != want || (want != "" && r["status"] != "dialog "+want) {
			t.Fatalf("row %v: dialog %q, want %q", r, got, want)
		}
	}
}
