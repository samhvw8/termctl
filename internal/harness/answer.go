package harness

import (
	"errors"
	"fmt"
	"reflect"
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
	return readPaneDialog(o.Backend, p, prof, lines), nil
}

// historyLines is how far back a request taller than the pane is looked for: tmux's default history-limit.
const historyLines = 2000

// readPaneDialog reads the dialog on a captured screen. A request taller than the pane (Truncated) is read
// again with the scrollback when the backend reads it without disturbing the program (term.SafeHistory) and
// the harness keeps its dialog there; the whole read is taken only when it ends with what the screen showed
// (the same question, choices and end of the request), else the screen's truncated read stands.
func readPaneDialog(b term.Backend, p *term.Pane, prof *Profile, screen []string) *Dialog {
	d := prof.ReadDialog(screen)
	if d == nil || !d.Truncated || d.Ambiguous || prof.ReadScrollbackDialog == nil {
		return d
	}
	if h, ok := b.(term.SafeHistory); !ok || !h.HistoryIsSafe(p) {
		return d
	}
	lines, err := b.Capture(p, historyLines)
	if err != nil {
		return d
	}
	whole := prof.ReadScrollbackDialog(lines)
	switch {
	case whole == nil || whole.Truncated:
		return d
	case whole.Ambiguous:
		if strings.Contains(unspaced(whole.Body), unspaced(d.Body)) {
			return whole
		}
		return d
	case whole.Question != d.Question || !reflect.DeepEqual(whole.Options, d.Options) || !strings.HasSuffix(unspaced(whole.Body), unspaced(d.Body)):
		return d
	}
	return whole
}

// AnswerOpts are options of Answer. N is the choice; Deny presses Esc instead and never Enter.
type AnswerOpts struct {
	N       int
	Deny    bool
	Expect  string
	Persist bool
}

// Answered is what Answer did. Escaped: a deny, which presses Esc and chooses nothing.
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
// The dialog is read again right before the first key and right before Enter (or Esc), on every path, so
// a key never lands on a dialog that replaced the one read, nor on the input line of one that closed.
// A deny presses Esc, which both harnesses document as cancel, and never Enter: Enter would take whatever
// option is really selected, and a parser fooled by the worker's own text could take a "Yes" for the "No".
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
	if d.Ambiguous && !opts.Deny {
		return nil, term.Errf(term.CodeGuard, "the dialog on %s is ambiguous: lines shaped like its choices are out of sequence or more than one is selected, so which choice is which cannot be told. Read the pane and answer there by hand, or deny it", p.Address())
	}
	if d.Hidden && !opts.Deny {
		return nil, term.Errf(term.CodeGuard, "the dialog on %s shows only part of its request (the harness hid lines or cut a line): fingerprint %s does not cover the hidden part, so it cannot be approved. Deny it (task deny), have the worker write the command to a file and run that file, read the file while that short dialog waits, and answer it", p.Address(), d.Fingerprint)
	}
	if d.Truncated && !opts.Deny {
		return nil, term.Errf(term.CodeGuard, "the request in the dialog on %s is longer than the screen: its start is not read, so fingerprint %s does not cover it. Read the pane and answer there by hand, or deny it", p.Address(), d.Fingerprint)
	}
	res := &Answered{Dialog: d}
	if opts.Deny {
		res.Escaped = true
		if _, err := o.recheck(p, d, "Esc"); err != nil {
			return nil, err
		}
		if err := o.Backend.SendKey(p, "escape"); err != nil {
			return nil, err
		}
		return res, o.awaitGone(p, d, res)
	}
	n := opts.N
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
		if _, err := o.recheck(p, d, "the arrow keys"); err != nil { // an arrow key on an input line recalls history
			return nil, err
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
	now, err := o.recheck(p, d, "Enter")
	if err != nil {
		return nil, err
	}
	if now.Selected != n {
		return nil, term.BackendErr("the selection on %s is at %d, not %d; Enter was not pressed", p.Address(), now.Selected, n)
	}
	if err := o.Backend.SendKey(p, "enter"); err != nil {
		return nil, err
	}
	return res, o.awaitGone(p, d, res)
}

