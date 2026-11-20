package cli

import (
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/harness"
	"github.com/samhvw8/termctl/internal/term"
)

// The same text to the same pane within 10 minutes is refused (exit 6) naming --again; --again sends it, and
// another text, another pane or a send after 10 minutes goes. A send refused before typing (a draft) does not count.
func TestSameMessageTwiceIsRefused(t *testing.T) {
	e := newEnv(t)
	if code, _, stderr := e.run("msg", "target", "build it"); code != 0 {
		t.Fatal(code, stderr)
	}
	code, _, stderr := e.run("send", "target", "build", "it")
	if code != 6 || !strings.Contains(stderr, "--again") || len(e.b.Texts("fake:1")) != 1 {
		t.Fatal(code, stderr, e.b.Texts("fake:1"))
	}
	if code, _, stderr := e.run("msg", "--again", "target", "build it"); code != 0 || len(e.b.Texts("fake:1")) != 2 {
		t.Fatal(code, stderr)
	}
	if code, _, _ := e.run("msg", "target", "test it"); code != 0 {
		t.Fatal("another text refused", code)
	}
	e.app.Clock.(interface{ Sleep(float64) }).Sleep(601)
	if code, _, stderr := e.run("msg", "target", "build it"); code != 0 {
		t.Fatal("refused after 10 minutes", code, stderr)
	}
	e.b.State["fake:1"].Input = "draft"
	if code, _, _ := e.run("msg", "target", "lint it"); code != 4 {
		t.Fatal(code)
	}
	e.b.State["fake:1"].Input = ""
	if code, _, stderr := e.run("msg", "target", "lint it"); code != 0 {
		t.Fatal("a send refused before typing counted", code, stderr)
	}
}

// send --defer prints the message ID and ends the typed line with the ack instruction; ack from the pane it
// went to works once; --wait-ack exits 5 when no ack comes, and needs --defer.
func TestSendDeferAndAck(t *testing.T) {
	e := newEnv(t)
	code, out, stderr := e.run("send", "--defer", "target", "run the nightly build")
	if code != 0 {
		t.Fatal(code, stderr)
	}
	id := strings.Fields(out)[0]
	if !strings.HasPrefix(id, "m") || len(id) != 7 {
		t.Fatal(out)
	}
	if typed := e.b.Texts("fake:1")[0]; !strings.HasSuffix(typed, "run the nightly build [deferred "+id+": run harness-ops ack "+id+" once, then do not reply]") {
		t.Fatal(typed)
	}
	if code, _, _ := e.run("ack", id); code != 2 { // the sender's pane
		t.Fatal(code)
	}
	me := e.app.OpenSender
	e.app.OpenSender = func(*Options, term.Backend) (*term.Pane, error) { return e.b.Panes[0], nil }
	if code, out, stderr := e.run("ack", id, "on", "it"); code != 0 || !strings.Contains(out, "acked message "+id) {
		t.Fatal(code, out, stderr)
	}
	if code, _, _ := e.run("ack", id); code != 2 {
		t.Fatal("a second ack", code)
	}
	e.app.OpenSender = me
	if code, out, stderr := e.run("msg", "--defer", "--wait-ack", "2", "target", "and the docs"); code != 5 || !strings.Contains(stderr, "no ack") || !strings.HasPrefix(out, "m") {
		t.Fatal(code, out, stderr)
	}
	if code, _, _ := e.run("msg", "--defer", "--wait-ack", "target", "and the tests"); code != 5 { // the default 30 s
		t.Fatal(code)
	}
	if code, _, _ := e.run("msg", "--wait-ack", "target", "x"); code != 2 {
		t.Fatal(code)
	}
	if len(e.b.Texts(e.b.Me.ID)) != 0 {
		t.Fatal("something was typed to the sender")
	}
}

// task new --defer: ls marks it deferred, then acked once its worker acks; --wait-ack exits 5 without an ack.
func TestTaskNewDeferAndLsMarks(t *testing.T) {
	e := newEnv(t)
	me := e.app.OpenSender
	id := e.newTask(t, "build", "/w", "--defer")
	if code, out, _ := e.run("task", "ls"); code != 0 || !strings.Contains(out, "[deferred]") || strings.Contains(out, "[acked]") {
		t.Fatal(out)
	}
	e.as(t, id)
	if code, out, stderr := e.run("ack", id, "building"); code != 0 || !strings.Contains(out, "acked task "+id) {
		t.Fatal(code, out, stderr)
	}
	e.app.OpenSender = me
	if code, out, _ := e.run("task", "ls"); code != 0 || !strings.Contains(out, "[deferred] [acked]") {
		t.Fatal(out)
	}
	if code, out, _ := e.run("task", "show", id); code != 0 || !strings.Contains(out, "acked") || !strings.Contains(out, "building") {
		t.Fatal(out)
	}
	code, out, stderr := e.run("task", "new", "generic", "--title", "x", "--brief", "b", "--cwd", "/w", "--command", "c", "--timeout", "60", "--defer", "--wait-ack", "3")
	if code != 5 || !strings.HasPrefix(out, "t") || !strings.Contains(stderr, "no ack") {
		t.Fatal(code, out, stderr)
	}
}

