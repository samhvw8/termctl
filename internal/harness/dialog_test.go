package harness

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/term"
	"github.com/samhvw8/termctl/internal/termtest"
)

// dialogTask starts a task whose worker runs claude and shows a permission dialog.
func dialogTask(t *testing.T, r *rig) (*Task, *termtest.Dialog) {
	t.Helper()
	x := r.mustNew(t, nil, NewOpts{})
	r.pane(x).Process = "claude"
	dlg := &termtest.Dialog{Body: "touch x", Options: []string{"Yes", "Yes, and don't ask again for touch commands", "No"}, Selected: 1}
	r.b.State[x.Pane].Dialog = dlg
	return x, dlg
}

func readFP(t *testing.T, r *rig, x *Task) string {
	t.Helper()
	d, err := r.tasks.ops(r.b, r.owner).ReadDialog(r.pane(x))
	if err != nil || d == nil {
		t.Fatal("no dialog", err)
	}
	return d.Fingerprint
}

func keys(r *rig, x *Task) []string {
	var out []string
	for _, k := range r.b.Typed(x.Pane) {
		if k == "key:up" || k == "key:down" || k == "key:enter" || k == "key:escape" {
			out = append(out, k)
		}
	}
	return out
}

func TestAnswerMovesTheSelectionThenPressesEnter(t *testing.T) {
	r := newRig(t)
	x, dlg := dialogTask(t, r)
	before := len(keys(r, x)) // the brief's Enter
	fp := readFP(t, r, x)
	r.tasks.apply([]Signal{{Task: x.ID, Kind: "dialog", Dialog: &Dialog{Fingerprint: fp}}}, false)
	res, _, err := r.tasks.Answer(x.ID, AnswerOpts{N: 3, Expect: fp})
	if err != nil || res.Chosen.N != 3 || res.Chosen.Text != "No" || dlg.Answer != 3 {
		t.Fatal(err, res, dlg.Answer)
	}
	if got := strings.Join(keys(r, x)[before:], " "); got != "key:down key:down key:enter" {
		t.Fatal(got)
	}
	if g, _ := r.tasks.Get(x.ID); g.Dialog != nil {
		t.Fatal("an answered dialog stays marked")
	}
}

func TestAnswerRefusals(t *testing.T) {
	r := newRig(t)
	x, dlg := dialogTask(t, r)
	fp := readFP(t, r, x)
	before := len(keys(r, x))
	_, _, err := r.tasks.Answer(x.ID, AnswerOpts{N: 1, Expect: "deadbeef"})
	wantCode(t, err, term.CodeDialog)
	_, _, err = r.tasks.Answer(x.ID, AnswerOpts{N: 2})
	wantCode(t, err, term.CodeGuard) // "don't ask again" needs --persist
	_, _, err = r.tasks.Answer(x.ID, AnswerOpts{N: 9})
	wantCode(t, err, term.CodeUsage)
	other := r.as(r.other) // not the owner
	_, _, err = other.Answer(x.ID, AnswerOpts{N: 1})
	wantCode(t, err, term.CodeUsage)
	if len(keys(r, x)) != before || dlg.Answer != 0 {
		t.Fatal("a refused answer pressed keys", keys(r, x)[before:])
	}
	if _, _, err := r.tasks.Answer(x.ID, AnswerOpts{N: 2, Persist: true, Expect: fp}); err != nil || dlg.Answer != 2 {
		t.Fatal(err, dlg.Answer)
	}
	_, _, err = r.tasks.Answer(x.ID, AnswerOpts{N: 1})
	wantCode(t, err, term.CodeDialog) // no dialog any more
}

