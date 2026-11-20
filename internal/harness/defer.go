package harness

import (
	"errors"
	"fmt"
	"strings"

	"github.com/samhvw8/termctl/internal/term"
)

// Deferred jobs: the receiver acks once, then never replies. A deferred task (task new --defer) tells its
// owner nothing, its dialogs are denied by the watcher, and its pane is closed on ok. A deferred message
// (msg --defer) carries an ID the receiver acks. Nothing is typed to the owner or the sender.

const (
	defaultAckWait = 30.0 // --wait-ack with no value
	ackPoll        = 1.0
	deferDenyCap   = 2 // dialogs denied on a deferred worker before it is interrupted and marked blocked
)

// deferFooter ends a deferred task's brief: ack once, do the job, task done, and no message to the owner.
func deferFooter(id string) string {
	return fmt.Sprintf(`Deferred job: run harness-ops ack %s "<one line>" once now, do the job, then finish with: harness-ops task done %s --status ok|failed|blocked --summary "<one line result>". Nobody reads replies: do not message the owner.`, id, id)
}

// DeferSuffix ends the line typed for a deferred message.
func DeferSuffix(id string) string {
	return fmt.Sprintf(" [deferred %s: run harness-ops ack %s once, then do not reply]", id, id)
}

// Ack records the receiver's one ack of a deferred task (only from the task's own pane) or of a deferred
// message (only from the pane it went to). A second ack is refused (exit 2). Nothing is typed to anyone.
// It returns what was acked, for the caller's output.
func (t *Tasks) Ack(id, note string) (string, error) {
	if t.Sender == nil {
		return "", term.UsageErr("cannot tell which pane this runs in; only the receiver acks")
	}
	note = OneLine(note)
	d, err := t.Reg.Read()
	if err != nil {
		return "", err
	}
	x, _, ferr := t.find(d, id)
	if errors.Is(ferr, errSeveral) {
		return "", ferr
	}
	if ferr == nil {
		return "task " + x.ID, t.ackTask(x.ID, note)
	}
	return "message " + id, t.ackMessage(id, note)
}

func (t *Tasks) ackTask(id, note string) error {
	var acked Job
	var ackedLease Lease
	err := t.Reg.Update(func(d *Data) error {
		x := d.Jobs[id]
		l := d.leaseOf(x)
		if l.Pane == "" || t.Sender.ID != l.Pane || !sameServer(l.Ref, t.callerRef()) {
			return term.TargetErr("only the worker of task %s (its own pane) can ack it", x.ID)
		}
		if !x.Defer {
			return term.UsageErr("task %s is not deferred: report with harness-ops task done", x.ID)
		}
		if x.AckedAt != 0 {
			return term.UsageErr("task %s was acked already; do not reply again", x.ID)
		}
		x.AckedAt, x.AckNote = t.clock().Now(), note
		acked, ackedLease = *x, *l
		return nil
	})
	if err != nil {
		return err
	}
	t.logTask("ack", &acked, &ackedLease, acked.Status, note)
	return nil
}

// ackMessage looks for the deferred message and an earlier ack under the journal's lock, so two acks
// cannot both pass.
func (t *Tasks) ackMessage(id, note string) error {
	j := t.journal()
	me := t.Sender.Address()
	return j.locked(func() error {
		var msg *Entry
		acked := false
		if err := j.scanBack(func(e Entry) bool {
			if e.ID != id {
				return true
			}
			switch e.Event {
			case "ack":
				acked = true
			case "msg":
				c := e
				msg = &c
				return false
			}
			return true
		}); err != nil {
			return err
		}
		if msg == nil {
			return term.TargetErr("no task and no deferred message %s", id)
		}
		if msg.To != me && trimBackend(msg.To) != trimBackend(me) {
			return term.TargetErr("message %s went to %s, not to this pane (%s)", id, msg.To, me)
		}
		if acked {
			return term.UsageErr("message %s was acked already; do not reply again", id)
		}
		e := Entry{Event: "ack", Project: msg.Project, From: me, FromName: entryName(t.Sender), To: msg.From, ToName: msg.FromName,
			ID: id, Text: note}
		j.stamp(&e)
		return j.appendLocked(e)
	})
}

// Acked reports whether a deferred task or message has been acked.
func (t *Tasks) Acked(id string) (bool, error) {
	d, err := t.Reg.Read()
	if err != nil {
		return false, err
	}
	if j := d.Jobs[id]; j != nil {
		return j.AckedAt != 0, nil
	}
	found := false
	err = t.journal().scanBack(func(e Entry) bool {
		if e.ID == id && e.Event == "ack" {
			found = true
		}
		return !found && !(e.ID == id && e.Event == "msg") // the message itself: nothing older can ack it
	})
	return found, err
}

// WaitAck waits up to timeout seconds for the ack of a deferred task or message; exit 5 when none came.
func (t *Tasks) WaitAck(id string, timeout float64) error {
	if timeout <= 0 {
		timeout = defaultAckWait
	}
	deadline := t.clock().Now() + timeout
	for {
		ok, err := t.Acked(id)
		if err != nil || ok {
			return err
		}
		if t.clock().Now() >= deadline {
			return term.Errf(term.CodeTimeout, "no ack for %s within %gs; read the pane before sending again", id, timeout)
		}
		t.sleep(ackPoll)
	}
}