// task new --sub-depth/--tree-max, a leaf's refusal, task close --tree, task adopt and resume --tree.
func TestTreeFlagsCloseTreeAdoptAndResumeTree(t *testing.T) {
	e := newEnv(t)
	e.app.Factory = func(string, map[string]string) (term.Backend, error) { return e.b, nil }
	me := e.app.OpenSender
	top := e.newTask(t, "top", "/w", "--sub-depth", "1", "--tree-max", "3")
	e.as(t, top)
	sub := e.newTask(t, "sub", "/w")
	e.as(t, sub)
	if code, _, stderr := e.run("task", "new", "generic", "--title", "x", "--brief", "b", "--cwd", "/w", "--command", "c", "--timeout", "60"); code != 6 || !strings.Contains(stderr, "leaf") {
		t.Fatal(code, stderr)
	}
	e.app.OpenSender = me
	if code, _, _ := e.run("task", "show", top); code != 0 {
		t.Fatal(code)
	}
	if err := harness.NewRegistry("").Update(func(d *harness.Data) error {
		d.Tasks[sub].Progress = &harness.ProgressNote{Text: "half way", At: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, out, _ := e.run("resume", top, "--tree")
	if code != 0 || !strings.Contains(out, "\n"+top+"  running") || !strings.Contains(out, "\n  "+sub+"  running") || !strings.Contains(out, "progress: half way") {
		t.Fatalf("resume --tree:\n%s", out)
	}
	if code, out, _ := e.run("resume", ".", "--tree"); code != 0 || !strings.Contains(out, "  "+sub+"  running") {
		t.Fatalf("resume . --tree:\n%s", out)
	}
	if code, _, _ := e.run("task", "close", "--tree", top); code != 6 {
		t.Fatal("closed running tasks without --force", code)
	}
	code, out, stderr := e.run("task", "close", "--tree", "--force", top)
	if code != 0 || !strings.Contains(out, "closed task "+sub) || !strings.Contains(out, "closed task "+top) {
		t.Fatal(code, out, stderr)
	}
	code, out, stderr = e.run("task", "adopt", "target")
	if code != 0 || !strings.Contains(out, "adopted fake:1 as task") {
		t.Fatal(code, out, stderr)
	}
	if code, _, _ := e.run("task", "adopt", "me"); code != 2 {
		t.Fatal(code)
	}
}

// CLI coverage for close leaving a held pane open, adopt reopening an exited task, and task ls/show
// noting when an exited task's harness is back in its pane.
func TestCloseAdoptAndRestartNoteCLI(t *testing.T) {
	e := newEnv(t)
	e.app.Factory = func(string, map[string]string) (term.Backend, error) { return e.b, nil }
	id := e.newTask(t, "lead", "/w")
	var paneID string
	if err := harness.NewRegistry("").Update(func(d *harness.Data) error {
		paneID = d.Tasks[id].Pane
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range e.b.Panes {
		if p.ID == paneID {
			p.Process = "zsh"
		}
	}
	if code, out, _ := e.run("task", "ls"); code != 0 || !strings.Contains(out, "exited") || strings.Contains(out, "harness back") {
		t.Fatalf("task ls exited:\n%s", out)
	}
	for _, p := range e.b.Panes {
		if p.ID == paneID {
			p.Process = "generic" // fake test task harness is generic; non-shell means back
		}
	}
	if code, out, _ := e.run("task", "ls"); code != 0 || !strings.Contains(out, "[harness back; task adopt "+paneID+"]") {
		t.Fatalf("task ls restarted:\n%s", out)
	}
	if code, out, _ := e.run("task", "show", id); code != 0 || !strings.Contains(out, "restarted:") {
		t.Fatalf("task show restarted:\n%s", out)
	}

	// task adopt reopens the existing exited task (same ID, round 2).
	code, out, stderr := e.run("task", "adopt", paneID)
	if code != 0 || !strings.Contains(out, "reopened task "+id+" (round 2)") {
		t.Fatalf("task adopt reopen: code=%d out=%q err=%q", code, out, stderr)
	}

	// When a second open task exists on the same pane, task close --force leaves the pane open and says why.
	if err := harness.NewRegistry("").Update(func(d *harness.Data) error {
		d.Tasks["tsecond"] = &harness.Task{
			ID: "tsecond", Title: "second", Harness: "generic", Pane: paneID,
			Owner: e.b.Me.ID, Status: "running", Started: 2, Round: 1,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = e.run("task", "close", "--force", id)
	if code != 0 || !strings.Contains(out, "(pane left open: task tsecond (running) holds it)") || e.b.Has("close", paneID) {
		t.Fatalf("task close held pane: code=%d out=%q err=%q closed=%v", code, out, stderr, e.b.Has("close", paneID))
	}
}
