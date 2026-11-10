package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/term"
	"github.com/samhvw8/termctl/internal/termtest"
)

type rig struct {
	owner, other *term.Pane
	b            *termtest.Fake
	clock        *termtest.Clock
	reg          *Registry
	tasks        *Tasks
	dir          string
}

func newRig(t *testing.T) *rig {
	owner := termtest.MakePane(9, "owner", "", "~/w/o", "")
	other := termtest.MakePane(1, "", "other", "~/w/x", "")
	b := termtest.New([]*term.Pane{other, owner}, owner)
	dir := t.TempDir()
	r := &rig{owner: owner, other: other, b: b, clock: &termtest.Clock{T: 1000}, reg: NewRegistry(filepath.Join(dir, "state")), dir: dir}
	r.tasks = r.as(owner)
	return r
}

func (r *rig) as(p *term.Pane) *Tasks {
	return &Tasks{Reg: r.reg, Backend: r.b, Sender: p, Clock: r.clock, Inbox: filepath.Join(r.dir, "inbox"),
		Factory: func(string, map[string]string) (term.Backend, error) { return r.b, nil }}
}

func (r *rig) new(t *testing.T, ts *Tasks, o NewOpts) (*Task, error) {
	t.Helper()
	if o.Title == "" {
		o.Title = "job"
	}
	if o.Brief == "" && o.BriefFile == "" {
		o.Brief = "do the thing"
	}
	if o.Cwd == "" {
		o.Cwd = "/w"
	}
	if o.Command == "" {
		o.Command = "fake-cmd"
	}
	if o.Harness == "" {
		o.Harness = "generic"
	}
	if o.Timeout == 0 {
		o.Timeout = 60
	}
	if ts == nil {
		ts = r.tasks
	}
	return ts.New(o)
}

