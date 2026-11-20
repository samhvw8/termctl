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

// capsOf reads the caps of the task a pane holds (its job's lease), nil for a pane that is not a task. A
// task from before these fields (Root "") keeps the old rule: it may start tasks down to the depth ceiling.
func capsOf(parent *Lease, maxDepth int) caps {
	switch {
	case parent == nil:
		return caps{0, maxDepth, defaultTreeMax, ""}
	case parent.Root == "":
		return caps{parent.Depth, max(0, maxDepth-parent.Depth), defaultTreeMax, parent.ID}
	}
	return caps{parent.Depth, parent.SubDepth, parent.TreeMax, parent.Root}
}

// rootOf is the tree a lease belongs to: the lease at its top.
func rootOf(l *Lease) string {
	if l.Root != "" {
		return l.Root
	}
	return l.ID
}

// runningInTree counts the running tasks of tree root.
func runningInTree(d *Data, root string) int {
	n := 0
	for _, j := range d.Jobs {
		if j.Status == "running" && !j.Closed && rootOf(d.leaseOf(j)) == root {
			n++
		}
	}
	return n
}

// place checks and fills a new task's depth, sub-depth, tree limit and root (on its lease l) under the task
// its owner pane holds (pj and its lease pl; nil: a pane that is not a task). sub and treeMax are what the
// caller asked for (treeMax 0: inherit, or the default at a root).
func place(d *Data, j *Job, l *Lease, pj *Job, pl *Lease, sub, treeMax, maxDepth int) error {
	c := capsOf(pl, maxDepth)
	if sub < 0 || treeMax < 0 {
		return term.UsageErr("--sub-depth and --tree-max must not be negative")
	}
	if pl != nil && c.sub < 1 {
		return term.Errf(term.CodeGuard, "task %s is a leaf: it may not start tasks (its lead gave it --sub-depth 0)", pj.ID)
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
	case pl != nil && treeMax > c.treeMax:
		return term.Errf(term.CodeGuard, "--tree-max %d is more than the tree allows (%d); a sub-task may only narrow it", treeMax, c.treeMax)
	}
	root := c.root
	if pl == nil {
		root = l.ID
	} else if n := runningInTree(d, root); n >= c.treeMax {
		return term.Errf(term.CodeGuard, "tree %s already runs %d tasks (limit %d)", treeName(d, root), n, c.treeMax)
	}
	l.Depth, l.SubDepth, l.TreeMax, l.Root = depth, sub, treeMax, root
	return nil
}

// treeName names a tree as v0.10.4 did: by the task at its top.
func treeName(d *Data, root string) string {
	if j := d.jobOf(d.Leases[root]); j != nil {
		return j.ID
	}
	return root
}

// roleName is a task's role as a pane shows it: what the person looking at the terminal needs to tell apart.
func roleName(j *Job, l *Lease) string {
	switch {
	case j.ReviewOf != "":
		return "REVIEWER"
	case l.SubDepth >= 2:
		return "LEAD OF LEADS"
	case l.SubDepth == 1:
		return "LEAD"
	}
	return "WORKER"
}

// paneTitle names a task's pane: role, ID and title, so a tab or a badge says what it is at a glance. A
// deferred task stands alone (it reports to no one), so it carries no role: its ID and title only.
func paneTitle(j *Job, l *Lease) string {
	s := roleName(j, l) + " · " + j.ID
	if j.Defer {
		s = j.ID
	}
	if j.Title != "" {
		s += " · " + j.Title
	}
	return s
}

// roleCard is the capability card in a task's brief footer.
func roleCard(l *Lease, running int) string {
	if l.SubDepth < 1 {
		return "Role: leaf (do not start tasks)."
	}
	return fmt.Sprintf("Role: lead (you may start tasks with --sub-depth up to %d; tree limit %d; %d running).", l.SubDepth-1, l.TreeMax, running)
}

// jobLease is a job with its lease, for lists of tasks.
type jobLease struct {
	j *Job
	l *Lease
}

