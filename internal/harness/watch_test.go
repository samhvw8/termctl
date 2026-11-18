package harness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samhvw8/termctl/internal/term"
	"github.com/samhvw8/termctl/internal/termtest"
)

// probe wraps the fake: it counts pane lists, measures how many screens are read at once, and can change
// the world on a given status poll.
type probe struct {
	*termtest.Fake
	mu             sync.Mutex
	lists          int
	inFlight, peak int
	delay          time.Duration
	polls          int
	onPoll         func(n int)
	onList         func(n int)
	clock          *termtest.Clock
	sentAt         map[string][]float64
}

func (p *probe) ListPanes() ([]*term.Pane, error) {
	p.mu.Lock()
	p.lists++
	n := p.lists
	p.mu.Unlock()
	if p.onList != nil {
		p.onList(n)
	}
	return p.Fake.ListPanes()
}

func (p *probe) Capture(pane *term.Pane, n int) ([]string, error) {
	p.mu.Lock()
	p.inFlight++
	p.peak = max(p.peak, p.inFlight)
	p.mu.Unlock()
	if pane.Label != "owner" {
		time.Sleep(p.delay) // the workers' screens are slow; the owner's (notice delivery) is not
	}
	defer func() { p.mu.Lock(); p.inFlight--; p.mu.Unlock() }()
	return p.Fake.Capture(pane, n)
}

func (p *probe) ProgramStatuses(panes []*term.Pane) (map[string]term.ProgramStatus, error) {
	p.polls++
	if p.onPoll != nil {
		p.onPoll(p.polls)
	}
	return p.Fake.ProgramStatuses(panes)
}

func (p *probe) SendText(pane *term.Pane, text string) error {
	if p.sentAt == nil {
		p.sentAt = map[string][]float64{}
	}
	if strings.Contains(text, "DIALOG") {
		p.sentAt[pane.ID] = append(p.sentAt[pane.ID], p.clock.T)
	}
	return p.Fake.SendText(pane, text)
}

func probeTasks(r *rig, p *probe) *Tasks {
	ts := r.as(r.owner)
	ts.Backend = p
	ts.Factory = func(string, map[string]string) (term.Backend, error) { return p, nil }
	return ts
}

// A worker's server that goes away for good does not keep the watcher alive: its task is lost after
// unreachableTTL and the watcher stops, warning once, not every round (round-3 review N1).
func TestWatcherStopsWhenTheServerStaysUnreachable(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	f := &downFake{Fake: r.b, down: true}
	ts := r.as(r.owner)
	ts.Backend = f
	ts.Factory = func(string, map[string]string) (term.Backend, error) { return f, nil }
	var warned []string
	old := Warn
	Warn = func(m string) { warned = append(warned, m) }
	defer func() { Warn = old }()
	rounds, limit := 0, int(2*unreachableTTL/WatchInterval)
	err := ts.Watch(WatchOpts{Restart: func() bool { rounds++; return rounds > limit }})
	if err != nil {
		t.Fatalf("the watcher ran %d rounds and did not stop: %v", rounds, err)
	}
	if g := mustGet(t, r, x.ID); g.Status != "lost" {
		t.Fatalf("task %s with its server gone for good", g.Status)
	}
	if len(warned) != 1 {
		t.Fatalf("warned %d times over %d rounds: %q", len(warned), rounds, warned)
	}
}

// The watcher log is cut to its last whole lines when a watcher starts, so it cannot grow without bound (review O10).
func TestWatcherLogIsCutAtStart(t *testing.T) {
	log := filepath.Join(t.TempDir(), "w.log")
	var b strings.Builder
	for i := 0; b.Len() <= logMax; i++ {
		fmt.Fprintf(&b, "12:00:00 line %d\n", i)
	}
	os.WriteFile(log, []byte(b.String()), 0o644)
	trimLog(log)
	data, _ := os.ReadFile(log)
	if len(data) > logKeep || len(data) < logKeep-100 {
		t.Fatalf("kept %d bytes, want just under %d", len(data), logKeep)
	}
	if !strings.HasSuffix(b.String(), string(data)) || !strings.HasPrefix(string(data), "12:00:00 line ") {
		t.Fatalf("not the last whole lines: %q", string(data[:40]))
	}
	small := "12:00:00 watching\n"
	os.WriteFile(log, []byte(small), 0o644)
	trimLog(log)
	if data, _ := os.ReadFile(log); string(data) != small {
		t.Fatal("a small log was changed")
	}
}