func (r *rig) mustNew(t *testing.T, ts *Tasks, o NewOpts) *Task {
	t.Helper()
	x, err := r.new(t, ts, o)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func (r *rig) pane(x *Task) *term.Pane {
	for _, p := range r.b.Panes {
		if p.ID == x.Pane {
			return p
		}
	}
	return nil
}

func (r *rig) get(t *testing.T, id string) *Task {
	t.Helper()
	x, err := r.tasks.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func (r *rig) done(t *testing.T, x *Task, status, summary string) *Task {
	t.Helper()
	d, err := r.as(r.pane(x)).Done(x.ID, status, summary, "")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (r *rig) data() *Data {
	d, err := r.reg.Read()
	if err != nil {
		panic(err)
	}
	return d
}

func TestNewSpawnsAndSubmitsBriefWithFooter(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{Title: "write docs"})
	if x.Status != "running" || x.Owner != r.owner.ID || x.Depth != 1 {
		t.Fatalf("%+v", x)
	}
	if r.b.Events[0].Kind != "spawn" || !strings.HasPrefix(r.b.Events[0].Text, "fake-cmd; s=$?;") {
		t.Fatal(r.b.Events[0])
	}
	line := r.b.Texts(x.Pane)[0]
	if !strings.HasPrefix(line, "[task "+x.ID+" from owner] do the thing") ||
		!strings.Contains(line, "harness-ops task done "+x.ID+" --status ok|failed|blocked") || strings.Contains(line, "\n") {
		t.Fatal(line)
	}
	if r.pane(x).Label != x.ID || r.data().Panes[x.Pane] == nil {
		t.Fatal("label or pane record missing")
	}
	if b, _ := os.ReadFile(x.Brief); !strings.Contains(string(b), "do the thing") {
		t.Fatal(string(b))
	}
}

func TestLongBriefGoesThroughItsFile(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{Brief: "line one\nline two"})
	line := r.b.Texts(x.Pane)[0]
	if !strings.Contains(line, x.Brief) || strings.Contains(line, "line two") || !strings.Contains(line, "task done") {
		t.Fatal(line)
	}
	b, _ := os.ReadFile(x.Brief)
	if !strings.Contains(string(b), "line two") || !strings.Contains(string(b), "task done") {
		t.Fatal(string(b))
	}
}

func TestNewUsage(t *testing.T) {
	r := newRig(t)
	_, err := r.tasks.New(NewOpts{Harness: "generic", Title: "x", Cwd: "/w", Command: "c"})
	wantCode(t, err, 2)
	_, err = r.tasks.New(NewOpts{Harness: "generic", Title: "x", Cwd: "/w", Command: "c", Brief: "a", BriefFile: "b"})
	wantCode(t, err, 2)
	_, err = r.new(t, r.as(nil), NewOpts{})
	wantCode(t, err, 2)
}

func TestMaxRunningTasksPerOwner(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 6; i++ {
		r.mustNew(t, nil, NewOpts{})
	}
	before := len(r.b.Spawned)
	_, err := r.new(t, nil, NewOpts{})
	wantCode(t, err, 6)
	if len(r.b.Spawned) != before || !strings.Contains(err.Error(), "6") {
		t.Fatal(err)
	}
	r.mustNew(t, nil, NewOpts{MaxTasks: 7})
	t.Setenv("TERMCTL_MAX_TASKS", "8")
	r.mustNew(t, nil, NewOpts{})
}

func TestFinishedTasksFreeTheirSlot(t *testing.T) {
	r := newRig(t)
	var first *Task
	for i := 0; i < 6; i++ {
		x := r.mustNew(t, nil, NewOpts{})
		if i == 0 {
			first = x
		}
	}
	r.done(t, first, "ok", "fine")
	r.mustNew(t, nil, NewOpts{})
}

func TestDepthLimit(t *testing.T) {
	r := newRig(t)
	t1 := r.mustNew(t, nil, NewOpts{})
	t2 := r.mustNew(t, r.as(r.pane(t1)), NewOpts{})
	if t1.Depth != 1 || t2.Depth != 2 {
		t.Fatal(t1.Depth, t2.Depth)
	}
	_, err := r.new(t, r.as(r.pane(t2)), NewOpts{})
	wantCode(t, err, 6)
	if !strings.Contains(err.Error(), "depth") {
		t.Fatal(err)
	}
	t.Setenv("TERMCTL_MAX_DEPTH", "3")
	if x := r.mustNew(t, r.as(r.pane(t2)), NewOpts{}); x.Depth != 3 {
		t.Fatal(x.Depth)
	}
}

func TestFailedSpawnReleasesTheSlot(t *testing.T) {
	r := newRig(t)
	r.b.FailSpawn = true
	_, err := r.new(t, nil, NewOpts{})
	wantCode(t, err, 1)
	r.b.FailSpawn = false
	all, _ := r.tasks.Ls(false)
	if len(all) != 1 || all[0].Status != "failed" || !strings.Contains(all[0].Summary, "spawn") {
		t.Fatalf("%+v", all)
	}
}

func TestHarnessNotReadyMarksFailedAndKeepsPaneClosable(t *testing.T) {
	r := newRig(t)
	r.tasks.spawnHook = func(p *term.Pane) { r.b.State[p.ID].Busy = 1 << 30 } // its prompt never appears
	_, err := r.new(t, nil, NewOpts{Timeout: 5})
	wantCode(t, err, 5)
	all, _ := r.tasks.Ls(false)
	if all[0].Status != "failed" || r.data().Panes[all[0].Pane] == nil {
		t.Fatalf("%+v", all[0])
	}
}

func TestDoneSetsStatus(t *testing.T) {
	for reported, stored := range map[string]string{"ok": "ready", "failed": "failed", "blocked": "blocked"} {
		r := newRig(t)
		x := r.mustNew(t, nil, NewOpts{})
		d := r.done(t, x, reported, "result text")
		if d.Status != stored || d.Summary != "result text" || d.Ended == 0 {
			t.Fatalf("%+v", d)
		}
		if r.b.Has("close", x.Pane) {
			t.Fatal("task done must leave the session open")
		}
	}
}

func TestDoneTwiceIsRefused(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	w := r.as(r.pane(x))
	if _, err := w.Done(x.ID, "ok", "a", ""); err != nil {
		t.Fatal(err)
	}
	_, err := w.Done(x.ID, "ok", "b", "")
	wantCode(t, err, 2)
	if r.get(t, x.ID).Summary != "a" {
		t.Fatal()
	}
}

func TestBlockedCanFinish(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	w := r.as(r.pane(x))
	w.Done(x.ID, "blocked", "need input", "")
	if d, err := w.Done(x.ID, "ok", "did it", ""); err != nil || d.Status != "ready" {
		t.Fatal(d, err)
	}
}

func TestDoneUsage(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	w := r.as(r.pane(x))
	_, err := w.Done(x.ID, "ok", "", "")
	wantCode(t, err, 2)
	_, err = w.Done(x.ID, "maybe", "x", "")
	wantCode(t, err, 2)
	f := filepath.Join(t.TempDir(), "s.txt")
	os.WriteFile(f, []byte("from file\n"), 0o644)
	if d, err := w.Done(x.ID, "ok", "", f); err != nil || d.Summary != "from file" {
		t.Fatal(d, err)
	}
}

func TestUnrelatedPaneCannotReport(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	_, err := r.as(r.other).Done(x.ID, "ok", "not mine", "")
	wantCode(t, err, 2)
	if r.get(t, x.ID).Status != "running" {
		t.Fatal()
	}
}

func TestIDPrefixAndUnknown(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	if r.get(t, x.ID[:4]).ID != x.ID {
		t.Fatal()
	}
	_, err := r.tasks.Get("zzzzzz")
	wantCode(t, err, 2)
}

func TestSend(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	if err := r.tasks.Send(x.ID, "one more thing", false); err != nil {
		t.Fatal(err)
	}
	if !termtest.Contains(r.b.Texts(x.Pane), "one more thing") {
		t.Fatal(r.b.Texts(x.Pane))
	}
	r.b.State[x.Pane].Input = "half"
	wantCode(t, r.tasks.Send(x.ID, "x", false), 4)
}

func TestSendReopensAFinishedTask(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.done(t, x, "blocked", "stuck")
	if err := r.tasks.Send(x.ID, "here is the answer", false); err != nil {
		t.Fatal(err)
	}
	if g := r.get(t, x.ID); g.Status != "running" || g.Ended != 0 || g.Round != 2 {
		t.Fatalf("%+v", g)
	}
}

func TestSendToLostTaskRefused(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.b.Close(r.pane(x))
	wantCode(t, r.tasks.Send(x.ID, "hello?", false), 2)
}

func TestGonePaneBecomesLost(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.b.Close(r.pane(x))
	if r.get(t, x.ID).Status != "lost" || r.data().Tasks[x.ID].Status != "lost" {
		t.Fatal()
	}
}

func TestBackendFailureIsNotLost(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.b.FailList = true
	if r.get(t, x.ID).Status != "running" {
		t.Fatal()
	}
}

func TestRestartedServerMeansLost(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.reg.Update(func(d *Data) error { d.Tasks[x.ID].Ref = map[string]string{"pid": "111"}; return nil })
	r.b.Ref = map[string]string{"pid": "222"}
	if r.get(t, x.ID).Status != "lost" {
		t.Fatal()
	}
}

func TestAcceptedTaskIsNotReconciled(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.done(t, x, "ok", "x")
	r.tasks.Accept(x.ID, false)
	r.b.Close(r.pane(x))
	if r.get(t, x.ID).Status != "done" {
		t.Fatal()
	}
}

func TestReadyTaskWithGonePaneIsLost(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.done(t, x, "ok", "x")
	r.b.Close(r.pane(x))
	if g := r.get(t, x.ID); g.Status != "lost" || g.Summary != "x" {
		t.Fatalf("%+v", g)
	}
}

func TestStaleReservationBecomesLost(t *testing.T) {
	r := newRig(t)
	r.reg.Update(func(d *Data) error {
		d.Tasks["tabc"] = &Task{ID: "tabc", Status: "running", Owner: r.owner.ID, Started: r.clock.T}
		return nil
	})
	if r.get(t, "tabc").Status != "running" {
		t.Fatal()
	}
	r.clock.T += 601
	if r.get(t, "tabc").Status != "lost" {
		t.Fatal()
	}
}

func TestDoneNotifiesOwner(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.done(t, x, "ok", "all good")
	texts := r.b.Texts(r.owner.ID)
	if len(texts) != 1 || !strings.HasPrefix(texts[0], "[agent-ops from ") || !strings.Contains(texts[0], "RE: task "+x.ID) || !strings.Contains(texts[0], "all good") {
		t.Fatal(texts)
	}
	if r.get(t, x.ID).Notify != "delivered" || len(r.data().Pending) != 0 {
		t.Fatal()
	}
}

func TestDraftOnOwnerKeepsNotificationPending(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.b.State[r.owner.ID].Input = "owner typing"
	start := r.clock.T
	d := r.done(t, x, "ok", "finished")
	if len(r.b.Texts(r.owner.ID)) != 0 || r.b.State[r.owner.ID].Input != "owner typing" {
		t.Fatal("joined the draft")
	}
	if d.Notify != "pending" || d.Status != "ready" || len(r.data().Pending) != 1 {
		t.Fatalf("%+v", d)
	}
	if e := r.clock.T - start; e <= 0 || e > 15 {
		t.Fatal(e)
	}
	all, _ := r.tasks.Ls(false)
	if all[0].Summary != "finished" {
		t.Fatal()
	}
}

func TestPendingDeliveredOnceTheDraftIsGone(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	st := r.b.State[r.owner.ID]
	st.Input = "owner typing"
	r.done(t, x, "ok", "finished")
	st.Input = ""
	if n, _ := r.tasks.FlushPending(); n != 1 {
		t.Fatal(n)
	}
	if n, _ := r.tasks.FlushPending(); n != 0 {
		t.Fatal(n)
	}
	if len(r.b.Texts(r.owner.ID)) != 1 || r.get(t, x.ID).Notify != "delivered" {
		t.Fatal(r.b.Texts(r.owner.ID))
	}
}

func TestClaimedNotificationIsNotDeliveredTwice(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	st := r.b.State[r.owner.ID]
	st.Input = "owner typing"
	r.done(t, x, "ok", "finished")
	st.Input = ""
	r.reg.Update(func(d *Data) error { d.Pending[0].Claimed = r.clock.T; return nil })
	if n, _ := r.tasks.FlushPending(); n != 0 || len(r.b.Texts(r.owner.ID)) != 0 {
		t.Fatal(n)
	}
	r.clock.T += 31 // the claimer crashed
	if n, _ := r.tasks.FlushPending(); n != 1 {
		t.Fatal(n)
	}
}

func TestNotificationToGoneOwner(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	w := r.as(r.pane(x))
	r.b.Close(r.owner)
	d, _ := w.Done(x.ID, "ok", "x", "")
	if d.Notify != "owner-gone" || d.Status != "ready" || len(r.data().Pending) != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestPendingExpires(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.b.State[r.owner.ID].Input = "owner typing"
	r.done(t, x, "ok", "x")
	r.clock.T += PendingTTL + 1
	r.tasks.FlushPending()
	if len(r.data().Pending) != 0 || r.get(t, x.ID).Notify != "expired" {
		t.Fatal()
	}
}

func TestLongSummaryUsesTheFileRule(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.done(t, x, "ok", strings.Repeat("z", 900))
	msg := r.b.Texts(r.owner.ID)[0]
	if !strings.Contains(msg, "read the file") || strings.Contains(msg, strings.Repeat("z", 900)) {
		t.Fatal(msg)
	}
}

func TestLs(t *testing.T) {
	r := newRig(t)
	mine := r.mustNew(t, nil, NewOpts{})
	theirs := r.mustNew(t, r.as(r.other), NewOpts{})
	got, _ := r.tasks.Ls(false)
	if len(got) != 1 || got[0].ID != mine.ID {
		t.Fatalf("%+v", got)
	}
	all, _ := r.tasks.Ls(true)
	if len(all) != 2 {
		t.Fatal(theirs.ID, len(all))
	}
	r.done(t, mine, "ok", "x")
	r.tasks.Accept(mine.ID, true)
	got, _ = r.tasks.Ls(false)
	if len(got) != 0 {
		t.Fatal(got)
	}
}

func TestWait(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	w := r.as(r.pane(x))
	r.tasks.sleepHook = func(n int) {
		if n == 3 {
			w.Done(x.ID, "ok", "x", "")
		}
	}
	if g, err := r.tasks.Wait(x.ID, false, 60); err != nil || g.Status != "ready" {
		t.Fatal(g, err)
	}
}

func TestWaitTimesOut(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	_, err := r.tasks.Wait(x.ID, false, 10)
	wantCode(t, err, 5)
}

func TestWaitAnyReturnsEachResultOnce(t *testing.T) {
	r := newRig(t)
	a := r.mustNew(t, nil, NewOpts{})
	b := r.mustNew(t, nil, NewOpts{})
	r.done(t, a, "ok", "1")
	r.clock.T++
	r.done(t, b, "failed", "2")
	if g, _ := r.tasks.Wait("", true, 1); g.ID != a.ID {
		t.Fatal(g.ID)
	}
	if g, _ := r.tasks.Wait("", true, 1); g.ID != b.ID {
		t.Fatal(g.ID)
	}
	_, err := r.tasks.Wait("", true, 1)
	wantCode(t, err, 2)
}

func TestWaitAnyTimesOutWhileRunning(t *testing.T) {
	r := newRig(t)
	r.mustNew(t, nil, NewOpts{})
	_, err := r.tasks.Wait("", true, 5)
	wantCode(t, err, 5)
}

func TestWaitReturnsOnLost(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.b.Close(r.pane(x))
	if g, _ := r.tasks.Wait(x.ID, false, 10); g.Status != "lost" {
		t.Fatal(g.Status)
	}
}

func TestTaskCloseRules(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	_, err := r.tasks.Close(x.ID, false)
	wantCode(t, err, 6)
	if r.b.Has("close", x.Pane) {
		t.Fatal("closed a running task")
	}
	if _, err := r.tasks.Close(x.ID, true); err != nil {
		t.Fatal(err)
	}
	g := r.get(t, x.ID)
	if !r.b.Has("close", x.Pane) || g.Status != "failed" || !g.Closed || !strings.Contains(g.Summary, "closed") {
		t.Fatalf("%+v", g)
	}
}

func TestTaskCloseBeforeAcceptIsRefused(t *testing.T) {
	for _, status := range []string{"ok", "failed", "blocked"} {
		r := newRig(t)
		x := r.mustNew(t, nil, NewOpts{})
		r.done(t, x, status, "x")
		_, err := r.tasks.Close(x.ID, false)
		wantCode(t, err, 6)
		if r.b.Has("close", x.Pane) {
			t.Fatal("closed before accept")
		}
		if _, err := r.tasks.Close(x.ID, true); err != nil || !r.b.Has("close", x.Pane) {
			t.Fatal(err)
		}
		if g := r.get(t, x.ID); !g.Closed || g.Status == "done" {
			t.Fatalf("--force must not accept: %+v", g)
		}
	}
}

func TestCloseAfterAcceptIgnoresBusy(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.done(t, x, "ok", "x")
	if _, err := r.tasks.Accept(x.ID, false); err != nil {
		t.Fatal(err)
	}
	r.b.Natives[x.Pane] = "working: finishing its turn"
	if _, err := r.tasks.Close(x.ID, false); err != nil {
		t.Fatal(err)
	}
	if !r.b.Has("close", x.Pane) || r.data().Panes[x.Pane] != nil {
		t.Fatal()
	}
}

func TestTaskCloseWithPaneAlreadyGone(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.done(t, x, "ok", "x")
	r.b.Close(r.pane(x))
	if _, err := r.tasks.Close(x.ID, false); err != nil { // lost: nothing left to accept
		t.Fatal(err)
	}
	if !r.get(t, x.ID).Closed {
		t.Fatal()
	}
}

func TestTaskCloseNeverClosesSelf(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	w := r.pane(x)
	r.done(t, x, "ok", "x")
	_, err := r.as(w).Close(x.ID, true)
	wantCode(t, err, 2)
	if r.b.Has("close", x.Pane) {
		t.Fatal()
	}
}

func TestReuseClearsThenSubmits(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{Reuse: "other"})
	typedText := r.b.Texts(r.other.ID)
	if x.Pane != r.other.ID || typedText[0] != "/clear" || !strings.Contains(typedText[1], x.ID) || !strings.Contains(typedText[1], "do the thing") {
		t.Fatal(typedText)
	}
	if len(r.b.Spawned) != 0 {
		t.Fatal("spawned")
	}
}

func TestReuseRefusals(t *testing.T) {
	r := newRig(t)
	r.b.Natives[r.other.ID] = "working: something"
	_, err := r.new(t, nil, NewOpts{Reuse: "other", Timeout: 5})
	wantCode(t, err, 6)
	all, _ := r.tasks.Ls(false)
	if len(r.b.Texts(r.other.ID)) != 0 || len(all) != 0 {
		t.Fatal(all)
	}
	delete(r.b.Natives, r.other.ID)

	r.b.State[r.other.ID].Input = "someone typing"
	_, err = r.new(t, nil, NewOpts{Reuse: "other"})
	wantCode(t, err, 4)
	r.b.State[r.other.ID].Input = ""

	_, err = r.tasks.New(NewOpts{Harness: "claude", Title: "x", Brief: "y", Cwd: "/w", Reuse: "other"})
	wantCode(t, err, 2)

	_, err = r.new(t, nil, NewOpts{Reuse: "owner"})
	wantCode(t, err, 2)

	x := r.mustNew(t, nil, NewOpts{})
	_, err = r.new(t, nil, NewOpts{Reuse: x.ID})
	wantCode(t, err, 6)
}

func TestClosingAReusedTaskLeavesThePane(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{Reuse: "other"})
	r.as(r.other).Done(x.ID, "ok", "x", "")
	if _, err := r.tasks.Close(x.ID, true); err != nil {
		t.Fatal(err)
	}
	if r.b.Has("close", r.other.ID) || !r.get(t, x.ID).Closed {
		t.Fatal()
	}
}

