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
	for _, l := range d.Leases {
		if isRunningJob(d, l) && rootOf(l) == root {
			n++
		}
	}
	return n
}

func leafRefusal(d *Data, pl *Lease) error {
	if j := d.jobOf(pl); j != nil {
		return term.Errf(term.CodeGuard, "task %s is a leaf: it may not start tasks (its lead gave it --sub-depth 0)", j.ID)
	}
	if pl != nil && pl.Scope != "" {
		if s := d.Scopes[pl.Scope]; s != nil {
			return term.Errf(term.CodeGuard, "scope %s (%s) is a leaf: it may not start tasks or scopes (its lead gave it --sub-depth 0)", s.Name, s.ID)
		}
	}
	return term.Errf(term.CodeGuard, "this pane is a leaf: it may not start tasks, panes, or scopes (its lead gave it --sub-depth 0)")
}

// placeLease checks and fills a new lease's depth, sub-depth, tree limit, parent and root under the
// caller's active lease pl (nil for a root). countsTree is true for Jobs (checked against tree_max) and
// false for Scopes/bare spawns (which consume 0 tree_max slots).
func placeLease(d *Data, l, pl *Lease, sub, treeMax, maxDepth int, countsTree bool) error {
	c := capsOf(pl, maxDepth)
	if sub < 0 || treeMax < 0 {
		return term.UsageErr("--sub-depth and --tree-max must not be negative")
	}
	if pl != nil && c.sub < 1 {
		return leafRefusal(d, pl)
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
	} else if countsTree {
		if n := runningInTree(d, root); n >= c.treeMax {
			return term.Errf(term.CodeGuard, "tree %s already runs %d tasks (limit %d)", treeName(d, root), n, c.treeMax)
		}
	}
	l.Depth, l.SubDepth, l.TreeMax, l.Root = depth, sub, treeMax, root
	if pl != nil {
		l.Parent = pl.ID
	}
	return nil
}

