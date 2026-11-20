package harness

import (
	"crypto/rand"
	"encoding/hex"
	"maps"
	"sort"
)

// TaskView assembles the output-only Task of a job from its Job, its Lease and the pane's CreatedPane
// record, with the v0.10.4 task's JSON. nil for an unknown job. Engine code never reads through it.
func (d *Data) TaskView(jobID string) *Task {
	j := d.Jobs[jobID]
	if j == nil {
		return nil
	}
	x := &Task{ID: j.ID, Title: j.Title, Brief: j.Brief, Status: j.Status, Started: j.Started, Ended: j.Ended, Summary: j.Summary,
		Notify: j.Notify, Closed: j.Closed, Owned: j.Owned, Seen: j.Seen, Round: j.Round, ExitCode: j.ExitCode, ReviewOf: j.ReviewOf, Blind: j.Blind,
		Defer: j.Defer, AckedAt: j.AckedAt, AckNote: j.AckNote, Denied: j.Denied, Unknown: maps.Clone(j.Unknown)}
	l := d.Leases[j.Lease]
	if l == nil {
		return x
	}
	x.Harness, x.Pane, x.Ref, x.Owner, x.OwnerRef, x.Cwd, x.Depth = l.Harness, l.Pane, maps.Clone(l.Ref), l.Owner, maps.Clone(l.OwnerRef), l.Cwd, l.Depth
	x.Launched, x.AnsweredAt, x.Unreachable, x.Project = l.Launched, l.AnsweredAt, l.Unreachable, l.Project
	x.Progress, x.IdleSince, x.Stall, x.SubDepth, x.TreeMax, x.Orphaned, x.Restarted = l.Progress, l.IdleSince, l.Stall, l.SubDepth, l.TreeMax, l.Orphaned, l.Restarted
	// A task showed only the marks taken while it ran (a closed one keeps the last); the lease also keeps the
	// mark of its pane while the task does not run, which v0.10.4 kept on the pane's record.
	if j.Status == "running" || j.Closed {
		x.Dialog = l.Dialog
	}
	if pl := d.Leases[l.Parent]; pl != nil && pl.Job != "" {
		x.Parent = pl.Job
	}
	if l.Root != "" {
		x.Root = l.Root
		if rl := d.Leases[l.Root]; rl != nil && rl.Job != "" {
			x.Root = rl.Job
		} else if rl != nil && rl.Scope != "" {
			x.Root = rl.Scope
		}
	}
	return x
}

// TaskViews is TaskView of every job, by job ID.
func (d *Data) TaskViews() map[string]*Task {
	out := make(map[string]*Task, len(d.Jobs))
	for id := range d.Jobs {
		out[id] = d.TaskView(id)
	}
	return out
}

// ScopeView is the output-only view of one Scope, combining Scope identity with its active (or latest)
// Lease runtime state.
type ScopeView struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	About      string        `json:"about,omitempty"`
	Project    string        `json:"project"`
	State      string        `json:"state"` // "live", "exited", "lost", "unbound", "dropped"
	Lease      string        `json:"lease,omitempty"`
	Pane       string        `json:"pane,omitempty"`
	Harness    string        `json:"harness,omitempty"`
	Owner      string        `json:"owner,omitempty"`
	Cwd        string        `json:"cwd,omitempty"`
	Depth      int           `json:"depth,omitempty"`
	SubDepth   int           `json:"sub_depth,omitempty"`
	TreeMax    int           `json:"tree_max,omitempty"`
	Root       string        `json:"root,omitempty"`
	StallAfter float64       `json:"stall_after,omitempty"`
	Stall      *StallMark    `json:"stall,omitempty"`
	Dialog     *DialogMark   `json:"dialog,omitempty"`
	Progress   *ProgressNote `json:"progress,omitempty"`
	Told       bool          `json:"told,omitempty"`
	Dropped    bool          `json:"dropped,omitempty"`
	Created    float64       `json:"created"`
}

// TreeNode is one node in a mixed scope + task tree (scope ls --tree).
type TreeNode struct {
	Kind     string      `json:"kind"` // "scope" or "task"
	Scope    *ScopeView  `json:"scope,omitempty"`
	Task     *Task       `json:"task,omitempty"`
	Children []*TreeNode `json:"children"`
}

func latestScopeLease(d *Data, scopeID string) *Lease {
	var best *Lease
	for _, l := range d.Leases {
		if l.Scope != scopeID {
			continue
		}
		if best == nil || l.Started > best.Started || (l.Started == best.Started && l.Ended > best.Ended) ||
			(l.Started == best.Started && l.Ended == best.Ended && l.ID > best.ID) {
			best = l
		}
	}
	return best
}