func spawned(t *testing.T, r *rig) *term.Pane {
	p, err := r.tasks.Spawn("generic", "tab", "/w", SpawnOpts{Command: "c", Label: "made", Timeout: 60})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCloseOwnedIdlePane(t *testing.T) {
	r := newRig(t)
	p := spawned(t, r)
	if r.data().Panes[p.ID] == nil {
		t.Fatal("not recorded")
	}
	if _, err := r.tasks.ClosePane("made", false); err != nil {
		t.Fatal(err)
	}
	if !r.b.Has("close", p.ID) || r.data().Panes[p.ID] != nil {
		t.Fatal()
	}
}

func TestCloseRefusesUnownedPane(t *testing.T) {
	r := newRig(t)
	_, err := r.tasks.ClosePane("other", false)
	wantCode(t, err, 6)
	if r.b.Has("close", r.other.ID) {
		t.Fatal()
	}
	if _, err := r.tasks.ClosePane("other", true); err != nil || !r.b.Has("close", r.other.ID) {
		t.Fatal(err)
	}
}

func TestCloseRefusesBusyPane(t *testing.T) {
	r := newRig(t)
	p := spawned(t, r)
	r.b.Natives[p.ID] = "working: long job"
	_, err := r.tasks.ClosePane("made", false)
	wantCode(t, err, 6)
	if !strings.Contains(err.Error(), "busy") {
		t.Fatal(err)
	}
	if _, err := r.tasks.ClosePane("made", true); err != nil || !r.b.Has("close", p.ID) {
		t.Fatal(err)
	}
}

func TestCloseRefusesSelfEvenWithForce(t *testing.T) {
	r := newRig(t)
	_, err := r.tasks.ClosePane("owner", true)
	wantCode(t, err, 2)
	if r.b.Has("close", r.owner.ID) {
		t.Fatal()
	}
}

func TestCloseRefusesPaneOfRunningTask(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	_, err := r.tasks.ClosePane(x.ID, false)
	wantCode(t, err, 6)
	if !strings.Contains(err.Error(), "task") {
		t.Fatal(err)
	}
}

func TestCloseStaleRecordIsNotOwned(t *testing.T) {
	r := newRig(t)
	p := spawned(t, r)
	r.reg.Update(func(d *Data) error { d.Panes[p.ID].Ref = map[string]string{"pid": "111"}; return nil })
	r.b.Ref = map[string]string{"pid": "222"}
	_, err := r.tasks.ClosePane("made", false)
	wantCode(t, err, 6)
}

func TestRegistryFieldNamesAreStable(t *testing.T) {
	r := newRig(t)
	r.mustNew(t, nil, NewOpts{})
	raw, _ := os.ReadFile(r.reg.Path)
	for _, f := range []string{`"id"`, `"title"`, `"harness"`, `"pane"`, `"owner"`, `"cwd"`, `"brief"`, `"status"`, `"started"`, `"ended"`, `"summary"`, `"depth"`, `"notify"`, `"closed"`, `"owned"`} {
		if !strings.Contains(string(raw), f) {
			t.Error(f)
		}
	}
}

// --- lead-owned lifecycle ------------------------------------------------

func TestAcceptFinishesAReadyTask(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.done(t, x, "ok", "x")
	g, err := r.tasks.Accept(x.ID, false)
	if err != nil || g.Status != "done" || g.Closed || r.b.Has("close", x.Pane) {
		t.Fatalf("%+v %v", g, err)
	}
}

func TestAcceptClose(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.done(t, x, "ok", "x")
	g, err := r.tasks.Accept(x.ID, true)
	if err != nil || g.Status != "done" || !g.Closed || !r.b.Has("close", x.Pane) {
		t.Fatalf("%+v %v", g, err)
	}
}

func TestOnlyTheOwnerAccepts(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.done(t, x, "ok", "x")
	for _, who := range []*term.Pane{r.pane(x), r.other, nil} {
		_, err := r.as(who).Accept(x.ID, false)
		wantCode(t, err, 2)
	}
	if r.get(t, x.ID).Status != "ready" {
		t.Fatal()
	}
}

func TestAcceptNeedsAReadyTask(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	_, err := r.tasks.Accept(x.ID, false) // still running
	wantCode(t, err, 2)
	r.done(t, x, "failed", "broke")
	_, err = r.tasks.Accept(x.ID, false)
	wantCode(t, err, 2)
	if r.get(t, x.ID).Status != "failed" {
		t.Fatal("accept must not turn a failure into done")
	}
}

func TestTaskDoneNeverMeansDoneEvenFromTheOwner(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	d, err := r.tasks.Done(x.ID, "ok", "owner reports", "")
	if err != nil || d.Status != "ready" {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestFollowUpRounds(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	if x.Round != 1 {
		t.Fatal(x.Round)
	}
	w := r.as(r.pane(x))
	w.Done(x.ID, "ok", "first", "")
	if err := r.tasks.Send(x.ID, "reviewer found a bug", false); err != nil {
		t.Fatal(err)
	}
	g := r.get(t, x.ID)
	if g.Status != "running" || g.Round != 2 {
		t.Fatalf("%+v", g)
	}
	if err := r.tasks.Send(x.ID, "and another note", false); err != nil { // still running: same round
		t.Fatal(err)
	}
	if r.get(t, x.ID).Round != 2 {
		t.Fatal("a message during a round must not open a new one")
	}
	if d, err := w.Done(x.ID, "ok", "second", ""); err != nil || d.Status != "ready" || d.Round != 2 || d.Summary != "second" {
		t.Fatalf("%+v %v", d, err)
	}
	notes := r.b.Texts(r.owner.ID)
	if len(notes) != 2 || !strings.Contains(notes[1], "round 2") {
		t.Fatal(notes)
	}
}

func TestFooterExplainsReady(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	if !strings.Contains(r.b.Texts(x.Pane)[0], "`task done` tells the lead you are ready; keep this session open.") {
		t.Fatal(r.b.Texts(x.Pane)[0])
	}
}

func TestClosePaneRefusesUnacceptedTask(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.done(t, x, "ok", "x")
	_, err := r.tasks.ClosePane(x.ID, false)
	wantCode(t, err, 6)
}

func TestReadyTasksFreeARunningSlotButStillHoldThePane(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.done(t, x, "ok", "x")
	_, err := r.new(t, nil, NewOpts{Reuse: x.ID})
	wantCode(t, err, 6)
}

// --- harness exit --------------------------------------------------------

func TestSpawnKeepsAShellAfterTheHarness(t *testing.T) {
	got := SurvivingCommand("claude --model haiku", "")
	if !strings.HasPrefix(got, "claude --model haiku; s=$?;") || !strings.HasSuffix(got, `exec "${SHELL:-/bin/sh}" -l`) {
		t.Fatal(got)
	}
	hooked := SurvivingCommand("omp", "/x/harness-ops task exited t1")
	if !strings.Contains(hooked, `/x/harness-ops task exited t1 --code "$s"`) {
		t.Fatal(hooked)
	}
}

func TestTaskSpawnCommandLeavesAShell(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	if !strings.Contains(r.b.Events[0].Text, "exec") || !strings.Contains(r.b.Events[0].Text, "task exited "+x.ID) {
		t.Fatal(r.b.Events[0].Text)
	}
}

func TestPlainSpawnAlsoLeavesAShell(t *testing.T) {
	r := newRig(t)
	spawned(t, r)
	if !strings.Contains(r.b.Events[0].Text, "exec") {
		t.Fatal(r.b.Events[0].Text)
	}
}

func TestExitedHarnessIsDetectedAndOwnerTold(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.pane(x).Process = "zsh" // the harness is gone, the shell is what remains
	g := r.get(t, x.ID)
	if g.Status != "exited" || r.b.Has("close", x.Pane) {
		t.Fatalf("%+v", g)
	}
	if _, err := r.tasks.FlushPending(); err != nil {
		t.Fatal(err)
	}
	notes := r.b.Texts(r.owner.ID)
	if len(notes) != 1 || !strings.Contains(notes[0], "exited") || !strings.Contains(notes[0], x.ID) {
		t.Fatal(notes)
	}
	if r.get(t, x.ID).Status != "exited" {
		t.Fatal("exited must not change on a second read")
	}
}

func TestExitedKeepsAReadyWorkersSummary(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	p := r.pane(x)
	r.done(t, x, "ok", "the report")
	p.Process = "bash"
	if g := r.get(t, x.ID); g.Status != "exited" || g.Summary != "the report" {
		t.Fatalf("%+v", g)
	}
}

func TestFailedStaysFailedWhenTheHarnessExits(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	p := r.pane(x)
	r.done(t, x, "failed", "broke")
	p.Process = "zsh"
	if g := r.get(t, x.ID); g.Status != "failed" {
		t.Fatalf("%+v", g)
	}
}

func TestShellBeforeLaunchIsNotExited(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.reg.Update(func(d *Data) error { d.Tasks[x.ID].Launched = false; return nil })
	r.pane(x).Process = "zsh" // the login shell, before the harness has started
	if r.get(t, x.ID).Status != "running" {
		t.Fatal()
	}
}

func TestExitHook(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	w := r.as(r.pane(x))
	g, err := w.Exited(x.ID, 3)
	if err != nil || g.Status != "exited" || g.ExitCode == nil || *g.ExitCode != 3 || !strings.Contains(g.Summary, "3") {
		t.Fatalf("%+v %v", g, err)
	}
	if !strings.Contains(r.b.Texts(r.owner.ID)[0], "exited") {
		t.Fatal(r.b.Texts(r.owner.ID))
	}
	// A second report, and reconcile, change nothing and tell nobody twice.
	if g2, err := w.Exited(x.ID, 9); err != nil || *g2.ExitCode != 3 {
		t.Fatalf("%+v %v", g2, err)
	}
	if len(r.b.Texts(r.owner.ID)) != 1 {
		t.Fatal(r.b.Texts(r.owner.ID))
	}
}

func TestExitHookFromAnotherPaneIsRefused(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	_, err := r.as(r.other).Exited(x.ID, 1)
	wantCode(t, err, 2)
}

func TestExitedAfterAcceptChangesNothing(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	w := r.as(r.pane(x))
	r.done(t, x, "ok", "x")
	r.tasks.Accept(x.ID, false)
	if g, err := w.Exited(x.ID, 0); err != nil || g.Status != "done" {
		t.Fatalf("%+v %v", g, err)
	}
}

func TestSendToExitedTaskIsRefused(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.pane(x).Process = "zsh"
	wantCode(t, r.tasks.Send(x.ID, "hello?", false), 2)
	if len(r.b.Texts(x.Pane)) != 1 { // only the brief: nothing typed into the bare shell
		t.Fatal(r.b.Texts(x.Pane))
	}
}

func TestCloseExitedTaskNeedsForce(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.pane(x).Process = "zsh"
	r.get(t, x.ID)
	_, err := r.tasks.Close(x.ID, false)
	wantCode(t, err, 6)
	if _, err := r.tasks.Close(x.ID, true); err != nil || !r.b.Has("close", x.Pane) {
		t.Fatal(err)
	}
}

// --- blind review -------------------------------------------------------

func readyTask(t *testing.T, r *rig) *Task {
	x := r.mustNew(t, nil, NewOpts{Brief: "add the widget"})
	r.done(t, x, "ok", "SECRET-WORKER-SUMMARY")
	return r.get(t, x.ID)
}

func TestBlindReviewGetsNoWorkerSummary(t *testing.T) {
	r := newRig(t)
	x := readyTask(t, r)
	rv, err := r.tasks.Review(ReviewOpts{ID: x.ID, Blind: true, Harness: "claude", Model: "haiku", Timeout: 60})
	if err != nil {
		t.Fatal(err)
	}
	if rv.ReviewOf != x.ID || !rv.Blind || rv.ID == x.ID || rv.Pane == x.Pane {
		t.Fatalf("%+v", rv)
	}
	line := r.b.Texts(rv.Pane)[0]
	brief, _ := os.ReadFile(rv.Brief)
	for _, text := range []string{line, string(brief)} {
		if strings.Contains(text, "SECRET-WORKER-SUMMARY") {
			t.Fatal("the reviewer saw the worker's summary: ", text)
		}
	}
	if !strings.Contains(string(brief), "add the widget") {
		t.Fatal("the reviewer needs the requirements: ", string(brief))
	}
	if strings.Contains(string(brief), "task done "+x.ID) {
		t.Fatal("the reviewer must not get the worker's report line: ", string(brief))
	}
}

func TestBlindReviewCommandUsesModelAndReadOnlyMode(t *testing.T) {
	r := newRig(t)
	x := readyTask(t, r)
	r.tasks.Review(ReviewOpts{ID: x.ID, Blind: true, Harness: "claude", Model: "haiku", Timeout: 60})
	var cmd string
	for _, e := range r.b.Events {
		if e.Kind == "spawn" {
			cmd = e.Text
		}
	}
	if !strings.Contains(cmd, "claude --model=haiku") || !strings.Contains(cmd, "--permission-mode dontAsk") || !strings.Contains(cmd, "Bash(harness-ops task done *)") || !strings.Contains(cmd, "--setting-sources project") || !strings.Contains(cmd, "--disallowedTools Edit Write") {
		t.Fatal(cmd)
	}
}

func TestBlindReviewWarnsWithoutReadOnlyMode(t *testing.T) {
	r := newRig(t)
	x := readyTask(t, r)
	var warned string
	old := Warn
	Warn = func(m string) { warned += m }
	defer func() { Warn = old }()
	if _, err := r.tasks.Review(ReviewOpts{ID: x.ID, Blind: true, Harness: "omp", Model: "m", Timeout: 60}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warned, "read-only") {
		t.Fatal(warned)
	}
}

func TestBlindReviewBriefFile(t *testing.T) {
	r := newRig(t)
	x := readyTask(t, r)
	f := filepath.Join(t.TempDir(), "b.md")
	os.WriteFile(f, []byte("Review only cmd/main.go."), 0o644)
	rv, err := r.tasks.Review(ReviewOpts{ID: x.ID, Blind: true, Harness: "claude", BriefFile: f, Timeout: 60})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(rv.Brief); !strings.Contains(string(b), "Review only cmd/main.go.") {
		t.Fatal(string(b))
	}
}

func TestReviewUsage(t *testing.T) {
	r := newRig(t)
	x := readyTask(t, r)
	_, err := r.tasks.Review(ReviewOpts{ID: x.ID, Harness: "claude"})
	wantCode(t, err, 2) // non-blind review is task send
	_, err = r.tasks.Review(ReviewOpts{ID: x.ID, Blind: true})
	wantCode(t, err, 2) // needs a harness
	y := r.mustNew(t, nil, NewOpts{})
	_, err = r.tasks.Review(ReviewOpts{ID: y.ID, Blind: true, Harness: "claude"})
	wantCode(t, err, 2) // the worker has not finished
}

func TestReviewerCannotReadTheReviewedTask(t *testing.T) {
	r := newRig(t)
	x := readyTask(t, r)
	rv, _ := r.tasks.Review(ReviewOpts{ID: x.ID, Blind: true, Harness: "claude", Timeout: 60})
	reviewer := r.as(r.pane(rv))
	_, err := reviewer.Show(x.ID)
	wantCode(t, err, 2)
	all, _ := reviewer.Ls(true)
	for _, y := range all {
		if y.ID == x.ID {
			t.Fatal("reviewed task visible to its blind reviewer")
		}
	}
	if g, err := r.tasks.Show(x.ID); err != nil || g.ID != x.ID {
		t.Fatal(g, err)
	}
}

func TestReviewerReportsLikeATask(t *testing.T) {
	r := newRig(t)
	x := readyTask(t, r)
	before := len(r.b.Texts(r.owner.ID))
	rv, _ := r.tasks.Review(ReviewOpts{ID: x.ID, Blind: true, Harness: "claude", Timeout: 60})
	d, err := r.as(r.pane(rv)).Done(rv.ID, "ok", "verdict: pass", "")
	if err != nil || d.Status != "ready" {
		t.Fatalf("%+v %v", d, err)
	}
	notes := r.b.Texts(r.owner.ID)
	if len(notes) != before+1 || !strings.Contains(notes[before], "verdict: pass") {
		t.Fatal(notes)
	}
}

// --- migration ----------------------------------------------------------

const oldRegistry = `{
 "version": 1,
 "tasks": {
  "tcfcdbf": {"id": "tcfcdbf", "title": "job", "harness": "omp", "pane": "fake:50", "ref": {}, "owner": "fake:9", "owner_ref": {},
   "cwd": "/w", "brief": "/b", "status": "running", "started": 1000, "ended": 0, "summary": "", "depth": 1, "notify": "none",
   "closed": false, "owned": true, "seen": false},
  "told": {"id": "told", "title": "old", "harness": "omp", "pane": "", "ref": {}, "owner": "fake:9", "owner_ref": {},
   "cwd": "/w", "brief": "/b", "status": "done", "started": 900, "ended": 950, "summary": "s", "depth": 1, "notify": "delivered",
   "closed": false, "owned": true, "seen": true}
 },
 "panes": {}, "pending": []
}`

func TestOldRegistryMigratesOnRead(t *testing.T) {
	r := newRig(t)
	os.MkdirAll(r.reg.Root, 0o755)
	os.WriteFile(r.reg.Path, []byte(oldRegistry), 0o644)
	worker := termtest.MakePane(50, "tcfcdbf", "", "/w", "omp")
	r.b.Panes = append(r.b.Panes, worker)
	r.b.State[worker.ID] = &termtest.Screen{Log: []string{"x"}}
	d := r.data()
	if d.Tasks["tcfcdbf"].Round != 1 || !d.Tasks["tcfcdbf"].Launched || d.Version != registryVersion {
		t.Fatalf("%+v", d.Tasks["tcfcdbf"])
	}
	if r.get(t, "tcfcdbf").Status != "running" {
		t.Fatal("an old running task must stay running")
	}
	// A worker from the old format can report, and an already-finished old task stays closable.
	g, err := r.as(worker).Done("tcfcdbf", "ok", "finished", "")
	if err != nil || g.Status != "ready" {
		t.Fatalf("%+v %v", g, err)
	}
	if _, err := r.tasks.Close("told", false); err != nil {
		t.Fatal(err)
	}
}
