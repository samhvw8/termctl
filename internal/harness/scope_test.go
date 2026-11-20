package harness

import (
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/samhvw8/termctl/internal/term"
	"github.com/samhvw8/termctl/internal/termtest"
)

func TestAttachSilentCostsZeroMessagesAndCreatesScopeAndLease(t *testing.T) {
	r := newRig(t)
	sv, updated, err := r.tasks.Attach(AttachOpts{
		Target:   r.other.ID,
		Name:     "backend",
		About:    "own API\nendpoints",
		HasAbout: true,
	})
	if err != nil || updated {
		t.Fatalf("Attach updated=%v err=%v", updated, err)
	}
	if sv.Name != "backend" || sv.State != "live" || sv.Pane != r.other.ID || sv.Owner != r.owner.ID || sv.About != "own API endpoints" || sv.Told {
		t.Fatalf("unexpected ScopeView: %+v", sv)
	}
	if len(r.b.Texts(r.other.ID)) != 0 {
		t.Fatalf("silent attach typed into target pane: %v", r.b.Texts(r.other.ID))
	}
	d := r.data()
	if d.Created[r.other.ID] != nil {
		t.Fatalf("attach must not write CreatedPane: %+v", d.Created[r.other.ID])
	}
	l := d.Leases[sv.Lease]
	if l == nil || l.Scope != sv.ID || l.State != "active" || !l.Launched {
		t.Fatalf("unexpected Lease: %+v", l)
	}
	entries, _, err := r.tasks.journal().Read(Filter{Scope: sv.ID})
	if err != nil || len(entries) != 1 || entries[0].Event != "scope.attach" {
		t.Fatalf("journal entries=%+v err=%v", entries, err)
	}
}

func TestAttachNotifyChecksDraftBeforeWritingRegistry(t *testing.T) {
	r := newRig(t)
	r.other.Process = "omp"
	r.b.State[r.other.ID].Input = "unfinished user prompt"
	_, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "backend", Notify: true})
	wantCode(t, err, term.CodeDraft)
	d := r.data()
	if len(d.Scopes) != 0 || len(d.Leases) != 0 {
		t.Fatalf("draft refusal wrote registry: scopes=%d leases=%d", len(d.Scopes), len(d.Leases))
	}

	r.b.State[r.other.ID].Input = ""
	sv, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "backend", Notify: true})
	if err != nil || !sv.Told {
		t.Fatalf("Attach --notify err=%v sv=%+v", err, sv)
	}
	texts := r.b.Texts(r.other.ID)
	if len(texts) != 1 || !strings.Contains(texts[0], "Attached to scope backend ("+sv.ID+")") || !strings.Contains(texts[0], "harness-ops report") {
		t.Fatalf("unexpected notify texts: %v", texts)
	}
}

func TestAttachIdempotentUpdatesAboutAndStallInPlace(t *testing.T) {
	r := newRig(t)
	sv1, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "backend", About: "v1", HasAbout: true, Stall: 1800, HasStall: true})
	if err != nil {
		t.Fatal(err)
	}
	sv2, updated, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, About: "v2", HasAbout: true})
	if err != nil || !updated {
		t.Fatalf("re-attach updated=%v err=%v", updated, err)
	}
	if sv2.ID != sv1.ID || sv2.Lease != sv1.Lease || sv2.About != "v2" || sv2.StallAfter != 1800 {
		t.Fatalf("re-attach changed wrong fields: %+v", sv2)
	}
	_, _, err = r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "other-name"})
	wantCode(t, err, term.CodeUsage)

	sv3, updated, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Stall: 0, HasStall: true})
	if err != nil || !updated || sv3.StallAfter != 0 || sv3.About != "v2" {
		t.Fatalf("expected StallAfter cleared and About kept, got %+v updated=%v err=%v", sv3, updated, err)
	}
}