func TestAnswerAPaneTarget(t *testing.T) {
	r := newRig(t)
	r.other.Process = "claude"
	r.b.State[r.other.ID].Dialog = &termtest.Dialog{Body: "rm x", Options: []string{"Yes", "No"}, Selected: 1}
	_, _, err := r.tasks.Answer(r.other.ID, AnswerOpts{N: 1})
	wantCode(t, err, term.CodeUsage) // not a pane this caller started
	r.reg.Update(func(d *Data) error {
		d.Panes[r.other.ID] = &PaneRecord{Harness: "claude", Owner: r.owner.ID}
		return nil
	})
	_, _, err = r.as(r.other).Answer(r.owner.ID, AnswerOpts{N: 1})
	wantCode(t, err, term.CodeUsage) // never a pane of someone else, nor your own
	if _, _, err := r.tasks.Answer(r.other.ID, AnswerOpts{N: 1}); err != nil || r.b.State[r.other.ID].Dialog.Answer != 1 {
		t.Fatal(err)
	}
}

func TestAnswerUncalibratedHarnessRefused(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	_, _, err := r.tasks.Answer(x.ID, AnswerOpts{N: 1})
	wantCode(t, err, term.CodeUsage)
}

func TestAnswerStuckDialogFails(t *testing.T) {
	r := newRig(t)
	x, dlg := dialogTask(t, r)
	dlg.Stuck = true
	_, _, err := r.tasks.Answer(x.ID, AnswerOpts{N: 1})
	wantCode(t, err, term.CodeBackend)
	if !strings.Contains(err.Error(), "still on") {
		t.Fatal(err)
	}
}

// On a real Claude Code session the first look after the arrow keys still showed the old selection.
func TestAnswerWaitsForTheSelectionToRedraw(t *testing.T) {
	r := newRig(t)
	x, dlg := dialogTask(t, r)
	dlg.Lag = 3
	if _, _, err := r.tasks.Answer(x.ID, AnswerOpts{N: 3}); err != nil || dlg.Answer != 3 {
		t.Fatal(err, dlg.Answer)
	}
}

func TestAnswerNeverPressesEnterOffTheChoice(t *testing.T) {
	r := newRig(t)
	x, dlg := dialogTask(t, r)
	dlg.NoArrows = true
	before := len(keys(r, x))
	_, _, err := r.tasks.Answer(x.ID, AnswerOpts{N: 3})
	wantCode(t, err, term.CodeBackend)
	for _, k := range keys(r, x)[before:] {
		if k == "key:enter" {
			t.Fatal("Enter pressed with the wrong choice selected")
		}
	}
	if dlg.Answer != 0 {
		t.Fatal(dlg.Answer)
	}
}

func TestDenyPicksNoElseEsc(t *testing.T) {
	r := newRig(t)
	x, dlg := dialogTask(t, r)
	res, _, err := r.tasks.Answer(x.ID, AnswerOpts{Deny: true})
	if err != nil || res.Escaped || res.Chosen.N != 3 || dlg.Answer != 3 {
		t.Fatal(err, res, dlg.Answer)
	}
	dlg2 := &termtest.Dialog{Body: "trust?", Options: []string{"Yes, proceed", "Exit"}, Selected: 1}
	r.b.State[x.Pane].Dialog = dlg2
	res, _, err = r.tasks.Answer(x.ID, AnswerOpts{Deny: true})
	if err != nil || !res.Escaped || dlg2.Answer != -1 {
		t.Fatal(err, res, dlg2.Answer)
	}
}

func TestWaitReturnsADialogOnce(t *testing.T) {
	r := newRig(t)
	x, dlg := dialogTask(t, r)
	w, err := r.tasks.Wait("", true, 10)
	if err != nil || w.Dialog == nil || w.Task.ID != x.ID || w.Dialog.Question != "Do you want to proceed?" || w.Task.Status != "running" {
		t.Fatal(w, err)
	}
	if w2, err := r.tasks.Wait(x.ID, false, 10); err == nil {
		t.Fatal("the same dialog came back", w2)
	}
	// Answered, then the same request again: a new dialog.
	r.tasks.Answer(x.ID, AnswerOpts{N: 1})
	if _, err := r.tasks.Wait(x.ID, false, 4); !isCode(err, term.CodeTimeout) {
		t.Fatal(err)
	}
	dlg.Answer, dlg.Selected = 0, 1
	if w, err := r.tasks.Wait(x.ID, false, 10); err != nil || w.Dialog == nil {
		t.Fatal(w, err)
	}
	// A finished task comes before a dialog.
	r.as(r.pane(x)).Done(x.ID, "ok", "done", "")
	if w, err := r.tasks.Wait("", true, 10); err != nil || w.Dialog != nil || w.Task.Status != "ready" {
		t.Fatal(w, err)
	}
}

