package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The version 2 registry records, as v0.10.4 wrote them. migrate reads them from Data.Unknown: from a version
// 2 file, and from a version 3 file that a v0.10.x binary wrote into (it keeps the version 3 keys as fields it
// does not know, and adds its own "tasks", "panes" and "pending").

type v2Task struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Harness     string            `json:"harness"`
	Pane        string            `json:"pane"`
	Ref         map[string]string `json:"ref"`
	Owner       string            `json:"owner"`
	OwnerRef    map[string]string `json:"owner_ref"`
	Cwd         string            `json:"cwd"`
	Brief       string            `json:"brief"`
	Status      string            `json:"status"`
	Started     float64           `json:"started"`
	Ended       float64           `json:"ended"`
	Summary     string            `json:"summary"`
	Depth       int               `json:"depth"`
	Notify      string            `json:"notify"`
	Closed      bool              `json:"closed"`
	Owned       bool              `json:"owned"`
	Seen        bool              `json:"seen"`
	Round       int               `json:"round"`
	Launched    bool              `json:"launched"`
	ExitCode    *int              `json:"exit_code,omitempty"`
	ReviewOf    string            `json:"review_of,omitempty"`
	Blind       bool              `json:"blind,omitempty"`
	Dialog      *DialogMark       `json:"dialog,omitempty"`
	AnsweredAt  float64           `json:"answered_at,omitempty"`
	Unreachable float64           `json:"unreachable,omitempty"`
	Project     string            `json:"project,omitempty"`
	Parent      string            `json:"parent,omitempty"`
	Progress    *ProgressNote     `json:"progress,omitempty"`
	IdleSince   float64           `json:"idle_since,omitempty"`
	Stall       *StallMark        `json:"stall,omitempty"`
	Defer       bool              `json:"defer,omitempty"`
	AckedAt     float64           `json:"acked_at,omitempty"`
	AckNote     string            `json:"ack_note,omitempty"`
	Denied      int               `json:"denied,omitempty"`
	Root        string            `json:"root,omitempty"`
	SubDepth    int               `json:"sub_depth,omitempty"`
	TreeMax     int               `json:"tree_max,omitempty"`
	Orphaned    float64           `json:"orphaned,omitempty"`
	Restarted   bool              `json:"restarted,omitempty"`
	Unknown     Unknown           `json:"-"`
}

type v2PaneRecord struct {
	Harness    string            `json:"harness"`
	Label      string            `json:"label"`
	Cwd        string            `json:"cwd"`
	Ref        map[string]string `json:"ref"`
	Owner      string            `json:"owner"`
	OwnerRef   map[string]string `json:"owner_ref,omitempty"`
	Created    float64           `json:"created"`
	Dialog     *DialogMark       `json:"dialog,omitempty"`
	AnsweredAt float64           `json:"answered_at,omitempty"`
	Worked     float64           `json:"worked,omitempty"`
	Adopted    bool              `json:"adopted,omitempty"`
	Unknown    Unknown           `json:"-"`
}

type v2Pending struct {
	Task     string            `json:"task"`
	Owner    string            `json:"owner"`
	OwnerRef map[string]string `json:"owner_ref"`
	Tag      string            `json:"tag"`
	Text     string            `json:"text"`
	Created  float64           `json:"created"`
	Claimed  float64           `json:"claimed"`
	ClaimPid int               `json:"claim_pid,omitempty"`
	Kind     string            `json:"kind,omitempty"`
	Dialog   string            `json:"fingerprint,omitempty"`
	Pane     string            `json:"pane,omitempty"`
	Unknown  Unknown           `json:"-"`
}

func (x *v2Task) UnmarshalJSON(b []byte) (err error) {
	type plain v2Task
	x.Unknown, err = decodeKeeping(b, (*plain)(x))
	return err
}

func (x *v2PaneRecord) UnmarshalJSON(b []byte) (err error) {
	type plain v2PaneRecord
	x.Unknown, err = decodeKeeping(b, (*plain)(x))
	return err
}

func (x *v2Pending) UnmarshalJSON(b []byte) (err error) {
	type plain v2Pending
	x.Unknown, err = decodeKeeping(b, (*plain)(x))
	return err
}