func TestAttachUpgradesBareSpawnLeasePreservingCreatedPane(t *testing.T) {
	r := newRig(t)
	p, err := r.tasks.Spawn("claude", "tab", "/w", SpawnOpts{Command: "claude", Label: "pair", Timeout: 60})
	if err != nil {
		t.Fatal(err)
	}
	orig := activeLeaseForPane(r.data(), p.ID, r.b.ServerRef())
	if err := r.reg.Update(func(d *Data) error {
		d.Leases[orig.ID].Dialog = &DialogMark{Fingerprint: "fp11", Question: "Run?", Since: 1000}
		d.Leases[orig.ID].Worked = 1005
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	sv, _, err := r.tasks.Attach(AttachOpts{Target: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	if sv.Lease != orig.ID || sv.Name != "pair" {
		t.Fatalf("expected upgraded lease %s and name pair, got %+v", orig.ID, sv)
	}
	d := r.data()
	if c := d.Created[p.ID]; c == nil || c.ForLease != orig.ID {
		t.Fatalf("CreatedPane changed or deleted: %+v", c)
	}
	l := d.Leases[orig.ID]
	if l.Dialog == nil || l.Dialog.Fingerprint != "fp11" || l.Worked != 1005 {
		t.Fatalf("upgraded lease lost dialog/worked: %+v", l)
	}
}

func TestAttachRefusesOpenJobAndLiveOtherOwnerWithoutForce(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{Title: "busy"})
	_, _, err := r.tasks.Attach(AttachOpts{Target: x.Pane, Name: "s1"})
	wantCode(t, err, term.CodeGuard)

	// Self pane refused (exit 2).
	_, _, err = r.tasks.Attach(AttachOpts{Target: r.owner.ID, Name: "self"})
	wantCode(t, err, term.CodeUsage)

	// Another live lead's scope: refused without --force, taken over with --force + adopt notice.
	p2 := termtest.MakePane(2, "p2", "", "/w/p2", "claude")
	r.b.Panes = append(r.b.Panes, p2)
	r.b.State[p2.ID] = &termtest.Screen{Log: []string{"ready"}}
	sv, _, err := r.tasks.Attach(AttachOpts{Target: p2.ID, Name: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	r.other.Process = "omp"
	_, _, err = r.as(r.other).Attach(AttachOpts{Target: p2.ID})
	wantCode(t, err, term.CodeGuard)

	sv2, _, err := r.as(r.other).Attach(AttachOpts{Target: p2.ID, Force: true})
	if err != nil || sv2.ID != sv.ID || sv2.Owner != r.other.ID {
		t.Fatalf("forced attach sv2=%+v err=%v", sv2, err)
	}
	if texts := r.b.Texts(r.owner.ID); len(texts) != 1 || !strings.Contains(texts[0], "was attached/adopted by "+r.other.Address()) {
		t.Fatalf("old owner did not receive adopt notice: %v", texts)
	}

	// Scope name already bound to another live pane in caller's project exits 2.
	p3 := termtest.MakePane(3, "p3", "", "/w/p3", "claude")
	r.b.Panes = append(r.b.Panes, p3)
	r.b.State[p3.ID] = &termtest.Screen{Log: []string{"ready"}}
	_, _, err = r.tasks.Attach(AttachOpts{Target: p3.ID, Name: "shared"})
	wantCode(t, err, term.CodeUsage)
}

func TestClosePaneRefusesActiveScopeWithoutForce(t *testing.T) {
	r := newRig(t)
	p, err := r.tasks.Spawn("generic", "tab", "/w", SpawnOpts{Command: "c", Label: "scoped", Timeout: 60})
	if err != nil {
		t.Fatal(err)
	}
	sv, _, err := r.tasks.Attach(AttachOpts{Target: p.ID, Name: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.tasks.ClosePane(p.ID, false)
	wantCode(t, err, term.CodeGuard)
	if r.b.Has("close", p.ID) {
		t.Fatal("ClosePane closed pane with active Scope without --force")
	}
	if _, err := r.tasks.ClosePane(p.ID, true); err != nil {
		t.Fatal(err)
	}
	d := r.data()
	if !r.b.Has("close", p.ID) || d.Scopes[sv.ID].Lease != "" || d.Leases[sv.Lease].State != "ended" {
		t.Fatalf("expected closed pane and unbound scope, got scope=%+v lease=%+v", d.Scopes[sv.ID], d.Leases[sv.Lease])
	}
}

func TestTaskNewReuseRefusesAttachedScope(t *testing.T) {
	r := newRig(t)
	if _, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "svc"}); err != nil {
		t.Fatal(err)
	}
	_, err := r.new(t, nil, NewOpts{Reuse: r.other.ID})
	wantCode(t, err, term.CodeGuard)
	if !strings.Contains(err.Error(), "attached scope svc") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFirstNonDeferSendPiggybacksAttachSuffixOnce(t *testing.T) {
	r := newRig(t)
	sv, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	// Deferred send skips suffix and leaves Told == false.
	scopeID, suf := r.tasks.PrepareMsg(r.b, r.other, true)
	if scopeID != sv.ID || suf != "" {
		t.Fatalf("defer PrepareMsg got scope=%q suf=%q", scopeID, suf)
	}
	// First normal send gets suffix; if typed, MarkScopeTold flips Told = true; second send gets no suffix.
	scopeID, suf = r.tasks.PrepareMsg(r.b, r.other, false)
	wantSuf := AttachSuffix("backend", r.owner.Address())
	if scopeID != sv.ID || suf != wantSuf {
		t.Fatalf("first PrepareMsg got scope=%q suf=%q, want %q", scopeID, suf, wantSuf)
	}
	ops := r.tasks.ops(r.b, r.owner)
	ops.Scope = scopeID
	sent, err := ops.Msg(r.other, "check tests", MsgOpts{Suffix: suf})
	if err != nil || !sent.Typed {
		t.Fatalf("Msg err=%v sent=%+v", err, sent)
	}
	if err := r.tasks.MarkScopeTold(r.b, r.other); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(r.b.Texts(r.other.ID)[0], wantSuf) {
		t.Fatalf("message missing suffix: %q", r.b.Texts(r.other.ID)[0])
	}
	_, suf2 := r.tasks.PrepareMsg(r.b, r.other, false)
	if suf2 != "" {
		t.Fatalf("second PrepareMsg should be empty, got %q", suf2)
	}
	entries, _, _ := r.tasks.journal().Read(Filter{Scope: sv.ID})
	if len(entries) != 2 || entries[1].Event != "msg" || entries[1].Scope != sv.ID {
		t.Fatalf("expected msg attributed to scope %s, got %+v", sv.ID, entries)
	}
}

func TestDetachEndsLeaseDropsScopeUnlessKeepAndLeavesPaneOpen(t *testing.T) {
	r := newRig(t)
	p, err := r.tasks.Spawn("generic", "tab", "/w", SpawnOpts{Command: "c", Label: "helper", Timeout: 60})
	if err != nil {
		t.Fatal(err)
	}
	sv, _, err := r.tasks.Attach(AttachOpts{Target: p.ID, Name: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	// Draft check on detach --notify refuses before mutating.
	p.Process = "omp"
	r.b.State[p.ID].Input = "typing"
	_, err = r.tasks.Detach(DetachOpts{Target: "backend", Notify: true})
	wantCode(t, err, term.CodeDraft)
	if r.data().Scopes[sv.ID].Lease != sv.Lease {
		t.Fatal("detach --notify mutated registry despite draft")
	}

	r.b.State[p.ID].Input = ""
	kept, err := r.tasks.Detach(DetachOpts{Target: "backend", Keep: true, Notify: true})
	if err != nil || kept.State != "unbound" || kept.Dropped {
		t.Fatalf("detach --keep got %+v err=%v", kept, err)
	}
	if r.b.Has("close", p.ID) || r.data().Created[p.ID] == nil {
		t.Fatal("detach closed pane or deleted CreatedPane")
	}
	if texts := r.b.Texts(p.ID); len(texts) != 1 || !strings.Contains(texts[0], "Detached from scope backend") {
		t.Fatalf("detach --notify text: %v", texts)
	}
	// Dropping an already-unbound scope without --keep marks Dropped=true; --keep on unbound exits 2.
	_, err = r.tasks.Detach(DetachOpts{Target: "backend", Keep: true})
	wantCode(t, err, term.CodeUsage)
	dropped, err := r.tasks.Detach(DetachOpts{Target: "backend"})
	if err != nil || dropped.State != "dropped" || !dropped.Dropped {
		t.Fatalf("detach unbound got %+v err=%v", dropped, err)
	}
}

func TestDetachLiveScopeDoesNotFireOrphanEscalation(t *testing.T) {
	r := newRig(t)
	sv, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "backend", SubDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	child := r.mustNew(t, r.as(r.other), NewOpts{Title: "sub"})
	if _, err := r.tasks.Detach(DetachOpts{Target: sv.ID}); err != nil {
		t.Fatal(err)
	}
	d := r.data()
	if d.Leases[sv.Lease].State != "ended" {
		t.Fatalf("expected ended lease after detach, got %s", d.Leases[sv.Lease].State)
	}
	if r.tasks.escalations(d, r.owner.ID) {
		t.Fatalf("detach must not trigger orphan escalation, notices=%+v", d.Notices)
	}
	_ = child
}

func TestRebindCrashedScopeCreatesFreshLeaseAndKeepsOrphanAlertAccurate(t *testing.T) {
	r := newRig(t)
	p4 := termtest.MakePane(4, "p4", "", "/w/p4", "claude")
	p9 := termtest.MakePane(90, "p9", "", "/w/p9", "claude")
	r.b.Panes = append(r.b.Panes, p4, p9)
	r.b.State[p4.ID] = &termtest.Screen{Log: []string{"ok"}}
	r.b.State[p9.ID] = &termtest.Screen{Log: []string{"ok"}}

	sv1, _, err := r.tasks.Attach(AttachOpts{Target: p4.ID, Name: "backend", SubDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	child := r.mustNew(t, r.as(p4), NewOpts{Title: "sub"})

	// p4 crashes (shell left open).
	p4.Process = "zsh"
	if err := r.tasks.Reconcile(); err != nil {
		t.Fatal(err)
	}
	d := r.data()
	if d.Leases[sv1.Lease].State != "exited" || d.Scopes[sv1.ID].Lease != "" {
		t.Fatalf("reconcile did not mark crashed scope exited: lease=%+v scope=%+v", d.Leases[sv1.Lease], d.Scopes[sv1.ID])
	}
	if svShow, err := r.tasks.ScopeShow("backend"); err != nil || svShow.State != "exited" {
		t.Fatalf("ScopeShow after crash: %+v err=%v", svShow, err)
	}
	var exitedNotice *Notice
	for _, n := range d.Notices {
		if n.Kind == "exited" {
			exitedNotice = n
		}
	}
	if exitedNotice == nil || !strings.Contains(exitedNotice.Text, "rebind with harness-ops attach <new-pane> --name backend") {
		t.Fatalf("missing exited notice: %+v", exitedNotice)
	}

	// Rebind backend to p9 -> fresh Lease l2; dead l1 still triggers orphan escalation for child.
	sv2, _, err := r.tasks.Attach(AttachOpts{Target: p9.ID, Name: "backend", SubDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if sv2.ID != sv1.ID || sv2.Lease == sv1.Lease {
		t.Fatalf("expected same scope ID and fresh lease, got sv1=%+v sv2=%+v", sv1, sv2)
	}
	if err := r.tasks.escalate(r.owner.ID); err != nil {
		t.Fatal(err)
	}
	d = r.data()
	var esc *Notice
	for _, n := range d.Notices {
		if n.Kind == "escalate" {
			esc = n
		}
	}
	if esc == nil || !strings.Contains(esc.Text, child.ID) || esc.Lease != sv1.Lease {
		t.Fatalf("expected orphan escalation anchored to dead lease %s naming %s, got %+v", sv1.Lease, child.ID, esc)
	}
}

func TestReportUpdatesScopeProgressResetsStallAndReplacesUnclaimedNotice(t *testing.T) {
	r := newRig(t)
	sv, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "backend", Stall: 60, HasStall: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.reg.Update(func(d *Data) error {
		l := d.Leases[sv.Lease]
		l.IdleSince = 100
		l.Stall = &StallMark{At: 200}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Hold owner input so notices queue rather than flush immediately, then verify dedup.
	r.b.State[r.owner.ID].Input = "typing"
	res, err := r.as(r.other).Report("first update", true)
	if err != nil || !res.Notified || res.Scope.Progress == nil || res.Scope.Progress.Text != "first update" {
		t.Fatalf("Report 1: res=%+v err=%v", res, err)
	}
	r.clock.T += 5
	res, err = r.as(r.other).Report("second update", true)
	if err != nil {
		t.Fatal(err)
	}
	d := r.data()
	l := d.Leases[sv.Lease]
	if l.IdleSince != 0 || l.Stall != nil || l.Progress.Text != "second update" {
		t.Fatalf("report did not reset idle/stall or update progress: %+v", l)
	}
	progCount := 0
	for _, n := range d.Notices {
		if n.Kind == "progress" {
			progCount++
			if !strings.Contains(n.Text, "PROGRESS scope backend ("+sv.ID+"): second update") {
				t.Fatalf("unexpected progress notice: %+v", n)
			}
		}
	}
	if progCount != 1 {
		t.Fatalf("expected 1 deduped progress notice, got %d", progCount)
	}

	// Dead lead pane records locally without error and marks OwnerGone.
	dropPane(r.b, r.owner.ID)
	resGone, err := r.as(r.other).Report("owner left", true)
	if err != nil || !resGone.OwnerGone || resGone.Notified {
		t.Fatalf("Report with gone owner: res=%+v err=%v", resGone, err)
	}

	// Pane running a Job is refused (exit 2).
	x := r.mustNew(t, nil, NewOpts{Title: "busy"})
	_, err = r.as(r.pane(x)).Report("wrong cmd", false)
	wantCode(t, err, term.CodeUsage)
}

func TestLeafTaskCannotAttach(t *testing.T) {
	r := newRig(t)
	leaf := r.mustNew(t, nil, NewOpts{Title: "leaf"})
	p := termtest.MakePane(2, "p2", "", "/w/p2", "claude")
	r.b.Panes = append(r.b.Panes, p)
	r.b.State[p.ID] = &termtest.Screen{Log: []string{"ok"}}
	_, _, err := r.as(r.pane(leaf)).Attach(AttachOpts{Target: p.ID, Name: "sneak"})
	wantCode(t, err, term.CodeGuard)
}

func TestLeafTaskCannotSpawn(t *testing.T) {
	r := newRig(t)
	leaf := r.mustNew(t, nil, NewOpts{Title: "leaf"})
	before := len(r.b.Spawned)
	_, err := r.as(r.pane(leaf)).Spawn("generic", "tab", "/w", SpawnOpts{Command: "c", Timeout: 60})
	wantCode(t, err, term.CodeGuard)
	if len(r.b.Spawned) != before {
		t.Fatal("leaf spawn opened a pane before failing")
	}

	lead := r.mustNew(t, nil, NewOpts{Title: "lead", SubDepth: 1})
	p, err := r.as(r.pane(lead)).Spawn("generic", "tab", "/w", SpawnOpts{Command: "c", Label: "helper", Timeout: 60})
	if err != nil || p == nil {
		t.Fatalf("lead spawn failed: p=%v err=%v", p, err)
	}
	// Spawned pane itself is a leaf (SubDepth 0) and cannot spawn or start root tasks.
	_, err = r.as(p).Spawn("generic", "tab", "/w", SpawnOpts{Command: "c", Timeout: 60})
	wantCode(t, err, term.CodeGuard)
}

func TestAttachedScopesConsumeZeroTaskAndTreeMaxSlots(t *testing.T) {
	r := newRig(t)
	sv, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "main-lead", SubDepth: 2, TreeMax: 2})
	if err != nil {
		t.Fatal(err)
	}
	// Attach 9 more scopes (10 total) under root: 0 task slots, 0 tree_max slots consumed.
	for i := 0; i < 9; i++ {
		p := termtest.MakePane(20+i, "", "", "/w", "claude")
		r.b.Panes = append(r.b.Panes, p)
		r.b.State[p.ID] = &termtest.Screen{Log: []string{"ok"}}
		if _, _, err := r.tasks.Attach(AttachOpts{Target: p.ID}); err != nil {
			t.Fatalf("attach %d failed: %v", i, err)
		}
	}
	// Child Jobs under main-lead inherit Root, Depth+1, SubDepth-1, TreeMax=2.
	j1 := r.mustNew(t, r.as(r.other), NewOpts{Title: "j1", SubDepth: 1})
	j2 := r.mustNew(t, r.as(r.other), NewOpts{Title: "j2"})
	if j1.Depth != 2 || j1.SubDepth != 1 || j1.TreeMax != 2 || j1.Root != sv.ID {
		t.Fatalf("unexpected child job placement: %+v", j1)
	}
	// 3rd running job in tree (tree_max=2) is refused.
	_, err = r.new(t, r.as(r.other), NewOpts{Title: "j3"})
	wantCode(t, err, term.CodeGuard)
	_ = j2
}

func TestKeyAllowedOnAttachedScopeThroughOwnsLease(t *testing.T) {
	r := newRig(t)
	sv, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	r.other.Process = "omp"
	res, err := r.tasks.Keys("backend", []string{"shift+tab"}, false)
	if err != nil || res.Pane.ID != r.other.ID || len(res.Keys) != 1 || res.Keys[0] != "shift-tab" {
		t.Fatalf("Keys on scope: res=%+v err=%v", res, err)
	}
	entries, _, _ := r.tasks.journal().Read(Filter{Scope: sv.ID})
	if len(entries) < 2 || entries[len(entries)-1].Event != "key" || entries[len(entries)-1].Scope != sv.ID {
		t.Fatalf("key journal entry missing scope attribution: %+v", entries)
	}
	// Another caller is refused (exit 6).
	p2 := termtest.MakePane(2, "stranger", "", "/w", "omp")
	r.b.Panes = append(r.b.Panes, p2)
	_, err = r.as(p2).Keys("backend", []string{"enter"}, false)
	wantCode(t, err, term.CodeGuard)

	// Answer/deny by scope name works too.
	r.other.Process = "claude"
	dlg := &termtest.Dialog{Body: "ls", Options: []string{"Yes", "No"}, Selected: 1}
	r.b.State[r.other.ID].Dialog = dlg
	if _, _, err := r.tasks.Answer("backend", AnswerOpts{N: 1}); err != nil || dlg.Answer != 1 {
		t.Fatalf("Answer on scope err=%v answer=%d", err, dlg.Answer)
	}
}

func TestScopeStallWaitsForWorkingChildJob(t *testing.T) {
	r := newRig(t)
	r.other.Process = "omp"
	sv, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "backend", Stall: 120, HasStall: true, SubDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	sub := r.mustNew(t, r.as(r.other), NewOpts{Title: "worker"})
	if err := r.tasks.Watch(WatchOpts{Interval: 30, Rounds: 6}); err != nil {
		t.Fatal(err)
	}
	if d := r.data(); d.Leases[sv.Lease].Stall != nil || d.Leases[sv.Lease].IdleSince != 0 {
		t.Fatalf("scope stalled while child job %s worked: %+v", sub.ID, d.Leases[sv.Lease])
	}
	if err := r.reg.Update(func(d *Data) error {
		taskLease(d, sub.ID).Stall = &StallMark{At: r.clock.T}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.tasks.Watch(WatchOpts{Interval: 30, Rounds: 6}); err != nil {
		t.Fatal(err)
	}
	d := r.data()
	if d.Leases[sv.Lease].Stall == nil {
		t.Fatal("scope did not stall once child job stalled")
	}
	var stallMsg string
	for _, s := range r.b.Texts(r.owner.ID) {
		if strings.Contains(s, "scope backend ("+sv.ID+") looks stalled") {
			stallMsg = s
		}
	}
	if stallMsg == "" || !strings.Contains(stallMsg, sub.ID) {
		t.Fatalf("expected scope stall notice naming %s, got %q", sub.ID, r.b.Texts(r.owner.ID))
	}
}

func TestTaskLsTreeRemainsJobOnlyAndPreservesTaskNodeJSON(t *testing.T) {
	r := newRig(t)
	sv, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "backend", SubDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	child := r.mustNew(t, r.as(r.other), NewOpts{Title: "top-under-scope", SubDepth: 1})
	grand := r.mustNew(t, r.as(r.pane(child)), NewOpts{Title: "leaf-under-child"})

	nodes, err := r.tasks.Tree(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Task.ID != child.ID || len(nodes[0].Children) != 1 || nodes[0].Children[0].Task.ID != grand.ID {
		t.Fatalf("unexpected TaskNode tree: %+v", nodes)
	}
	data, err := json.Marshal(nodes)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0]["id"] != child.ID || decoded[0]["root"] != sv.ID {
		t.Fatalf("unexpected decoded TaskNode JSON: %s", data)
	}
	if _, hasKind := decoded[0]["kind"]; hasKind {
		t.Fatalf("TaskNode JSON must not have kind field: %s", data)
	}
	if _, hasScope := decoded[0]["scope"]; hasScope {
		t.Fatalf("TaskNode JSON must not have scope field: %s", data)
	}
}

func TestWatcherRunsSlow30sIdleCadenceForActiveScopeWithZeroDiskWrites(t *testing.T) {
	r := newRig(t)
	r.other.Process = "claude"
	sv, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	before := registryFile(t, r)
	start := r.clock.T
	rounds := 0
	// Run 3 idle rounds (round 1 -> sleeps 30s, round 2 -> sleeps 30s, round 3 -> stops via Restart).
	err = r.tasks.Watch(WatchOpts{Restart: func() bool {
		rounds++
		return rounds > 3
	}})
	if err != nil && err != ErrRestart {
		t.Fatalf("Watch err=%v", err)
	}
	elapsed := r.clock.T - start
	// 3 rounds means 3 sleeps of 30s (or at least 2*30s before round 4's Restart check).
	if elapsed < 2*IdleWatchInterval {
		t.Fatalf("expected 30s slow-idle sleeps, elapsed=%gs (rounds=%d)", elapsed, rounds)
	}
	if r.b.StatusCalls != 3 {
		t.Fatalf("expected 1 status call per round and 0 between-round polls during slow-idle, got %d", r.b.StatusCalls)
	}
	if !os.SameFile(before, registryFile(t, r)) {
		t.Fatal("slow-idle scope rounds rewrote registry.json")
	}

	// Dialog on scope snaps back to 3s fast cadence with 1s status polls.
	r.b.StatusCalls = 0
	r.b.State[r.other.ID].Dialog = &termtest.Dialog{Body: "ls", Options: []string{"Yes", "No"}, Selected: 1}
	t0 := r.clock.T
	fastRounds := 0
	_ = r.tasks.Watch(WatchOpts{Restart: func() bool {
		fastRounds++
		return fastRounds > 1
	}})
	if dt := r.clock.T - t0; dt > WatchInterval+0.5 {
		t.Fatalf("expected fast 3s cadence on dialog, took %gs", dt)
	}
	if r.b.StatusCalls < 2 {
		t.Fatalf("expected between-round 1s status polls during fast cadence, got %d", r.b.StatusCalls)
	}
	if d := r.data(); d.Leases[sv.Lease].Dialog == nil {
		t.Fatal("dialog mark not recorded")
	}
}

func TestWatcherStopsImmediatelyWhenNoActiveScopeAndNoWork(t *testing.T) {
	// 1. Bare spawn stops without keeping an idle watcher.
	r := newRig(t)
	if _, err := r.tasks.Spawn("claude", "tab", "/w", SpawnOpts{Command: "claude", Label: "pair", Timeout: 60}); err != nil {
		t.Fatal(err)
	}
	rounds := 0
	if err := r.tasks.Watch(WatchOpts{Restart: func() bool { rounds++; return rounds > 10 }}); err != nil {
		t.Fatalf("bare spawn watcher did not stop: %v", err)
	}
	if rounds != 1 {
		t.Fatalf("bare spawn watcher ran %d rounds, want 1", rounds)
	}

	// 2. Settled job stops immediately.
	r2 := newRig(t)
	x := r2.mustNew(t, nil, NewOpts{Title: "done-job"})
	r2.done(t, x, "ok", "finished")
	r2.tasks.FlushPending()
	rounds = 0
	if err := r2.tasks.Watch(WatchOpts{Restart: func() bool { rounds++; return rounds > 10 }}); err != nil {
		t.Fatalf("settled job watcher did not stop: %v", err)
	}
	if rounds != 1 {
		t.Fatalf("settled job watcher ran %d rounds, want 1", rounds)
	}

	// 3. Detach ends active scope -> watcher stops on next round.
	r3 := newRig(t)
	r3.other.Process = "claude"
	if _, _, err := r3.tasks.Attach(AttachOpts{Target: r3.other.ID, Name: "svc"}); err != nil {
		t.Fatal(err)
	}
	rounds = 0
	err := r3.tasks.Watch(WatchOpts{Restart: func() bool {
		rounds++
		if rounds == 2 {
			r3.tasks.Detach(DetachOpts{Target: "svc"})
		}
		return rounds > 10
	}})
	if err != nil || rounds != 2 {
		t.Fatalf("expected watcher to stop on round 2 after detach, got rounds=%d err=%v", rounds, err)
	}

	// 4. Owner pane closed -> watcher stops immediately even with active scope.
	r4 := newRig(t)
	r4.other.Process = "claude"
	if _, _, err := r4.tasks.Attach(AttachOpts{Target: r4.other.ID, Name: "svc"}); err != nil {
		t.Fatal(err)
	}
	dropPane(r4.b, r4.owner.ID)
	rounds = 0
	if err := r4.tasks.Watch(WatchOpts{Restart: func() bool { rounds++; return rounds > 10 }}); err != nil || rounds != 1 {
		t.Fatalf("expected immediate stop when owner pane closed, got rounds=%d err=%v", rounds, err)
	}
}

func TestWakeWatcherInterruptsSlowIdleSleep(t *testing.T) {
	r := newRig(t)
	r.other.Process = "claude"
	if _, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "svc"}); err != nil {
		t.Fatal(err)
	}

	// Legacy watcher lock without "wake" token must NOT be signaled by WakeWatcher.
	lock, err := r.tasks.lockWatcher(r.owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	writeWatchLock(lock, false)
	if r.tasks.WakeWatcher(r.owner.ID) {
		t.Fatal("WakeWatcher signaled a watcher lock without wake token")
	}
	lock.Close()

	// Watcher with Wake channel writes "wake" token and WakeWatcher signals self (handled by wake chan).
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

	realTasks := &Tasks{Reg: r.reg, Backend: r.b, Sender: r.owner, Clock: RealClock{}, Factory: r.tasks.Factory}
	done := make(chan error, 1)
	go func() {
		done <- realTasks.Watch(WatchOpts{Rounds: 2, Wake: wake})
	}()
	// Wait until watcher holds lock with "wake", then wake it from 30s sleep and verify it exits quickly.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if realTasks.WakeWatcher(r.owner.ID) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Watch returned err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WakeWatcher did not interrupt 30s slow-idle sleep")
	}
}

func TestAdoptPaneOnAttachedScope(t *testing.T) {
	r := newRig(t)
	lead2 := termtest.MakePane(2, "lead2", "", "~/w/o", "omp")
	w2 := termtest.MakePane(3, "w2", "", "~/w/x", "")
	w3 := termtest.MakePane(4, "w3", "", "~/w/x", "")
	r.b.Panes = append(r.b.Panes, lead2, w2, w3)
	r.b.State[lead2.ID] = &termtest.Screen{Log: []string{"some earlier output"}}
	r.b.State[w2.ID] = &termtest.Screen{Log: []string{"some earlier output"}}
	r.b.State[w3.ID] = &termtest.Screen{Log: []string{"some earlier output"}}

	// Own active Scope -> Adopt succeeds, unbinds scope cleanly, leaves d.Created empty.
	sv1, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "own-scope"})
	if err != nil {
		t.Fatal(err)
	}
	ad, err := r.tasks.Adopt(r.other.ID, false)
	if err != nil {
		t.Fatalf("Adopt own active Scope failed (B1): %v", err)
	}
	d, _ := r.reg.Read()
	if d.Scopes[sv1.ID].Lease != "" {
		t.Fatalf("expected Scope %s Lease cleared after Adopt, got %q", sv1.ID, d.Scopes[sv1.ID].Lease)
	}
	if d.Leases[sv1.Lease].State != "ended" {
		t.Fatalf("expected old Scope lease ended, got %q", d.Leases[sv1.Lease].State)
	}
	if d.Created[r.other.ID] != nil {
		t.Fatalf("adopted pane must not gain CreatedPane: %+v", d.Created[r.other.ID])
	}
	_ = ad

	// Live other lead's Scope -> refused without --force (exit 6), succeeds with --force + "adopt" notice.
	sv2, _, err := r.as(lead2).Attach(AttachOpts{Target: w2.ID, Name: "other-live-scope"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.tasks.Adopt(w2.ID, false); !isCode(err, term.CodeGuard) {
		t.Fatalf("expected exit 6 without --force on live other lead's Scope, got %v", err)
	}
	if _, err := r.tasks.Adopt(w2.ID, true); err != nil {
		t.Fatalf("Adopt live other lead's Scope with --force failed (B1): %v", err)
	}
	d, _ = r.reg.Read()
	if d.Scopes[sv2.ID].Lease != "" {
		t.Fatalf("expected taken Scope %s Lease cleared", sv2.ID)
	}
	if !strings.Contains(strings.Join(r.b.Texts(lead2.ID), "\n"), "adopted by") {
		t.Fatalf("expected adopt notice delivered to live old owner lead2, got %v", r.b.Texts(lead2.ID))
	}

	// Dead other lead's Scope -> succeeds without --force.
	deadLead := termtest.MakePane(5, "dead", "", "~/w/o", "omp")
	r.b.Panes = append(r.b.Panes, deadLead)
	r.b.State[deadLead.ID] = &termtest.Screen{Log: []string{"some earlier output"}}
	if _, _, err := r.as(deadLead).Attach(AttachOpts{Target: w3.ID, Name: "dead-lead-scope"}); err != nil {
		t.Fatal(err)
	}
	// Remove deadLead from panes so ownerAlive returns false.
	r.b.Panes = r.b.Panes[:len(r.b.Panes)-1]
	if _, err := r.tasks.Adopt(w3.ID, false); err != nil {
		t.Fatalf("Adopt dead lead's Scope without --force failed: %v", err)
	}
}

func TestDefaultAttachAfterDetachDeduplicatesAcrossDroppedScopes(t *testing.T) {
	r := newRig(t)
	r.other.Label = "worker"
	w2 := termtest.MakePane(2, "worker", "", "~/w/x", "")
	r.b.Panes = append(r.b.Panes, w2)
	r.b.State[w2.ID] = &termtest.Screen{Log: []string{"some earlier output"}}

	sv1, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID})
	if err != nil || sv1.Name != "worker" {
		t.Fatalf("first attach: sv=%+v err=%v", sv1, err)
	}
	if _, err := r.tasks.Detach(DetachOpts{Target: "worker"}); err != nil {
		t.Fatal(err)
	}
	sv2, _, err := r.tasks.Attach(AttachOpts{Target: w2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if sv2.Name == "worker" {
		t.Fatalf("second default attach reused dropped name %q -> duplicate (Project, Name) (B2)", sv2.Name)
	}
	// Name-based operations must resolve cleanly without ambiguity.
	if got, err := r.tasks.ScopeShow("worker"); err != nil || got.ID != sv1.ID {
		t.Fatalf("ScopeShow(worker) after B2: got=%+v err=%v", got, err)
	}
	if got, err := r.tasks.ScopeShow(sv2.Name); err != nil || got.ID != sv2.ID {
		t.Fatalf("ScopeShow(%q): got=%+v err=%v", sv2.Name, got, err)
	}
	if _, err := r.tasks.Detach(DetachOpts{Target: sv2.Name}); err != nil {
		t.Fatalf("Detach(%q): %v", sv2.Name, err)
	}
	// Explicit --name worker rebinds dropped s1 deterministically (same ID).
	w3 := termtest.MakePane(3, "other3", "", "~/w/x", "")
	r.b.Panes = append(r.b.Panes, w3)
	r.b.State[w3.ID] = &termtest.Screen{Log: []string{"some earlier output"}}
	sv3, _, err := r.tasks.Attach(AttachOpts{Target: w3.ID, Name: "worker"})
	if err != nil || sv3.ID != sv1.ID || sv3.Dropped {
		t.Fatalf("explicit --name rebind failed: sv3=%+v want ID %s", sv3, sv1.ID)
	}
}

func TestAttachTakeoverRenamesSubtreeCapsAndAlignsBareProject(t *testing.T) {
	t.Setenv("TERMCTL_MAX_DEPTH", "3")
	r := newRig(t)
	leadA := termtest.MakePane(2, "leadA", "", "~/w/a", "omp")
	mid := termtest.MakePane(3, "mid", "", "~/w/a", "omp")
	leadB := termtest.MakePane(4, "leadB", "", "~/w/b", "omp")
	bare := termtest.MakePane(5, "bare", "", "~/w/a", "omp")
	r.b.Panes = append(r.b.Panes, leadA, mid, leadB, bare)
	for _, p := range []*term.Pane{leadA, mid, leadB, bare} {
		r.b.State[p.ID] = &termtest.Screen{Log: []string{"some earlier output"}}
	}

	// LeadA attaches mid (--sub-depth 2), mid starts child task with --sub-depth 1 (LEAD role title).
	if _, _, err := r.as(leadA).Attach(AttachOpts{Target: mid.ID, Name: "mid-scope", SubDepth: 2, TreeMax: 10}); err != nil {
		t.Fatal(err)
	}
	child, err := r.new(t, r.as(mid), NewOpts{Title: "child", Harness: "generic", Cwd: "~/w/a", Command: "fake", Timeout: 10, SubDepth: 1})
	if err != nil {
		t.Fatal(err)
	}

	// LeadB starts a root scope so LeadB is at depth 1; LeadB takes over mid-scope with --force and --sub-depth 1 --tree-max 4.
	bRoot := termtest.MakePane(6, "broot", "", "~/w/b", "omp")
	r.b.Panes = append(r.b.Panes, bRoot)
	r.b.State[bRoot.ID] = &termtest.Screen{Log: []string{"some earlier output"}}
	parentTask, err := r.new(t, r.as(bRoot), NewOpts{Title: "bparent", Harness: "generic", Cwd: "~/w/b", Command: "fake", Timeout: 10, SubDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	leadB2 := paneByID(r.b, parentTask.Pane)

	r.b.Events = nil
	sv, _, err := r.as(leadB2).Attach(AttachOpts{
		Target: mid.ID, Name: "mid-scope", SubDepth: 1, HasSubDepth: true, TreeMax: 4, HasTreeMax: true, Force: true,
	})
	if err != nil {
		t.Fatalf("Attach takeover failed: %v", err)
	}
	// I1(b): explicit narrowed caps applied to Case 2 takeover.
	if sv.SubDepth != 1 || sv.TreeMax != 4 {
		t.Fatalf("expected narrowed caps SubDepth=1 TreeMax=4, got %+v (I1b)", sv)
	}
	// I1(a): child task title was renamed after depth shift (LEAD -> WORKER when depth hits 3 = maxDepth).
	renamed := false
	for _, ev := range r.b.Events {
		if ev.Kind == "title" && ev.Pane == child.Pane {
			renamed = true
			break
		}
	}
	if !renamed {
		t.Fatalf("expected descendant pane %s title rename on Attach takeover (I1a), events=%+v", child.Pane, r.b.Events)
	}

	// I1(c): upgrading a bare spawn lease aligns l.Project with Scope.Project (callerProject).
	pBare, err := r.as(leadA).Spawn("generic", "tab", "~/w/a", SpawnOpts{Command: "fake", Label: "sp"})
	if err != nil {
		t.Fatal(err)
	}
	svBare, _, err := r.as(leadB).Attach(AttachOpts{Target: pBare.ID, Name: "rehomed", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := r.reg.Read()
	lBare := d.Leases[svBare.Lease]
	if filepath.Clean(lBare.Project) != filepath.Clean(svBare.Project) {
		t.Fatalf("expected l.Project=%q to match s.Project=%q on bare-spawn upgrade (I1c)", lBare.Project, svBare.Project)
	}
}

func paneByID(b *termtest.Fake, id string) *term.Pane {
	for _, p := range b.Panes {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func TestAdoptTaskReopenChecksLiveScopeOwner(t *testing.T) {
	r := newRig(t)
	leadB := termtest.MakePane(2, "leadB", "", "~/w/b", "omp")
	r.b.Panes = append(r.b.Panes, leadB)
	r.b.State[leadB.ID] = &termtest.Screen{Log: []string{"some earlier output"}}

	x, err := r.new(t, r.tasks, NewOpts{Title: "crash", Harness: "generic", Cwd: "~/w/x", Command: "fake", Timeout: 10})
	if err != nil {
		t.Fatal(err)
	}
	paneByID(r.b, x.Pane).Process = "zsh"
	if err := r.tasks.Reconcile(); err != nil {
		t.Fatal(err)
	}
	paneByID(r.b, x.Pane).Process = "claude"
	svB, _, err := r.as(leadB).Attach(AttachOpts{Target: x.Pane, Name: "b-scope"})
	if err != nil {
		t.Fatal(err)
	}

	// Without --force, reopening tID onto a pane now held by live LeadB's active Scope must fail exit 6 (I2).
	if _, err := r.tasks.Adopt(x.ID, false); !isCode(err, term.CodeGuard) {
		t.Fatalf("expected exit 6 adopting exited task over live Scope without --force (I2), got %v", err)
	}
	// With --force, succeeds and sends "adopt" notice to live LeadB.
	if _, err := r.tasks.Adopt(x.ID, true); err != nil {
		t.Fatalf("expected Adopt --force to succeed: %v", err)
	}
	d, _ := r.reg.Read()
	if d.Scopes[svB.ID].Lease != "" {
		t.Fatalf("expected LeadB scope lease cleared after reopen --force")
	}
	if !strings.Contains(strings.Join(r.b.Texts(leadB.ID), "\n"), "adopted") {
		t.Fatalf("expected adopt notice sent to live LeadB, got %v", r.b.Texts(leadB.ID))
	}
}

func TestOrphanEscalationNamesAllRunningSiblingJobs(t *testing.T) {
	r := newRig(t)
	sv, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "par", SubDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	t1, err := r.new(t, r.as(r.other), NewOpts{Title: "c1", Harness: "generic", Cwd: "~/w/x", Command: "fake", Timeout: 10, SubDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	t2, err := r.new(t, r.as(r.other), NewOpts{Title: "c2", Harness: "generic", Cwd: "~/w/x", Command: "fake", Timeout: 10})
	if err != nil {
		t.Fatal(err)
	}
	t3, err := r.new(t, r.as(paneByID(r.b, t1.Pane)), NewOpts{Title: "c3-grandchild", Harness: "generic", Cwd: "~/w/x", Command: "fake", Timeout: 10})
	if err != nil {
		t.Fatal(err)
	}
	// Kill scope pane -> Reconcile marks Scope lost -> run watcher 1 round for escalations.
	r.b.Panes = removePane(r.b.Panes, r.other.ID)
	if err := r.tasks.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if err := r.tasks.Watch(WatchOpts{Interval: 1, Rounds: 1}); err != nil {
		t.Fatal(err)
	}
	texts := strings.Join(r.b.Texts(r.owner.ID), "\n")
	if !strings.Contains(texts, t1.ID) || !strings.Contains(texts, t2.ID) || !strings.Contains(texts, t3.ID) ||
		!strings.Contains(texts, "task close --tree --force "+t1.ID) ||
		!strings.Contains(texts, "task close --tree --force "+t2.ID) ||
		strings.Contains(texts, "task close --tree --force "+t3.ID) {
		t.Fatalf("orphan escalation mismatch (I4); scope=%s texts:\n%s", sv.ID, texts)
	}
}

func removePane(panes []*term.Pane, id string) []*term.Pane {
	var out []*term.Pane
	for _, p := range panes {
		if p.ID != id {
			out = append(out, p)
		}
	}
	return out
}

func TestReportNotifyWakesSlowIdleWatcherAndGuardOwner(t *testing.T) {
	r := newRig(t)
	r.other.Process = "claude"
	if _, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "svc"}); err != nil {
		t.Fatal(err)
	}
	// Lead has a draft so Report(..., true) leaves its progress notice pending ("retry").
	r.b.State[r.owner.ID].Input = "drafting"

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

	realTasks := &Tasks{Reg: r.reg, Backend: r.b, Sender: r.owner, Clock: RealClock{}, Factory: r.tasks.Factory}
	done := make(chan error, 1)
	go func() {
		done <- realTasks.Watch(WatchOpts{Rounds: 2, Wake: wake})
	}()
	// Wait until watcher lock carries "wake".
	lockPath, _ := realTasks.watchPaths(r.owner.ID)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(lockPath)
		if strings.Contains(string(data), "wake") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Report --notify from worker while lead holds draft -> must wake watcher from 30s sleep immediately (I5).
	workerTasks := &Tasks{Reg: r.reg, Backend: r.b, Sender: r.other, Clock: RealClock{}, Factory: r.tasks.Factory}
	res, err := workerTasks.Report("milestone ready", true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Notified {
		t.Fatalf("expected Notified=true, got %+v", res)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Watch err=%v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Report(..., true) did not wake lead's slow-idle watcher (I5)")
	}
}

func TestReconcileScopeExitedLogsWorkerFromAndOwnerTo(t *testing.T) {
	r := newRig(t)
	third := termtest.MakePane(3, "third", "", "~/w/o", "omp")
	r.b.Panes = append(r.b.Panes, third)
	r.b.State[third.ID] = &termtest.Screen{Log: []string{"some earlier output"}}

	sv, _, err := r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "crash-log"})
	if err != nil {
		t.Fatal(err)
	}
	r.b.Panes = removePane(r.b.Panes, r.other.ID)
	if err := r.as(third).Reconcile(); err != nil {
		t.Fatal(err)
	}
	entries, _, err := r.tasks.journal().Read(Filter{Scope: sv.ID})
	if err != nil {
		t.Fatal(err)
	}
	var lostEntry *Entry
	for i := range entries {
		if entries[i].Event == "scope.lost" {
			lostEntry = &entries[i]
		}
	}
	if lostEntry == nil {
		t.Fatalf("missing scope.lost journal entry: %+v", entries)
	}
	if lostEntry.From != r.other.ID || lostEntry.To != r.owner.ID {
		t.Fatalf("expected scope.lost From=%s To=%s, got From=%q To=%q (I6)", r.other.ID, r.owner.ID, lostEntry.From, lostEntry.To)
	}
}

func TestFindScopeErrorHasNoDuplicateText(t *testing.T) {
	r := newRig(t)
	_, _, _ = r.tasks.Attach(AttachOpts{Target: r.other.ID, Name: "s123456-a"})
	_, err := r.tasks.ScopeShow("nonexistent")
	if err == nil {
		t.Fatal("expected error")
	}
	d, _ := r.reg.Read()
	d.Scopes["s111111"] = &Scope{ID: "s111111", Name: "dup-x", Project: ProjectOf(r.owner)}
	d.Scopes["s111222"] = &Scope{ID: "s111222", Name: "dup-x", Project: ProjectOf(r.owner)}
	_, err = r.tasks.findScope(d, "s111")
	if err == nil || !errors.Is(err, errSeveralScopes) {
		t.Fatalf("expected errSeveralScopes, got %v", err)
	}
	if strings.Contains(err.Error(), ": several scopes match") {
		t.Fatalf("duplicated error text in findScope (N3): %q", err.Error())
	}
}

type failSendBackend struct{ *termtest.Fake }

func (f failSendBackend) SendText(*term.Pane, string) error {
	return term.BackendErr("send failed after commit")
}

func TestAttachNotifySendFailureStillLogsCommittedAttach(t *testing.T) {
	r := newRig(t)
	fb := failSendBackend{r.b}
	ts := &Tasks{Reg: r.reg, Backend: fb, Sender: r.owner, Clock: r.clock, Factory: func(string, map[string]string) (term.Backend, error) { return fb, nil }}
	sv, _, err := ts.Attach(AttachOpts{Target: r.other.ID, Name: "n2-scope", Notify: true})
	if err == nil || sv == nil {
		t.Fatalf("expected committed sv alongside send error, got sv=%+v err=%v", sv, err)
	}
	entries, _, jerr := ts.journal().Read(Filter{Scope: sv.ID})
	if jerr != nil {
		t.Fatal(jerr)
	}
	found := false
	for _, e := range entries {
		if e.Event == "scope.attach" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("committed Attach missing scope.attach journal entry on notify send failure (N2): %+v", entries)
	}
}