// recheck reads the dialog once more right before a key and returns it; a different dialog, or none, stops
// the answer before that key.
func (o *Ops) recheck(p *term.Pane, d *Dialog, key string) (*Dialog, error) {
	now, err := o.ReadDialog(p)
	if err != nil {
		return nil, err
	}
	if now == nil || now.Fingerprint != d.Fingerprint {
		return nil, term.BackendErr("the dialog on %s changed before %s; %s was not pressed. Read it again before answering", p.Address(), key, key)
	}
	return now, nil
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
			return term.BackendErr("dialog %s is still on %s %gs after the answer: the harness did not take it, or asked the same request again; read it again before answering", d.Fingerprint, p.Address(), answerLook)
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
	if errors.Is(err, errSeveral) {
		return nil, nil, nil, err
	}
	if err == nil {
		if x.Owner != t.Sender.ID || !sameServer(x.OwnerRef, t.callerRef()) { // a pane ID is reused on another server
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
	b, p, err := t.paneTarget(d, target)
	if err != nil {
		return nil, nil, nil, err
	}
	if p.IsSelf || t.isSelf(p.ID) {
		return nil, nil, nil, term.TargetErr("refusing to answer a dialog on your own pane")
	}
	if !t.ownsRecord(d.Panes[p.ID], b.ServerRef()) {
		return nil, nil, nil, term.TargetErr("%s is not a pane you started with spawn or task new; its dialogs are not yours to answer", p.Address())
	}
	for _, x := range d.Tasks { // the pane of a task: the answer must reach its dialog mark too
		if x.Pane == p.ID && open(x.Status) && x.Owner == t.Sender.ID && sameServer(x.Ref, b.ServerRef()) {
			return b, p, x, nil
		}
	}
	return b, p, nil, nil
}

// paneTarget resolves a pane target. A pane ID with a record (the form a notice names) is looked up on
// the server its record names, as a task's pane is, so a pane on a private tmux socket is found without -L;
// any other target is resolved among the panes of the caller's backend.
func (t *Tasks) paneTarget(d *Data, target string) (term.Backend, *term.Pane, error) {
	if rec := d.Panes[target]; rec != nil {
		b, p, err := t.paneByID(target, rec.Ref)
		if err != nil {
			return nil, nil, err
		}
		if p == nil {
			return nil, nil, term.TargetErr("pane %s is gone", target)
		}
		return b, p, nil
	}
	panes, err := t.Backend.ListPanes()
	if err != nil {
		return nil, nil, err
	}
	p, err := term.Resolve(panes, target)
	return t.Backend, p, err
}

// Answer answers the dialog on a task's worker, or on a pane the caller started.
func (t *Tasks) Answer(target string, opts AnswerOpts) (*Answered, *term.Pane, error) {
	b, p, x, err := t.answerTarget(target)
	if err != nil {
		return nil, nil, err
	}
	res, err := t.ops(b, t.Sender).Answer(p, opts)
	if x != nil && res != nil {
		// A key was pressed. The answered dialog's mark goes, even when the dialog is still on screen: the
		// harness may have asked the same request again, and that one must be announced. A mark of another
		// dialog stays: the watcher marked and announced it after this answer read its dialog. Reads taken
		// until now are stale.
		if uerr := t.Reg.Update(func(d *Data) error {
			if c := d.Tasks[x.ID]; c != nil {
				if dialogOnScreen(c, res.Dialog.Fingerprint) {
					c.Dialog = nil
				}
				c.AnsweredAt = t.clock().Now()
			}
			return nil
		}); uerr != nil {
			Warn(fmt.Sprintf("could not record the answer: %v", uerr))
		}
	}
	return res, p, err
}