// deferredLeft: a deferred task of the owner still needs the watcher: it runs (its dialogs are denied), or
// it reported ok and its pane is not closed yet.
func deferredLeft(d *Data, owner string, ref map[string]string) bool {
	for _, x := range d.Jobs {
		l := d.leaseOf(x)
		if x.Defer && l.Owner == owner && sameServer(l.OwnerRef, ref) && !x.Closed &&
			(x.Status == "running" || (x.Status == "ready" && createdFor(d, l))) {
			return true
		}
	}
	return false
}

// closeDeferred closes the panes of the owner's deferred tasks that reported ok: tasks the watcher's owner
// started (not --reuse panes, which the task did not create), never the owner's own pane.
func (t *Tasks) closeDeferred(owner string, say func(string)) {
	d, err := t.Reg.Read()
	if err != nil {
		return
	}
	for _, x := range d.Jobs {
		xl := d.leaseOf(x)
		if !x.Defer || xl.Owner != owner || !sameServer(xl.OwnerRef, t.SenderRef) || x.Status != "ready" || x.Closed || !createdFor(d, xl) ||
			xl.Pane == "" || xl.Pane == owner || t.isSelf(xl.Pane) {
			continue
		}
		keepPane := paneKeepReason(d, x, xl) != ""
		if !keepPane {
			b, p, err := t.paneByID(xl.Pane, xl.Ref)
			if err != nil {
				continue // unknown is not gone: try again next round
			}
			if p != nil {
				if err := b.Close(p); err != nil {
					say(fmt.Sprintf("task %s: could not close its pane: %v", x.ID, err))
					continue
				}
			}
		}
		var closed *Job
		var closedLease Lease
		if err := t.Reg.Update(func(d *Data) error {
			c := d.Jobs[x.ID]
			if c == nil || c.Closed {
				return nil
			}
			cl := d.leaseOf(c)
			c.Closed, cl.Dialog, cl.Restarted = true, nil, false
			if !keepPane && paneKeepReason(d, c, cl) == "" && createdFor(d, cl) {
				delete(d.Created, cl.Pane)
			}
			endLease(cl, t.clock().Now())
			closed, closedLease = c, *cl
			return nil
		}); err != nil {
			say("record: " + err.Error())
			continue
		}
		if closed != nil {
			t.logTask("task.close", closed, &closedLease, closed.Status, "deferred job reported ok: its pane was closed")
			say(fmt.Sprintf("task %s: deferred job reported ok; closed its pane", x.ID))
		}
	}
}

// denyDeferred denies (Esc, never approve) each dialog on a deferred task's worker: nobody is there to read
// a notice. Each denial is journaled; after deferDenyCap denials the worker is interrupted and the task
// marked blocked ("denied 2 dialogs"), with no notice.
func (t *Tasks) denyDeferred(sigs []Signal, say func(string)) {
	for _, s := range sigs {
		if s.Task == "" || s.Kind != "dialog" || s.Dialog == nil {
			continue
		}
		d, err := t.Reg.Read()
		if err != nil {
			return
		}
		x := d.Jobs[s.Task]
		if x == nil || !x.Defer || x.Status != "running" || x.Closed {
			continue
		}
		xl := d.leaseOf(x)
		b, p, err := t.paneByID(xl.Pane, xl.Ref)
		if err != nil || p == nil {
			continue
		}
		ops := t.taskOps(b, t.Sender, x, xl)
		res, err := ops.Answer(p, AnswerOpts{Deny: true, Expect: s.Dialog.Fingerprint})
		if res == nil {
			if err != nil {
				say(fmt.Sprintf("task %s: could not deny dialog %s: %v", x.ID, s.Dialog.Fingerprint, err))
			}
			continue
		}
		denied := 0
		if uerr := t.Reg.Update(func(d *Data) error {
			c := d.Jobs[x.ID]
			cl := d.leaseOf(c)
			if dialogOnScreen(c, cl, res.Dialog.Fingerprint) {
				cl.Dialog = nil
			}
			cl.AnsweredAt = t.clock().Now()
			c.Denied++
			denied = c.Denied
			return nil
		}); uerr != nil {
			say("record: " + uerr.Error())
			continue
		}
		say(fmt.Sprintf("task %s: deferred job: denied dialog %s (%d of %d)", x.ID, res.Dialog.Fingerprint, denied, deferDenyCap))
		if denied < deferDenyCap {
			continue
		}
		if _, _, err := ops.Interrupt(p, ""); err != nil {
			say(fmt.Sprintf("task %s: interrupt: %v", x.ID, err))
		}
		summary := fmt.Sprintf("denied %d dialogs", denied)
		var blocked *Job
		var blockedLease Lease
		if err := t.Reg.Update(func(d *Data) error {
			c := d.Jobs[x.ID]
			if c.Status != "running" {
				return nil
			}
			cl := d.leaseOf(c)
			c.Status, c.Ended, c.Summary, c.Notify, cl.Dialog = "blocked", t.clock().Now(), summary, "none", nil
			cl.IdleSince, cl.Stall = 0, nil
			blocked, blockedLease = c, *cl
			return nil
		}); err != nil {
			say("record: " + err.Error())
			continue
		}
		if blocked != nil {
			t.logTask("task.blocked", blocked, &blockedLease, "blocked", summary)
			say(fmt.Sprintf("task %s: deferred job blocked: %s", x.ID, summary))
		}
	}
}

// AckMarks are a task's deferred and acked marks, for task ls.
func AckMarks(x *Task) string {
	var marks []string
	if x.Defer {
		marks = append(marks, "[deferred]")
	}
	if x.AckedAt != 0 {
		marks = append(marks, "[acked]")
	}
	return strings.Join(marks, " ")
}
