package harness

import (
	"errors"
	"fmt"
	"strings"

	"github.com/samhvw8/termctl/internal/term"
)

const (
	answerLook = 3.0 // how long a dialog gets to leave the screen after Enter
	selectLook = 3.0 // how long a dialog gets to redraw its selection after the arrow keys
)

// ReadDialog reads the dialog on a pane. A harness with no calibrated dialog reader is refused (exit 2).
func (o *Ops) ReadDialog(p *term.Pane) (*Dialog, error) {
	prof := ForProcess(p.Process)
	if prof.ReadDialog == nil {
		return nil, term.UsageErr("cannot read dialogs on %s (%s): no calibrated dialog reader for that harness", p.Address(), HarnessName(p))
	}
	lines, err := o.Backend.Capture(p, screenLines(p))
	if err != nil {
		return nil, err
	}
	return prof.ReadDialog(lines), nil
}

// AnswerOpts are options of Answer. N is the choice; Deny picks the "No" choice instead.
type AnswerOpts struct {
	N       int
	Deny    bool
	Expect  string
	Persist bool
}

// Answered is what Answer did. Escaped: there was no "No" choice, so deny pressed Esc.
// Next is a different dialog that came up afterwards, if any.
type Answered struct {
	Dialog  *Dialog
	Chosen  *Option
	Escaped bool
	Next    *Dialog
}

func noDialog(p *term.Pane) error {
	return term.Errf(term.CodeDialog, "no dialog on %s", p.Address())
}

// Answer picks a choice of the dialog on a pane. It moves the selection with up and down, checks on screen
// that the selection reached the choice and the dialog is the same, and only then presses Enter: digit keys
// submit at once on Claude Code, with no chance to check. A choice that widens permission needs Persist.
func (o *Ops) Answer(p *term.Pane, opts AnswerOpts) (*Answered, error) {
	d, err := o.ReadDialog(p)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, noDialog(p)
	}
	if opts.Expect != "" && d.Fingerprint != opts.Expect {
		return nil, term.Errf(term.CodeDialog, "the dialog on %s is %s, not %s: %q; read it again before answering", p.Address(), d.Fingerprint, opts.Expect, d.Question)
	}
	res := &Answered{Dialog: d}
	n := opts.N
	if opts.Deny {
		if n = d.DenyOption(); n == 0 {
			res.Escaped = true
			if err := o.Backend.SendKey(p, "escape"); err != nil {
				return nil, err
			}
			return res, o.awaitGone(p, d, res)
		}
	}
	res.Chosen = d.Option(n)
	if res.Chosen == nil {
		return nil, term.UsageErr("the dialog on %s has choices 1-%d, not %d", p.Address(), len(d.Options), n)
	}
	if res.Chosen.Persist && !opts.Persist {
		return nil, term.Errf(term.CodeGuard, "choice %d widens permission beyond this request (%q); pass --persist to choose it", n, res.Chosen.Text)
	}
	if n != d.Selected {
		key, steps := "down", n-d.Selected
		if steps < 0 {
			key, steps = "up", -steps
		}
		for i := 0; i < steps; i++ {
			if err := o.Backend.SendKey(p, key); err != nil {
				return nil, err
			}
		}
		if err := o.awaitSelected(p, d, n); err != nil {
			return nil, err
		}
	}
	if err := o.Backend.SendKey(p, "enter"); err != nil {
		return nil, err
	}
	return res, o.awaitGone(p, d, res)
}

// awaitSelected waits until the dialog shows choice n selected. Claude Code redraws the selection
// a moment after the keys; one early look saw the old selection on a real session.
func (o *Ops) awaitSelected(p *term.Pane, d *Dialog, n int) error {
	for waited := 0.0; ; waited += poll {
		o.Clock.Sleep(poll)
		now, err := o.ReadDialog(p)
		if err != nil {
			return err
		}
		if now == nil || now.Fingerprint != d.Fingerprint {
			return term.BackendErr("the dialog on %s changed while choosing; Enter was not pressed. Read the screen: the arrow keys may have reached its input line", p.Address())
		}
		if now.Selected == n {
			return nil
		}
		if waited >= selectLook {
			return term.BackendErr("the selection on %s is at %d, not %d, %gs after the arrow keys; Enter was not pressed", p.Address(), now.Selected, n, selectLook)
		}
	}
}

