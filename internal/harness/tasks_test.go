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
	if r.b.Events[0].Kind != "spawn" || r.b.Events[0].Text != "fake-cmd|/w" {
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
	for reported, stored := range map[string]string{"ok": "done", "failed": "failed", "blocked": "blocked"} {
		r := newRig(t)
		x := r.mustNew(t, nil, NewOpts{})
		d := r.done(t, x, reported, "result text")
		if d.Status != stored || d.Summary != "result text" || d.Ended == 0 {
			t.Fatalf("%+v", d)
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
	if d, err := w.Done(x.ID, "ok", "did it", ""); err != nil || d.Status != "done" {
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
	if g := r.get(t, x.ID); g.Status != "running" || g.Ended != 0 {
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

func TestFinishedTaskIsNotReconciled(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.done(t, x, "ok", "x")
	r.b.Close(r.pane(x))
	if r.get(t, x.ID).Status != "done" {
		t.Fatal()
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
	if len(texts) != 1 || !strings.HasPrefix(texts[0], "[agent-msg from ") || !strings.Contains(texts[0], "RE: task "+x.ID) || !strings.Contains(texts[0], "all good") {
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
	if d.Notify != "pending" || d.Status != "done" || len(r.data().Pending) != 1 {
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
	if d.Notify != "owner-gone" || d.Status != "done" || len(r.data().Pending) != 0 {
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
	r.tasks.Close(mine.ID, false)
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
	if g, err := r.tasks.Wait(x.ID, false, 60); err != nil || g.Status != "done" {
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

func TestTaskCloseAfterDoneIgnoresBusy(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.done(t, x, "ok", "x")
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
	if _, err := r.tasks.Close(x.ID, false); err != nil {
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
