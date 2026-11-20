package cli

import (
	"encoding/json"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/samhvw8/termctl/internal/harness"
	"github.com/samhvw8/termctl/internal/term"
	"github.com/samhvw8/termctl/internal/termtest"
)

func TestCrossProjectDuplicateScopeNamesLogAndResume(t *testing.T) {
	e := newScopeEnv(t)
	e.app.StartWatcher = func(*Options, string) error { return nil }
	leadA := termtest.MakePane(10, "leadA", "", "/w/projA", "omp")
	leadB := termtest.MakePane(11, "leadB", "", "/w/projB", "omp")
	wA := termtest.MakePane(12, "wA", "", "/w/projA", "claude")
	wB := termtest.MakePane(13, "wB", "", "/w/projB", "claude")
	e.b.Panes = append(e.b.Panes, leadA, leadB, wA, wB)
	for _, p := range []*term.Pane{leadA, leadB, wA, wB} {
		e.b.State[p.ID] = &termtest.Screen{Log: []string{"some earlier output"}}
	}

	e.app.OpenSender = func(*Options, term.Backend) (*term.Pane, error) { return leadA, nil }
	if code, _, stderr := e.run("attach", wA.ID, "--name", "shared"); code != 0 {
		t.Fatal(stderr)
	}
	e.app.OpenSender = func(*Options, term.Backend) (*term.Pane, error) { return leadB, nil }
	if code, _, stderr := e.run("attach", wB.ID, "--name", "shared"); code != 0 {
		t.Fatal(stderr)
	}

	// Caller in /w/me (third project) must see ambiguity error (exit 2), NOT "no scope" or fake directory.
	mePane := fakePane(e, "fake:9")
	e.app.OpenSender = func(*Options, term.Backend) (*term.Pane, error) { return mePane, nil }
	code, _, stderr := e.run("log", "--scope", "shared")
	if code != 2 || !strings.Contains(stderr, "several scopes") {
		t.Fatalf("log --scope ambiguous: code=%d stderr=%q (I3)", code, stderr)
	}
	code, out, stderr := e.run("resume", "shared")
	if code != 2 || !strings.Contains(stderr, "several scopes") {
		t.Fatalf("resume ambiguous: code=%d out=%q stderr=%q (I3)", code, out, stderr)
	}
}

func TestResumeTreeIncludesSubScopesAndDedupesGrandchildren(t *testing.T) {
	e := newScopeEnv(t)
	e.app.StartWatcher = func(*Options, string) error { return nil }
	subPane := termtest.MakePane(14, "subP", "", "/w/me", "omp")
	e.b.Panes = append(e.b.Panes, subPane)
	e.b.State[subPane.ID] = &termtest.Screen{Log: []string{"some earlier output"}}

	if code, _, stderr := e.run("attach", "target", "--name", "top-scope", "--sub-depth", "2"); code != 0 {
		t.Fatal(stderr)
	}
	targetPane := fakePane(e, "fake:1")
	e.app.OpenSender = func(*Options, term.Backend) (*term.Pane, error) { return targetPane, nil }
	if code, _, stderr := e.run("attach", subPane.ID, "--name", "sub-scope", "--sub-depth", "1"); code != 0 {
		t.Fatal(stderr)
	}
	e.app.OpenSender = func(*Options, term.Backend) (*term.Pane, error) { return subPane, nil }
	code, out, stderr := e.run("task", "new", "generic", "--title", "sub-job", "--brief", "do work", "--cwd", "/w/me", "--command", "fake", "--timeout", "10")
	if code != 0 {
		t.Fatalf("task new under sub-scope: %d %s %s", code, out, stderr)
	}
	childID := strings.Fields(out)[0]

	mePane := fakePane(e, "fake:9")
	e.app.OpenSender = func(*Options, term.Backend) (*term.Pane, error) { return mePane, nil }
	code, treeOut, stderr := e.run("resume", "top-scope", "--tree")
	if code != 0 || !strings.Contains(treeOut, "sub-scope") || !strings.Contains(treeOut, childID) {
		t.Fatalf("resume top-scope --tree missed sub-scope/child (N1): code=%d out=%q stderr=%q", code, treeOut, stderr)
	}
	code, projTreeOut, stderr := e.run("resume", ".", "--tree")
	if code != 0 || strings.Count(projTreeOut, childID) != 1 || strings.Count(projTreeOut, "sub-scope (") != 1 {
		t.Fatalf("resume . --tree duplicated or missed nodes (N1): code=%d out=%q stderr=%q", code, projTreeOut, stderr)
	}
}

