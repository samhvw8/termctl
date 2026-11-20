package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/term"
	"github.com/samhvw8/termctl/internal/termtest"
)

func TestPaneTitleAndRoleNameReadSplitJobAndLease(t *testing.T) {
	cases := []struct {
		job      Job
		lease    Lease
		wantRole string
		wantPane string
	}{
		{Job{ID: "t1", Title: "port"}, Lease{SubDepth: 0}, "WORKER", "WORKER · t1 · port"},
		{Job{ID: "t2", Title: "lead"}, Lease{SubDepth: 1}, "LEAD", "LEAD · t2 · lead"},
		{Job{ID: "t3"}, Lease{SubDepth: 2}, "LEAD OF LEADS", "LEAD OF LEADS · t3"},
		{Job{ID: "t4", Title: "cleanup", Defer: true}, Lease{SubDepth: 1}, "LEAD", "t4 · cleanup"},
		{Job{ID: "t5", Defer: true}, Lease{SubDepth: 0}, "WORKER", "t5"},
		{Job{ID: "t6", Title: "check", ReviewOf: "t1"}, Lease{SubDepth: 2}, "REVIEWER", "REVIEWER · t6 · check"},
	}
	for _, c := range cases {
		if got := roleName(&c.job, &c.lease); got != c.wantRole {
			t.Errorf("roleName(%+v, %+v) = %q, want %q", c.job, c.lease, got, c.wantRole)
		}
		if got := paneTitle(&c.job, &c.lease); got != c.wantPane {
			t.Errorf("paneTitle(%+v, %+v) = %q, want %q", c.job, c.lease, got, c.wantPane)
		}
	}
}

func TestChildWorkOnLeasesPreventsStallOnWaitingLead(t *testing.T) {
	d := emptyData()
	d.Leases["llead"] = &Lease{ID: "llead", Pane: "fake:5", State: "active"}
	d.Jobs["tc1"] = &Job{ID: "tc1", Lease: "lc1", Status: "running"}
	d.Leases["lc1"] = &Lease{ID: "lc1", Parent: "llead", Job: "tc1", State: "active"}
	d.Jobs["tc2"] = &Job{ID: "tc2", Lease: "lc2", Status: "running"}
	d.Leases["lc2"] = &Lease{ID: "lc2", Parent: "llead", Job: "tc2", State: "active", Stall: &StallMark{At: 1200}}
	// Closed and non-running children do not keep the parent working or count as stalled.
	d.Jobs["tc3"] = &Job{ID: "tc3", Lease: "lc3", Status: "ready"}
	d.Leases["lc3"] = &Lease{ID: "lc3", Parent: "llead", Job: "tc3", State: "active"}
	d.Jobs["tc4"] = &Job{ID: "tc4", Lease: "lc4", Status: "running", Closed: true}
	d.Leases["lc4"] = &Lease{ID: "lc4", Parent: "llead", Job: "tc4", State: "ended"}

	working, stalled := childWork(d, "llead")
	if !working || len(stalled) != 1 || stalled[0] != "tc2" {
		t.Fatalf("working=%v stalled=%v, want true [tc2]", working, stalled)
	}

	d.Leases["lc1"].Stall = &StallMark{At: 1300}
	working, stalled = childWork(d, "llead")
	if working || len(stalled) != 2 || stalled[0] != "tc1" || stalled[1] != "tc2" {
		t.Fatalf("working=%v stalled=%v, want false [tc1 tc2]", working, stalled)
	}

	// End-to-end via trackIdle: lead waits while child runs, then stalls naming stalled children.
	leadJob := &Job{ID: "tlead", Lease: "llead", Title: "lead", Status: "running"}
	d.Jobs["tlead"] = leadJob
	d.Leases["llead"].Job = "tlead"
	d.Leases["llead"].Owner = "fake:9"
	d.Leases["lc1"].Stall = nil
	sig := Signal{Task: "tlead", Lease: "llead", Pane: "fake:5", Kind: "clear", Idle: true, At: 1000}
	trackIdle(d, leadJob, d.Leases["llead"], sig, 1000)
	trackIdle(d, leadJob, d.Leases["llead"], sig, 1000+stallAfter+10)
	if d.Leases["llead"].Stall != nil {
		t.Fatal("lead stalled while a child lease was working")
	}
	d.Leases["lc1"].Stall = &StallMark{At: 1700}
	trackIdle(d, leadJob, d.Leases["llead"], sig, 1000+2*stallAfter+20)
	if d.Leases["llead"].Stall == nil {
		t.Fatal("lead did not stall once all running child leases stalled")
	}
	last := d.Notices[len(d.Notices)-1]
	if last.Kind != "stall" || !strings.Contains(last.Text, "tc1") || !strings.Contains(last.Text, "tc2") {
		t.Fatalf("unexpected stall notice: %+v", last)
	}
}