// awaitGone waits for the answered dialog to leave the screen. A different dialog in its place is the next one.
func (o *Ops) awaitGone(p *term.Pane, d *Dialog, res *Answered) error {
	for waited := 0.0; ; waited += poll {
		o.Clock.Sleep(poll)
		now, err := o.ReadDialog(p)
		if err != nil {
			return err
		}
		if now == nil || now.Fingerprint != d.Fingerprint {
			res.Next = now
			return nil
		}
		if waited >= answerLook {
			return term.BackendErr("dialog %s is still on %s %gs after the answer; check with read", d.Fingerprint, p.Address(), answerLook)
		}
	}
}

// answerTarget is the pane of a task the caller owns, or a pane the caller started with spawn or task new.
func (t *Tasks) answerTarget(target string) (term.Backend, *term.Pane, *Task, error) {
	if t.Sender == nil {
		return nil, nil, nil, term.UsageErr("cannot tell which pane this runs in; only the owner of a task can answer its dialogs")
	}
	if err := t.Reconcile(); err != nil {
		return nil, nil, nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, nil, nil, err
	}
	x, err := t.find(d.Tasks, target)
	if err != nil && strings.HasPrefix(err.Error(), "several") {
		return nil, nil, nil, err
	}
	if err == nil {
		if x.Owner != t.Sender.ID {
			return nil, nil, nil, term.TargetErr("only the owner of task %s can answer its dialogs", x.ID)
		}
		if !open(x.Status) || x.Pane == "" {
			return nil, nil, nil, term.UsageErr("task %s is %s: there is no harness to answer", x.ID, x.Status)
		}
		b, p, err := t.paneByID(x.Pane, x.Ref)
		if err != nil {
			return nil, nil, nil, err
		}
		if p == nil {
			return nil, nil, nil, term.UsageErr("task %s: its pane is gone", x.ID)
		}
		return b, p, x, nil
	}
	panes, err := t.Backend.ListPanes()
	if err != nil {
		return nil, nil, nil, err
	}
	p, err := term.Resolve(panes, target)
	if err != nil {
		return nil, nil, nil, err
	}
	if p.IsSelf || t.isSelf(p.ID) {
		return nil, nil, nil, term.TargetErr("refusing to answer a dialog on your own pane")
	}
	rec := d.Panes[p.ID]
	if rec == nil || rec.Owner != t.Sender.ID || !sameServer(rec.Ref, t.Backend.ServerRef()) {
		return nil, nil, nil, term.TargetErr("%s is not a pane you started with spawn or task new; its dialogs are not yours to answer", p.Address())
	}
	return t.Backend, p, nil, nil
}

// Answer answers the dialog on a task's worker, or on a pane the caller started.
func (t *Tasks) Answer(target string, opts AnswerOpts) (*Answered, *term.Pane, error) {
	b, p, x, err := t.answerTarget(target)
	if err != nil {
		return nil, nil, err
	}
	res, err := t.ops(b, t.Sender).Answer(p, opts)
	if x != nil && res != nil {
		var te *term.Error
		stuck := errors.As(err, &te) && te.Code == term.CodeBackend
		if !stuck {
			if uerr := t.Reg.Update(func(d *Data) error {
				if c := d.Tasks[x.ID]; c != nil {
					c.Dialog = nil // answered: the same request asked again later is a new one; a next dialog is read anew
				}
				return nil
			}); uerr != nil {
				Warn(fmt.Sprintf("could not record the answer: %v", uerr))
			}
		}
	}
	return res, p, err
}
