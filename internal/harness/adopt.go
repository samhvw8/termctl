package harness

import (
	"errors"
	"fmt"

	"github.com/samhvw8/termctl/internal/term"
)

// Adopted is what task adopt did: the task the caller now owns, the owner it had ("" for a pane that held
// no task), and whether a new task record was made for a pane.
type Adopted struct {
	Task     *Task
	OldOwner string
	New      bool
}

// Adopt makes the caller the owner of a task, or of a pane that holds none (a new running task record is
// made for it, and the pane gets one message saying how to report). Refused: the caller's own pane (exit
// 2); a task whose old owner pane is alive, or a pane holding a task owned by a live pane, unless force
// (exit 6). The task's sub-tasks stay under it; its tree, depth and caps are recomputed under the caller.
// The old owner, if alive, gets one notice.
func (t *Tasks) Adopt(target string, force bool) (*Adopted, error) {
	me, err := t.owner()
	if err != nil {
		return nil, err
	}
	if err := t.Reconcile(); err != nil {
		return nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	x, xl, ferr := t.find(d, target)
	if errors.Is(ferr, errSeveral) {
		return nil, ferr
	}
	var b term.Backend
	var p *term.Pane
	if ferr != nil {
		b, p, err = t.paneTarget(d, target)
		if err != nil {
			return nil, err
		}
		if p.IsSelf || t.isSelf(p.ID) {
			return nil, term.TargetErr("refusing to adopt your own pane")
		}
		x, xl = taskToAdoptOnPane(d, p.ID, b.ServerRef())
		if x == nil {
			return t.adoptPane(me, b, p)
		}
	}
	return t.adoptTask(me, x, xl, b, p, force)
}

// taskToAdoptOnPane picks the task a pane holds for task adopt: an open unclosed task first, else the
// latest task on the pane if it is exited or lost (while the pane lives) so adopt reopens it instead of
// creating a second record.
func taskToAdoptOnPane(d *Data, paneID string, ref map[string]string) (*Job, *Lease) {
	var openHit, latest *Job
	var openHitL, latestL *Lease
	for _, y := range d.Jobs {
		yl := d.leaseOf(y)
		if yl.Pane != paneID || !sameServer(yl.Ref, ref) {
			continue
		}
		if open(y.Status) && !y.Closed && newerTask(y, yl, openHit, openHitL) {
			openHit, openHitL = y, yl
		}
		if newerTask(y, yl, latest, latestL) {
			latest, latestL = y, yl
		}
	}
	if openHit != nil {
		return openHit, openHitL
	}
	if latest != nil && !latest.Closed && (latest.Status == "exited" || latest.Status == "lost") {
		return latest, latestL
	}
	return nil, nil
}

func newerTask(a *Job, al *Lease, b *Job, bl *Lease) bool {
	if b == nil {
		return true
	}
	if a.Started != b.Started {
		return a.Started > b.Started
	}
	if a.Ended != b.Ended {
		return a.Ended > b.Ended
	}
	return a.ID > b.ID
}

// ownerAlive: the task's owner pane is there. A server that does not answer counts as alive: unknown is not gone.
func (t *Tasks) ownerAlive(owner string, ref map[string]string) bool {
	if owner == "" {
		return false
	}
	_, p, err := t.paneByID(owner, ref)
	return err != nil || p != nil
}

// heldBy is the open task a pane holds, nil for none.
func heldBy(d *Data, pane string) (*Job, *Lease) {
	for _, j := range d.Jobs {
		if l := d.leaseOf(j); open(j.Status) && !j.Closed && l.Pane == pane {
			return j, l
		}
	}
	return nil, nil
}

func (t *Tasks) adoptTask(me *term.Pane, x *Job, xl *Lease, b term.Backend, p *term.Pane, force bool) (*Adopted, error) {
	if t.isSelf(xl.Pane) {
		return nil, term.TargetErr("refusing to adopt your own pane (task %s)", x.ID)
	}
	reopen := !x.Closed && (x.Status == "exited" || x.Status == "lost")
	if !reopen && t.owns(xl) {
		return nil, term.UsageErr("task %s is yours already", x.ID)
	}
	if x.Closed || (settled(x.Status) && !reopen) {
		return nil, term.UsageErr("task %s is %s; there is nothing to adopt", x.ID, map[bool]string{true: "closed", false: x.Status}[x.Closed])
	}
	if reopen && p == nil {
		if xl.Pane == "" {
			return nil, term.UsageErr("task %s is %s; there is nothing to adopt", x.ID, x.Status)
		}
		var err error
		b, p, err = t.paneByID(xl.Pane, xl.Ref)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, term.UsageErr("task %s is %s; there is nothing to adopt", x.ID, x.Status)
		}
	}
	alive := !t.owns(xl) && t.ownerAlive(xl.Owner, xl.OwnerRef)
	if alive && !force {
		return nil, term.Errf(term.CodeGuard, "task %s belongs to %s, which is still there; ask it, or use --force", x.ID, xl.Owner)
	}
	maxDepth, err := envInt("TERMCTL_MAX_DEPTH", defaultMaxDepth)
	if err != nil {
		return nil, err
	}
	old, oldRef, wasStatus := xl.Owner, xl.OwnerRef, x.Status
	var out *Task
	var outJob Job
	var outLease Lease
	type titleUpdate struct {
		pane  string
		ref   map[string]string
		title string
	}
	var renames []titleUpdate
	err = t.Reg.Update(func(d *Data) error {
		c := d.Jobs[x.ID]
		if c == nil || c.Closed {
			return term.UsageErr("task %s is closed; there is nothing to adopt", x.ID)
		}
		cl := d.leaseOf(c)
		if reopen {
			if other, _ := otherTaskOnPane(d, c, cl); other != nil {
				return term.UsageErr("pane %s already holds task %s (%s); adopt the pane or that task instead", cl.Pane, other.ID, other.Status)
			}
		}
		parent, pl := heldBy(d, me.ID)
		seen := map[string]bool{}
		for y := pl; y != nil && !seen[y.ID]; y = d.Leases[y.Parent] { // the caller's own task under the adopted one
			seen[y.ID] = true
			if y.ID == cl.ID {
				return term.TargetErr("task %s is above your own task; it cannot report to you", c.ID)
			}
		}
		k := capsOf(pl, maxDepth)
		if pl != nil && k.sub < 1 {
			return term.Errf(term.CodeGuard, "your task %s is a leaf: it may not own tasks", parent.ID)
		}
		depth := k.depth + 1
		below := descendants(d, cl.ID)
		deepest := depth
		for _, y := range below {
			deepest = max(deepest, depth+y.l.Depth-cl.Depth)
		}
		if deepest > maxDepth {
			return term.Errf(term.CodeGuard, "task %s and the tasks under it would reach depth %d, over the limit %d", c.ID, deepest, maxDepth)
		}
		root, treeMax := cl.ID, cl.TreeMax
		if treeMax == 0 {
			treeMax = defaultTreeMax
		}
		sub := cl.SubDepth
		if cl.Root == "" { // a task from before the caps: what the old rule allowed
			sub = maxDepth - cl.Depth
		}
		cl.Parent = ""
		if pl != nil {
			root, treeMax, sub = k.root, min(treeMax, k.treeMax), min(sub, k.sub-1)
			cl.Parent = pl.ID
		}
		sub = min(sub, maxDepth-depth)
		shift := depth - cl.Depth
		cl.Owner, cl.OwnerRef, cl.Depth, cl.Root, cl.SubDepth, cl.TreeMax = me.ID, t.SenderRef, depth, root, sub, treeMax
		if reopen {
			now := t.clock().Now()
			c.Status, c.Ended, c.Summary, c.Notify, c.Seen, c.ExitCode = "running", 0, "", "none", false, nil
			c.Round++
			cl.State, cl.Ended = "active", 0
			cl.Dialog, cl.IdleSince, cl.Stall, cl.Progress = nil, 0, nil, nil
			cl.Unreachable, cl.Orphaned, cl.Restarted, cl.Launched = 0, 0, false, true
			if b != nil {
				cl.Ref = b.ServerRef()
			}
			for _, lid := range sortedLeaseIDs(d) {
				if ol := d.Leases[lid]; ol != cl && ol.State == "active" && ol.Pane == cl.Pane && sameServer(ol.Ref, cl.Ref) {
					endLease(ol, now)
				}
			}
			if c.Owned && cl.Pane != "" && d.Created[cl.Pane] == nil {
				label, cwd, h := c.ID, cl.Cwd, cl.Harness
				if p != nil {
					if p.Label != "" {
						label = p.Label
					}
					if cwd == "" {
						cwd = p.Folder
					}
					if h == "" {
						h = HarnessName(p)
					}
				}
				d.Created[cl.Pane] = &CreatedPane{Ref: cl.Ref, Harness: h, Label: label, Cwd: cwd, ForLease: cl.ID, CreatedAt: now}
			}
		}
		if cl.Pane != "" {
			renames = append(renames, titleUpdate{cl.Pane, cl.Ref, paneTitle(c, cl)})
		}
		for _, y := range below { // they stay under it, and may only narrow what it now has
			beforeTitle := paneTitle(y.j, y.l)
			legacy := y.l.Root == ""
			y.l.Depth += shift
			y.l.Root = root
			if legacy || y.l.TreeMax == 0 || y.l.TreeMax > treeMax {
				y.l.TreeMax = treeMax
			}
			if legacy {
				y.l.SubDepth = maxDepth - y.l.Depth
			}
			y.l.SubDepth = max(0, min(y.l.SubDepth, maxDepth-y.l.Depth))
			if afterTitle := paneTitle(y.j, y.l); afterTitle != beforeTitle && y.l.Pane != "" && open(y.j.Status) && !y.j.Closed {
				renames = append(renames, titleUpdate{y.l.Pane, y.l.Ref, afterTitle})
			}
		}
		if reopen {
			keep := d.Notices[:0]
			for _, n := range d.Notices {
				if n.Lease != cl.ID {
					keep = append(keep, n)
				}
			}
			d.Notices = keep
		} else {
			for _, n := range d.Notices { // unclaimed worker notices held for the old owner go to the new one; never adopt or escalate
				if n.Lease == cl.ID && n.Owner == old && n.Claimed == 0 && n.Kind != "adopt" && n.Kind != "escalate" {
					n.Owner, n.OwnerRef = me.ID, t.SenderRef
				}
			}
		}
		if alive {
			d.Notices = append(d.Notices, &Notice{ID: newNoticeID(d), Lease: cl.ID, Owner: old, OwnerRef: oldRef, Tag: dialogTag(c, cl), Kind: "adopt", Created: t.clock().Now(),
				Text: fmt.Sprintf("task %s (%s) was adopted by %s; it no longer reports to you", c.ID, c.Title, me.Address())})
		}
		out, outJob, outLease = d.TaskView(c.ID), *c, *cl
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, u := range renames {
		if b, p, err := t.paneByID(u.pane, u.ref); err != nil {
			Warn(fmt.Sprintf("could not rename pane %s: %v", u.pane, err))
		} else if p != nil {
			if err := term.SetTitle(b, p, u.title); err != nil {
				Warn(fmt.Sprintf("could not rename pane %s: %v", u.pane, err))
			}
		}
	}
	note := "adopted from " + old
	if reopen {
		if old == "" || old == me.ID {
			note = "reopened from " + wasStatus
		} else {
			note = "reopened from " + wasStatus + ", adopted from " + old
		}
	}
	t.logTask("task.adopt", &outJob, &outLease, outJob.Status, note)
	if _, err := t.FlushPending(); err != nil {
		Warn(fmt.Sprintf("could not deliver notices: %v", err))
	}
	return &Adopted{Task: out, OldOwner: old}, nil
}

// adoptPane makes a running task record for a pane that holds none, owned by the caller, and types it one
// message: who it reports to and how. Nothing else is typed (no /clear).
func (t *Tasks) adoptPane(me *term.Pane, b term.Backend, p *term.Pane) (*Adopted, error) {
	title := "adopted " + p.Address()
	if p.Label != "" {
		title = "adopted " + p.Label
	}
	ref := b.ServerRef()
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	// Whoever held the pane before: the lead that started it, or that held it last.
	oldOwner, oldRef, label := "", map[string]string(nil), p.Label
	if prev := activeLeaseForPane(d, p.ID, ref); prev != nil {
		if prev.Label != "" {
			label = prev.Label
		}
		if prev.Owner != me.ID {
			oldOwner, oldRef = prev.Owner, prev.OwnerRef
		}
	}
	id, _, err := t.reserve(me, NewOpts{Harness: HarnessName(p), Title: title, Cwd: p.Folder}, p)
	if err != nil {
		return nil, err
	}
	var j *Job
	var l *Lease
	if err := t.Reg.Update(func(d *Data) error {
		now := t.clock().Now()
		j = d.Jobs[id]
		l = d.leaseOf(j)
		for _, lid := range sortedLeaseIDs(d) { // a lease left on the pane, on its own server, ends: this task holds it now
			if old := d.Leases[lid]; old != l && old.State == "active" && old.Pane == p.ID && sameServer(old.Ref, ref) {
				endLease(old, now)
			}
		}
		// The harness is already in the pane. A pane harness-ops created keeps its creation record (it stays
		// closable); an adopted one never gets one.
		l.Ref, l.Launched, l.Label = ref, true, label
		return nil
	}); err != nil {
		return nil, err
	}
	if err := term.SetTitle(b, p, paneTitle(j, l)); err != nil {
		Warn(fmt.Sprintf("could not rename pane %s: %v", p.Address(), err))
	}
	name := me.Label
	if name == "" {
		name = me.Title
	}
	who := me.Address()
	if name != "" {
		who = name + " (" + me.Address() + ")"
	}
	text := fmt.Sprintf(`You now report to %s. When this work is done, run: harness-ops task done %s --status ok|failed|blocked --summary "<one line result>"`, who, id)
	if sent, err := t.taskOps(b, me, j, l).Msg(p, text, MsgOpts{}); err != nil && !sent.Typed {
		t.fail(id, err)
		return nil, err
	}
	if oldOwner != "" && t.ownerAlive(oldOwner, oldRef) {
		if err := t.Reg.Update(func(d *Data) error {
			j := d.Jobs[id]
			l := d.leaseOf(j)
			d.Notices = append(d.Notices, &Notice{ID: newNoticeID(d), Lease: l.ID, Owner: oldOwner, OwnerRef: oldRef, Tag: dialogTag(j, l), Kind: "adopt", Created: t.clock().Now(),
				Text: fmt.Sprintf("pane %s, which you started, was adopted by %s as task %s", p.Address(), me.Address(), id)})
			return nil
		}); err != nil {
			return nil, err
		}
		if _, err := t.FlushPending(); err != nil {
			Warn(fmt.Sprintf("could not deliver notices: %v", err))
		}
	}
	d, err = t.Reg.Read()
	if err != nil {
		return nil, err
	}
	j = d.Jobs[id]
	t.logTask("task.adopt", j, d.leaseOf(j), j.Status, "a pane with no task: "+p.Address())
	return &Adopted{Task: d.TaskView(id), OldOwner: oldOwner, New: true}, nil
}