func isCode(err error, code int) bool {
	var te *term.Error
	return errors.As(err, &te) && te.Code == code
}

func ownerNotes(r *rig) []string {
	var out []string
	for _, s := range r.b.Texts(r.owner.ID) {
		if strings.Contains(s, "DIALOG") {
			out = append(out, s)
		}
	}
	return out
}

func TestWatchNotifiesEachNewDialogOnce(t *testing.T) {
	r := newRig(t)
	x, dlg := dialogTask(t, r)
	fp := readFP(t, r, x)
	if err := r.tasks.Watch(WatchOpts{Rounds: 3}); err != nil {
		t.Fatal(err)
	}
	notes := ownerNotes(r)
	if len(notes) != 1 {
		t.Fatalf("want one notice, got %q", notes)
	}
	n := notes[0]
	for _, want := range []string{"[agent-ops from " + x.ID + " (generic) id=" + x.Pane, "DIALOG " + fp + ": Do you want to proceed?", "touch x",
		"2. Yes, and don't ask again for touch commands [persist]", "harness-ops task answer " + x.ID + " <n> --expect " + fp, "--persist",
		"harness-ops task deny " + x.ID} {
		if !strings.Contains(n, want) {
			t.Fatalf("notice lacks %q: %s", want, n)
		}
	}
	if g, _ := r.tasks.Get(x.ID); g.Dialog == nil || !g.Dialog.Notified || g.Notify != "none" {
		t.Fatal("the mark must record the notice, and leave the result notice alone", g.Dialog, g.Notify)
	}
	// Answered elsewhere, input line back, then asked again: told again.
	dlg.Answer = 1
	r.tasks.Watch(WatchOpts{Rounds: 1})
	dlg.Answer, dlg.Selected = 0, 1
	r.tasks.Watch(WatchOpts{Rounds: 2})
	if len(ownerNotes(r)) != 2 {
		t.Fatalf("%q", ownerNotes(r))
	}
}

func TestWatchHoldsANoticeForADraftAndDropsItWhenStale(t *testing.T) {
	r := newRig(t)
	_, dlg := dialogTask(t, r)
	r.b.State[r.owner.ID].Input = "owner typing"
	r.tasks.Watch(WatchOpts{Rounds: 1})
	if len(ownerNotes(r)) != 0 {
		t.Fatal("typed over a draft")
	}
	d, _ := r.reg.Read()
	if len(d.Pending) != 1 || d.Pending[0].Kind != "dialog" {
		t.Fatal(d.Pending)
	}
	dlg.Answer = 1 // answered before the owner was free
	r.tasks.Watch(WatchOpts{Rounds: 1})
	r.b.State[r.owner.ID].Input = ""
	r.tasks.FlushPending()
	d, _ = r.reg.Read()
	if len(ownerNotes(r)) != 0 || len(d.Pending) != 0 {
		t.Fatal("a stale dialog notice was delivered", ownerNotes(r), d.Pending)
	}
}

