package harness

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/term"
	"github.com/samhvw8/termctl/internal/termtest"
)

// --- 1: deferred jobs -------------------------------------------------------

func deferredTask(t *testing.T, r *rig) *Task {
	t.Helper()
	x := r.mustNew(t, nil, NewOpts{Title: "build", Defer: true})
	r.pane(x).Process = "claude"
	return x
}

func journalEvents(t *testing.T, r *rig, f Filter) []string {
	t.Helper()
	entries, _, err := r.tasks.journal().Read(f)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Event+":"+e.Status+":"+e.Text)
	}
	return out
}

// A deferred task's brief tells the worker to ack once, do the job, finish with task done, and not message
// the owner; the task records Defer.
func TestDeferredTaskBriefAsksForOneAck(t *testing.T) {
	r := newRig(t)
	x := deferredTask(t, r)
	line := r.b.Texts(x.Pane)[0]
	for _, want := range []string{"harness-ops ack " + x.ID + " \"<one line>\" once now", "harness-ops task done " + x.ID + " --status ok|failed|blocked",
		"do not message the owner"} {
		if !strings.Contains(line, want) {
			t.Fatalf("brief line lacks %q: %s", want, line)
		}
	}
	if g := r.get(t, x.ID); !g.Defer || g.AckedAt != 0 {
		t.Fatalf("%+v", g)
	}
}

// Only the task's own pane acks it, once; nothing is typed to the owner; the ack is journaled.
func TestAckOnceFromTheWorkersPaneOnly(t *testing.T) {
	r := newRig(t)
	x := deferredTask(t, r)
	_, err := r.tasks.Ack(x.ID, "on it") // the owner
	wantCode(t, err, 2)
	_, err = r.as(r.other).Ack(x.ID, "on it")
	wantCode(t, err, 2)
	if what, err := r.as(r.pane(x)).Ack(x.ID, "on it\nnow"); err != nil || what != "task "+x.ID {
		t.Fatal(what, err)
	}
	if g := r.get(t, x.ID); g.AckedAt != r.clock.T || g.AckNote != "on it now" {
		t.Fatalf("%+v", g)
	}
	_, err = r.as(r.pane(x)).Ack(x.ID, "again")
	wantCode(t, err, 2)
	if texts := r.b.Texts(r.owner.ID); len(texts) != 0 {
		t.Fatalf("the owner was typed %q", texts)
	}
	if ev := journalEvents(t, r, Filter{Task: x.ID}); !termtest.Contains(ev, "ack:running:on it now") {
		t.Fatalf("no ack entry: %q", ev)
	}
	plain := r.mustNew(t, nil, NewOpts{})
	_, err = r.as(r.pane(plain)).Ack(plain.ID, "x") // not deferred
	wantCode(t, err, 2)
}

// A deferred message is acked once, only from the pane it went to; an unknown ID is refused.
func TestAckOfADeferredMessage(t *testing.T) {
	r := newRig(t)
	id := NewMsgID()
	ops := r.tasks.ops(r.b, r.owner)
	if _, err := ops.Msg(r.other, "run the nightly build", MsgOpts{ID: id, Suffix: DeferSuffix(id)}); err != nil {
		t.Fatal(err)
	}
	if typed := r.b.Texts(r.other.ID); len(typed) != 1 || !strings.HasSuffix(typed[0], "run the nightly build [deferred "+id+": run harness-ops ack "+id+" once, then do not reply]") {
		t.Fatalf("typed %q", typed)
	}
	_, err := r.tasks.Ack(id, "x") // the sender
	wantCode(t, err, 2)
	_, err = r.as(r.other).Ack("m000000", "x")
	wantCode(t, err, 2)
	if acked, _ := r.tasks.Acked(id); acked {
		t.Fatal("acked before the ack")
	}
	if what, err := r.as(r.other).Ack(id, "will do"); err != nil || what != "message "+id {
		t.Fatal(what, err)
	}
	_, err = r.as(r.other).Ack(id, "again")
	wantCode(t, err, 2)
	if acked, _ := r.tasks.Acked(id); !acked {
		t.Fatal("not acked")
	}
	if texts := r.b.Texts(r.owner.ID); len(texts) != 0 {
		t.Fatalf("the sender was typed %q", texts)
	}
}

