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
			return t.adoptPane(me, b, p, force)
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

type titleUpdate struct {
	pane  string
	ref   map[string]string
	title string
}

func adoptNoticeText(d *Data, l *Lease, by string) string {
	if j := d.jobOf(l); j != nil {
		return fmt.Sprintf("task %s (%s) was adopted by %s; it no longer reports to you", j.ID, j.Title, by)
	}
	if l.Scope != "" {
		if s := d.Scopes[l.Scope]; s != nil {
			return fmt.Sprintf("scope %s (%s) was attached/adopted by %s; it no longer reports to you", s.Name, s.ID, by)
		}
		return fmt.Sprintf("scope %s was attached/adopted by %s; it no longer reports to you", l.Scope, by)
	}
	return fmt.Sprintf("pane %s, which you started, was adopted by %s", l.Pane, by)
}

// reparentLease transfers supervision of cl (and its descendant subtree) to me, recomputing Depth, Root,
// SubDepth and TreeMax, re-targeting unclaimed non-adopt/non-escalate notices (unless skipRetarget), and
// queuing an "adopt" notice to the live old owner. Never touches d.Created.
func (t *Tasks) reparentLease(d *Data, cl *Lease, me *term.Pane, meRef map[string]string, maxDepth int, ownerAlive bool, renames *[]titleUpdate, skipRetarget ...bool) (string, error) {
	old, oldRef := cl.Owner, cl.OwnerRef
	pl := activeLeaseForPane(d, me.ID, meRef)
	seen := map[string]bool{}
	for y := pl; y != nil && !seen[y.ID]; y = d.Leases[y.Parent] {
		seen[y.ID] = true
		if y.ID == cl.ID {
			return "", term.TargetErr("%s is above your own pane; it cannot report to you", leaseRefLabel(d, cl))
		}
	}
	k := capsOf(pl, maxDepth)
	if pl != nil && k.sub < 1 {
		return "", leafRefusal(d, pl)
	}
	depth := k.depth + 1
	below := leaseDescendants(d, cl.ID)
	deepest := depth
	for _, y := range below {
		deepest = max(deepest, depth+y.Depth-cl.Depth)
	}
	if deepest > maxDepth {
		return "", term.Errf(term.CodeGuard, "%s and the tasks under it would reach depth %d, over the limit %d", leaseRefLabel(d, cl), deepest, maxDepth)
	}
	root, treeMax := cl.ID, cl.TreeMax
	if treeMax == 0 {
		treeMax = defaultTreeMax
	}
	sub := cl.SubDepth
	if cl.Root == "" {
		sub = maxDepth - cl.Depth
	}
	cl.Parent = ""
	if pl != nil {
		root, treeMax, sub = k.root, min(treeMax, k.treeMax), min(sub, k.sub-1)
		cl.Parent = pl.ID
	}
	sub = min(sub, maxDepth-depth)
	shift := depth - cl.Depth
	cl.Owner, cl.OwnerRef, cl.Depth, cl.Root, cl.SubDepth, cl.TreeMax = me.ID, meRef, depth, root, sub, treeMax
	if j := d.jobOf(cl); j != nil && cl.Pane != "" && renames != nil {
		*renames = append(*renames, titleUpdate{cl.Pane, cl.Ref, paneTitle(j, cl)})
	}
	for _, y := range below {
		yj := d.jobOf(y)
		var beforeTitle string
		if yj != nil {
			beforeTitle = paneTitle(yj, y)
		}
		legacy := y.Root == ""
		y.Depth += shift
		y.Root = root
		if legacy || y.TreeMax == 0 || y.TreeMax > treeMax {
			y.TreeMax = treeMax
		}
		if legacy {
			y.SubDepth = maxDepth - y.Depth
		}
		y.SubDepth = max(0, min(y.SubDepth, maxDepth-y.Depth))
		if yj != nil && renames != nil {
			if afterTitle := paneTitle(yj, y); afterTitle != beforeTitle && y.Pane != "" && open(yj.Status) && !yj.Closed {
				*renames = append(*renames, titleUpdate{y.Pane, y.Ref, afterTitle})
			}
		}
	}
	if len(skipRetarget) == 0 || !skipRetarget[0] {
		for _, n := range d.Notices {
			if n.Lease == cl.ID && n.Owner == old && n.Claimed == 0 && n.Kind != "adopt" && n.Kind != "escalate" {
				n.Owner, n.OwnerRef = me.ID, meRef
			}
		}
	}
	if ownerAlive && old != "" && old != me.ID {
		d.Notices = append(d.Notices, &Notice{
			ID: newNoticeID(d), Lease: cl.ID, Owner: old, OwnerRef: oldRef,
			Tag: leaseTag(d, cl), Kind: "adopt", Created: t.clock().Now(),
			Text: adoptNoticeText(d, cl, me.Address()),
		})
	}
	return old, nil
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
	var prevScopeLeaseID string
	var scopeAlive bool
	if reopen && b != nil {
		if d0, rerr := t.Reg.Read(); rerr == nil {
			if curPane := activeLeaseForPane(d0, xl.Pane, b.ServerRef()); curPane != nil && curPane.ID != xl.ID && curPane.Scope != "" && !t.owns(curPane) {
				prevScopeLeaseID = curPane.ID
				scopeAlive = t.ownerAlive(curPane.Owner, curPane.OwnerRef)
				if scopeAlive && !force {
					scName := curPane.Scope
					if sc := d0.Scopes[curPane.Scope]; sc != nil {
						scName = sc.Name
					}
					return nil, term.Errf(term.CodeGuard, "%s holds scope %s owned by %s, which is still there; ask it, or use --force", xl.Pane, scName, curPane.Owner)
				}
			}
		}
	}
	maxDepth, err := envInt("TERMCTL_MAX_DEPTH", defaultMaxDepth)
	if err != nil {
		return nil, err
	}
	old, wasStatus := xl.Owner, x.Status
	var out *Task
	var outJob Job
	var outLease Lease
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
			paneRef := cl.Ref
			if b != nil {
				paneRef = b.ServerRef()
			}
			if al := activeLeaseForPane(d, cl.Pane, paneRef); al != nil && al != cl && al.Scope != "" && !t.owns(al) {
				scName := al.Scope
				if sc := d.Scopes[al.Scope]; sc != nil {
					scName = sc.Name
				}
				if !force && (scopeAlive || al.ID != prevScopeLeaseID) {
					return term.Errf(term.CodeGuard, "%s holds scope %s owned by %s, which is still there; ask it, or use --force", cl.Pane, scName, al.Owner)
				}
				if scopeAlive && al.Owner != "" && al.Owner != old && al.Owner != me.ID {
					d.Notices = append(d.Notices, &Notice{
						ID: newNoticeID(d), Lease: al.ID, Owner: al.Owner, OwnerRef: al.OwnerRef,
						Tag: leaseTag(d, al), Kind: "adopt", Created: t.clock().Now(),
						Text: adoptNoticeText(d, al, me.Address()),
					})
				}
			}
		}
		if _, err := t.reparentLease(d, cl, me, t.callerRef(), maxDepth, alive, &renames, reopen); err != nil {
			return err
		}
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
					if ol.Scope != "" {
						if sc := d.Scopes[ol.Scope]; sc != nil && sc.Lease == ol.ID {
							sc.Lease = ""
						}
					}
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
			keep := d.Notices[:0]
			for _, n := range d.Notices {
				if n.Lease != cl.ID || n.Kind == "adopt" {
					keep = append(keep, n)
				}
			}
			d.Notices = keep
		}
		out, outJob, outLease = d.TaskView(c.ID), *c, *cl
		return nil
	})
	if err != nil {
		return nil, err
	}
	t.applyRenames(renames)
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

