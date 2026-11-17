package harness

import (
	"errors"
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
	clock          *termtest.Clock
	sentAt         map[string][]float64
}

func (p *probe) ListPanes() ([]*term.Pane, error) {
	p.mu.Lock()
	p.lists++
	p.mu.Unlock()
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
