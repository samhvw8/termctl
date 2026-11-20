package harness

import (
	"encoding/json"
	"sort"
	"testing"
)

// signalOf is a signal of the task's worker, as the watcher reads one.
func signalOf(t *testing.T, r *rig, id, kind string) Signal {
	t.Helper()
	d := r.data()
	j := d.Jobs[id]
	l := d.leaseOf(j)
	return Signal{Task: j.ID, Lease: l.ID, Pane: l.Pane, Kind: kind}
}

// taskLease is the lease of task id in d: where its owner, pane and watch marks are kept.
func taskLease(d *Data, id string) *Lease { return d.leaseOf(d.Jobs[id]) }

// leaseOfTask is the stored lease of task id, for a screen source.
func leaseOfTask(t *testing.T, r *rig, id string) *Lease {
	t.Helper()
	l := taskLease(r.data(), id)
	if l == nil {
		t.Fatalf("no lease for task %s", id)
	}
	return l
}

// paneLease is the active lease on a pane: who holds it and its watch marks (what a v0.10.4 pane record held).
func paneLease(d *Data, pane string) *Lease {
	ids := make([]string, 0, len(d.Leases))
	for id := range d.Leases {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if l := d.Leases[id]; l.State == "active" && l.Pane == pane {
			return l
		}
	}
	return nil
}

// noticeTask is the task a notice is about, "" for a pane's notice.
func noticeTask(d *Data, n *Notice) string {
	if j := d.jobOf(d.Leases[n.Lease]); j != nil {
		return j.ID
	}
	return ""
}

// noticePane is the pane of a notice of a pane no running task held, "" for a task's.
func noticePane(d *Data, n *Notice) string {
	if l := d.Leases[n.Lease]; l != nil && l.Job == "" {
		return l.Pane
	}
	return ""
}

// seedTask stores a task given in its v0.10.4 shape, as the migration would.
func seedTask(d *Data, x *Task) {
	raw, err := json.Marshal(x)
	if err != nil {
		panic(err)
	}
	var v v2Task
	if err := json.Unmarshal(raw, &v); err != nil {
		panic(err)
	}
	mergeV2(d, map[string]*v2Task{v.ID: &v}, nil, nil)
}

// seedPane stores a v0.10.4 pane record, as the migration would.
func seedPane(d *Data, id string, rec *v2PaneRecord) {
	mergeV2(d, nil, map[string]*v2PaneRecord{id: rec}, nil)
}

// split is a task given in its v0.10.4 shape as a job and its lease.
func split(x *Task) (*Job, *Lease) {
	d := emptyData()
	seedTask(d, x)
	j := d.Jobs[x.ID]
	return j, d.leaseOf(j)
}