// --wait-ack: WaitAck returns once the ack is in, and exits 5 when none comes in time.
func TestWaitAckWaitsForTheAck(t *testing.T) {
	r := newRig(t)
	x := deferredTask(t, r)
	start := r.clock.T
	err := r.tasks.WaitAck(x.ID, 5)
	wantCode(t, err, 5)
	if r.clock.T-start < 5 {
		t.Fatalf("gave up after %gs", r.clock.T-start)
	}
	r.tasks.sleepHook = func(n int) {
		if n == 2 {
			if _, err := r.as(r.pane(x)).Ack(x.ID, "ok"); err != nil {
				t.Error(err)
			}
		}
	}
	r.tasks.sleeps = 0
	if err := r.tasks.WaitAck(x.ID, 30); err != nil {
		t.Fatal(err)
	}
}

// task done on a deferred task records and journals the result and tells nobody, for ok, failed and blocked.
func TestDeferredDoneTellsNobody(t *testing.T) {
	for reported, stored := range map[string]string{"ok": "ready", "failed": "failed", "blocked": "blocked"} {
		r := newRig(t)
		x := deferredTask(t, r)
		d := r.done(t, x, reported, "built")
		if d.Status != stored || d.Notify != "none" || d.Summary != "built" {
			t.Fatalf("%s: %+v", reported, d)
		}
		if len(r.data().Pending) != 0 || len(r.b.Texts(r.owner.ID)) != 0 {
			t.Fatalf("%s: a notice went out: %+v %q", reported, r.data().Pending, r.b.Texts(r.owner.ID))
		}
		if ev := journalEvents(t, r, Filter{Task: x.ID}); !termtest.Contains(ev, "task.done:"+stored+":built") {
			t.Fatalf("%s: %q", reported, ev)
		}
	}
}

// The watcher closes the pane of a deferred task that reported ok, on its next round; a failed one, and an
// ok one on a --reuse pane, stay open.
func TestWatcherClosesAnOkDeferredTask(t *testing.T) {
	r := newRig(t)
	ok := deferredTask(t, r)
	failed := deferredTask(t, r)
	p, err := r.tasks.Spawn("generic", "tab", "/w", SpawnOpts{Command: "x", Timeout: 60})
	if err != nil {
		t.Fatal(err)
	}
	reused := r.mustNew(t, nil, NewOpts{Reuse: p.ID, Defer: true})
	r.done(t, ok, "ok", "built")
	r.done(t, failed, "failed", "broke")
	r.done(t, reused, "ok", "built")
	if err := r.tasks.Watch(WatchOpts{Rounds: 1}); err != nil {
		t.Fatal(err)
	}
	if !r.b.Has("close", ok.Pane) || !r.get(t, ok.ID).Closed {
		t.Fatal("the ok deferred task's pane was not closed")
	}
	if r.b.Has("close", failed.Pane) || r.get(t, failed.ID).Closed {
		t.Fatal("a failed deferred task was closed")
	}
	if r.b.Has("close", p.ID) || r.get(t, reused.ID).Closed {
		t.Fatal("a --reuse pane was closed")
	}
	if ev := journalEvents(t, r, Filter{Task: ok.ID}); !termtest.Contains(ev, "task.close:ready:deferred job reported ok") {
		t.Fatalf("%q", ev)
	}
	// Its pane is gone on purpose: a later reconcile must not call the task lost (seen on a real run).
	if err := r.tasks.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if s := r.get(t, ok.ID).Status; s != "ready" {
		t.Fatalf("a closed ok deferred task became %s", s)
	}
}