func TestCloseForceScopeWakesWatcher(t *testing.T) {
	e := newScopeEnv(t)
	e.app.StartWatcher = func(*Options, string) error { return nil }
	code, out, stderr := e.run("spawn", "claude", "--where", "tab", "--cwd", "/w/me", "--command", "fake", "--label", "wclose")
	if code != 0 {
		t.Fatalf("spawn: %d %s %s", code, out, stderr)
	}
	paneID := strings.TrimSpace(out)
	if code, out, stderr = e.run("attach", paneID, "--name", "wscope"); code != 0 {
		t.Fatalf("attach: %d %s %s", code, out, stderr)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGUSR1)
	defer signal.Stop(sigCh)
	wake := make(chan struct{}, 1)
	go func() {
		for range sigCh {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}()

	mePane := fakePane(e, "fake:9")
	wt := &harness.Tasks{Reg: harness.NewRegistry(""), Backend: e.b, Sender: mePane, Clock: harness.RealClock{}, Factory: e.app.Factory}
	done := make(chan error, 1)
	go func() {
		done <- wt.Watch(harness.WatchOpts{Rounds: 0, Wake: wake})
	}()
	// Wait until watcher lock advertises "wake" and round 1 enters 30s slow-idle sleep.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if wt.Watching(mePane.ID) > 0 {
			logs, _ := os.ReadDir(harness.DefaultRoot() + "/watch")
			ready := false
			for _, ent := range logs {
				if strings.HasSuffix(ent.Name(), ".lock") {
					data, _ := os.ReadFile(harness.DefaultRoot() + "/watch/" + ent.Name())
					if strings.Contains(string(data), "wake") {
						ready = true
					}
				}
			}
			if ready {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)

	if code, out, stderr = e.run("close", "--force", paneID); code != 0 {
		t.Fatalf("close --force: %d %s %s", code, out, stderr)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Watch err=%v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("close --force on active Scope did not wake slow-idle watcher (N5)")
	}
}

func newScopeEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.app.Factory = func(string, map[string]string) (term.Backend, error) { return e.b, nil }
	return e
}