func TestWatchStopsWithNoOpenTasksAndRunsOncePerOwner(t *testing.T) {
	r := newRig(t)
	x, _ := dialogTask(t, r)
	lock, err := r.tasks.lockWatcher(r.owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.tasks.Watch(WatchOpts{Rounds: 1}); !errors.Is(err, ErrWatching) {
		t.Fatal(err)
	}
	if r.tasks.Watching(r.owner.ID) == 0 {
		t.Fatal("the held lock is a running watcher")
	}
	lock.Close()
	if r.tasks.Watching(r.owner.ID) != 0 {
		t.Fatal("no watcher runs")
	}
	r.tasks.Close(x.ID, true)
	if err := r.tasks.Watch(WatchOpts{}); err != nil { // Rounds 0: runs until no task is open
		t.Fatal(err)
	}
}

func TestBlockedIsAHintToLookAgain(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		r := newRig(t)
		x, dlg := dialogTask(t, r)
		dlg.HideFirst = 1
		if blocked {
			r.b.Natives[x.Pane] = "blocked"
		}
		sigs := screenSource{r.tasks}.Signals([]*Task{mustGet(t, r, x.ID)})
		got := len(sigs) == 1 && sigs[0].Kind == "dialog"
		if got != blocked {
			t.Fatalf("blocked=%v: %+v", blocked, sigs)
		}
	}
	// blocked alone is no dialog.
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	r.pane(x).Process = "claude"
	r.b.Natives[x.Pane] = "blocked"
	for _, s := range (screenSource{r.tasks}).Signals([]*Task{mustGet(t, r, x.ID)}) {
		if s.Kind == "dialog" {
			t.Fatal(s)
		}
	}
}