func TestClosePaneRefusesAdoptedPanesWithoutForce(t *testing.T) {
	// Even with an active owned Lease on the pane, ClosePane checks d.Created only.
	r := newRig(t)
	if err := r.reg.Update(func(d *Data) error {
		d.Leases["ladopt"] = &Lease{ID: "ladopt", Owner: r.owner.ID, OwnerRef: r.b.ServerRef(), Pane: r.other.ID,
			Ref: r.b.ServerRef(), Harness: "generic", State: "active", Launched: true, Started: 1000}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := r.tasks.ClosePane(r.other.ID, false)
	wantCode(t, err, term.CodeGuard)
	if r.b.Has("close", r.other.ID) {
		t.Fatal("closed a pane without a CreatedPane record")
	}
	if _, err := r.tasks.ClosePane(r.other.ID, true); err != nil {
		t.Fatal(err)
	}
	if !r.b.Has("close", r.other.ID) || r.data().Leases["ladopt"].State != "ended" {
		t.Fatalf("expected pane closed and lease ended, got state=%s", r.data().Leases["ladopt"].State)
	}
}

func TestTaskCloseOnReusePaneRestoresBareLease(t *testing.T) {
	r := newRig(t)
	p := spawned(t, r)
	orig := activeLeaseForPane(r.data(), p.ID, r.b.ServerRef())
	if orig == nil || orig.Job != "" {
		t.Fatalf("expected bare spawn lease, got %+v", orig)
	}
	if c := r.data().Created[p.ID]; c == nil || c.ForLease != orig.ID {
		t.Fatalf("created record %+v", c)
	}

	x := r.mustNew(t, nil, NewOpts{Reuse: p.ID})
	d := r.data()
	if d.Leases[orig.ID].State != "ended" {
		t.Fatalf("old bare lease should be ended, got %s", d.Leases[orig.ID].State)
	}
	if d.Created[p.ID].ForLease != orig.ID {
		t.Fatalf("reuse overwrote CreatedPane.ForLease: %s", d.Created[p.ID].ForLease)
	}

	r.done(t, x, "ok", "finished")
	if _, err := r.tasks.Accept(x.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.tasks.Close(x.ID, false); err != nil {
		t.Fatal(err)
	}
	if r.b.Has("close", p.ID) {
		t.Fatal("closing a reused task closed the pane")
	}
	d = r.data()
	if d.Created[p.ID] == nil || d.Created[p.ID].ForLease != orig.ID {
		t.Fatalf("CreatedPane lost or changed after task close: %+v", d.Created[p.ID])
	}
	restored := activeLeaseForPane(d, p.ID, r.b.ServerRef())
	if restored == nil || restored.ID == orig.ID || restored.ID == d.Jobs[x.ID].Lease || restored.Job != "" || restored.Owner != r.owner.ID {
		t.Fatalf("expected fresh restored bare lease, got %+v", restored)
	}

	// ls --mine, key and answer keep working on the surviving spawned pane.
	mine, err := r.tasks.Mine([]*term.Pane{p})
	if err != nil || len(mine) != 1 || mine[0].ID != p.ID {
		t.Fatalf("Mine=%v err=%v", mine, err)
	}
	if _, err := r.tasks.Keys(p.ID, []string{"escape"}, false); err != nil {
		t.Fatalf("Keys on restored bare lease failed: %v", err)
	}
	p.Process = "claude"
	r.b.State[p.ID].Dialog = &termtest.Dialog{Body: "run?", Options: []string{"Yes", "No"}, Selected: 1}
	if _, _, err := r.tasks.Answer(p.ID, AnswerOpts{N: 1}); err != nil {
		t.Fatalf("Answer on restored bare lease failed: %v", err)
	}
}

func TestDeliverAttributesJournalProjectFromAndScopeFromLease(t *testing.T) {
	r := newRig(t)
	r.owner.Process = "claude"
	if err := r.reg.Update(func(d *Data) error {
		d.Scopes["s123456"] = &Scope{ID: "s123456", Name: "backend", Project: "/proj/scope-a", Lease: "lscope1", Created: 900}
		d.Leases["lscope1"] = &Lease{
			ID: "lscope1", Owner: r.owner.ID, OwnerRef: r.b.ServerRef(),
			Pane: r.other.ID, Ref: r.b.ServerRef(), Harness: "claude",
			Project: "/proj/scope-a", Scope: "s123456", State: "active", Launched: true,
			Dialog: &DialogMark{Fingerprint: "fp11", Question: "Run?", Since: 1000}, Started: 950,
		}
		d.Notices = append(d.Notices, &Notice{
			ID: "n111111", Lease: "lscope1", Owner: r.owner.ID, OwnerRef: r.b.ServerRef(),
			Tag: "[s]", Text: "scope backend (s123456) DIALOG fp11: Run?", Kind: "dialog", Dialog: "fp11", Created: 1000,
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Deliver from a standalone flusher (Sender == nil) so Project/From/Scope must come from the Lease.
	flusher := &Tasks{Reg: r.reg, Backend: r.b, Clock: r.clock, Factory: r.tasks.Factory}
	n, err := flusher.FlushPending()
	if err != nil || n != 1 {
		t.Fatalf("FlushPending=%d err=%v", n, err)
	}
	entries, _, err := (&Journal{Root: r.reg.Root}).Read(Filter{})
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	e := entries[0]
	if e.Project != "/proj/scope-a" || e.From != r.other.ID || e.Scope != "s123456" {
		t.Fatalf("journal attribution wrong: project=%q from=%q scope=%q", e.Project, e.From, e.Scope)
	}
}

func TestReparentLeaseNeverRetargetsAdoptNotice(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, r.as(r.other), NewOpts{Title: "port"})
	// Hold a result notice for the old owner (draft on its line), plus pre-existing adopt and escalate notices.
	r.b.State[r.other.ID].Input = "typing"
	r.done(t, x, "ok", "finished")
	if err := r.reg.Update(func(d *Data) error {
		lid := d.Jobs[x.ID].Lease
		d.Notices = append(d.Notices,
			&Notice{ID: "nadopt0", Lease: lid, Owner: r.other.ID, OwnerRef: r.b.ServerRef(), Tag: "[a]", Text: "prior adopt", Kind: "adopt", Created: 1001},
			&Notice{ID: "nescal0", Lease: lid, Owner: r.other.ID, OwnerRef: r.b.ServerRef(), Tag: "[e]", Text: "prior escalate", Kind: "escalate", Created: 1002},
		)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Keep drafts on both so Adopt writes notices without delivering yet, then verify targets.
	r.b.State[r.owner.ID].Input = "busy"
	if _, err := r.tasks.Adopt(x.ID, true); err != nil {
		t.Fatal(err)
	}
	d := r.data()
	byID := map[string]*Notice{}
	var newAdopt *Notice
	for _, n := range d.Notices {
		byID[n.ID] = n
		if n.Kind == "adopt" && n.ID != "nadopt0" {
			newAdopt = n
		}
	}
	if newAdopt == nil || newAdopt.Owner != r.other.ID {
		t.Fatalf("new adopt notice wrong owner: %+v", newAdopt)
	}
	if byID["nadopt0"].Owner != r.other.ID || byID["nescal0"].Owner != r.other.ID {
		t.Fatalf("existing adopt/escalate retargeted: adopt=%+v escalate=%+v", byID["nadopt0"], byID["nescal0"])
	}
	var resultNotice *Notice
	for _, n := range d.Notices {
		if n.Kind == "result" {
			resultNotice = n
		}
	}
	if resultNotice == nil || resultNotice.Owner != r.owner.ID {
		t.Fatalf("result notice not retargeted to new owner: %+v", resultNotice)
	}

	// Clear drafts and flush: new owner gets result, old owner gets adopt (never the reverse).
	r.b.State[r.owner.ID].Input = ""
	r.b.State[r.other.ID].Input = ""
	if _, err := r.tasks.FlushPending(); err != nil {
		t.Fatal(err)
	}
	newOwnerTexts := strings.Join(r.b.Texts(r.owner.ID), "\n")
	oldOwnerTexts := strings.Join(r.b.Texts(r.other.ID), "\n")
	if !strings.Contains(newOwnerTexts, "RE: task "+x.ID) || strings.Contains(newOwnerTexts, "was adopted by") {
		t.Fatalf("new owner got wrong notices: %q", newOwnerTexts)
	}
	if !strings.Contains(oldOwnerTexts, "was adopted by "+r.owner.Address()) || strings.Contains(oldOwnerTexts, "RE: task "+x.ID) {
		t.Fatalf("old owner got wrong notices: %q", oldOwnerTexts)
	}
}

// B1: Migrating an exited Owned:true task (t000007 on fake:11) creates no extra active bare lease, and
// task close on a created pane ends every active lease on that pane.
func TestMigrateExitedOwnedTaskCreatesNoExtraActiveBareLeaseAndCloseEndsAll(t *testing.T) {
	r := newRig(t)
	r.b.Ref = map[string]string{"pid": "111"}
	r.b.Panes = append(r.b.Panes, termtest.MakePane(11, "t000007", "", "/w", "zsh"))
	r.b.State["fake:11"] = &termtest.Screen{Log: []string{"$ "}}
	if err := os.MkdirAll(r.reg.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.reg.Path, v2Fixture(t), 0o644); err != nil {
		t.Fatal(err)
	}

	d := r.data()
	l7 := taskLease(d, "t000007")
	if l7 == nil || l7.State != "exited" || d.Created["fake:11"] == nil || d.Created["fake:11"].ForLease != l7.ID {
		t.Fatalf("unexpected t000007 state: lease=%+v created=%+v", l7, d.Created["fake:11"])
	}
	if al := activeLeaseForPane(d, "fake:11", r.b.Ref); al != nil {
		t.Fatalf("exited Owned task resurrected an active bare lease on fake:11: %+v", al)
	}

	if _, err := r.tasks.Close("t000007", true); err != nil {
		t.Fatal(err)
	}
	d = r.data()
	if !r.b.Has("close", "fake:11") || d.Created["fake:11"] != nil {
		t.Fatalf("expected fake:11 closed and Created deleted, created=%+v", d.Created["fake:11"])
	}
	if al := activeLeaseForPane(d, "fake:11", r.b.Ref); al != nil {
		t.Fatalf("active lease remained on fake:11 after task close: %+v", al)
	}

	// Also verify Close ends any co-located active lease when closing a created pane.
	r2 := newRig(t)
	x := r2.mustNew(t, nil, NewOpts{Title: "created"})
	if err := r2.reg.Update(func(d *Data) error {
		d.Leases["lstale"] = &Lease{ID: "lstale", Owner: r2.owner.ID, Pane: x.Pane, Ref: r2.b.ServerRef(), State: "active", Started: 900}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r2.tasks.Close(x.ID, true); err != nil {
		t.Fatal(err)
	}
	if al := activeLeaseForPane(r2.data(), x.Pane, r2.b.ServerRef()); al != nil {
		t.Fatalf("co-located active lease survived Close: %+v", al)
	}
}

// I1: A failing Update(fn) over a v2 registry leaves no backup file, so a subsequent succeeding Update backs up
// the latest v2 bytes (including any v0.10.x writes made after the failed Update).
func TestFailedUpdateDoesNotWritePrematureV2Backup(t *testing.T) {
	v2Initial := []byte(`{"version":2,"tasks":{"t1":{"id":"t1","status":"running","owner":"fake:9","started":1,"round":1}},"panes":{},"pending":[]}`)
	r := writeRegistry(t, v2Initial)

	want := errors.New("cap refused")
	if err := r.Update(func(d *Data) error { return want }); !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
	if got := backups(t, r); len(got) != 0 {
		t.Fatalf("failed Update wrote a premature backup: %v", got)
	}

	v2Later := []byte(`{"version":2,"tasks":{"t1":{"id":"t1","status":"running","owner":"fake:9","started":1,"round":1},"t2":{"id":"t2","status":"running","owner":"fake:9","started":2,"round":1}},"panes":{},"pending":[]}`)
	if err := os.WriteFile(r.Path, v2Later, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Update(func(*Data) error { return nil }); err != nil {
		t.Fatal(err)
	}
	got := backups(t, r)
	if len(got) != 1 {
		t.Fatalf("expected 1 backup after succeeding Update, got %v", got)
	}
	b, err := os.ReadFile(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, v2Later) {
		t.Fatalf("backup missed newer v2 writes:\ngot:  %s\nwant: %s", b, v2Later)
	}
}

// I2: A v2 task in reservation (Status: "running", Pane: "") migrates with Lease.State == "active", so
// Reconcile() transitions it to "lost" once reservationTTL passes.
func TestMigrateReservedRunningTaskStaysActiveUntilReconcileLosesIt(t *testing.T) {
	r := newRig(t)
	if err := os.MkdirAll(r.reg.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{"version":2,"tasks":{"tres01":{"id":"tres01","status":"running","owner":"fake:9","started":1000,"round":1}},"panes":{},"pending":[]}`
	if err := os.WriteFile(r.reg.Path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	d := r.data()
	if l := taskLease(d, "tres01"); l == nil || l.State != "active" {
		t.Fatalf("migrated reservation lease=%+v, want State=active", l)
	}
	r.clock.T = 1000 + reservationTTL + 1
	if err := r.tasks.Reconcile(); err != nil {
		t.Fatal(err)
	}
	d = r.data()
	if d.Jobs["tres01"].Status != "lost" || taskLease(d, "tres01").State != "lost" {
		t.Fatalf("reconcile status=%s lease.State=%s, want both lost", d.Jobs["tres01"].Status, taskLease(d, "tres01").State)
	}
}

// I3: Owned lives on Job (historical output-only provenance for TaskView after CreatedPane is deleted),
// while bare Leases carry no Owned field.
func TestJobOwnedPreservedWhileBareLeaseHasNoOwnedField(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{Title: "owned"})
	if !r.data().Jobs[x.ID].Owned || !x.Owned {
		t.Fatalf("expected Owned=true on Job and TaskView: job=%+v view=%+v", r.data().Jobs[x.ID], x)
	}
	r.done(t, x, "ok", "done")
	if _, err := r.tasks.Accept(x.ID, true); err != nil {
		t.Fatal(err)
	}
	if d := r.data(); d.Created[x.Pane] != nil || !d.TaskView(x.ID).Owned {
		t.Fatalf("expected CreatedPane deleted and TaskView.Owned preserved: created=%+v view=%+v", d.Created[x.Pane], d.TaskView(x.ID))
	}

	p := spawned(t, r)
	raw, err := json.Marshal(paneLease(r.data(), p.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"owned"`) {
		t.Fatalf("bare Lease JSON unexpectedly contains owned: %s", raw)
	}
}

// I4: On a reused pane whose prior ready task was closed with --force (Closed:true, Status:"ready"), Answer
// and Keys target the pane's active bare Lease rather than the closed task's ended Lease.
func TestAnswerAndKeyUseActiveLeaseWhenPanePreviouslyHeldClosedTask(t *testing.T) {
	r := newRig(t)
	p := spawned(t, r)
	x := r.mustNew(t, nil, NewOpts{Reuse: p.ID})
	r.done(t, x, "ok", "ready")
	if _, err := r.tasks.Close(x.ID, true); err != nil {
		t.Fatal(err)
	}
	d := r.data()
	if !d.Jobs[x.ID].Closed || d.Jobs[x.ID].Status != "ready" {
		t.Fatalf("expected closed ready task, got %+v", d.Jobs[x.ID])
	}
	oldLease := taskLease(d, x.ID)
	bare := activeLeaseForPane(d, p.ID, r.b.ServerRef())
	if bare == nil || bare.ID == oldLease.ID || oldLease.State != "ended" {
		t.Fatalf("oldLease=%+v bare=%+v", oldLease, bare)
	}

	if _, err := r.tasks.Keys(p.ID, []string{"escape"}, false); err != nil {
		t.Fatal(err)
	}
	p.Process = "claude"
	r.b.State[p.ID].Dialog = &termtest.Dialog{Body: "run?", Options: []string{"Yes", "No"}, Selected: 1}
	fp := func() string {
		dlg, err := r.tasks.ops(r.b, r.owner).ReadDialog(p)
		if err != nil || dlg == nil {
			t.Fatal(err)
		}
		return dlg.Fingerprint
	}()
	if err := r.reg.Update(func(d *Data) error {
		activeLeaseForPane(d, p.ID, r.b.ServerRef()).Dialog = &DialogMark{Fingerprint: fp, Question: "run?", Since: 1000}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.tasks.Answer(p.ID, AnswerOpts{N: 1, Expect: fp}); err != nil {
		t.Fatal(err)
	}

	d = r.data()
	if got := d.Leases[bare.ID]; got.Dialog != nil || got.AnsweredAt == 0 {
		t.Fatalf("active bare lease not updated on Answer: %+v", got)
	}
	if got := d.Leases[oldLease.ID]; got.AnsweredAt != 0 {
		t.Fatalf("closed task's ended lease was modified: %+v", got)
	}
	entries, _, err := (&Journal{Root: r.reg.Root}).Read(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if (e.Event == "key" || e.Event == "answer") && e.Task != "" {
			t.Fatalf("pane action misattributed to closed task %s: %+v", e.Task, e)
		}
	}
}

// I5: applyOn, trackIdle, and applyPane return changed=false on no-op inputs and true only on mutations,
// so repeat idle/clear/same-dialog rounds skip Reg.Update without double-marshaling.
func TestApplyOnReportsChangedOnlyOnRealMutation(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	d := r.data()

	if r.tasks.applyOn(d, []Signal{signalOf(t, r, x.ID, "clear")}, true, r.clock.T) {
		t.Fatal("clear with no dialog should report changed=false")
	}
	idleSig := signalOf(t, r, x.ID, "clear")
	idleSig.Idle = true
	if !r.tasks.applyOn(d, []Signal{idleSig}, true, 1000) {
		t.Fatal("first idle look should set IdleSince and report changed=true")
	}
	if r.tasks.applyOn(d, []Signal{idleSig}, true, 1003) {
		t.Fatal("repeat idle look before stallAfter should report changed=false")
	}
	if !r.tasks.applyOn(d, []Signal{idleSig}, true, 1000+stallAfter+1) {
		t.Fatal("idle past stallAfter should stall and report changed=true")
	}
	if r.tasks.applyOn(d, []Signal{idleSig}, true, 1000+stallAfter+4) {
		t.Fatal("repeat idle look while already stalled should report changed=false")
	}

	// Same via apply() on disk: repeat idle round before stallAfter must not rewrite registry.json.
	if err := r.tasks.apply([]Signal{idleSig}, true); err != nil {
		t.Fatal(err)
	}
	before := registryFile(t, r)
	r.clock.T += 3
	if err := r.tasks.apply([]Signal{idleSig}, true); err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, registryFile(t, r)) {
		t.Fatal("no-op repeat idle round rewrote registry.json")
	}

	// Bare pane applyPane changed tracking.
	p := spawned(t, r)
	d = r.data()
	lid := paneLease(d, p.ID).ID
	if r.tasks.applyOn(d, []Signal{{Lease: lid, Pane: p.ID, Kind: "clear"}}, true, r.clock.T) {
		t.Fatal("pane clear with no dialog should report changed=false")
	}
	dlgSig := Signal{Lease: lid, Pane: p.ID, Kind: "dialog", Dialog: &Dialog{Fingerprint: "1111aaaa", Question: "Run?"}}
	if !r.tasks.applyOn(d, []Signal{dlgSig}, true, r.clock.T) {
		t.Fatal("new pane dialog should report changed=true")
	}
	if r.tasks.applyOn(d, []Signal{dlgSig}, true, r.clock.T+3) {
		t.Fatal("repeat notified pane dialog should report changed=false")
	}
	if !r.tasks.applyOn(d, []Signal{{Lease: lid, Pane: p.ID, Kind: "clear"}}, true, r.clock.T+6) {
		t.Fatal("pane clear after dialog should report changed=true")
	}
}