func fakePane(e *env, id string) *term.Pane {
	for _, p := range e.b.Panes {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func TestAttachMsgPiggybackAndWatcherStartOrWake(t *testing.T) {
	e := newScopeEnv(t)
	starts := 0
	e.app.StartWatcher = func(_ *Options, log string) error {
		starts++
		return nil
	}

	// Silent attach (no --notify): 0 texts sent to target, watcher started.
	code, out, stderr := e.run("attach", "target", "--name", "auth-refactor", "--about", "refactor auth", "--stall", "5m")
	if code != 0 || !strings.Contains(out, "auth-refactor (s") || !strings.Contains(out, ", lease ") {
		t.Fatalf("attach: code=%d out=%q stderr=%q", code, out, stderr)
	}
	if len(e.b.Texts("fake:1")) != 0 {
		t.Fatalf("attach must be silent without --notify, got texts=%v", e.b.Texts("fake:1"))
	}
	if starts != 1 {
		t.Fatalf("expected 1 watcher start, got %d", starts)
	}

	// Idempotent update via attach --name updates fields cleanly.
	code, out, stderr = e.run("attach", "target", "--name", "auth-refactor", "--about", "v2 about", "--stall", "10m")
	if code != 0 || !strings.HasPrefix(out, "updated auth-refactor (s") {
		t.Fatalf("attach update: code=%d out=%q stderr=%q", code, out, stderr)
	}

	// Deferred msg must NOT consume the pending piggyback suffix.
	code, _, stderr = e.run("msg", "--defer", "target", "wait for later")
	if code != 0 {
		t.Fatalf("msg --defer: %d %s", code, stderr)
	}
	if len(e.b.Texts("fake:1")) != 1 || !strings.Contains(e.b.Texts("fake:1")[0], "[deferred ") ||
		strings.Contains(e.b.Texts("fake:1")[0], harness.AttachSuffix("auth-refactor", "fake:9")) {
		t.Fatalf("expected deferred msg without attach suffix, got %v", e.b.Texts("fake:1"))
	}

	// First non-deferred msg includes attach suffix once; second send does not.
	code, _, stderr = e.run("msg", "target", "start refactoring")
	if code != 0 {
		t.Fatalf("first msg: %d %s", code, stderr)
	}
	code, _, stderr = e.run("send", "target", "check tests too")
	if code != 0 {
		t.Fatalf("second send: %d %s", code, stderr)
	}
	texts := e.b.Texts("fake:1")
	if len(texts) != 3 {
		t.Fatalf("expected 3 texts, got %v", texts)
	}
	wantSuffix := harness.AttachSuffix("auth-refactor", "fake:9")
	if !strings.Contains(texts[1], wantSuffix) {
		t.Fatalf("first msg missing attach suffix %q: %q", wantSuffix, texts[1])
	}
	if strings.Contains(texts[2], wantSuffix) {
		t.Fatalf("second msg repeated attach suffix: %q", texts[2])
	}

	// --notify refused on draft (exit 4) without leaving any new scope created.
	e.b.State["fake:2"].Input = "draft text"
	code, _, stderr = e.run("attach", "fake:2", "--name", "bad-draft", "--notify")
	if code != 4 || !strings.Contains(stderr, "draft") {
		t.Fatalf("expected exit 4 on --notify draft, got %d %q", code, stderr)
	}
	if _, out, _ := e.run("scope", "ls"); strings.Contains(out, "bad-draft") {
		t.Fatalf("draft --notify must not create scope: %s", out)
	}
}

func TestReportFlagsBeforeAfterAndLiteralAndJobRefusal(t *testing.T) {
	e := newScopeEnv(t)
	e.app.StartWatcher = func(*Options, string) error { return nil }
	if code, _, stderr := e.run("attach", "target", "--name", "worker-scope"); code != 0 {
		t.Fatal(stderr)
	}
	targetPane := fakePane(e, "fake:1")
	mePane := fakePane(e, "fake:9")

	// Switch sender to target pane.
	e.app.OpenSender = func(*Options, term.Backend) (*term.Pane, error) { return targetPane, nil }

	code, out, stderr := e.run("report", "halfway", "done", "--notify")
	if code != 0 || !strings.Contains(out, "reported on scope worker-scope") || !strings.Contains(out, "lead notice queued") {
		t.Fatalf("report trailing --notify: code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = e.run("report", "--notify", "--", "--notify", "is", "literal")
	if code != 0 || !strings.Contains(out, "lead notice queued") {
		t.Fatalf("report leading --notify --: code=%d out=%q stderr=%q", code, out, stderr)
	}

	// Verify scope show records the literal progress text and whoami prints scope supervision line.
	e.app.OpenSender = func(*Options, term.Backend) (*term.Pane, error) { return mePane, nil }
	code, out, stderr = e.run("scope", "show", "worker-scope")
	if code != 0 || !strings.Contains(out, "--notify is literal") {
		t.Fatalf("scope show: code=%d out=%q stderr=%q", code, out, stderr)
	}
	e.app.OpenSender = func(*Options, term.Backend) (*term.Pane, error) { return targetPane, nil }
	code, out, stderr = e.run("whoami")
	if code != 0 || !strings.Contains(out, "scope: worker-scope (s") || !strings.Contains(out, "lead: fake:9") {
		t.Fatalf("whoami on scope: code=%d out=%q stderr=%q", code, out, stderr)
	}

	// Refused on a running Job.
	e.app.OpenSender = func(*Options, term.Backend) (*term.Pane, error) { return mePane, nil }
	code, out, stderr = e.run("task", "new", "generic", "--title", "j1", "--brief", "do job", "--cwd", "/w", "--command", "cmd", "--timeout", "60")
	if code != 0 {
		t.Fatalf("task new: %d %s %s", code, out, stderr)
	}
	tid := strings.Fields(out)[0]
	var tv harness.Task
	_, jsonOut, _ := e.run("task", "show", tid, "--json")
	_ = json.Unmarshal([]byte(jsonOut), &tv)
	jobPane := fakePane(e, tv.Pane)
	e.app.OpenSender = func(*Options, term.Backend) (*term.Pane, error) { return jobPane, nil }
	code, _, stderr = e.run("report", "cannot")
	if code != 2 || !strings.Contains(stderr, "use harness-ops task progress") {
		t.Fatalf("expected refusal on running job, got %d %q", code, stderr)
	}
	code, out, _ = e.run("whoami")
	if code != 0 || !strings.Contains(out, "task: "+tid+" (j1)") || !strings.Contains(out, "lead: fake:9") {
		t.Fatalf("whoami on task: code=%d out=%q", code, out)
	}
}

func TestScopeLsTreeShowMineLogResumeAndAnswerDenyKeyDetach(t *testing.T) {
	e := newScopeEnv(t)
	e.app.StartWatcher = func(*Options, string) error { return nil }
	clk := e.app.Clock.(*termtest.Clock)

	// Attach target as scope with stall=30s and sub-depth=1.
	code, out, stderr := e.run("attach", "target", "--name", "api-scope", "--about", "build api", "--stall", "30s", "--sub-depth", "1")
	if code != 0 {
		t.Fatalf("attach: %d %s %s", code, out, stderr)
	}
	_, sJSON, _ := e.run("scope", "show", "api-scope", "--json")
	var sv harness.ScopeView
	if err := json.Unmarshal([]byte(sJSON), &sv); err != nil {
		t.Fatal(err)
	}

	// Send msg to target pane and verify log --scope and unscoped [scope <sID>].
	if code, _, stderr := e.run("msg", "target", "implement endpoints"); code != 0 {
		t.Fatal(stderr)
	}
	// Spawn a child task from target (lead of api-scope).
	targetPane := fakePane(e, "fake:1")
	e.app.OpenSender = func(*Options, term.Backend) (*term.Pane, error) { return targetPane, nil }
	code, out, stderr = e.run("task", "new", "generic", "--title", "api-tests", "--brief", "write tests", "--cwd", "/w/t", "--command", "cmd", "--timeout", "60")
	if code != 0 {
		t.Fatalf("child task new: %d %s %s", code, out, stderr)
	}
	childID := strings.Fields(out)[0]

	// Child is leaf (sub-depth 0) -> spawn must exit 6!
	var ctv harness.Task
	_, cJSON, _ := e.run("task", "show", childID, "--json")
	_ = json.Unmarshal([]byte(cJSON), &ctv)
	childPane := fakePane(e, ctv.Pane)
	e.app.OpenSender = func(*Options, term.Backend) (*term.Pane, error) { return childPane, nil }
	code, _, stderr = e.run("spawn", "generic", "--cwd", "/w/t", "--command", "cmd")
	if code != 6 || !strings.Contains(stderr, "leaf") {
		t.Fatalf("expected exit 6 leaf spawn refusal, got %d %q", code, stderr)
	}

	// Switch back to me (lead of api-scope) and test scope ls, ls --tree, ls --mine, answer/deny/key.
	mePane := fakePane(e, "fake:9")
	e.app.OpenSender = func(*Options, term.Backend) (*term.Pane, error) { return mePane, nil }

	// Set dialog on target (and watcher mark on lease, matching watch loop) so dialog mark & answer/deny/key work by scope name / sID.
	targetPane.Process = "claude"
	dlg1 := &termtest.Dialog{Body: "write x", Options: []string{"Yes", "No"}, Selected: 1}
	e.b.State["fake:1"].Dialog = dlg1
	d1, _ := e.app.newOps(e.b, mePane).ReadDialog(targetPane)
	if d1 == nil {
		t.Fatal("expected parsed dialog fp1")
	}
	if err := harness.NewRegistry("").Update(func(d *harness.Data) error {
		d.Leases[sv.Lease].Dialog = &harness.DialogMark{Fingerprint: d1.Fingerprint, Question: d1.Question, Notified: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	code, mineOut, stderr := e.run("ls", "--mine")
	if code != 0 || !strings.Contains(mineOut, "(scope api-scope)") || !strings.Contains(mineOut, "dialog "+d1.Fingerprint) {
		t.Fatalf("ls --mine: code=%d out=%q stderr=%q", code, mineOut, stderr)
	}
	var mineRows []row
	_, mineJSON, _ := e.run("ls", "--mine", "--json")
	if err := json.Unmarshal([]byte(mineJSON), &mineRows); err != nil || len(mineRows) != 1 || mineRows[0].Scope != "api-scope" || mineRows[0].Dialog != d1.Fingerprint {
		t.Fatalf("ls --mine --json: %s (%v)", mineJSON, err)
	}
	fp1 := mineRows[0].Dialog

	// Test key targeting scope by name (temporarily clear dialog so key passes dialog guard, then restore).
	e.b.State["fake:1"].Dialog = nil
	if code, _, stderr := e.run("key", "api-scope", "Down"); code != 0 {
		t.Fatalf("key on scope: %d %s", code, stderr)
	}
	e.b.State["fake:1"].Dialog = dlg1
	// Test answer targeting scope by sID.
	if code, _, stderr := e.run("answer", sv.ID, "1", "--expect", fp1); code != 0 || dlg1.Answer != 1 {
		t.Fatalf("answer on scope: %d %s ans=%d", code, stderr, dlg1.Answer)
	}

	// Set another dialog for deny + scope show --json dialog output.
	dlg2 := &termtest.Dialog{Body: "rm -rf build", Options: []string{"Allow", "Deny"}, Selected: 1}
	e.b.State["fake:1"].Dialog = dlg2
	d2, _ := e.app.newOps(e.b, mePane).ReadDialog(targetPane)
	if err := harness.NewRegistry("").Update(func(d *harness.Data) error {
		d.Leases[sv.Lease].Dialog = &harness.DialogMark{Fingerprint: d2.Fingerprint, Question: d2.Question, Notified: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, sJSON2, _ := e.run("scope", "show", "api-scope", "--json")
	var sv2 harness.ScopeView
	_ = json.Unmarshal([]byte(sJSON2), &sv2)
	if sv2.Dialog == nil {
		t.Fatalf("expected scope show --json dialog, got %+v", sv2)
	}
	if code, _, stderr := e.run("deny", "api-scope", "--expect", sv2.Dialog.Fingerprint); code != 0 || dlg2.Answer != -1 {
		t.Fatalf("deny on scope: %d %s ans=%d", code, stderr, dlg2.Answer)
	}

	// scope ls --tree includes both api-scope and child api-tests.
	code, treeOut, stderr := e.run("scope", "ls", "--tree")
	if code != 0 || !strings.Contains(treeOut, "api-scope") || !strings.Contains(treeOut, "api-tests") {
		t.Fatalf("scope ls --tree: code=%d out=%q stderr=%q", code, treeOut, stderr)
	}
	clk.Sleep(35)

	// log --scope filters entries cleanly; unscoped log prints [scope <sID>].
	code, logOut, _ := e.run("log", "--scope", "api-scope")
	if code != 0 || !strings.Contains(logOut, "scope.attach") || !strings.Contains(logOut, "answer") {
		t.Fatalf("log --scope: code=%d out=%q", code, logOut)
	}
	code, allLogOut, _ := e.run("log")
	if code != 0 || !strings.Contains(allLogOut, "[scope "+sv.ID+"]") {
		t.Fatalf("unscoped log missing [scope sID]: %q", allLogOut)
	}

	// resume <scope>, resume <scope> --tree, and mixed resume . [--tree].
	code, resOut, stderr := e.run("resume", "api-scope")
	if code != 0 || !strings.Contains(resOut, "# Scope api-scope ("+sv.ID+")") || !strings.Contains(resOut, "build api") || !strings.Contains(resOut, "## Open child tasks (1)") {
		t.Fatalf("resume scope: code=%d out=%q stderr=%q", code, resOut, stderr)
	}
	code, resTreeOut, stderr := e.run("resume", "api-scope", "--tree")
	if code != 0 || !strings.Contains(resTreeOut, "# Tree of scope api-scope ("+sv.ID+")") || !strings.Contains(resTreeOut, childID) {
		t.Fatalf("resume scope --tree: code=%d out=%q stderr=%q", code, resTreeOut, stderr)
	}
	code, projOut, stderr := e.run("resume", ".")
	if code != 0 || !strings.Contains(projOut, "## Scopes (1)") || !strings.Contains(projOut, sv.ID) {
		t.Fatalf("resume .: code=%d out=%q stderr=%q", code, projOut, stderr)
	}
	code, projTreeOut, stderr := e.run("resume", ".", "--tree")
	if code != 0 || !strings.Contains(projTreeOut, sv.ID) || !strings.Contains(projTreeOut, childID) {
		t.Fatalf("resume . --tree: code=%d out=%q stderr=%q", code, projTreeOut, stderr)
	}

	// Detach --keep keeps scope unbound (visible in default scope ls); detaching without --keep drops it (--all only).
	code, out, stderr = e.run("detach", "api-scope", "--keep", "--notify")
	if code != 0 || !strings.Contains(out, "detached api-scope ("+sv.ID+", kept)") {
		t.Fatalf("detach --keep: code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, _ = e.run("scope", "ls")
	if code != 0 || !strings.Contains(out, "api-scope") || !strings.Contains(out, "unbound") {
		t.Fatalf("kept unbound scope missing in default scope ls: %q", out)
	}
	if code, out, stderr = e.run("detach", "api-scope"); code != 0 || !strings.Contains(out, "detached api-scope ("+sv.ID+")") {
		t.Fatalf("detach unbound drop: code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, _ = e.run("scope", "ls")
	if code != 0 || strings.Contains(out, "api-scope") {
		t.Fatalf("dropped scope still in default scope ls: %q", out)
	}
	code, out, _ = e.run("scope", "ls", "--all")
	if code != 0 || !strings.Contains(out, "api-scope") || !strings.Contains(out, "dropped") {
		t.Fatalf("dropped scope missing in scope ls --all: %q", out)
	}
}

func TestHarnessUsageListsScopeCommandsAndFlags(t *testing.T) {
	u := harnessUsage
	for _, want := range []string{
		"attach TARGET",
		"detach TARGET",
		"report [--notify] TEXT...",
		"scope ls",
		"scope show",
		"--mine",
		"whoami",
		"--scope NAME|ID",
		"resume (TASK-ID | SCOPE | DIR) [--tree]",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("harnessUsage missing %q:\n%s", want, u)
		}
	}
}
