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
	x, ferr := t.find(d.Tasks, target)
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
		x = taskToAdoptOnPane(d, p.ID, b.ServerRef())
		if x == nil {
			return t.adoptPane(me, b, p)
		}
	}
	return t.adoptTask(me, x, b, p, force)
}

// taskToAdoptOnPane picks the task a pane holds for task adopt: an open unclosed task first, else the
// latest task on the pane if it is exited or lost (while the pane lives) so adopt reopens it instead of
// creating a second record.
func taskToAdoptOnPane(d *Data, paneID string, ref map[string]string) *Task {
	var openHit, latest *Task
	for _, y := range d.Tasks {
		if y.Pane != paneID || !sameServer(y.Ref, ref) {
			continue
		}
		if open(y.Status) && !y.Closed && newerTask(y, openHit) {
			openHit = y
		}
		if newerTask(y, latest) {
			latest = y
		}
	}
	if openHit != nil {
		return openHit
	}
	if latest != nil && !latest.Closed && (latest.Status == "exited" || latest.Status == "lost") {
		return latest
	}
	return nil
}

func newerTask(a, b *Task) bool {
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
func heldBy(d *Data, pane string) *Task {
	for _, x := range d.Tasks {
		if open(x.Status) && !x.Closed && x.Pane == pane {
			return x
		}
	}
	return nil
}

func (t *Tasks) adoptTask(me *term.Pane, x *Task, b term.Backend, p *term.Pane, force bool) (*Adopted, error) {
	if t.isSelf(x.Pane) {
		return nil, term.TargetErr("refusing to adopt your own pane (task %s)", x.ID)
	}
	reopen := !x.Closed && (x.Status == "exited" || x.Status == "lost")
	if !reopen && t.owns(x) {
		return nil, term.UsageErr("task %s is yours already", x.ID)
	}
	if x.Closed || (settled(x.Status) && !reopen) {
		return nil, term.UsageErr("task %s is %s; there is nothing to adopt", x.ID, map[bool]string{true: "closed", false: x.Status}[x.Closed])
	}
	if reopen && p == nil {
		if x.Pane == "" {
			return nil, term.UsageErr("task %s is %s; there is nothing to adopt", x.ID, x.Status)
		}
		var err error
		b, p, err = t.paneByID(x.Pane, x.Ref)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, term.UsageErr("task %s is %s; there is nothing to adopt", x.ID, x.Status)
		}
	}
	alive := !t.owns(x) && t.ownerAlive(x.Owner, x.OwnerRef)
	if alive && !force {
		return nil, term.Errf(term.CodeGuard, "task %s belongs to %s, which is still there; ask it, or use --force", x.ID, x.Owner)
	}
	maxDepth, err := envInt("TERMCTL_MAX_DEPTH", defaultMaxDepth)
	if err != nil {
		return nil, err
	}
	old, oldRef, wasStatus := x.Owner, x.OwnerRef, x.Status
	var out Task
	type titleUpdate struct {
		pane  string
		ref   map[string]string
		title string
	}
	var renames []titleUpdate
	err = t.Reg.Update(func(d *Data) error {
		c := d.Tasks[x.ID]
		if c == nil || c.Closed {
			return term.UsageErr("task %s is closed; there is nothing to adopt", x.ID)
		}
		if reopen {
			if other := otherTaskOnPane(d, c); other != nil {
				return term.UsageErr("pane %s already holds task %s (%s); adopt the pane or that task instead", c.Pane, other.ID, other.Status)
			}
		}
		parent := heldBy(d, me.ID)
		seen := map[string]bool{}
		for y := parent; y != nil && !seen[y.ID]; y = d.Tasks[y.Parent] { // the caller's own task under the adopted one
			seen[y.ID] = true
			if y.ID == c.ID {
				return term.TargetErr("task %s is above your own task; it cannot report to you", c.ID)
			}
		}
		k := capsOf(parent, maxDepth)
		if parent != nil && k.sub < 1 {
			return term.Errf(term.CodeGuard, "your task %s is a leaf: it may not own tasks", parent.ID)
		}
		depth := k.depth + 1
		below := descendants(d, c.ID)
		deepest := depth
		for _, y := range below {
			deepest = max(deepest, depth+y.Depth-c.Depth)
		}
		if deepest > maxDepth {
			return term.Errf(term.CodeGuard, "task %s and the tasks under it would reach depth %d, over the limit %d", c.ID, deepest, maxDepth)
		}
		root, treeMax := c.ID, c.TreeMax
		if treeMax == 0 {
			treeMax = defaultTreeMax
		}
		sub := c.SubDepth
		if c.Root == "" { // a task from before the caps: what the old rule allowed
			sub = maxDepth - c.Depth
		}
		c.Parent = ""
		if parent != nil {
			root, treeMax, sub = k.root, min(treeMax, k.treeMax), min(sub, k.sub-1)
			c.Parent = parent.ID
		}
		sub = min(sub, maxDepth-depth)
		shift := depth - c.Depth
		c.Owner, c.OwnerRef, c.Depth, c.Root, c.SubDepth, c.TreeMax = me.ID, t.SenderRef, depth, root, sub, treeMax
		if reopen {
			c.Status, c.Ended, c.Summary, c.Notify, c.Seen = "running", 0, "", "none", false
			c.ExitCode, c.Dialog, c.IdleSince, c.Stall, c.Progress = nil, nil, 0, nil, nil
			c.Unreachable, c.Orphaned, c.Restarted, c.Launched = 0, 0, false, true
			c.Round++
			if b != nil {
				c.Ref = b.ServerRef()
			}
		}
		if c.Pane != "" {
			renames = append(renames, titleUpdate{c.Pane, c.Ref, paneTitle(c)})
		}
		for _, y := range below { // they stay under it, and may only narrow what it now has
			beforeTitle := paneTitle(y)
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
			if afterTitle := paneTitle(y); afterTitle != beforeTitle && y.Pane != "" && open(y.Status) && !y.Closed {
				renames = append(renames, titleUpdate{y.Pane, y.Ref, afterTitle})
			}
		}
		if rec := d.Panes[c.Pane]; rec != nil {
			rec.Owner, rec.OwnerRef = me.ID, t.SenderRef
			if b != nil {
				rec.Ref = b.ServerRef()
			}
		} else if reopen && c.Pane != "" {
			label, cwd, h := c.ID, c.Cwd, c.Harness
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
			var ref map[string]string
			if b != nil {
				ref = b.ServerRef()
			}
			d.Panes[c.Pane] = &PaneRecord{Harness: h, Label: label, Cwd: cwd, Ref: ref, Owner: me.ID, OwnerRef: t.SenderRef,
				Created: t.clock().Now(), Adopted: !c.Owned}
		}
		if reopen {
			keep := d.Pending[:0]
			for _, q := range d.Pending {
				if q.Task != c.ID {
					keep = append(keep, q)
				}
			}
			d.Pending = keep
		} else {
			for _, q := range d.Pending { // a result held for the old owner goes to the new one
				if q.Task == c.ID && q.Owner == old {
					q.Owner, q.OwnerRef = me.ID, t.SenderRef
				}
			}
		}
		if alive {
			d.Pending = append(d.Pending, &Pending{Task: c.ID, Owner: old, OwnerRef: oldRef, Tag: dialogTag(c), Kind: "adopt", Created: t.clock().Now(),
				Text: fmt.Sprintf("task %s (%s) was adopted by %s; it no longer reports to you", c.ID, c.Title, me.Address())})
		}
		out = *c
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
	t.logTask("task.adopt", &out, out.Status, note)
	if _, err := t.FlushPending(); err != nil {
		Warn(fmt.Sprintf("could not deliver notices: %v", err))
	}
	return &Adopted{Task: &out, OldOwner: old}, nil
}

// adoptPane makes a running task record for a pane that holds none, owned by the caller, and types it one
// message: who it reports to and how. Nothing else is typed (no /clear).
func (t *Tasks) adoptPane(me *term.Pane, b term.Backend, p *term.Pane) (*Adopted, error) {
	title := "adopted " + p.Address()
	if p.Label != "" {
		title = "adopted " + p.Label
	}
	x, _, err := t.reserve(me, NewOpts{Harness: HarnessName(p), Title: title, Cwd: p.Folder}, p)
	if err != nil {
		return nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	oldOwner, oldRef := "", map[string]string(nil)
	if rec := d.Panes[p.ID]; rec != nil && rec.Owner != me.ID {
		oldOwner, oldRef = rec.Owner, rec.OwnerRef
	}
	ref := b.ServerRef()
	if err := t.Reg.Update(func(d *Data) error {
		c := d.Tasks[x.ID]
		c.Ref, c.Launched = ref, true // the harness is already in the pane
		label, adopted := p.Label, true
		if rec := d.Panes[p.ID]; rec != nil {
			label, adopted = rec.Label, rec.Adopted // a pane harness-ops created stays closable
		}
		d.Panes[p.ID] = &PaneRecord{Harness: HarnessName(p), Label: label, Cwd: p.Folder, Ref: ref, Owner: me.ID, OwnerRef: t.SenderRef,
			Created: t.clock().Now(), Adopted: adopted}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := term.SetTitle(b, p, paneTitle(x)); err != nil {
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
	text := fmt.Sprintf(`You now report to %s. When this work is done, run: harness-ops task done %s --status ok|failed|blocked --summary "<one line result>"`, who, x.ID)
	if sent, err := t.taskOps(b, me, x).Msg(p, text, MsgOpts{}); err != nil && !sent.Typed {
		t.fail(x.ID, err)
		return nil, err
	}
	if oldOwner != "" && t.ownerAlive(oldOwner, oldRef) {
		if err := t.Reg.Update(func(d *Data) error {
			d.Pending = append(d.Pending, &Pending{Task: x.ID, Owner: oldOwner, OwnerRef: oldRef, Tag: dialogTag(x), Kind: "adopt", Created: t.clock().Now(),
				Text: fmt.Sprintf("pane %s, which you started, was adopted by %s as task %s", p.Address(), me.Address(), x.ID)})
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
	out := d.Tasks[x.ID]
	t.logTask("task.adopt", out, out.Status, "a pane with no task: "+p.Address())
	return &Adopted{Task: out, OldOwner: oldOwner, New: true}, nil
}