func mustGet(t *testing.T, r *rig, id string) *Task {
	t.Helper()
	x, err := r.tasks.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func TestOtherSignalKindsReachTheOwner(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	if err := r.tasks.apply([]Signal{{Task: x.ID, Kind: "alert", Text: "build finished"}}, true); err != nil {
		t.Fatal(err)
	}
	r.tasks.FlushPending()
	if !termtest.Contains(r.b.Texts(r.owner.ID), "ALERT: build finished") {
		t.Fatal(r.b.Texts(r.owner.ID))
	}
	if g, _ := r.tasks.Get(x.ID); g.Notify != "none" {
		t.Fatal(g.Notify)
	}
}

// vrule is Claude Code's full-width rule above a dialog.
var vrule = strings.Repeat("─", 60)

// claudeScreen draws a Claude Code dialog: the request lines, the question lines, the options (a "\n" in
// one wraps it, indented under its text), extra lines under the options, and the footer.
func claudeScreen(body []string, question []string, opts []string, extra []string) []string {
	out := []string{"some output", vrule, " Bash command", ""}
	for _, b := range body {
		out = append(out, "   "+b)
	}
	out = append(out, "")
	for _, q := range question {
		out = append(out, " "+q)
	}
	for i, o := range opts {
		mark := "  "
		if i == 0 {
			mark = "❯ "
		}
		lines := strings.Split(o, "\n")
		out = append(out, fmt.Sprintf(" %s%d. %s", mark, i+1, lines[0]))
		for _, l := range lines[1:] {
			out = append(out, "      "+l)
		}
	}
	out = append(out, extra...)
	return append(out, "", " Esc to cancel · Tab to amend")
}

// swapper wraps the fake to change the world on the n-th capture of a pane, or on Enter.
type swapper struct {
	*termtest.Fake
	pane      string
	captures  int
	onCapture func(n int)
	onEnter   func()
}

func (s *swapper) Capture(p *term.Pane, n int) ([]string, error) {
	if p.ID == s.pane {
		s.captures++
		defer func(n int) {
			if s.onCapture != nil {
				s.onCapture(n)
			}
		}(s.captures)
	}
	return s.Fake.Capture(p, n)
}

func (s *swapper) SendKey(p *term.Pane, key string) error {
	err := s.Fake.SendKey(p, key)
	if p.ID == s.pane && key == "enter" && s.onEnter != nil {
		s.onEnter()
	}
	return err
}

func swapTasks(r *rig, s *swapper) *Tasks {
	ts := r.as(r.owner)
	ts.Backend = s
	ts.Factory = func(string, map[string]string) (term.Backend, error) { return s, nil }
	return ts
}

// A choice is one-shot only when its whole text is known to be: wording not seen before, such as the
// plan dialog's "auto-accept edits", needs --persist (review F3).
func TestPersistUnlessKnownOneShot(t *testing.T) {
	lines := []string{"plan text", vrule, " Ready to code?", "", " Here is Claude's plan:", " step 1", "", " Would you like to proceed?", "",
		" ❯ 1. Yes, and auto-accept edits", "   2. Yes, clear context and auto-accept edits", "   3. Yes, accept edits", "   4. Yes, and manually approve edits", "   5. No, keep planning", "", " Esc to cancel"}
	d := claudeReadDialog(lines)
	if d == nil {
		t.Fatal("not read")
	}
	for _, n := range []int{1, 2, 3, 4} {
		if !d.Option(n).Persist {
			t.Errorf("option %d %q is not marked persist", n, d.Option(n).Text)
		}
	}
	if d.Option(5).Persist {
		t.Error("a No choice grants nothing")
	}
	for text, persist := range map[string]bool{"Yes": false, "Yes, run command": false, "Yes, allow access": false, "Yes, proceed": false,
		"No, and tell Claude what to do differently (esc)": false, "Yes, allow all edits during this session (shift+tab)": true, "Yes, sure": true} {
		d := claudeReadDialog(claudeScreen([]string{"x"}, []string{"Do you want to proceed?"}, []string{text, "No"}, nil))
		if d == nil || d.Option(1).Persist != persist {
			t.Errorf("%q: %+v", text, d)
		}
	}
}

// A watcher round that read the screen before an answer must not bring the answered dialog back, nor
// tell the owner of it (review F1).
func TestStaleReadDoesNotBringAnAnsweredDialogBack(t *testing.T) {
	r := newRig(t)
	x, dlg := dialogTask(t, r)
	fp := readFP(t, r, x)
	sigs := screenSource{r.tasks}.Signals([]*Task{mustGet(t, r, x.ID)}) // the watcher's capture
	if _, _, err := r.tasks.Answer(x.ID, AnswerOpts{N: 1, Expect: fp}); err != nil || dlg.Answer != 1 {
		t.Fatal(err)
	}
	r.tasks.apply(sigs, true) // the watcher's apply, after the answer
	r.tasks.FlushPending()
	g := mustGet(t, r, x.ID)
	if n := ownerNotes(r); len(n) != 0 || g.Dialog != nil {
		t.Fatalf("answered dialog came back: notes=%q mark=%+v", n, g.Dialog)
	}
}

// The worker asks the same request again at once, so the answer cannot tell it from a dialog that did
// not take the key: it says both, and the re-ask is announced (review F7).
func TestIdenticalReaskRightAfterEnterIsAnnounced(t *testing.T) {
	r := newRig(t)
	x, _ := dialogTask(t, r)
	s := &swapper{Fake: r.b, pane: x.Pane}
	ts := swapTasks(r, s)
	if err := ts.Watch(WatchOpts{Rounds: 1}); err != nil {
		t.Fatal(err)
	}
	s.onEnter = func() {
		r.b.State[x.Pane].Dialog = &termtest.Dialog{Body: "touch x", Options: []string{"Yes", "Yes, and don't ask again for touch commands", "No"}, Selected: 1}
	}
	_, _, err := ts.Answer(x.ID, AnswerOpts{N: 1})
	s.onEnter = nil
	if !isCode(err, term.CodeBackend) || !strings.Contains(err.Error(), "asked the same request again") {
		t.Fatal(err)
	}
	ts.Watch(WatchOpts{Rounds: 2})
	if n := len(ownerNotes(r)); n != 2 {
		t.Errorf("notices=%d, want 2: the re-ask is a new request", n)
	}
}

// Answered through the pane address, then asked again before any round saw the input line: the mark
// went with the answer, so the new ask is announced (review F7).
func TestPaneAddressAnswerThenReaskIsAnnounced(t *testing.T) {
	r := newRig(t)
	x, dlg := dialogTask(t, r)
	if err := r.tasks.Watch(WatchOpts{Rounds: 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.tasks.Answer(x.Pane, AnswerOpts{N: 1}); err != nil {
		t.Fatal("pane-address answer refused:", err)
	}
	dlg.Answer, dlg.Selected = 0, 1 // asked again before the watcher looked
	r.tasks.Watch(WatchOpts{Rounds: 2})
	if n := len(ownerNotes(r)); n != 2 {
		t.Errorf("re-asked dialog after a pane-address answer: %d notice(s), want 2", n)
	}
}
