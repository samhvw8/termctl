package harness

import (
	"fmt"
	"sort"
	"strings"

	"github.com/samhvw8/termctl/internal/term"
)

// Lead of lead: a task records the top task of its tree (Root), how many levels below it may still be
// started (SubDepth) and how many tasks of the tree may run at once (TreeMax). A pane that is not a task
// starts roots: its depth is 0 and its cap is TERMCTL_MAX_DEPTH. A child may only narrow what its parent has.

const (
	defaultMaxDepth = 4  // TERMCTL_MAX_DEPTH: the hard ceiling on total depth
	defaultTreeMax  = 12 // running tasks in one tree, at the root
	escalateAfter   = 180.0
)

// caps is what a pane may start: the depth of the task it holds (0 for none), the sub-depth it may give,
// its tree's limit and root ("" for a pane that is not a task: a new task is its own root).
type caps struct {
	depth, sub, treeMax int
	root                string
}

// capsOf reads the caps of the task a pane holds, nil for a pane that is not a task. A task from before
// these fields (Root "") keeps the old rule: it may start tasks down to the depth ceiling.
func capsOf(parent *Task, maxDepth int) caps {
	switch {
	case parent == nil:
		return caps{0, maxDepth, defaultTreeMax, ""}
	case parent.Root == "":
		return caps{parent.Depth, max(0, maxDepth-parent.Depth), defaultTreeMax, parent.ID}
	}
	return caps{parent.Depth, parent.SubDepth, parent.TreeMax, parent.Root}
}

// rootOf is the tree a task belongs to.
func rootOf(x *Task) string {
	if x.Root != "" {
		return x.Root
	}
	return x.ID
}

// runningInTree counts the running tasks of tree root.
func runningInTree(d *Data, root string) int {
	n := 0
	for _, x := range d.Tasks {
		if x.Status == "running" && !x.Closed && rootOf(x) == root {
			n++
		}
	}
	return n
}

// place checks and fills a new task's depth, sub-depth, tree limit and root under parent (nil: a pane that
// is not a task). sub and treeMax are what the caller asked for (treeMax 0: inherit, or the default at a root).
func place(d *Data, x, parent *Task, sub, treeMax, maxDepth int) error {
	c := capsOf(parent, maxDepth)
	if sub < 0 || treeMax < 0 {
		return term.UsageErr("--sub-depth and --tree-max must not be negative")
	}
	if parent != nil && c.sub < 1 {
		return term.Errf(term.CodeGuard, "task %s is a leaf: it may not start tasks (its lead gave it --sub-depth 0)", parent.ID)
	}
	depth := c.depth + 1
	if depth > maxDepth {
		return term.Errf(term.CodeGuard, "task depth %d is over the limit %d; set TERMCTL_MAX_DEPTH to change it", depth, maxDepth)
	}
	if depth+sub > maxDepth {
		return term.Errf(term.CodeGuard, "--sub-depth %d would reach depth %d, over the limit %d; set TERMCTL_MAX_DEPTH to change it", sub, depth+sub, maxDepth)
	}
	if sub > c.sub-1 {
		return term.Errf(term.CodeGuard, "--sub-depth %d is more than this pane may give (up to %d): a sub-task may only narrow it", sub, c.sub-1)
	}
	switch {
	case treeMax == 0:
		treeMax = c.treeMax
	case parent != nil && treeMax > c.treeMax:
		return term.Errf(term.CodeGuard, "--tree-max %d is more than the tree allows (%d); a sub-task may only narrow it", treeMax, c.treeMax)
	}
	root := c.root
	if parent == nil {
		root = x.ID
	} else if n := runningInTree(d, root); n >= c.treeMax {
		return term.Errf(term.CodeGuard, "tree %s already runs %d tasks (limit %d)", root, n, c.treeMax)
	}
	x.Depth, x.SubDepth, x.TreeMax, x.Root = depth, sub, treeMax, root
	return nil
}

// roleCard is the capability card in a task's brief footer.
func roleCard(x *Task, running int) string {
	if x.SubDepth < 1 {
		return "Role: leaf (do not start tasks)."
	}
	return fmt.Sprintf("Role: lead (you may start tasks with --sub-depth up to %d; tree limit %d; %d running).", x.SubDepth-1, x.TreeMax, running)
}

// descendants lists the tasks under x (Parent links), depth first, parents before their children.
func descendants(d *Data, id string) []*Task {
	kids := map[string][]*Task{}
	for _, x := range d.Tasks {
		if x.Parent != "" {
			kids[x.Parent] = append(kids[x.Parent], x)
		}
	}
	var out []*Task
	seen := map[string]bool{id: true}
	var walk func(id string)
	walk = func(id string) {
		k := kids[id]
		sort.Slice(k, func(i, j int) bool { return k[i].Started < k[j].Started })
		for _, c := range k {
			if !seen[c.ID] {
				seen[c.ID] = true
				out = append(out, c)
				walk(c.ID)
			}
		}
	}
	walk(id)
	return out
}

