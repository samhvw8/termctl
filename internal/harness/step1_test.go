package harness

import (
	"os"
	"testing"
	"time"
)

// registryFile is the registry file as it is on disk now: a write replaces it (rename), so another file.
func registryFile(t *testing.T, r *rig) os.FileInfo {
	t.Helper()
	st, err := os.Stat(r.reg.Path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// A watch round whose signals change no mark, idle time or notice writes nothing; one that changes a mark writes.
func TestApplySkipsRegistryWriteOnNoOpRound(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	before := registryFile(t, r)
	clear := []Signal{signalOf(t, r, x.ID, "clear")}
	if err := r.tasks.apply(clear, true); err != nil { // no mark to drop, and not idle: nothing changes
		t.Fatal(err)
	}
	if err := r.tasks.apply(clear, false); err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, registryFile(t, r)) {
		t.Fatal("a round that changed nothing rewrote the registry")
	}
	dialog := signalOf(t, r, x.ID, "dialog")
	dialog.Dialog = &Dialog{Fingerprint: "abcd1234", Question: "Run?"}
	if err := r.tasks.apply([]Signal{dialog}, false); err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, registryFile(t, r)) || r.get(t, x.ID).Dialog == nil {
		t.Fatal("a new dialog was not recorded")
	}
	marked := registryFile(t, r)
	if err := r.tasks.apply([]Signal{dialog}, false); err != nil { // the same mark again
		t.Fatal(err)
	}
	if !os.SameFile(marked, registryFile(t, r)) {
		t.Fatal("the same dialog again rewrote the registry")
	}
}

// A wake ends the wait between two rounds at once; without one the watcher sleeps the whole interval.
func TestWakeCutsTheWaitBetweenRoundsShort(t *testing.T) {
	r := newRig(t)
	wake := make(chan struct{}, 1)
	w := &watchState{t: &Tasks{Reg: r.reg, Clock: RealClock{}}, say: func(string) {}, wake: wake}
	go func() { time.Sleep(50 * time.Millisecond); wake <- struct{}{} }()
	start := time.Now()
	if w.nap(10) {
		t.Fatal("the wake did not cut the nap short")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %v", took)
	}
	// A fake clock cannot be cut short: a wake sent before the wait ends it before it starts.
	f := &watchState{t: r.tasks, say: func(string) {}, wake: wake}
	wake <- struct{}{}
	if at := r.clock.T; f.nap(30) || r.clock.T != at {
		t.Fatal("a pending wake did not end the wait")
	}
	if at := r.clock.T; !f.nap(30) || r.clock.T != at+30 {
		t.Fatal("no wake: the whole wait")
	}
	// No wake channel at all (tests, older callers): a plain sleep.
	n := &watchState{t: r.tasks, say: func(string) {}}
	if at := r.clock.T; !n.nap(3) || r.clock.T != at+3 {
		t.Fatal("a plain sleep")
	}
}

// A journal entry names its scope, and a filter by scope keeps only its entries.
func TestJournalFilterByScope(t *testing.T) {
	r := newRig(t)
	ops := r.tasks.ops(r.b, r.owner)
	ops.Scope = "s123456"
	e := ops.entry("msg", r.other)
	if e.Scope != "s123456" {
		t.Fatalf("%+v", e)
	}
	j := r.tasks.journal()
	j.Record(e)
	j.Record(Entry{Event: "msg", Project: "/w"})
	got, _, err := j.Read(Filter{Scope: "s123456"})
	if err != nil || len(got) != 1 || got[0].Scope != "s123456" {
		t.Fatalf("%+v %v", got, err)
	}
	all, _, _ := j.Read(Filter{})
	if len(all) != 2 {
		t.Fatalf("%+v", all)
	}
}