// treeName names a tree by the task or scope at its top.
func treeName(d *Data, root string) string {
	if rl := d.Leases[root]; rl != nil {
		if j := d.jobOf(rl); j != nil {
			return j.ID
		}
		if rl.Scope != "" {
			if s := d.Scopes[rl.Scope]; s != nil {
				return s.Name
			}
		}
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

// leaseDescendants lists all leases under lid (Parent links between leases), depth first, parents before children.
func leaseDescendants(d *Data, lid string) []*Lease {
	kids := map[string][]*Lease{}
	for _, l := range d.Leases {
		if l.Parent != "" {
			kids[l.Parent] = append(kids[l.Parent], l)
		}
	}
	var out []*Lease
	seen := map[string]bool{lid: true}
	var walk func(id string)
	walk = func(id string) {
		k := kids[id]
		sort.Slice(k, func(a, b int) bool {
			if k[a].Started != k[b].Started {
				return k[a].Started < k[b].Started
			}
			return k[a].ID < k[b].ID
		})
		for _, c := range k {
			if !seen[c.ID] {
				seen[c.ID] = true
				out = append(out, c)
				walk(c.ID)
			}
		}
	}
	walk(lid)
	return out
}

// descendants lists the tasks under the lease lid, depth first, parents before their children.
func descendants(d *Data, lid string) []jobLease {
	var out []jobLease
	for _, c := range leaseDescendants(d, lid) {
		if j := d.jobOf(c); j != nil {
			out = append(out, jobLease{j, c})
		}
	}
	return out
}

// escalate reads the registry (no screen reads) and queues the owner one notice per case: (a) a lease below
// one of its direct leases has waited on a dialog for escalateAfter seconds; (b) a direct job or crashed
// scope/pane has running tasks under it. Alert only: the owner may not answer a grandchild's dialog.
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

func leaseRefLabel(d *Data, l *Lease) string {
	if j := d.jobOf(l); j != nil {
		return "task " + j.ID
	}
	if l.Scope != "" {
		if s := d.Scopes[l.Scope]; s != nil {
			return fmt.Sprintf("scope %s (%s)", s.Name, s.ID)
		}
		return "scope " + l.Scope
	}
	return "pane " + l.Pane
}

func grandchildDialogNotice(d *Data, xl, g *Lease, m *DialogMark, age int) string {
	under := "under your " + leaseRefLabel(d, xl)
	q := term.Cut(m.Question, 120)
	if gj := d.jobOf(g); gj != nil {
		return fmt.Sprintf("ESCALATION task %s (%s), %s, has waited %ds on dialog %s that its lead %s has not answered: %q. read it: harness-ops read %s",
			gj.ID, gj.Title, under, age, m.Fingerprint, g.Owner, q, gj.ID)
	}
	if g.Scope != "" {
		if gs := d.Scopes[g.Scope]; gs != nil {
			return fmt.Sprintf("ESCALATION scope %s (%s), %s, has waited %ds on dialog %s that its lead %s has not answered: %q. read it: harness-ops read %s",
				gs.Name, gs.ID, under, age, m.Fingerprint, g.Owner, q, g.Pane)
		}
	}
	return fmt.Sprintf("ESCALATION pane %s, %s, has waited %ds on dialog %s that its lead %s has not answered: %q. read it: harness-ops read %s",
		g.Pane, under, age, m.Fingerprint, g.Owner, q, g.Pane)
}

// escalations queues the escalation notices on d and reports whether it queued any.
func (t *Tasks) escalations(d *Data, owner string) bool {
	queued := false
	now := t.clock().Now()
	for _, lid := range sortedLeaseIDs(d) {
		xl := d.Leases[lid]
		if xl.Owner != owner || !sameServer(xl.OwnerRef, t.SenderRef) {
			continue
		}
		below := leaseDescendants(d, xl.ID)
		for _, g := range below {
			m := g.Dialog
			if m == nil || m.Escalated || !leaseDialogOnScreen(d, g, m.Fingerprint) || now-m.Since < escalateAfter {
				continue
			}
			m.Escalated, queued = true, true
			d.Notices = append(d.Notices, &Notice{
				ID: newNoticeID(d), Lease: g.ID, Owner: xl.Owner, OwnerRef: xl.OwnerRef,
				Tag: leaseTag(d, g), Kind: "escalate", Dialog: m.Fingerprint, Created: now,
				Text: grandchildDialogNotice(d, xl, g, m, int(now-m.Since)),
			})
		}
		if xl.Orphaned != 0 {
			continue
		}
		x := d.jobOf(xl)
		deadJob := x != nil && (x.Closed || x.Status == "exited" || x.Status == "lost")
		// Detach sets State == "ended" and never triggers orphan escalation; only crashes ("exited"/"lost") do.
		crashedNonJob := x == nil && (xl.State == "exited" || xl.State == "lost")
		if !deadJob && !crashedNonJob {
			continue
		}
		var running []string
		runningSet := map[string]bool{}
		for _, g := range below {
			if isRunningJob(d, g) {
				running = append(running, g.Job)
				runningSet[g.ID] = true
			}
		}
		if len(running) == 0 {
			continue
		}
		xl.Orphaned, queued = now, true
		var text string
		if x != nil {
			state := x.Status
			if x.Closed {
				state = "closed"
			}
			text = fmt.Sprintf("ESCALATION task %s (%s) is %s, but tasks under it still run: %s. Close them with: harness-ops task close --tree --force %s",
				x.ID, x.Title, state, strings.Join(running, ", "), x.ID)
		} else {
			var topRunning []string
			for _, g := range below {
				if runningSet[g.ID] && !runningSet[g.Parent] {
					topRunning = append(topRunning, g.Job)
				}
			}
			if len(topRunning) == 0 {
				topRunning = running
			}
			cmds := make([]string, len(topRunning))
			for i, rid := range topRunning {
				cmds[i] = "harness-ops task close --tree --force " + rid
			}
			text = fmt.Sprintf("ESCALATION %s pane %s is %s, but tasks under it still run: %s. Close them with: %s",
				leaseRefLabel(d, xl), xl.Pane, xl.State, strings.Join(running, ", "), strings.Join(cmds, "; "))
		}
		d.Notices = append(d.Notices, &Notice{
			ID: newNoticeID(d), Lease: xl.ID, Owner: xl.Owner, OwnerRef: xl.OwnerRef,
			Tag: leaseTag(d, xl), Kind: "escalate", Created: now, Text: text,
		})
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