// v2Keys are the version 2 top-level keys.
var v2Keys = []string{"tasks", "panes", "pending"}

// v2KeysIn lists the keys of Unknown that are version 2 keys, matched without regard to case as
// encoding/json matches them, sorted.
func v2KeysIn(u Unknown) []string {
	var out []string
	for k := range u {
		for _, v := range v2Keys {
			if strings.EqualFold(k, v) {
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// migrate brings the data up to version 3. It runs on a file from before version 3, and on any file that
// holds version 2 keys: a v0.10.x binary that read a version 3 file writes its own tasks, pane records and
// pending results beside the version 3 keys. Those are merged in: a job, lease or notice already in the
// version 3 maps is never replaced, and the IDs it derives are the same on every read, so merging the same
// records twice adds nothing. Then the version 2 keys go. A file at version 3 with none of them is left as
// it is.
func migrate(d *Data) error {
	keys := v2KeysIn(d.Unknown)
	if d.Version >= registryVersion && len(keys) == 0 {
		return nil
	}
	tasks := map[string]*v2Task{}
	panes := map[string]*v2PaneRecord{}
	var pending []*v2Pending
	for _, k := range keys {
		raw := d.Unknown[k]
		var err error
		switch strings.ToLower(k) {
		case "tasks":
			var m map[string]*v2Task
			if err = json.Unmarshal(raw, &m); err == nil {
				for id, x := range m {
					if x != nil {
						tasks[id] = x
					}
				}
			}
		case "panes":
			var m map[string]*v2PaneRecord
			if err = json.Unmarshal(raw, &m); err == nil {
				for id, x := range m {
					if x != nil {
						panes[id] = x
					}
				}
			}
		case "pending":
			var l []*v2Pending
			if err = json.Unmarshal(raw, &l); err == nil {
				for _, x := range l {
					if x != nil {
						pending = append(pending, x)
					}
				}
			}
		}
		if err != nil {
			return fmt.Errorf("version 2 %q: %v", k, err)
		}
	}
	if d.Version < 2 { // tasks from version 1 were all launched, with one round
		for _, x := range tasks {
			if x.Round == 0 {
				x.Round = 1
			}
			x.Launched = x.Pane != ""
		}
	}
	mergeV2(d, tasks, panes, pending)
	for _, k := range keys {
		delete(d.Unknown, k)
	}
	if len(d.Unknown) == 0 {
		d.Unknown = nil
	}
	d.Version = registryVersion
	return nil
}

// derivedID is prefix and 6 hex characters of the SHA-256 of input. When the ID is taken by a record that is
// not this one (mine says which is), ":2", ":3" ... is added to the input until it is free.
func derivedID(prefix, input string, taken func(id string) bool) string {
	for n := 1; ; n++ {
		in := input
		if n > 1 {
			in = fmt.Sprintf("%s:%d", input, n)
		}
		sum := sha256.Sum256([]byte(in))
		id := prefix + hex.EncodeToString(sum[:])[:6]
		if !taken(id) {
			return id
		}
	}
}

// refsMatch: two refs name the same server, or either is empty (a record from before refs were kept).
func refsMatch(a, b map[string]string) bool { return sameServer(a, b) || sameServer(b, a) }

// paneKey keys a pane on its server for the migration's one-active-lease-per-pane rule.
func paneKey(pane string, ref map[string]string) string { return pane + "\x00" + serverKey(ref) }

// mergeV2 merges version 2 tasks, pane records and pending results into the version 3 maps.
func mergeV2(d *Data, tasks map[string]*v2Task, panes map[string]*v2PaneRecord, pending []*v2Pending) {
	ids := make([]string, 0, len(tasks))
	for id := range tasks {
		if d.Jobs[id] == nil { // a job the version 3 maps hold already is authoritative
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	// The lease ID of each new task, derived from its ID, so a second read derives the same.
	lidOf := map[string]string{}
	claimed := map[string]bool{}
	for _, id := range ids {
		lidOf[id] = derivedID("l", "v2:task:"+id, func(lid string) bool {
			if claimed[lid] {
				return true
			}
			l := d.Leases[lid]
			return l != nil && l.Job != id
		})
		claimed[lidOf[id]] = true
	}
	leaseOfTask := func(tid string) string {
		if j := d.Jobs[tid]; j != nil {
			return j.Lease
		}
		return lidOf[tid]
	}
	// At most one active lease per pane: of the tasks that may be active there, an open one started last,
	// else the one started last.
	winner := map[string]*v2Task{}
	rank := func(x *v2Task) (int, float64) {
		if open(x.Status) {
			return 1, x.Started
		}
		return 0, x.Started
	}
	for _, id := range ids {
		x := tasks[id]
		if x.Closed || x.Status == "exited" || x.Status == "lost" || x.Pane == "" {
			continue
		}
		k := paneKey(x.Pane, x.Ref)
		w := winner[k]
		if w == nil {
			winner[k] = x
			continue
		}
		xo, xs := rank(x)
		wo, ws := rank(w)
		if xo > wo || (xo == wo && xs > ws) {
			winner[k] = x
		}
	}
	for _, id := range ids {
		x := tasks[id]
		lid := lidOf[id]
		state := "active"
		switch {
		case x.Closed:
			state = "ended"
		case x.Status == "exited":
			state = "exited"
		case x.Status == "lost":
			state = "lost"
		case x.Pane == "" && !open(x.Status):
			state = "ended"
		case x.Pane != "" && winner[paneKey(x.Pane, x.Ref)] != x:
			state = "ended"
		}
		parent := ""
		if x.Parent != "" && (d.Jobs[x.Parent] != nil || tasks[x.Parent] != nil) {
			parent = leaseOfTask(x.Parent)
		}
		root := ""
		if x.Root != "" {
			root = lid
			if d.Jobs[x.Root] != nil || tasks[x.Root] != nil {
				root = leaseOfTask(x.Root)
			}
		}
		l := &Lease{ID: lid, Owner: x.Owner, OwnerRef: x.OwnerRef, Pane: x.Pane, Ref: x.Ref, Harness: x.Harness, Cwd: x.Cwd,
			Project: x.Project, Parent: parent, Root: root, Depth: x.Depth, SubDepth: x.SubDepth, TreeMax: x.TreeMax, Job: x.ID,
			State: state, Launched: x.Launched, Dialog: x.Dialog, AnsweredAt: x.AnsweredAt, IdleSince: x.IdleSince,
			Stall: x.Stall, Progress: x.Progress, Unreachable: x.Unreachable, Orphaned: x.Orphaned, Restarted: x.Restarted, Started: x.Started, Ended: x.Ended}
		j := &Job{ID: x.ID, Lease: lid, Title: x.Title, Brief: x.Brief, Status: x.Status, Round: x.Round, Summary: x.Summary,
			Notify: x.Notify, Owned: x.Owned, Seen: x.Seen, Closed: x.Closed, ExitCode: x.ExitCode, ReviewOf: x.ReviewOf, Blind: x.Blind, Defer: x.Defer,
			AckedAt: x.AckedAt, AckNote: x.AckNote, Denied: x.Denied, Started: x.Started, Ended: x.Ended, Unknown: x.Unknown}
		if state == "active" {
			// One active lease per pane against the version 3 leases too: a bare pane's lease gives way to the
			// task; of two task leases the one started later stays.
			if old := activeOn(d, x.Pane, x.Ref); old != nil {
				switch {
				case old.Job == "" && old.Scope == "":
					old.State, old.Ended = "ended", max(old.Ended, l.Started)
				case old.Started > l.Started:
					l.State = "ended"
				default:
					old.State = "ended"
				}
			}
		}
		d.Leases[lid] = l
		d.Jobs[x.ID] = j
	}
	// Pane records: creation provenance (never for an adopted pane), and the watch state of a pane no
	// task lease holds, as a bare lease.
	pids := make([]string, 0, len(panes))
	for id := range panes {
		pids = append(pids, id)
	}
	sort.Strings(pids)
	for _, pid := range pids {
		rec := panes[pid]
		if !rec.Adopted && d.Created[pid] == nil {
			c := &CreatedPane{Ref: rec.Ref, Harness: rec.Harness, Label: rec.Label, Cwd: rec.Cwd, CreatedAt: rec.Created, Unknown: rec.Unknown}
			var best *v2Task
			for _, id := range ids {
				x := tasks[id]
				if !x.Owned || x.Pane != pid || !refsMatch(rec.Ref, x.Ref) {
					continue
				}
				if best == nil {
					best = x
					continue
				}
				xa, ba := d.Leases[lidOf[x.ID]].State == "active", d.Leases[lidOf[best.ID]].State == "active"
				if (xa && !ba) || (xa == ba && x.Started > best.Started) {
					best = x
				}
			}
			if best != nil {
				c.ForLease = lidOf[best.ID]
			}
			d.Created[pid] = c
		}
		if l := activeOn(d, pid, rec.Ref); l != nil {
			if l.Label == "" {
				l.Label = rec.Label
			}
			l.Worked = max(l.Worked, rec.Worked)
			if l.Job == "" {
				l.AnsweredAt = max(l.AnsweredAt, rec.AnsweredAt)
				if l.Dialog == nil {
					l.Dialog = rec.Dialog
				}
			} else if j := d.Jobs[l.Job]; j != nil && j.Status != "running" && !j.Closed && l.Dialog == nil {
				// The pane's mark while its task does not run: the task's own JSON never showed it.
				l.Dialog = rec.Dialog
			}
			continue
		}
		if c := d.Created[pid]; c != nil && c.ForLease != "" && refsMatch(c.Ref, rec.Ref) {
			// Pane was created for a migrated Owned task (now exited/lost/ended); do not resurrect an active bare lease.
			continue
		}
		lid := derivedID("l", "v2:pane:"+pid, func(lid string) bool {
			l := d.Leases[lid]
			return l != nil && (l.Job != "" || l.Scope != "" || l.Pane != pid || !refsMatch(l.Ref, rec.Ref) || l.Started != rec.Created)
		})
		if d.Leases[lid] != nil { // the same record merged before, and since ended
			continue
		}
		d.Leases[lid] = &Lease{ID: lid, Owner: rec.Owner, OwnerRef: rec.OwnerRef, Pane: pid, Ref: rec.Ref, Harness: rec.Harness,
			Label: rec.Label, Cwd: rec.Cwd, Project: rec.Cwd, Root: lid, Depth: 1, TreeMax: defaultTreeMax,
			State: "active", Launched: true, Dialog: rec.Dialog, AnsweredAt: rec.AnsweredAt, Worked: rec.Worked, Started: rec.Created}
		if c := d.Created[pid]; c != nil && c.ForLease == "" {
			c.ForLease = lid
		}
	}
	// Pending results and notices, anchored to a lease. One about nothing that is left is dropped.
	have := map[string]bool{}
	for _, n := range d.Notices {
		have[n.ID] = true
	}
	for i, p := range pending {
		nid := "n" + func() string {
			sum := sha256.Sum256([]byte(fmt.Sprintf("v2:pending:%s:%s:%.6f:%d", p.Task, p.Pane, p.Created, i)))
			return hex.EncodeToString(sum[:])[:6]
		}()
		if have[nid] {
			continue
		}
		lid := ""
		if j := d.Jobs[p.Task]; p.Task != "" && j != nil {
			lid = j.Lease
		} else if p.Pane != "" {
			if l := activeOn(d, p.Pane, nil); l != nil {
				lid = l.ID
			}
		}
		if lid == "" {
			continue
		}
		kind := p.Kind
		if kind == "" {
			kind = "result"
		}
		d.Notices = append(d.Notices, &Notice{ID: nid, Lease: lid, Owner: p.Owner, OwnerRef: p.OwnerRef, Tag: p.Tag, Text: p.Text,
			Kind: kind, Dialog: p.Dialog, Created: p.Created, Claimed: p.Claimed, ClaimPid: p.ClaimPid, Unknown: p.Unknown})
		have[nid] = true
	}
}

// activeOn is the active lease on a pane whose server matches ref (refsMatch: an empty ref matches any),
// the first by ID when, against the rule, there are several.
func activeOn(d *Data, pane string, ref map[string]string) *Lease {
	var out *Lease
	for _, l := range d.Leases {
		if l.State == "active" && l.Pane == pane && pane != "" && refsMatch(l.Ref, ref) && (out == nil || l.ID < out.ID) {
			out = l
		}
	}
	return out
}