// escalate reads the registry (no screen reads) and queues the owner one notice per case: (a) a task below
// one of its direct tasks has waited on a dialog for escalateAfter seconds; (b) a direct task exited, was
// lost or closed while tasks under it still run. Alert only: the owner may not answer a grandchild's dialog.
func (t *Tasks) escalate(owner string) error {
	// Look on a read first: a round with nothing to tell writes nothing (a write is a lock and two syncs).
	d, err := t.Reg.Read()
	if err != nil {
		return err
	}
	if !t.escalations(d, owner) {
		return nil
	}
	return t.Reg.Update(func(d *Data) error {
		t.escalations(d, owner)
		return nil
	})
}

// escalations queues the escalation notices on d and reports whether it queued any.
func (t *Tasks) escalations(d *Data, owner string) bool {
	queued := false
	now := t.clock().Now()
	for _, x := range d.Tasks {
		if x.Owner != owner || !sameServer(x.OwnerRef, t.SenderRef) {
			continue
		}
		below := descendants(d, x.ID)
		for _, g := range below {
			m := g.Dialog
			if m == nil || m.Escalated || !dialogOnScreen(g, m.Fingerprint) || now-m.Since < escalateAfter {
				continue
			}
			m.Escalated, queued = true, true
			d.Pending = append(d.Pending, &Pending{Task: g.ID, Owner: x.Owner, OwnerRef: x.OwnerRef, Tag: dialogTag(g), Kind: "escalate",
				Dialog: m.Fingerprint, Created: now,
				Text: fmt.Sprintf("ESCALATION task %s (%s), under your task %s, has waited %ds on dialog %s that its lead %s has not answered: %q. read it: harness-ops read %s",
					g.ID, g.Title, x.ID, int(now-m.Since), m.Fingerprint, g.Owner, term.Cut(m.Question, 120), g.ID)})
		}
		if x.Orphaned != 0 || !(x.Closed || x.Status == "exited" || x.Status == "lost") {
			continue
		}
		var running []string
		for _, g := range below {
			if g.Status == "running" && !g.Closed {
				running = append(running, g.ID)
			}
		}
		if len(running) == 0 {
			continue
		}
		state := x.Status
		if x.Closed {
			state = "closed"
		}
		x.Orphaned, queued = now, true
		d.Pending = append(d.Pending, &Pending{Task: x.ID, Owner: x.Owner, OwnerRef: x.OwnerRef, Tag: dialogTag(x), Kind: "escalate", Created: now,
			Text: fmt.Sprintf("ESCALATION task %s (%s) is %s, but tasks under it still run: %s. Close them with: harness-ops task close --tree --force %s",
				x.ID, x.Title, state, strings.Join(running, ", "), x.ID)})
	}
	return queued
}

// inSubtree: the caller owns task x or a task above it.
func (t *Tasks) inSubtree(d *Data, x *Task) bool {
	seen := map[string]bool{}
	for y := x; y != nil && !seen[y.ID]; y = d.Tasks[y.Parent] {
		seen[y.ID] = true
		if t.owns(y) {
			return true
		}
	}
	return false
}

// CloseTree closes task id and every task under it the caller may close, deepest first, as task close does
// for each. With force it closes the caller's own subtree whatever the status: the task must be one the
// caller owns, or below one. It returns the tasks closed, and exit 6 naming those it left open.
func (t *Tasks) CloseTree(id string, force bool) ([]*Task, error) {
	top, err := t.Get(id)
	if err != nil {
		return nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	if force && !t.inSubtree(d, top) {
		return nil, term.TargetErr("task %s is not in your subtree: --tree --force closes only tasks you own and those under them", top.ID)
	}
	all := append(descendants(d, top.ID), top)
	sort.SliceStable(all, func(i, j int) bool { return all[i].Depth > all[j].Depth }) // deepest first
	var closed []*Task
	var left []string
	for _, x := range all {
		if x.Closed {
			continue
		}
		if t.isSelf(x.Pane) {
			left = append(left, x.ID+" (your own pane)")
			continue
		}
		c, err := t.Close(x.ID, force)
		if err != nil {
			left = append(left, fmt.Sprintf("%s (%v)", x.ID, err))
			continue
		}
		closed = append(closed, c)
	}
	if len(left) > 0 {
		return closed, term.Errf(term.CodeGuard, "closed %d; left open: %s", len(closed), strings.Join(left, "; "))
	}
	return closed, nil
}
