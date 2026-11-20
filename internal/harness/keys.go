package harness

import (
	"errors"
	"strings"

	"github.com/samhvw8/termctl/internal/term"
)

// Pressed is what Keys did: the pane, and the keys pressed on it by their canonical names.
type Pressed struct {
	Pane *term.Pane
	Keys []string
}

// Keys presses keys on a pane the caller started (spawn or task new), or on the worker of a task it owns:
// a menu, a picker, a mode switch (shift-tab), anything a harness reads as keys. It is refused on your own
// pane (exit 2), on a pane you did not start (exit 6: termctl send-key is the raw tool), while a permission
// dialog is on screen (exit 7: answer and deny check what they choose), and, unless force, while a
// calibrated harness shows text on its input line (exit 4). Every key is checked against the backend
// before the first is pressed.
func (t *Tasks) Keys(target string, keys []string, force bool) (*Pressed, error) {
	if len(keys) == 0 {
		return nil, term.UsageErr("key needs at least one key")
	}
	for _, k := range keys { // a misspelt key is a usage error before any pane is looked up
		if _, err := term.NormalizeKey(k); err != nil {
			return nil, err
		}
	}
	if t.Sender == nil {
		return nil, term.UsageErr("cannot tell which pane this runs in; key acts only on panes you started with spawn or task new")
	}
	b, p, x, xl, err := t.keyTarget(target)
	if err != nil {
		return nil, err
	}
	names, err := term.CheckKeys(b, keys)
	if err != nil {
		return nil, err
	}
	lines, err := b.Capture(p, screenLines(p))
	if err != nil {
		return nil, err
	}
	prof := ForProcess(p.Process)
	if prof.ReadDialog != nil {
		if d := readPaneDialog(b, p, prof, lines); d != nil {
			return nil, term.Errf(term.CodeDialog, "a permission dialog (%s) waits on %s: %q. Use harness-ops answer or deny, which check what they choose; keys could pick a choice unread", d.Fingerprint, p.Address(), d.Question)
		}
	}
	if prof.Calibrated && !force {
		if d := prof.ReadInput(lines); d != nil && *d != "" {
			return nil, term.Errf(term.CodeDraft, "%s has text on its input line: %q; keys would act on it. Use --force to press them anyway", p.Address(), term.Cut(*d, 80))
		}
	}
	ops := t.ops(b, t.Sender)
	if x != nil {
		ops = t.taskOps(b, t.Sender, x, xl)
	}
	e := ops.entry("key", p)
	e.Text = strings.Join(names, " ")
	if force {
		e.Status = "forced"
	}
	var sendErr error
	sent := 0
	for _, k := range names {
		if sendErr = b.SendKey(p, k); sendErr != nil {
			break
		}
		sent++
	}
	if sendErr != nil {
		e.Error = errText(sendErr)
	}
	ops.Journal.Record(e)
	return &Pressed{Pane: p, Keys: names[:sent]}, sendErr
}

// keyTarget is the worker of a task the caller owns, or a pane the caller started with spawn or task new.
// Unlike answerTarget, a pane that is not the caller's is a safety refusal (exit 6) that names the raw tool.
func (t *Tasks) keyTarget(target string) (term.Backend, *term.Pane, *Job, *Lease, error) {
	if err := t.Reconcile(); err != nil {
		return nil, nil, nil, nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	x, xl, err := t.find(d, target)
	if errors.Is(err, errSeveral) {
		return nil, nil, nil, nil, err
	}
	if err == nil {
		if xl.Owner != t.Sender.ID || !sameServer(xl.OwnerRef, t.callerRef()) {
			return nil, nil, nil, nil, notYours("the worker of task " + x.ID)
		}
		if x.Closed {
			return nil, nil, nil, nil, term.UsageErr("task %s is closed", x.ID)
		}
		if !open(x.Status) || xl.Pane == "" {
			return nil, nil, nil, nil, term.UsageErr("task %s is %s: there is no harness to press keys on", x.ID, x.Status)
		}
		b, p, err := t.paneByID(xl.Pane, xl.Ref)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if p == nil {
			return nil, nil, nil, nil, term.UsageErr("task %s: its pane is gone", x.ID)
		}
		return b, p, x, xl, nil
	}
	b, p, err := t.paneTarget(d, target)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if p.IsSelf || t.isSelf(p.ID) {
		return nil, nil, nil, nil, term.TargetErr("refusing to press keys on your own pane")
	}
	l := t.ownsPane(d, p.ID, b.ServerRef())
	if l == nil {
		return nil, nil, nil, nil, notYours(p.Address())
	}
	x = nil
	if j := d.jobOf(l); j != nil && open(j.Status) && !j.Closed {
		x = j
	}
	return b, p, x, l, nil
}

func notYours(what string) error {
	return term.Errf(term.CodeGuard, "%s is not a pane you started with spawn or task new; harness-ops key acts only on those. termctl send-key is the raw tool for any pane", what)
}