// The watcher log is cut by the watcher that holds the owner's lock, in place: a stopping watcher that
// takes the lock again, with the log open for append, keeps writing into the file on disk. The starter
// cut it by rename before any lock was held, so that watcher wrote on into the unlinked old file, out of
// sight (round-5 review D3). A watcher that does not get the lock leaves the log alone.
func TestWatcherCutsItsLogInPlaceWhileHoldingTheLock(t *testing.T) {
	r := newRig(t)
	r.mustNew(t, nil, NewOpts{})
	ts := r.as(r.owner)
	path := ts.WatchLog(r.owner.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; b.Len() <= 2<<20; i++ {
		fmt.Fprintf(&b, "12:00:00 line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	held, err := ts.lockWatcher(r.owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Watch(WatchOpts{Rounds: 1}); !errors.Is(err, ErrWatching) {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Size() != int64(b.Len()) {
		t.Fatalf("a watcher without the lock changed the log to %d bytes", st.Size())
	}
	held.Close()
	out, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0) // the stopping watcher's output
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := ts.Watch(WatchOpts{Rounds: 1}); err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(out, "12:00:01 still here\n")
	data, _ := os.ReadFile(path)
	if len(data) > 256<<10+100 || !strings.HasPrefix(string(data), "12:00:00 line ") ||
		!strings.HasSuffix(string(data), "12:00:01 still here\n") || !strings.Contains(b.String(), strings.TrimSuffix(string(data), "12:00:01 still here\n")) {
		t.Fatalf("not cut in place to its last whole lines: %d bytes, ends %q", len(data), data[max(0, len(data)-60):])
	}
}

func TestWatchReadsWorkersInParallelAndListsPanesOnce(t *testing.T) {
	t.Setenv("TERMCTL_MAX_TASKS", "20")
	r := newRig(t)
	var xs []*Task
	for i := 0; i < 5; i++ {
		x, _ := dialogTask(t, r)
		xs = append(xs, x)
	}
	p := &probe{Fake: r.b, delay: 150 * time.Millisecond, clock: r.clock}
	ts := probeTasks(r, p)
	start := time.Now()
	if err := ts.Watch(WatchOpts{Rounds: 1}); err != nil {
		t.Fatal(err)
	}
	if p.peak < 5 {
		t.Fatalf("at most %d screens were read at once; want all 5 together", p.peak)
	}
	if took := time.Since(start); took > 5*150*time.Millisecond {
		t.Fatalf("a round took %v: the reads ran one after another", took)
	}
	if p.lists != 1 {
		t.Fatalf("panes were listed %d times in one round (reconcile and the reads share one list)", p.lists)
	}
	if n := len(ownerNotes(r)); n != 5 {
		t.Fatalf("want 5 notices, got %d", n)
	}
	for i := 0; i < readWorkers+3; i++ {
		dialogTask(t, r)
	}
	p.peak = 0
	if err := ts.Watch(WatchOpts{Rounds: 1}); err != nil {
		t.Fatal(err)
	}
	if p.peak > readWorkers {
		t.Fatalf("%d screens were read at once; the bound is %d", p.peak, readWorkers)
	}
}

func TestDialogNoticeCarriesTheHarnessReason(t *testing.T) {
	r := newRig(t)
	x, _ := dialogTask(t, r)
	r.b.Statuses = map[string]term.ProgramStatus{x.Pane: {State: "blocked", Kind: "permission", Msg: "Claude needs your permission to use Bash"}}
	if err := r.tasks.Watch(WatchOpts{Rounds: 1}); err != nil {
		t.Fatal(err)
	}
	notes := ownerNotes(r)
	if len(notes) != 1 || !strings.Contains(notes[0], "| the harness reports: Claude needs your permission to use Bash") {
		t.Fatalf("%q", notes)
	}
	// A status that is not blocked adds nothing.
	r2 := newRig(t)
	x2, _ := dialogTask(t, r2)
	r2.b.Statuses = map[string]term.ProgramStatus{x2.Pane: {State: "working", Msg: "thinking"}}
	r2.tasks.Watch(WatchOpts{Rounds: 1})
	if notes := ownerNotes(r2); len(notes) != 1 || strings.Contains(notes[0], "reports") {
		t.Fatalf("%q", notes)
	}
}

// A status source that costs a process per read (Rex: rex do, hc: hc ls) is read once per round, never
// between rounds, so a watcher does not start a process every second.
func TestCostlyStatusIsReadOncePerRound(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.pane(x).Process = "claude"
	r.b.CostlyStatus = true
	p := &probe{Fake: r.b, clock: r.clock}
	ts := probeTasks(r, p)
	if err := ts.Watch(WatchOpts{Rounds: 2, Interval: 3, StatusEvery: 1}); err != nil {
		t.Fatal(err)
	}
	if p.polls != 2 {
		t.Fatalf("status polls with a costly source: %d, want 2 (one per round)", p.polls)
	}
}

// A worker that turns blocked between screen rounds has its screen read at that status poll, not at the
// next round. Without status polls the same dialog waits for the round.
func TestBlockedStatusReadsTheScreenAtOnce(t *testing.T) {
	for _, every := range []float64{1, -1} {
		r := newRig(t)
		x := r.mustNew(t, nil, NewOpts{})
		r.pane(x).Process = "claude"
		p := &probe{Fake: r.b, clock: r.clock}
		p.onPoll = func(n int) {
			if n == 2 { // the first poll between rounds
				r.b.State[x.Pane].Dialog = &termtest.Dialog{Body: "touch x", Options: []string{"Yes", "No"}, Selected: 1}
				r.b.Statuses = map[string]term.ProgramStatus{x.Pane: {State: "blocked", Msg: "Run touch x?"}}
			}
		}
		ts := probeTasks(r, p)
		start := r.clock.T
		if err := ts.Watch(WatchOpts{Rounds: 2, Interval: 3, StatusEvery: every}); err != nil {
			t.Fatal(err)
		}
		if every < 0 {
			if p.polls != 2 { // one per round, none between
				t.Fatalf("status polls: %d", p.polls)
			}
			r.b.State[x.Pane].Dialog = &termtest.Dialog{Body: "touch x", Options: []string{"Yes", "No"}, Selected: 1}
			ts.Watch(WatchOpts{Rounds: 1})
			if got := p.sentAt[r.owner.ID]; len(got) != 1 || got[0]-start < 3 {
				t.Fatalf("without status polls the notice waits for a round: %v (start %v)", got, start)
			}
			continue
		}
		got := p.sentAt[r.owner.ID]
		if len(got) != 1 || got[0]-start >= 2 {
			t.Fatalf("want one notice within the first status poll; sent at %v (start %v)", got, start)
		}
		if notes := ownerNotes(r); len(notes) != 1 || !strings.Contains(notes[0], "the harness reports: Run touch x?") {
			t.Fatalf("%q", notes)
		}
	}
}

// Rounds start every Interval, whatever a round took: delivering notices does not stretch the cadence.
func TestRoundsStartEveryInterval(t *testing.T) {
	r := newRig(t)
	x, _ := dialogTask(t, r) // round 1 delivers a notice, which takes clock time
	var starts []float64
	r.tasks.Watch(WatchOpts{Rounds: 3, Interval: 3, StatusEvery: -1, Restart: func() bool {
		starts = append(starts, r.clock.T)
		return false
	}})
	if len(ownerNotes(r)) != 1 || len(starts) != 3 {
		t.Fatal(ownerNotes(r), starts, x.ID)
	}
	if took := starts[1] - starts[0]; took < 3 || took > 3.01 {
		t.Fatalf("round 2 started %.2f s after round 1, want 3", took)
	}
}

func TestWatchStopsForARestartAndFreesItsLock(t *testing.T) {
	r := newRig(t)
	dialogTask(t, r)
	asks := 0
	err := r.tasks.Watch(WatchOpts{Restart: func() bool { asks++; return asks == 3 }})
	if !errors.Is(err, ErrRestart) || asks != 3 {
		t.Fatal(err, asks)
	}
	if r.tasks.Watching(r.owner.ID) != 0 {
		t.Fatal("the lock outlived the watcher, so the restarted one could not take it")
	}
}

// A watcher stops when its owner pane is gone, and when nothing of the owner runs: a ready task that is
// never accepted must not keep it polling (review F12).
func TestWatcherStopsWhenOwnerGoneOrNothingRuns(t *testing.T) {
	for _, ownerGone := range []bool{true, false} {
		r := newRig(t)
		x := r.mustNew(t, nil, NewOpts{})
		r.pane(x).Process = "claude"
		y := r.mustNew(t, nil, NewOpts{})
		if _, err := r.as(r.pane(x)).Done(x.ID, "ok", "done", ""); err != nil {
			t.Fatal(err)
		}
		if ownerGone {
			kept := r.b.Panes[:0]
			for _, p := range r.b.Panes {
				if p.ID != r.owner.ID {
					kept = append(kept, p)
				}
			}
			r.b.Panes = kept
		} else {
			r.tasks.Close(y.ID, true)
		}
		rounds := 0
		err := r.tasks.Watch(WatchOpts{Restart: func() bool { rounds++; return rounds > 200 }})
		if err != nil || rounds != 1 {
			t.Errorf("owner gone=%v: still polling after %d rounds (err %v)", ownerGone, rounds, err)
		}
	}
}

// A notice still waiting for the owner keeps the watcher on until it is delivered.
func TestWatcherWaitsForAHeldNotice(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.b.State[r.owner.ID].Input = "owner typing"
	if _, err := r.as(r.pane(x)).Done(x.ID, "ok", "done", ""); err != nil {
		t.Fatal(err)
	}
	rounds := 0
	r.tasks.Watch(WatchOpts{Restart: func() bool {
		rounds++
		if rounds == 3 {
			r.b.State[r.owner.ID].Input = ""
		}
		return rounds > 50
	}})
	if rounds != 4 || !termtest.Contains(r.b.Texts(r.owner.ID), "RE: task "+x.ID) {
		t.Fatalf("rounds=%d texts=%q", rounds, r.b.Texts(r.owner.ID))
	}
}

// The watcher lock is per owner pane and server: the same pane ID on another tmux server is another
// owner, with a watcher of its own (review F15).
func TestWatcherLockIsPerServer(t *testing.T) {
	r := newRig(t)
	a, b := r.as(r.owner), r.as(r.owner)
	a.SenderRef, b.SenderRef = map[string]string{"pid": "111"}, map[string]string{"pid": "222"}
	la, err := a.lockWatcher(r.owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer la.Close()
	lb, err := b.lockWatcher(r.owner.ID)
	if err != nil {
		t.Fatal("another server's owner was refused its watcher:", err)
	}
	lb.Close()
	if a.Watching(r.owner.ID) == 0 || b.Watching(r.owner.ID) != 0 {
		t.Fatal("Watching does not tell the servers apart")
	}
}

// A task created after the watcher read the registry, but before it let go of its lock, sees a watcher
// running and starts none (review O6). The stopping watcher looks again once the lock is free and goes on.
func TestStoppingWatcherServesATaskCreatedMeanwhile(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.done(t, x, "ok", "done")
	if _, err := r.tasks.Accept(x.ID, false); err != nil {
		t.Fatal(err)
	}
	p := &probe{Fake: r.b, clock: r.clock}
	ts := probeTasks(r, p)
	var heldInRound2 bool
	p.onList = func(n int) {
		switch n {
		case 1: // the owner check, after the read that found nothing running
			r.reg.Update(func(d *Data) error {
				y := *d.Tasks[x.ID]
				y.ID, y.Status = "tnew", "running"
				d.Tasks[y.ID] = &y
				return nil
			})
			if r.as(r.owner).Watching(r.owner.ID) == 0 {
				t.Error("the lock should still be held here")
			}
		case 2:
			heldInRound2 = r.as(r.owner).Watching(r.owner.ID) != 0
		}
	}
	var logs []string
	if err := ts.Watch(WatchOpts{Rounds: 2, Log: func(s string) { logs = append(logs, s) }}); err != nil {
		t.Fatal(err)
	}
	for _, l := range logs {
		if strings.HasSuffix(l, "; stopping") {
			t.Fatalf("stopped with a running task left unwatched: %q", logs)
		}
	}
	if p.lists < 2 || !heldInRound2 {
		t.Fatalf("no second round under the lock: lists=%d held=%v logs=%q", p.lists, heldInRound2, logs)
	}
}

// A worker that turns blocked between rounds is read and announced at once, which takes clock time; the
// next round still starts Interval after the last, not that time later (agy review A12.1).
func TestRoundsKeepTheirCadenceWhenAStatusPollAnnounces(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.pane(x).Process = "claude"
	p := &probe{Fake: r.b, clock: r.clock}
	p.onPoll = func(n int) {
		if n == 2 { // the first poll between rounds
			r.b.State[x.Pane].Dialog = &termtest.Dialog{Body: "touch x", Options: []string{"Yes", "No"}, Selected: 1}
			r.b.Statuses = map[string]term.ProgramStatus{x.Pane: {State: "blocked", Msg: "Run touch x?"}}
		}
	}
	var starts []float64
	if err := probeTasks(r, p).Watch(WatchOpts{Rounds: 2, Interval: 3, StatusEvery: 1, Restart: func() bool {
		starts = append(starts, r.clock.T)
		return false
	}}); err != nil {
		t.Fatal(err)
	}
	if len(ownerNotes(r)) != 1 || len(starts) != 2 {
		t.Fatal(ownerNotes(r), starts)
	}
	if took := starts[1] - starts[0]; took < 3 || took > 3.01 {
		t.Fatalf("round 2 started %.2f s after round 1, want 3", took)
	}
}

// A lead whose harness has no calibrated input reader (codex, gemini, opencode, generic) is never typed a
// dialog notice: typed blind, its letters could land in the lead's own menus. The notice is held, the
// dialog stays marked for task ls and is returned by task wait, and task new and task send warn once.
// Task results are still typed, as before (round-6 review F13/R4).
func TestDialogNoticesToAnUncalibratedLeadAreHeld(t *testing.T) {
	var warned []string
	old := Warn
	Warn = func(m string) { warned = append(warned, m) }
	defer func() { Warn = old }()
	r := newRig(t)
	r.owner.Process = "python3"
	x, _ := dialogTask(t, r)
	if len(warned) != 1 || !strings.Contains(warned[0], "dialog notices") || !strings.Contains(warned[0], "task wait") {
		t.Fatalf("task new must warn once: %q", warned)
	}
	if err := r.tasks.Watch(WatchOpts{Rounds: 2}); err != nil {
		t.Fatal(err)
	}
	if notes := ownerNotes(r); len(notes) != 0 {
		t.Fatalf("a dialog notice was typed to an uncalibrated lead: %q", notes)
	}
	d := r.data()
	if len(d.Pending) != 1 || d.Pending[0].Kind != "dialog" || d.Pending[0].Claimed != 0 || d.Tasks[x.ID].Dialog == nil {
		t.Fatalf("the notice must stay held and the dialog marked: %+v %+v", d.Pending, d.Tasks[x.ID].Dialog)
	}
	w, err := r.tasks.Wait(x.ID, false, 5)
	if err != nil || w == nil || w.Dialog == nil {
		t.Fatalf("task wait must return the held dialog: %+v %v", w, err)
	}
	r.b.State[x.Pane].Dialog = nil
	warned = nil
	if err := r.tasks.Send(x.ID, "more", false); err != nil {
		t.Fatal(err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "dialog notices") {
		t.Fatalf("task send must warn once: %q", warned)
	}
	if _, err := r.as(r.pane(x)).Done(x.ID, "ok", "finished", ""); err != nil {
		t.Fatal(err)
	}
	if !termtest.Contains(r.b.Texts(r.owner.ID), "RE: task "+x.ID) {
		t.Fatalf("a task result still reaches an uncalibrated lead: %q", r.b.Texts(r.owner.ID))
	}
	// A calibrated lead is told, and not warned.
	warned = nil
	r2 := newRig(t)
	dialogTask(t, r2)
	r2.tasks.Watch(WatchOpts{Rounds: 1})
	if len(warned) != 0 || len(ownerNotes(r2)) != 1 {
		t.Fatalf("calibrated lead: warned %q, notes %q", warned, ownerNotes(r2))
	}
}

// spawnedDialog starts a pane as spawn does (pairs and live teams start members so), running claude, and
// shows a permission dialog on it.
func spawnedDialog(t *testing.T, r *rig, ts *Tasks, label, body string) (*term.Pane, *termtest.Dialog) {
	t.Helper()
	p, err := ts.Spawn("claude", "tab", "/w/"+label, SpawnOpts{Command: "claude", Label: label, Timeout: 60})
	if err != nil {
		t.Fatal(err)
	}
	p.Process = "claude"
	dlg := &termtest.Dialog{Body: body, Options: []string{"Yes", "Yes, and don't ask again", "No"}, Selected: 1}
	r.b.State[p.ID].Dialog = dlg
	return p, dlg
}

// The watcher reads every pane its owner started, not only running tasks' workers: a dialog on a spawned
// pane gets a notice in the task notice's format, naming the pane (label and ID) and suggesting answer
// and deny by its ID, once while it stays on screen. The mark lives on the pane's record; answering drops
// it, and the same request asked again is announced again.
func TestWatcherAnnouncesADialogOnASpawnedPane(t *testing.T) {
	r := newRig(t)
	r.mustNew(t, nil, NewOpts{}) // a running task keeps this watcher on
	p, dlg := spawnedDialog(t, r, r.tasks, "partner", "touch x")
	fp := readFP(t, r, &Task{Pane: p.ID})
	if err := r.tasks.Watch(WatchOpts{Rounds: 3}); err != nil {
		t.Fatal(err)
	}
	notes := ownerNotes(r)
	if len(notes) != 1 {
		t.Fatalf("want one notice, got %q", notes)
	}
	for _, want := range []string{"[agent-ops from partner (claude) id=" + p.ID + " folder=/w/partner]", "pane " + p.ID + " (partner) DIALOG " + fp + ": " + screenOpen,
		"touch x", screenClose + " | answer: harness-ops answer " + p.ID + " <n> --expect " + fp, "deny: harness-ops deny " + p.ID + " --expect " + fp} {
		if !strings.Contains(notes[0], want) {
			t.Fatalf("notice lacks %q: %s", want, notes[0])
		}
	}
	rec := r.data().Panes[p.ID]
	if rec.Dialog == nil || rec.Dialog.Fingerprint != fp || !rec.Dialog.Notified {
		t.Fatalf("the pane's record must carry the mark: %+v", rec.Dialog)
	}
	if _, _, err := r.tasks.Answer(p.ID, AnswerOpts{N: 1, Expect: fp}); err != nil {
		t.Fatal(err)
	}
	if rec := r.data().Panes[p.ID]; rec.Dialog != nil || rec.AnsweredAt == 0 {
		t.Fatalf("an answer drops the pane's mark and stamps the answer: %+v", rec)
	}
	r.tasks.Watch(WatchOpts{Rounds: 1}) // the input line is back
	dlg.Answer, dlg.Selected = 0, 1     // the same request again
	r.tasks.Watch(WatchOpts{Rounds: 2})
	if n := len(ownerNotes(r)); n != 2 {
		t.Fatalf("the request asked again must be announced again: %d notices", n)
	}
}

// Only the owner's own panes are watched: a pane another lead started, and a pane nobody started, get no
// notice and no mark, however long they show a dialog.
func TestWatcherIgnoresPanesItsOwnerDidNotStart(t *testing.T) {
	r := newRig(t)
	r.mustNew(t, nil, NewOpts{})
	theirs, _ := spawnedDialog(t, r, r.as(r.other), "theirs", "touch theirs")
	r.other.Process = "claude"
	r.b.State[r.other.ID].Dialog = &termtest.Dialog{Body: "touch nobody", Options: []string{"Yes", "No"}, Selected: 1}
	if err := r.tasks.Watch(WatchOpts{Rounds: 2}); err != nil {
		t.Fatal(err)
	}
	if notes := ownerNotes(r); len(notes) != 0 {
		t.Fatalf("told of a pane it did not start: %q", notes)
	}
	if rec := r.data().Panes[theirs.ID]; rec.Dialog != nil || len(r.data().Pending) != 0 {
		t.Fatalf("marked another owner's pane: %+v %+v", rec.Dialog, r.data().Pending)
	}
}

// Two pane notices made in one round share an empty task and a creation time: each is still claimed,
// delivered and removed on its own, and neither is lost or sent twice.
func TestTwoPaneNoticesOfOneRoundAreBothDelivered(t *testing.T) {
	r := newRig(t)
	r.mustNew(t, nil, NewOpts{})
	a, _ := spawnedDialog(t, r, r.tasks, "alice", "touch a")
	b, _ := spawnedDialog(t, r, r.tasks, "bob", "touch b")
	r.b.State[r.owner.ID].Input = "owner typing"
	r.tasks.Watch(WatchOpts{Rounds: 1})
	if d := r.data(); len(d.Pending) != 2 || d.Pending[0].Created != d.Pending[1].Created || d.Pending[0].Task != "" {
		t.Fatalf("want two held pane notices of one round: %+v", d.Pending)
	}
	r.b.State[r.owner.ID].Input = ""
	if n, err := r.tasks.FlushPending(); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	notes := strings.Join(ownerNotes(r), "\n")
	if len(ownerNotes(r)) != 2 || !strings.Contains(notes, "pane "+a.ID) || !strings.Contains(notes, "pane "+b.ID) || len(r.data().Pending) != 0 {
		t.Fatalf("notes %q, pending %+v", ownerNotes(r), r.data().Pending)
	}
}

// A held pane notice whose dialog was answered before the owner was free is dropped, not delivered late;
// a pane closed outside harness-ops loses its mark.
func TestStalePaneNoticeIsDroppedAndAGonePaneLosesItsMark(t *testing.T) {
	r := newRig(t)
	r.mustNew(t, nil, NewOpts{})
	p, dlg := spawnedDialog(t, r, r.tasks, "partner", "touch x")
	r.b.State[r.owner.ID].Input = "owner typing"
	r.tasks.Watch(WatchOpts{Rounds: 1})
	dlg.Answer = 1 // answered by hand in the pane
	r.tasks.Watch(WatchOpts{Rounds: 1})
	r.b.State[r.owner.ID].Input = ""
	r.tasks.FlushPending()
	if len(ownerNotes(r)) != 0 || len(r.data().Pending) != 0 {
		t.Fatalf("a stale pane notice was delivered: %q %+v", ownerNotes(r), r.data().Pending)
	}
	dlg.Answer, dlg.Selected = 0, 1
	r.tasks.Watch(WatchOpts{Rounds: 1})
	if r.data().Panes[p.ID].Dialog == nil {
		t.Fatal("no mark")
	}
	r.b.Panes = r.b.Panes[:len(r.b.Panes)-1] // closed by hand: the record stays, the pane is gone
	r.tasks.Watch(WatchOpts{Rounds: 1})
	if rec := r.data().Panes[p.ID]; rec == nil || rec.Dialog != nil {
		t.Fatalf("a gone pane keeps its mark: %+v", rec)
	}
}

// While a running task holds a pane, its dialog is the task's (task wait returns it, task ls shows it); a
// mark the pane's record kept from before goes.
func TestARunningTaskTakesThePaneMark(t *testing.T) {
	r := newRig(t)
	x, dlg := dialogTask(t, r)
	fp := readFP(t, r, x)
	r.reg.Update(func(d *Data) error {
		d.Panes[x.Pane].Dialog = &DialogMark{Fingerprint: "0ld0ld00", Notified: true}
		return nil
	})
	r.tasks.Watch(WatchOpts{Rounds: 1})
	d := r.data()
	if d.Panes[x.Pane].Dialog != nil || d.Tasks[x.ID].Dialog == nil || d.Tasks[x.ID].Dialog.Fingerprint != fp {
		t.Fatalf("pane mark %+v, task mark %+v", d.Panes[x.Pane].Dialog, d.Tasks[x.ID].Dialog)
	}
	if notes := ownerNotes(r); len(notes) != 1 || !strings.Contains(notes[0], "task "+x.ID) {
		t.Fatalf("%q", notes)
	}
	_ = dlg
}