func (t *Tasks) applyRenames(renames []titleUpdate) {
	for _, u := range renames {
		if b, p, err := t.paneByID(u.pane, u.ref); err != nil {
			Warn(fmt.Sprintf("could not rename pane %s: %v", u.pane, err))
		} else if p != nil {
			if err := term.SetTitle(b, p, u.title); err != nil {
				Warn(fmt.Sprintf("could not rename pane %s: %v", u.pane, err))
			}
		}
	}
}

// adoptPane makes a running task record for a pane that holds none, owned by the caller, and types it one
// message: who it reports to and how. Nothing else is typed (no /clear).
func (t *Tasks) adoptPane(me *term.Pane, b term.Backend, p *term.Pane, force bool) (*Adopted, error) {
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
	prevLeaseID, prevScopeID, prevScopeName, prevAlive := "", "", "", false
	if prev := activeLeaseForPane(d, p.ID, ref); prev != nil {
		prevLeaseID = prev.ID
		prevAlive = !t.owns(prev) && t.ownerAlive(prev.Owner, prev.OwnerRef)
		if prev.Scope != "" {
			prevScopeID, prevScopeName = prev.Scope, prev.Scope
			if s := d.Scopes[prev.Scope]; s != nil {
				prevScopeName = s.Name
			}
			if !t.owns(prev) && !force && prevAlive {
				return nil, term.Errf(term.CodeGuard, "%s holds scope %s owned by %s, which is still there; ask it, or use --force", p.Address(), prevScopeName, prev.Owner)
			}
		}
		if prev.Label != "" {
			label = prev.Label
		}
		if prev.Owner != me.ID {
			oldOwner, oldRef = prev.Owner, prev.OwnerRef
		}
	}
	id, _, err := t.reserveWith(me, NewOpts{Harness: HarnessName(p), Title: title, Cwd: p.Folder}, p, reservePolicy{
		paneRef:        ref,
		forAdopt:       true,
		force:          force,
		prevOwnerAlive: prevAlive,
		prevLeaseID:    prevLeaseID,
		label:          label,
	})
	if err != nil {
		return nil, err
	}
	d, err = t.Reg.Read()
	if err != nil {
		return nil, err
	}
	j := d.Jobs[id]
	l := d.leaseOf(j)
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
		adoptText := fmt.Sprintf("pane %s, which you started, was adopted by %s as task %s", p.Address(), me.Address(), id)
		if prevScopeID != "" {
			adoptText = fmt.Sprintf("scope %s (%s) on pane %s was adopted by %s as task %s", prevScopeName, prevScopeID, p.Address(), me.Address(), id)
		}
		if err := t.Reg.Update(func(d *Data) error {
			j := d.Jobs[id]
			l := d.leaseOf(j)
			d.Notices = append(d.Notices, &Notice{ID: newNoticeID(d), Lease: l.ID, Owner: oldOwner, OwnerRef: oldRef, Tag: dialogTag(j, l), Kind: "adopt", Created: t.clock().Now(),
				Text: adoptText})
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