// ScopeView assembles the output-only ScopeView for scopeID, or nil if unknown.
func (d *Data) ScopeView(scopeID string) *ScopeView {
	s := d.Scopes[scopeID]
	if s == nil {
		return nil
	}
	sv := &ScopeView{
		ID:         s.ID,
		Name:       s.Name,
		About:      s.About,
		Project:    s.Project,
		StallAfter: s.StallAfter,
		Dropped:    s.Dropped,
		Created:    s.Created,
	}
	if s.Lease != "" {
		if l := d.Leases[s.Lease]; l != nil && l.State == "active" {
			sv.State = "live"
			sv.Lease = l.ID
			sv.Pane = l.Pane
			sv.Harness = l.Harness
			sv.Owner = l.Owner
			sv.Cwd = l.Cwd
			sv.Depth = l.Depth
			sv.SubDepth = l.SubDepth
			sv.TreeMax = l.TreeMax
			if l.Root != "" {
				sv.Root = l.Root
				if rl := d.Leases[l.Root]; rl != nil && rl.Job != "" {
					sv.Root = rl.Job
				} else if rl != nil && rl.Scope != "" {
					sv.Root = rl.Scope
				}
			}
			sv.Stall = l.Stall
			sv.Dialog = l.Dialog
			sv.Progress = l.Progress
			sv.Told = l.Told
			return sv
		}
	}
	latest := latestScopeLease(d, s.ID)
	switch {
	case s.Dropped:
		sv.State = "dropped"
	case latest != nil && (latest.State == "exited" || latest.State == "lost"):
		sv.State = latest.State
		sv.Pane = latest.Pane
		sv.Harness = latest.Harness
		sv.Owner = latest.Owner
		sv.Cwd = latest.Cwd
	default:
		sv.State = "unbound"
	}
	if latest != nil {
		sv.Progress = latest.Progress
	}
	return sv
}

// ScopeViews returns ScopeView for every Scope, keyed by scope ID.
func (d *Data) ScopeViews() map[string]*ScopeView {
	out := make(map[string]*ScopeView, len(d.Scopes))
	for id := range d.Scopes {
		out[id] = d.ScopeView(id)
	}
	return out
}

func isRunningJob(d *Data, l *Lease) bool {
	if l == nil || l.Job == "" {
		return false
	}
	j := d.Jobs[l.Job]
	return j != nil && j.Status == "running" && !j.Closed
}

func leaseDialogOnScreen(d *Data, l *Lease, fp string) bool {
	if l == nil {
		return false
	}
	if paneHeld(d, l) {
		return dialogOnScreen(d.jobOf(l), l, fp)
	}
	return paneDialogOnScreen(d, l, fp)
}

// newID is prefix and 6 random hex characters that taken does not hold.
func newID(prefix string, taken func(string) bool) string {
	for {
		b := make([]byte, 3)
		rand.Read(b)
		id := prefix + hex.EncodeToString(b)
		if !taken(id) {
			return id
		}
	}
}

func newJobID(d *Data) string   { return newID("t", func(id string) bool { return d.Jobs[id] != nil }) }
func newLeaseID(d *Data) string { return newID("l", func(id string) bool { return d.Leases[id] != nil }) }
func newScopeID(d *Data) string { return newID("s", func(id string) bool { return d.Scopes[id] != nil }) }
func newNoticeID(d *Data) string {
	return newID("n", func(id string) bool {
		for _, n := range d.Notices {
			if n.ID == id {
				return true
			}
		}
		return false
	})
}

// activeLeaseForPane is the active lease on a pane recorded on the server ref names (sameServer: a lease
// with no ref matches any), nil for none.
func activeLeaseForPane(d *Data, pane string, ref map[string]string) *Lease {
	if pane == "" {
		return nil
	}
	for _, id := range sortedLeaseIDs(d) {
		if l := d.Leases[id]; l.State == "active" && l.Pane == pane && sameServer(l.Ref, ref) {
			return l
		}
	}
	return nil
}

func sortedLeaseIDs(d *Data) []string {
	ids := make([]string, 0, len(d.Leases))
	for id := range d.Leases {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// leaseOf is a job's lease.
func (d *Data) leaseOf(j *Job) *Lease {
	if j == nil {
		return nil
	}
	return d.Leases[j.Lease]
}

// jobOf is the job a lease carries, nil for none.
func (d *Data) jobOf(l *Lease) *Job {
	if l == nil || l.Job == "" {
		return nil
	}
	return d.Jobs[l.Job]
}

// findNotice is the index of the notice with this ID, -1 for none.
func (d *Data) findNotice(id string) int {
	for i, n := range d.Notices {
		if n.ID == id {
			return i
		}
	}
	return -1
}

// endLease ends an active lease: its supervision is over.
func endLease(l *Lease, now float64) {
	if l != nil && l.State == "active" {
		l.State, l.Ended = "ended", now
	}
}

// createdFor: the pane was created for this lease (spawn, or task new without --reuse), on the lease's
// server, and the lease has not been ended or superseded by an active lease on the pane.
func createdFor(d *Data, l *Lease) bool {
	if l == nil || l.Pane == "" || l.State == "ended" {
		return false
	}
	if l.State != "active" && activeLeaseForPane(d, l.Pane, l.Ref) != nil {
		return false
	}
	c := d.Created[l.Pane]
	return c != nil && c.ForLease == l.ID && sameServer(c.Ref, l.Ref)
}

// paneHeld: a running task holds the lease's pane, so a dialog on it is that task's.
func paneHeld(d *Data, l *Lease) bool {
	j := d.jobOf(l)
	return j != nil && j.Status == "running" && !j.Closed
}

// paneInfo is how a notice names a pane: the label, harness and folder of its creation record when there is
// one, else of the lease (an adopted pane).
func paneInfo(d *Data, l *Lease) (label, harness, cwd string) {
	if c := d.Created[l.Pane]; c != nil && sameServer(c.Ref, l.Ref) {
		return c.Label, c.Harness, c.Cwd
	}
	return l.Label, l.Harness, l.Cwd
}