// The watcher denies (Esc) each dialog on a deferred worker and never tells the owner; after two denials it
// interrupts the worker and marks the task blocked.
func TestWatcherDeniesADeferredWorkersDialogs(t *testing.T) {
	r := newRig(t)
	x := deferredTask(t, r)
	first := &termtest.Dialog{Body: "touch x", Options: []string{"Yes", "No"}, Selected: 1}
	r.b.State[x.Pane].Dialog = first
	if err := r.tasks.Watch(WatchOpts{Rounds: 1}); err != nil {
		t.Fatal(err)
	}
	if first.Answer != -1 {
		t.Fatalf("the dialog was not denied with Esc: answer %d", first.Answer)
	}
	if g := r.get(t, x.ID); g.Status != "running" || g.Denied != 1 {
		t.Fatalf("%+v", g)
	}
	second := &termtest.Dialog{Body: "rm -rf build", Options: []string{"Yes", "No"}, Selected: 1}
	r.b.State[x.Pane].Dialog = second
	r.clock.T += 3
	if err := r.tasks.Watch(WatchOpts{Rounds: 1}); err != nil {
		t.Fatal(err)
	}
	g := r.get(t, x.ID)
	if second.Answer != -1 || g.Status != "blocked" || g.Summary != "denied 2 dialogs" || g.Notify != "none" {
		t.Fatalf("answer %d, %+v", second.Answer, g)
	}
	if n := ownerNotes(r); len(n) != 0 || len(r.b.Texts(r.owner.ID)) != 0 {
		t.Fatalf("the owner was told: %q", r.b.Texts(r.owner.ID))
	}
	// No notice is even queued for a deferred worker's dialog, should the deny fail or come late.
	y := deferredTask(t, r)
	if err := r.tasks.apply([]Signal{{Task: y.ID, Kind: "dialog", Dialog: &Dialog{Fingerprint: "ffff0000", Question: "Run?"}}}, true); err != nil {
		t.Fatal(err)
	}
	for _, p := range r.data().Pending {
		if p.Task == y.ID {
			t.Fatalf("a notice was queued for a deferred worker's dialog: %+v", p)
		}
	}
	ev := journalEvents(t, r, Filter{Task: x.ID})
	denies := 0
	for _, e := range ev {
		if strings.HasPrefix(e, "deny:") {
			denies++
		}
	}
	if denies != 2 || !termtest.Contains(ev, "interrupt::escape") || !termtest.Contains(ev, "task.blocked:blocked:denied 2 dialogs") {
		t.Fatalf("%q", ev)
	}
	if keys := r.b.Typed(x.Pane); !termtest.Contains(keys, "key:escape") || termtest.Contains(keys, "key:enter") && strings.Count(strings.Join(keys, " "), "key:enter") > 1 {
		t.Fatalf("keys %q", keys)
	}
}

