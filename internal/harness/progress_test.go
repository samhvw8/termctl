package harness

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/term"
)

// --- G1: parent link and tree -------------------------------------------

// A task started from a worker's pane records the task that pane holds as its parent; one started from a
// pane that holds none has no parent.
func TestNewRecordsTheParentTask(t *testing.T) {
	r := newRig(t)
	t1 := r.mustNew(t, nil, NewOpts{SubDepth: 1})
	t2 := r.mustNew(t, r.as(r.pane(t1)), NewOpts{})
	if g := r.get(t, t1.ID); g.Parent != "" {
		t.Fatalf("a top task has parent %q", g.Parent)
	}
	if g := r.get(t, t2.ID); g.Parent != t1.ID || g.Depth != 2 {
		t.Fatalf("sub-task parent %q depth %d, want %s and 2", g.Parent, g.Depth, t1.ID)
	}
}

// task ls --tree nests each task's descendants under it, across owners; --all is a forest of every task.
func TestTreeNestsDescendantsAcrossOwners(t *testing.T) {
	t.Setenv("TERMCTL_MAX_DEPTH", "3")
	r := newRig(t)
	t1 := r.mustNew(t, nil, NewOpts{Title: "top", SubDepth: 2})
	t2 := r.mustNew(t, r.as(r.pane(t1)), NewOpts{Title: "sub", SubDepth: 1})
	t3 := r.mustNew(t, r.as(r.pane(t2)), NewOpts{Title: "subsub"})
	lone := r.mustNew(t, r.as(r.other), NewOpts{Title: "another lead's"})
	nodes, err := r.tasks.Tree(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Task.ID != t1.ID || len(nodes[0].Children) != 1 || nodes[0].Children[0].Task.ID != t2.ID ||
		len(nodes[0].Children[0].Children) != 1 || nodes[0].Children[0].Children[0].Task.ID != t3.ID {
		t.Fatalf("tree of the owner's tasks: %+v", nodes)
	}
	forest, err := r.tasks.Tree(true)
	if err != nil {
		t.Fatal(err)
	}
	var roots []string
	for _, n := range forest {
		roots = append(roots, n.Task.ID)
	}
	if strings.Join(roots, ",") != t1.ID+","+lone.ID {
		t.Fatalf("forest roots %v, want %s and %s", roots, t1.ID, lone.ID)
	}
	data, err := json.Marshal(forest)
	if err != nil {
		t.Fatal(err)
	}
	var back []map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	kids, _ := back[0]["children"].([]any)
	if back[0]["id"] != t1.ID || len(kids) != 1 || kids[0].(map[string]any)["id"] != t2.ID || kids[0].(map[string]any)["parent"] != t1.ID {
		t.Fatalf("json tree: %s", data)
	}
	if leaf, _ := back[1]["children"].([]any); leaf == nil || len(leaf) != 0 {
		t.Fatalf("a leaf has no empty children array: %s", data)
	}
}

// --- G6: task progress ----------------------------------------------------

// The worker's progress line is stored and journaled, the status and round stay, and --notify tells the owner.
func TestProgressKeepsTheStatusAndTellsTheOwner(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{Title: "port"})
	r.clock.T += 30
	at := r.clock.T
	got, err := r.as(r.pane(x)).Progress(x.ID, "half the files\nported", true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" || got.Round != 1 || got.Progress == nil || got.Progress.Text != "half the files ported" || got.Progress.At != at {
		t.Fatalf("%+v %+v", got, got.Progress)
	}
	texts := r.b.Texts(r.owner.ID)
	if len(texts) != 1 || !strings.Contains(texts[0], "PROGRESS task "+x.ID+" (port): half the files ported") {
		t.Fatalf("owner got %q", texts)
	}
	if g := r.get(t, x.ID); g.Status != "running" || g.Notify != "none" || g.Progress.Text != "half the files ported" {
		t.Fatalf("%+v", g)
	}
	entries, _, err := r.tasks.journal().Read(Filter{Task: x.ID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		found = found || (e.Event == "task.progress" && e.Text == "half the files ported")
	}
	if !found {
		t.Fatalf("no task.progress entry: %+v", entries)
	}
	// Without --notify nothing is typed; the owner may record progress too.
	if _, err := r.tasks.Progress(x.ID, "owner's note", false); err != nil {
		t.Fatal(err)
	}
	if len(r.b.Texts(r.owner.ID)) != 1 || r.get(t, x.ID).Progress.Text != "owner's note" {
		t.Fatal("progress without --notify typed something, or was not kept")
	}
}

// A busy owner gets only the latest progress: a newer notice replaces an older one not yet typed.
func TestNewerProgressReplacesAnUnsentNotice(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	w := r.as(r.pane(x))
	st := r.b.State[r.owner.ID]
	st.Input = "owner typing"
	for _, line := range []string{"step 1", "step 2", "step 3"} {
		if _, err := w.Progress(x.ID, line, true); err != nil {
			t.Fatal(err)
		}
	}
	var held []string
	for _, p := range r.data().Pending {
		held = append(held, p.Kind+":"+p.Text)
	}
	if len(held) != 1 || !strings.HasSuffix(held[0], ": step 3") || !strings.HasPrefix(held[0], "progress:") {
		t.Fatalf("held %q", held)
	}
	st.Input = ""
	r.tasks.FlushPending()
	if texts := r.b.Texts(r.owner.ID); len(texts) != 1 || !strings.Contains(texts[0], "step 3") {
		t.Fatalf("owner got %q", texts)
	}
}

// Progress is refused (exit 2) for an unknown task, a pane that neither runs nor owns it, and a task that is
// not running or is closed.
func TestProgressRefusals(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	_, err := r.tasks.Progress("zzzzzz", "x", false)
	wantCode(t, err, 2)
	_, err = r.as(r.other).Progress(x.ID, "not mine", false)
	wantCode(t, err, 2)
	_, err = r.tasks.Progress(x.ID, "  ", false)
	wantCode(t, err, 2)
	r.done(t, x, "ok", "done")
	_, err = r.as(r.pane(x)).Progress(x.ID, "after done", false)
	wantCode(t, err, 2)
	if _, err := r.tasks.Accept(x.ID, true); err != nil {
		t.Fatal(err)
	}
	_, err = r.as(r.pane(x)).Progress(x.ID, "after close", false)
	wantCode(t, err, 2)
	if g := r.get(t, x.ID); g.Progress != nil {
		t.Fatalf("a refused progress was kept: %+v", g.Progress)
	}
}

// --- G5: stall detection --------------------------------------------------

func stallNotes(r *rig) []string {
	var out []string
	for _, s := range r.b.Texts(r.owner.ID) {
		if strings.Contains(s, "looks stalled") {
			out = append(out, s)
		}
	}
	return out
}

func idleClaudeTask(t *testing.T, r *rig) *Task {
	t.Helper()
	x := r.mustNew(t, nil, NewOpts{Title: "port"})
	r.pane(x).Process = "claude"
	return x
}

// The stall tests run rounds stallTick apart (fake clock), so few rounds cover stallAfter; stallRounds
// covers it with room to spare.
const stallTick = 30.0

var stallRounds = int(stallAfter/stallTick) + 2

func watchFor(r *rig, rounds int) error {
	return r.tasks.Watch(WatchOpts{Interval: stallTick, Rounds: rounds})
}

// A calibrated worker idle for stallAfter with no done and no progress gets one stall notice per idle stretch:
// the mark keeps it to one, the worker at work again clears it, and a new stretch can bring another.
func TestWatcherTellsTheOwnerOfAStalledTaskOncePerIdleStretch(t *testing.T) {
	r := newRig(t)
	x := idleClaudeTask(t, r)
	if err := watchFor(r, stallRounds-8); err != nil {
		t.Fatal(err)
	}
	if n := stallNotes(r); len(n) != 0 {
		t.Fatalf("told before stallAfter: %q", n)
	}
	watchFor(r, 10)
	notes := stallNotes(r)
	want := "task " + x.ID + " (port) looks stalled: idle for 10 min without task done or progress. read it: harness-ops read " + x.ID
	if len(notes) != 1 || !strings.Contains(notes[0], want) {
		t.Fatalf("notes %q, want one with %q", notes, want)
	}
	if g := r.get(t, x.ID); g.Stall == nil || g.Status != "running" {
		t.Fatalf("no stall mark: %+v", g)
	}
	watchFor(r, stallRounds)
	if n := stallNotes(r); len(n) != 1 {
		t.Fatalf("told %d times in one idle stretch", len(n))
	}
	// At work again: the mark goes. Idle for another stretch: told again.
	r.b.Statuses = map[string]term.ProgramStatus{x.Pane: {State: "working"}}
	watchFor(r, 1)
	if g := r.get(t, x.ID); g.Stall != nil || g.IdleSince != 0 {
		t.Fatalf("the stall stayed while the worker works: %+v", g.Stall)
	}
	r.b.Statuses = nil
	watchFor(r, stallRounds)
	if n := stallNotes(r); len(n) != 2 {
		t.Fatalf("a new idle stretch told %d times in all, want 2", len(n))
	}
}

// Progress restarts the idle timer, and a worker with a dialog or a draft is not idle.
func TestProgressAndADraftKeepAStallAway(t *testing.T) {
	r := newRig(t)
	x := idleClaudeTask(t, r)
	watchFor(r, stallRounds-8)
	if _, err := r.as(r.pane(x)).Progress(x.ID, "still going", false); err != nil {
		t.Fatal(err)
	}
	watchFor(r, stallRounds-8)
	if n := stallNotes(r); len(n) != 0 {
		t.Fatalf("progress did not restart the idle timer: %q", n)
	}
	r.b.State[x.Pane].Input = "half a line"
	watchFor(r, 2*stallRounds)
	if n := stallNotes(r); len(n) != 0 || r.get(t, x.ID).IdleSince != 0 {
		t.Fatalf("a worker with a draft counted as idle: %q", n)
	}
}

// An uncalibrated harness's idle read is unreliable: its look never counts as idle.
func TestUncalibratedWorkerNeverStalls(t *testing.T) {
	r := newRig(t)
	x := idleClaudeTask(t, r)
	p := r.pane(x)
	prof := *ForProcess("claude")
	ws := []worker{{task: x, b: r.b, p: p, prof: &prof}}
	if sigs := r.tasks.read(ws, nil); len(sigs) != 1 || !sigs[0].Idle {
		t.Fatalf("a calibrated idle worker: %+v", sigs)
	}
	prof.Calibrated = false
	if sigs := r.tasks.read(ws, nil); len(sigs) != 1 || sigs[0].Idle {
		t.Fatalf("an uncalibrated worker read idle: %+v", sigs)
	}
}

// A stall notice held for a busy owner is dropped once the worker is at work again, and on done.
func TestHeldStallNoticeIsDroppedWhenTheWorkerMovesOn(t *testing.T) {
	r := newRig(t)
	x := idleClaudeTask(t, r)
	r.b.State[r.owner.ID].Input = "owner typing"
	watchFor(r, stallRounds)
	if d := r.data(); len(d.Pending) != 1 || d.Pending[0].Kind != "stall" {
		t.Fatalf("pending %+v", d.Pending)
	}
	r.b.Statuses = map[string]term.ProgramStatus{x.Pane: {State: "working"}}
	watchFor(r, 1)
	r.b.State[r.owner.ID].Input = ""
	r.tasks.FlushPending()
	if n := stallNotes(r); len(n) != 0 || len(r.data().Pending) != 0 {
		t.Fatalf("a stale stall notice went out: %q", n)
	}
}

// A lead idle while its sub-task works is waiting, not stalled; once the sub-task is stalled itself, the
// lead's stall notice goes and names it (2026-11-19: a lead flagged twice while its worker built).
func TestALeadWaitingOnAWorkingSubTaskIsNotStalled(t *testing.T) {
	r := newRig(t)
	lead := r.mustNew(t, nil, NewOpts{Title: "port", SubDepth: 1})
	r.pane(lead).Process = "claude"
	sub := r.mustNew(t, r.as(r.pane(lead)), NewOpts{Title: "build"})
	watchFor(r, stallRounds*2)
	if n := stallNotes(r); len(n) != 0 {
		t.Fatalf("a lead waiting on a working sub-task was called stalled: %q", n)
	}
	r.reg.Update(func(d *Data) error { d.Tasks[sub.ID].Stall = &StallMark{At: r.clock.T}; return nil })
	watchFor(r, stallRounds)
	n := stallNotes(r)
	if len(n) != 1 || !strings.Contains(n[0], sub.ID) {
		t.Fatalf("want one lead stall naming %s, got %q", sub.ID, n)
	}
}

// A task's pane is named by its role, ID and title, so a person can tell a worker from a lead.
func TestPaneTitleNamesTheRole(t *testing.T) {
	for _, c := range []struct {
		x    Task
		want string
	}{
		{Task{ID: "t1", Title: "port"}, "WORKER · t1 · port"},
		{Task{ID: "t2", Title: "f062", SubDepth: 1}, "LEAD · t2 · f062"},
		{Task{ID: "t3", SubDepth: 2}, "LEAD OF LEADS · t3"},
		{Task{ID: "t4", Title: "cleanup", Defer: true, SubDepth: 1}, "t4 · cleanup"},
		{Task{ID: "t5", ReviewOf: "t1", SubDepth: 1}, "REVIEWER · t5"},
	} {
		if got := paneTitle(&c.x); got != c.want {
			t.Errorf("%+v: %q, want %q", c.x, got, c.want)
		}
	}
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{Title: "port", SubDepth: 1})
	if got := r.pane(x).Title; got != "LEAD · "+x.ID+" · port" {
		t.Fatalf("the pane was spawned with title %q", got)
	}
}