// descendants lists the tasks under the task whose lease is lid (Parent links between leases), depth first,
// parents before their children.
func descendants(d *Data, lid string) []jobLease {
	kids := map[string][]jobLease{}
	for _, l := range d.Leases {
		if j := d.jobOf(l); j != nil && l.Parent != "" {
			kids[l.Parent] = append(kids[l.Parent], jobLease{j, l})
		}
	}
	var out []jobLease
	seen := map[string]bool{lid: true}
	var walk func(id string)
	walk = func(id string) {
		k := kids[id]
		sort.Slice(k, func(a, b int) bool { return k[a].j.Started < k[b].j.Started })
		for _, c := range k {
			if !seen[c.l.ID] {
				seen[c.l.ID] = true
				out = append(out, c)
				walk(c.l.ID)
			}
		}
	}
	walk(lid)
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
	for _, x := range d.Jobs {
		xl := d.leaseOf(x)
		if xl.Owner != owner || !sameServer(xl.OwnerRef, t.SenderRef) {
			continue
		}
		below := descendants(d, xl.ID)
		for _, g := range below {
			m := g.l.Dialog
			if m == nil || m.Escalated || !dialogOnScreen(g.j, g.l, m.Fingerprint) || now-m.Since < escalateAfter {
				continue
			}
			m.Escalated, queued = true, true
			d.Notices = append(d.Notices, &Notice{ID: newNoticeID(d), Lease: g.l.ID, Owner: xl.Owner, OwnerRef: xl.OwnerRef, Tag: dialogTag(g.j, g.l), Kind: "escalate",
				Dialog: m.Fingerprint, Created: now,
				Text: fmt.Sprintf("ESCALATION task %s (%s), under your task %s, has waited %ds on dialog %s that its lead %s has not answered: %q. read it: harness-ops read %s",
					g.j.ID, g.j.Title, x.ID, int(now-m.Since), m.Fingerprint, g.l.Owner, term.Cut(m.Question, 120), g.j.ID)})
		}
		if xl.Orphaned != 0 || !(x.Closed || x.Status == "exited" || x.Status == "lost") {
			continue
		}
		var running []string
		for _, g := range below {
			if g.j.Status == "running" && !g.j.Closed {
				running = append(running, g.j.ID)
			}
		}
		if len(running) == 0 {
			continue
		}
		state := x.Status
		if x.Closed {
			state = "closed"
		}
		xl.Orphaned, queued = now, true
		d.Notices = append(d.Notices, &Notice{ID: newNoticeID(d), Lease: xl.ID, Owner: xl.Owner, OwnerRef: xl.OwnerRef, Tag: dialogTag(x, xl), Kind: "escalate", Created: now,
			Text: fmt.Sprintf("ESCALATION task %s (%s) is %s, but tasks under it still run: %s. Close them with: harness-ops task close --tree --force %s",
				x.ID, x.Title, state, strings.Join(running, ", "), x.ID)})
	}
	return queued
}

// inSubtree: the caller owns the task of lease l or a task above it.
func (t *Tasks) inSubtree(d *Data, l *Lease) bool {
	seen := map[string]bool{}
	for y := l; y != nil && !seen[y.ID]; y = d.Leases[y.Parent] {
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
	d, top, topLease, err := t.getJob(id)
	if err != nil {
		return nil, err
	}
	if force && !t.inSubtree(d, topLease) {
		return nil, term.TargetErr("task %s is not in your subtree: --tree --force closes only tasks you own and those under them", top.ID)
	}
	all := append(descendants(d, topLease.ID), jobLease{top, topLease})
	sort.SliceStable(all, func(a, b int) bool { return all[a].l.Depth > all[b].l.Depth }) // deepest first
	var closed []*Task
	var left []string
	for _, x := range all {
		if x.j.Closed {
			continue
		}
		if t.isSelf(x.l.Pane) {
			left = append(left, x.j.ID+" (your own pane)")
			continue
		}
		c, err := t.Close(x.j.ID, force)
		if err != nil {
			left = append(left, fmt.Sprintf("%s (%v)", x.j.ID, err))
			continue
		}
		closed = append(closed, c)
	}
	if len(left) > 0 {
		return closed, term.Errf(term.CodeGuard, "closed %d; left open: %s", len(closed), strings.Join(left, "; "))
	}
	return closed, nil
}