func dropPane(b *termtest.Fake, id string) {
	kept := b.Panes[:0]
	for _, p := range b.Panes {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	b.Panes = kept
}

// After its owner pane is gone, the watcher stays only while a deferred task of that owner runs or an ok
// one is unclosed: it closes that pane, then stops. Without a deferred task it stops at once.
func TestWatcherOutlivesItsOwnerOnlyForDeferredTasks(t *testing.T) {
	r := newRig(t)
	r.mustNew(t, nil, NewOpts{}) // a task that is not deferred keeps nothing on
	dropPane(r.b, r.owner.ID)
	var logs []string
	if err := r.tasks.Watch(WatchOpts{Rounds: 5, Log: func(s string) { logs = append(logs, s) }}); err != nil {
		t.Fatal(err)
	}
	if !termtest.Contains(logs, "the owner pane is gone; stopping") {
		t.Fatalf("%q", logs)
	}

	r = newRig(t)
	x := deferredTask(t, r)
	dropPane(r.b, r.owner.ID)
	logs = nil
	rounds := 0
	err := r.tasks.Watch(WatchOpts{Log: func(s string) { logs = append(logs, s) }, Restart: func() bool {
		rounds++
		if rounds == 3 {
			r.done(t, x, "ok", "built") // the worker reports while the owner is gone
		}
		return rounds > 20
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !termtest.Contains(logs, "the owner pane is gone; staying for its deferred tasks") || !termtest.Contains(logs, "the owner pane is gone; stopping") {
		t.Fatalf("%q", logs)
	}
	if rounds != 3 || !r.b.Has("close", x.Pane) { // the round that saw the report closed the pane, then stopped
		t.Fatalf("rounds %d, closed %v, log %q", rounds, r.b.Has("close", x.Pane), logs)
	}
}

// --- 2: lead of lead ----------------------------------------------------------

// The capability card in the footer: a leaf, or a lead with what it may give, its tree limit and how many run.
// A child may only narrow --sub-depth and --tree-max; a leaf's own task new is refused.
func TestSubDepthTreeMaxAndTheRoleCard(t *testing.T) {
	r := newRig(t)
	lead := r.mustNew(t, nil, NewOpts{SubDepth: 2, TreeMax: 5})
	if line := r.b.Texts(lead.Pane)[0]; !strings.Contains(line, "Role: lead (you may start tasks with --sub-depth up to 1; tree limit 5; 1 running).") {
		t.Fatal(line)
	}
	if g := r.get(t, lead.ID); g.Root != lead.ID || g.SubDepth != 2 || g.TreeMax != 5 || g.Depth != 1 {
		t.Fatalf("%+v", g)
	}
	from := r.as(r.pane(lead))
	_, err := r.new(t, from, NewOpts{SubDepth: 2})
	wantCode(t, err, 6)
	_, err = r.new(t, from, NewOpts{TreeMax: 6})
	wantCode(t, err, 6)
	mid := r.mustNew(t, from, NewOpts{SubDepth: 1, TreeMax: 4})
	if g := r.get(t, mid.ID); g.Root != lead.ID || g.Parent != lead.ID || g.TreeMax != 4 || g.SubDepth != 1 || g.Depth != 2 {
		t.Fatalf("%+v", g)
	}
	leaf := r.mustNew(t, r.as(r.pane(mid)), NewOpts{})
	if line := r.b.Texts(leaf.Pane)[0]; !strings.Contains(line, "Role: leaf (do not start tasks).") {
		t.Fatal(line)
	}
	if g := r.get(t, leaf.ID); g.Root != lead.ID || g.TreeMax != 4 || g.SubDepth != 0 {
		t.Fatalf("%+v", g)
	}
	_, err = r.new(t, r.as(r.pane(leaf)), NewOpts{})
	wantCode(t, err, 6)
	if !strings.Contains(err.Error(), "leaf") {
		t.Fatal(err)
	}
}

// reserve refuses (exit 6) when the running tasks of the tree reach its limit, whoever starts them.
func TestTreeMaxCountsTheRunningTasksOfTheTree(t *testing.T) {
	r := newRig(t)
	lead := r.mustNew(t, nil, NewOpts{SubDepth: 2, TreeMax: 3})
	mid := r.mustNew(t, r.as(r.pane(lead)), NewOpts{SubDepth: 1})
	_, err := r.new(t, r.as(r.pane(mid)), NewOpts{}) // 3 running: lead, mid and this one
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.new(t, r.as(r.pane(lead)), NewOpts{})
	wantCode(t, err, 6)
	if !strings.Contains(err.Error(), "tree") {
		t.Fatal(err)
	}
	r.mustNew(t, nil, NewOpts{}) // another tree is not counted
}

func escalations(r *rig) []string {
	var out []string
	for _, s := range r.b.Texts(r.owner.ID) {
		if strings.Contains(s, "ESCALATION") {
			out = append(out, s)
		}
	}
	return out
}

// The owner's watcher tells it once when a task below its own tasks has waited on a dialog for 180 s.
func TestEscalationOfADialogBelowYourTasks(t *testing.T) {
	r := newRig(t)
	lead := r.mustNew(t, nil, NewOpts{SubDepth: 1})
	sub := r.mustNew(t, r.as(r.pane(lead)), NewOpts{Title: "deploy"})
	mark := func(since float64) {
		r.reg.Update(func(d *Data) error {
			d.Tasks[sub.ID].Dialog = &DialogMark{Fingerprint: "abcd1234", Question: "Run deploy?", Since: since}
			return nil
		})
	}
	mark(r.clock.T - 100)
	r.tasks.Watch(WatchOpts{Rounds: 1})
	if n := escalations(r); len(n) != 0 {
		t.Fatalf("told before 180 s: %q", n)
	}
	mark(r.clock.T - 200)
	r.tasks.Watch(WatchOpts{Rounds: 1})
	r.tasks.Watch(WatchOpts{Rounds: 1})
	n := escalations(r)
	if len(n) != 1 || !strings.Contains(n[0], "task "+sub.ID+" (deploy), under your task "+lead.ID) || !strings.Contains(n[0], "abcd1234") {
		t.Fatalf("%q", n)
	}
}

// The owner is told once when one of its tasks exited while tasks under it still run, with the command.
func TestEscalationOfAnEndedTaskWithRunningSubTasks(t *testing.T) {
	r := newRig(t)
	lead := r.mustNew(t, nil, NewOpts{SubDepth: 1})
	sub := r.mustNew(t, r.as(r.pane(lead)), NewOpts{})
	if _, err := r.tasks.Exited(lead.ID, 1); err != nil {
		t.Fatal(err)
	}
	r.tasks.Watch(WatchOpts{Rounds: 1})
	r.tasks.Watch(WatchOpts{Rounds: 1})
	n := escalations(r)
	if len(n) != 1 || !strings.Contains(n[0], "task "+lead.ID) || !strings.Contains(n[0], sub.ID) ||
		!strings.Contains(n[0], "harness-ops task close --tree --force "+lead.ID) {
		t.Fatalf("%q", n)
	}
}

// task close --tree closes a task and those under it that may be closed, deepest first; --force closes the
// caller's own subtree whatever the status, and is refused outside it.
func TestCloseTree(t *testing.T) {
	r := newRig(t)
	lead := r.mustNew(t, nil, NewOpts{SubDepth: 2})
	mid := r.mustNew(t, r.as(r.pane(lead)), NewOpts{SubDepth: 1})
	leaf := r.mustNew(t, r.as(r.pane(mid)), NewOpts{})
	r.done(t, leaf, "ok", "fine")
	if _, err := r.as(r.pane(mid)).Accept(leaf.ID, false); err != nil {
		t.Fatal(err)
	}
	closed, err := r.tasks.CloseTree(lead.ID, false)
	wantCode(t, err, 6)
	if len(closed) != 1 || closed[0].ID != leaf.ID || r.get(t, mid.ID).Closed || r.get(t, lead.ID).Closed {
		t.Fatalf("%+v %v", closed, err)
	}
	_, err = r.as(r.other).CloseTree(lead.ID, true)
	wantCode(t, err, 2)
	closed, err = r.tasks.CloseTree(lead.ID, true)
	if err != nil || len(closed) != 2 || closed[0].ID != mid.ID || closed[1].ID != lead.ID {
		t.Fatalf("%+v %v", closed, err)
	}
	for _, x := range []*Task{lead, mid, leaf} {
		if !r.get(t, x.ID).Closed {
			t.Fatalf("%s not closed", x.ID)
		}
	}
}

// --- 3: task adopt --------------------------------------------------------------

// A task whose owner pane is gone is adopted: owner, owner ref, pane record and journal follow; a task
// whose owner is alive needs --force, and its old owner gets one notice.
func TestAdoptATask(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, r.as(r.other), NewOpts{Title: "port"})
	_, err := r.tasks.Adopt(x.ID, false)
	wantCode(t, err, 6)
	res, err := r.tasks.Adopt(x.ID, true)
	if err != nil || res.Task.Owner != r.owner.ID || res.OldOwner != r.other.ID || res.New {
		t.Fatalf("%+v %v", res, err)
	}
	if rec := r.data().Panes[x.Pane]; rec.Owner != r.owner.ID {
		t.Fatalf("pane record owner %s", rec.Owner)
	}
	if texts := r.b.Texts(r.other.ID); len(texts) != 1 || !strings.Contains(texts[0], "task "+x.ID+" (port) was adopted by "+r.owner.Address()) {
		t.Fatalf("old owner got %q", texts)
	}
	if ev := journalEvents(t, r, Filter{Task: x.ID}); !termtest.Contains(ev, "task.adopt:running:adopted from "+r.other.ID) {
		t.Fatalf("%q", ev)
	}
	_, err = r.tasks.Adopt(x.ID, true) // yours already
	wantCode(t, err, 2)

	// A result held for the old owner (its draft), whose pane then went, reaches the new owner.
	r = newRig(t)
	y := r.mustNew(t, r.as(r.other), NewOpts{})
	r.b.State[r.other.ID].Input = "typing"
	r.done(t, y, "ok", "ported")
	dropPane(r.b, r.other.ID)
	if res, err := r.tasks.Adopt(y.ID, false); err != nil || res.Task.Owner != r.owner.ID {
		t.Fatalf("%+v %v", res, err)
	}
	if texts := r.b.Texts(r.owner.ID); len(texts) != 1 || !strings.Contains(texts[0], "RE: task "+y.ID) {
		t.Fatalf("%q", texts)
	}
}

// An adopted task keeps its sub-tasks under it; its tree, depth and caps are recomputed under the adopter's task.
func TestAdoptKeepsTheSubtreeAndRecomputesTheTree(t *testing.T) {
	r := newRig(t)
	lead := r.mustNew(t, nil, NewOpts{SubDepth: 3})
	x := r.mustNew(t, r.as(r.other), NewOpts{SubDepth: 3})
	y := r.mustNew(t, r.as(r.pane(x)), NewOpts{SubDepth: 2})
	dropPane(r.b, r.other.ID)
	if _, err := r.as(r.pane(lead)).Adopt(x.ID, false); err != nil {
		t.Fatal(err)
	}
	gx, gy := r.get(t, x.ID), r.get(t, y.ID)
	if gx.Owner != lead.Pane || gx.Parent != lead.ID || gx.Root != lead.ID || gx.Depth != 2 || gx.SubDepth != 2 {
		t.Fatalf("adopted: %+v", gx)
	}
	if gy.Parent != x.ID || gy.Owner != x.Pane || gy.Root != lead.ID || gy.Depth != 3 || gy.SubDepth != 1 {
		t.Fatalf("under it: %+v", gy)
	}
	_, err := r.as(r.pane(y)).Adopt(lead.ID, true) // a task above the caller's own
	wantCode(t, err, 2)
}

// A pane with no task: a running task record is made for it, owned by the caller, and the pane gets one
// message (no /clear). Refused: your own pane (2), a pane holding a task of a live owner unless --force (6).
func TestAdoptAPaneWithNoTask(t *testing.T) {
	r := newRig(t)
	res, err := r.tasks.Adopt(r.other.ID, false)
	if err != nil || !res.New {
		t.Fatalf("%+v %v", res, err)
	}
	x := r.get(t, res.Task.ID)
	if x.Status != "running" || x.Owner != r.owner.ID || x.Pane != r.other.ID || !x.Launched || x.Owned {
		t.Fatalf("%+v", x)
	}
	typed := r.b.Typed(r.other.ID)
	texts := r.b.Texts(r.other.ID)
	if len(texts) != 1 || !strings.Contains(texts[0], "You now report to owner ("+r.owner.Address()+"). When this work is done, run: harness-ops task done "+x.ID) ||
		termtest.Contains(typed, "/clear") {
		t.Fatalf("typed %q", typed)
	}
	if rec := r.data().Panes[r.other.ID]; rec == nil || rec.Owner != r.owner.ID {
		t.Fatalf("%+v", rec)
	}
	_, err = r.tasks.Adopt(r.owner.ID, true)
	wantCode(t, err, 2)

	r = newRig(t)
	w := r.mustNew(t, r.as(r.other), NewOpts{})
	_, err = r.tasks.Adopt(w.Pane, false)
	wantCode(t, err, 6)
	if res, err := r.tasks.Adopt(w.Pane, true); err != nil || res.Task.ID != w.ID {
		t.Fatalf("%+v %v", res, err)
	}
}

// --- registry -----------------------------------------------------------------

// The new task fields are omitted when empty, and a task's unknown fields survive a rewrite beside them.
func TestRoundFieldsAreOmittedWhenEmptyAndUnknownFieldsSurvive(t *testing.T) {
	data, err := json.Marshal(Task{ID: "t000001"})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"defer", "acked_at", "ack_note", "denied", "root", "sub_depth", "tree_max", "orphaned", "escalated"} {
		if strings.Contains(string(data), `"`+k+`"`) {
			t.Fatalf("%s written when empty: %s", k, data)
		}
	}
	var x Task
	if err := json.Unmarshal([]byte(`{"id":"t000001","defer":true,"root":"t000001","sub_depth":2,"tree_max":5,"later":{"a":1},
		"dialog":{"fingerprint":"f","escalated":true,"later2":2}}`), &x); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"defer":true`, `"sub_depth":2`, `"tree_max":5`, `"later":{"a":1}`, `"escalated":true`, `"later2":2`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("lost %s: %s", want, out)
		}
	}
}

// FindKey reads the journal from its end in blocks: an entry far from the end, past many lines, is found.
func TestFindKeyReadsAcrossBlocks(t *testing.T) {
	r := newRig(t)
	j := r.tasks.journal()
	if err := j.Append(Entry{Event: "msg", Key: "k1", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 400; i++ {
		j.Append(Entry{Event: "msg", Key: "other", Text: strings.Repeat("x", 500)})
	}
	e, err := j.FindKey("k1", int64(r.clock.T)-600)
	if err != nil || e == nil || e.Text != "first" {
		t.Fatalf("%+v %v", e, err)
	}
	if e, _ := j.FindKey("k1", int64(r.clock.T)+1); e != nil {
		t.Fatal("found an entry before since")
	}
	n := 0
	j.scanBack(func(Entry) bool { n++; return true })
	if n != 401 {
		t.Fatalf("read %d entries back, wrote 401: a line split between two blocks was lost", n)
	}
	var _ term.Backend = r.b
}

// sendFails fails SendText: the message is never typed.
type sendFails struct{ *termtest.Fake }

func (sendFails) SendText(*term.Pane, string) error { return term.BackendErr("the terminal went away") }

// A msg whose submit failed before typing keeps no key in its journal entry: the same message may go again.
// One that was typed keeps it, and the same key is refused (exit 6) until Again.
func TestAKeyIsKeptOnlyOnceTyped(t *testing.T) {
	r := newRig(t)
	key := MsgKey(r.owner.Address(), r.other.ID, "build it")
	broken := r.tasks.ops(sendFails{r.b}, r.owner)
	if _, err := broken.Msg(r.other, "build it", MsgOpts{Key: key}); err == nil {
		t.Fatal("the send did not fail")
	}
	if e, _ := r.tasks.journal().FindKey(key, 0); e != nil {
		t.Fatalf("a send that typed nothing kept its key: %+v", e)
	}
	ops := r.tasks.ops(r.b, r.owner)
	if _, err := ops.Msg(r.other, "build it", MsgOpts{Key: key}); err != nil {
		t.Fatal(err)
	}
	_, err := ops.Msg(r.other, "build it", MsgOpts{Key: key})
	wantCode(t, err, 6)
	if _, err := ops.Msg(r.other, "build it", MsgOpts{Key: key, Again: true}); err != nil {
		t.Fatal(err)
	}
	if n := len(r.b.Texts(r.other.ID)); n != 2 {
		t.Fatalf("typed %d times", n)
	}
}

// A pane someone started by hand and then adopted was not created by harness-ops: after its task ends,
// close must still refuse it without --force (v0.10.0 left a record that made it look created).
func TestAnAdoptedHandStartedPaneIsNeverClosableWithoutForce(t *testing.T) {
	r := newRig(t)
	res, err := r.tasks.Adopt(r.other.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.tasks.Close(res.Task.ID, true); err != nil {
		t.Fatal(err)
	}
	_, err = r.tasks.ClosePane(r.other.ID, false)
	wantCode(t, err, 6)
	if r.b.Has("close", r.other.ID) {
		t.Fatal("a hand-started pane was closed")
	}
}
