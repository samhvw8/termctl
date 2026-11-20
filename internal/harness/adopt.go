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
	if ferr != nil {
		b, p, err := t.paneTarget(d, target)
		if err != nil {
			return nil, err
		}
		if p.IsSelf || t.isSelf(p.ID) {
			return nil, term.TargetErr("refusing to adopt your own pane")
		}
		for _, y := range d.Tasks {
			if open(y.Status) && !y.Closed && y.Pane == p.ID && sameServer(y.Ref, b.ServerRef()) {
				x = y
			}
		}
		if x == nil {
			return t.adoptPane(me, b, p)
		}
	}
	return t.adoptTask(me, x, force)
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

func (t *Tasks) adoptTask(me *term.Pane, x *Task, force bool) (*Adopted, error) {
	if t.isSelf(x.Pane) {
		return nil, term.TargetErr("refusing to adopt your own pane (task %s)", x.ID)
	}
	if t.owns(x) {
		return nil, term.UsageErr("task %s is yours already", x.ID)
	}
	if x.Closed || settled(x.Status) {
		return nil, term.UsageErr("task %s is %s; there is nothing to adopt", x.ID, map[bool]string{true: "closed", false: x.Status}[x.Closed])
	}
	alive := t.ownerAlive(x.Owner, x.OwnerRef)
	if alive && !force {
		return nil, term.Errf(term.CodeGuard, "task %s belongs to %s, which is still there; ask it, or use --force", x.ID, x.Owner)
	}
	maxDepth, err := envInt("TERMCTL_MAX_DEPTH", defaultMaxDepth)
	if err != nil {
		return nil, err
	}
	old, oldRef := x.Owner, x.OwnerRef
	var out Task
	err = t.Reg.Update(func(d *Data) error {
		c := d.Tasks[x.ID]
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
		for _, y := range below { // they stay under it, and may only narrow what it now has
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
		}
		if rec := d.Panes[c.Pane]; rec != nil {
			rec.Owner, rec.OwnerRef = me.ID, t.SenderRef
		}
		for _, p := range d.Pending { // a result held for the old owner goes to the new one
			if p.Task == c.ID && p.Owner == old {
				p.Owner, p.OwnerRef = me.ID, t.SenderRef
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
	t.logTask("task.adopt", &out, out.Status, "adopted from "+old)
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
