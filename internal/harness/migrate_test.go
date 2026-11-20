package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/term"
)

// v2Fixture is a version 2 registry as v0.10.4 wrote it, with the cases a migration can get wrong: an empty
// and a null ref, unknown keys at every level, a legacy task (root ""), an adopted pane, one pane reused by
// two tasks, a lost, an exited, a deferred and a reserved task, a pane-form pending notice, and a pane mark
// beside a task that does not run.
func v2Fixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/registry-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// writeRegistry puts raw bytes where a registry under a fresh state directory is read.
func writeRegistry(t *testing.T, raw []byte) *Registry {
	t.Helper()
	r := NewRegistry(filepath.Join(t.TempDir(), "state"))
	if err := os.MkdirAll(r.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.Path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return r
}

func mustRead(t *testing.T, r *Registry) *Data {
	t.Helper()
	d, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func backups(t *testing.T, r *Registry) []string {
	t.Helper()
	got, _ := filepath.Glob(filepath.Join(r.Root, backupPrefix+"*.json"))
	sort.Strings(got)
	return got
}

// The task JSON that task ls --all --json and task show --json print is the same, key for key and byte for
// byte, before (v0.10.4, testdata/tasks-v0.10.4.golden.json, made by that build from the same fixture) and
// after the migration.
func TestTaskViewOfMigratedV2IsByteIdenticalToV0104(t *testing.T) {
	d := mustRead(t, writeRegistry(t, v2Fixture(t)))
	raw, err := os.ReadFile("testdata/tasks-v0.10.4.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]json.RawMessage
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden) != len(d.Jobs) {
		t.Fatalf("%d tasks, want %d", len(d.Jobs), len(golden))
	}
	for id, want := range golden {
		var w bytes.Buffer
		json.Compact(&w, want)
		got, err := json.Marshal(d.TaskView(id))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, w.Bytes()) {
			t.Errorf("task %s:\nv0.10.4 %s\nv3      %s", id, w.Bytes(), got)
		}
	}
	// And through a write and a read: what the next Update stores prints the same.
	r := writeRegistry(t, v2Fixture(t))
	if err := r.Update(func(*Data) error { return nil }); err != nil {
		t.Fatal(err)
	}
	d2 := mustRead(t, r)
	for id := range golden {
		a, _ := json.Marshal(d.TaskView(id))
		b, _ := json.Marshal(d2.TaskView(id))
		if !bytes.Equal(a, b) {
			t.Errorf("task %s changed across a write:\n%s\n%s", id, a, b)
		}
	}
}

// Field by field: tasks become a job and a lease, pane records a creation record (never for an adopted pane)
// and, for a pane no task lease holds, a bare lease; pending results become notices anchored to a lease.
// The version 2 keys go; a key no version knows stays.
func TestMigrateV2ToV3SplitsTasksPanesAndPendingCleanly(t *testing.T) {
	d := mustRead(t, writeRegistry(t, v2Fixture(t)))
	if d.Version != 3 || len(v2KeysIn(d.Unknown)) != 0 || string(d.Unknown["zz_data"]) != "true" {
		t.Fatalf("version %d, unknown %v", d.Version, d.Unknown)
	}
	if len(d.Jobs) != 9 {
		t.Fatalf("%d jobs", len(d.Jobs))
	}
	j1, l1 := d.Jobs["t000001"], taskLease(d, "t000001")
	if j1.Lease != l1.ID || l1.Job != "t000001" || l1.ID != derivedID("l", "v2:task:t000001", func(string) bool { return false }) {
		t.Fatalf("job %+v lease %+v", j1, l1)
	}
	if l1.Owner != "fake:9" || l1.Pane != "fake:5" || l1.Root != l1.ID || l1.Depth != 1 || l1.SubDepth != 1 || l1.State != "active" ||
		l1.Dialog == nil || l1.Progress == nil || l1.Worked != 1450 || l1.Project != "/w" || !l1.Launched {
		t.Fatalf("lease %+v", l1)
	}
	if j1.Title != "lead job" || j1.Brief != "/s/brief-1" || j1.Status != "running" || j1.Round != 1 || !j1.Owned || string(j1.Unknown["zz_future"]) != "1" {
		t.Fatalf("job %+v", j1)
	}
	l2 := taskLease(d, "t000002")
	if l2.Parent != l1.ID || l2.Root != l1.ID || l2.Stall == nil || l2.IdleSince != 1600 {
		t.Fatalf("child lease %+v", l2)
	}
	states := map[string]string{"t000003": "active", "t000004": "ended", "t000005": "active", "t000006": "lost", "t000007": "exited",
		"t000008": "active", "t000009": "ended"}
	for id, want := range states {
		if got := taskLease(d, id).State; got != want {
			t.Errorf("task %s: lease %s, want %s", id, got, want)
		}
	}
	if c := d.Created["fake:5"]; c == nil || c.ForLease != l1.ID || c.Label != "t000001" || string(c.Unknown["zz_rec"]) != `"r"` {
		t.Fatalf("created fake:5 %+v", c)
	}
	if d.Created["fake:7"] != nil {
		t.Fatal("an adopted pane got a creation record")
	}
	if c := d.Created["fake:8"]; c == nil || c.ForLease != "" {
		t.Fatalf("created fake:8 %+v: no task on it created it", c)
	}
	// The pane mark beside the ready legacy task is kept on its lease; its task JSON never showed it.
	if m := taskLease(d, "t000003").Dialog; m == nil || m.Fingerprint != "bbbb2222" || d.TaskView("t000003").Dialog != nil {
		t.Fatalf("pane mark %+v", m)
	}
	// fake:12 holds no task: a bare lease with its record's watch state.
	bare := paneLease(d, "fake:12")
	if bare == nil || bare.Job != "" || bare.Owner != "fake:9" || bare.Dialog == nil || bare.AnsweredAt != 1600 || bare.Label != "spare" ||
		bare.Root != bare.ID || bare.Depth != 1 || bare.TreeMax != defaultTreeMax || d.Created["fake:12"].ForLease != bare.ID {
		t.Fatalf("bare lease %+v", bare)
	}
	if len(d.Notices) != 3 {
		t.Fatalf("%d notices", len(d.Notices))
	}
	kinds := map[string]string{}
	for _, n := range d.Notices {
		kinds[n.Kind] = n.Lease
	}
	if kinds["result"] != taskLease(d, "t000007").ID || kinds["dialog"] != bare.ID || kinds["progress"] != l2.ID {
		t.Fatalf("notices %v", kinds)
	}
	for _, n := range d.Notices {
		if n.Kind == "progress" && (n.Claimed != 1660 || n.ClaimPid != 4242 || n.Unknown["zz_p"] == nil) {
			t.Fatalf("progress notice %+v", n)
		}
	}
}

// The first write over a file from before version 3 keeps an exact copy of it beside it, before anything
// is written; never a second; and refuses to write at all when the copy cannot be made.
func TestMigrateWritesV2BackupBeforeFirstV3Write(t *testing.T) {
	raw := v2Fixture(t)
	r := writeRegistry(t, raw)
	if _, err := r.Read(); err != nil || len(backups(t, r)) != 0 {
		t.Fatal("a read made a copy", err)
	}
	if err := r.Update(func(*Data) error { return nil }); err != nil {
		t.Fatal(err)
	}
	got := backups(t, r)
	if len(got) != 1 {
		t.Fatalf("copies %v", got)
	}
	if b, _ := os.ReadFile(got[0]); !bytes.Equal(b, raw) {
		t.Fatal("the copy is not the file as it was")
	}
	if now, _ := os.ReadFile(r.Path); bytes.Contains(now, []byte(`"tasks":`)) || !bytes.Contains(now, []byte(`"version": 3`)) {
		t.Fatalf("not migrated on disk: %.200s", now)
	}
	if err := r.Update(func(*Data) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if again := backups(t, r); len(again) != 1 || again[0] != got[0] {
		t.Fatalf("copies %v", again)
	}
	// state clean of a throwaway state directory takes the copy too.
	if err := CleanState(r.Root); err != nil {
		t.Fatal(err)
	}
	// A copy that cannot be made stops the write: the old file stays as it was.
	r2 := writeRegistry(t, raw)
	stubSync(t, func(f *os.File) error {
		if strings.Contains(filepath.Base(f.Name()), "backup") {
			return errors.New("disk full")
		}
		return f.Sync()
	})
	err := r2.Update(func(*Data) error { return nil })
	var te *term.Error
	if !errors.As(err, &te) || te.Code != term.CodeBackend {
		t.Fatalf("want a backend error, got %v", err)
	}
	if now, _ := os.ReadFile(r2.Path); !bytes.Equal(now, raw) || len(backups(t, r2)) != 0 {
		t.Fatal("written without its copy, or a partial copy left")
	}
}

// Migrating its own output changes nothing: a version 3 file read and written again is the same, byte for byte.
func TestMigrateIsIdempotentOnItsOwnOutput(t *testing.T) {
	r := writeRegistry(t, v2Fixture(t))
	if err := r.Update(func(*Data) error { return nil }); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(r.Path)
	for i := 0; i < 2; i++ {
		if err := r.Update(func(*Data) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	again, _ := os.ReadFile(r.Path)
	if !bytes.Equal(first, again) {
		t.Fatalf("a second migration changed the file:\n%s\n---\n%s", first, again)
	}
	// The same version 2 records merged twice add nothing either.
	d := mustRead(t, writeRegistry(t, v2Fixture(t)))
	before, _ := json.Marshal(d)
	var old map[string]json.RawMessage
	json.Unmarshal(v2Fixture(t), &old)
	d.Unknown = Unknown{"tasks": old["tasks"], "panes": old["panes"], "pending": old["pending"], "zz_data": old["zz_data"]}
	if err := migrate(d); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(d)
	if !bytes.Equal(before, after) {
		t.Fatalf("merging the same records again changed the data:\n%s\n---\n%s", before, after)
	}
}

// oldBinaryWrite is what a v0.10.x binary writes back after reading a version 3 file: it keeps the version
// 3 keys as fields it does not know, and writes its own maps beside them.
func oldBinaryWrite(t *testing.T, v3 []byte, version int, tasks, panes, pending string) []byte {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(v3, &doc); err != nil {
		t.Fatal(err)
	}
	doc["version"] = json.RawMessage(strings.TrimSpace(string(rune('0' + version))))
	doc["tasks"], doc["panes"], doc["pending"] = json.RawMessage(tasks), json.RawMessage(panes), json.RawMessage(pending)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A v0.10.x binary that wrote its own tasks, pane records and pending results into a migrated file (with
// "version": 2 or 3) has them merged in: no version 3 lease, scope or notice is lost, a job the version 3
// maps hold stays as they have it, and merging again adds nothing.
func TestMigrateMergesMixedVersionOldBinaryWritesWithoutLosingV3Leases(t *testing.T) {
	for _, version := range []int{2, 3} {
		r := writeRegistry(t, v2Fixture(t))
		if err := r.Update(func(d *Data) error {
			d.Scopes["s000001"] = &Scope{ID: "s000001", Name: "backend", Project: "/w"}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		v3, _ := os.ReadFile(r.Path)
		base := mustRead(t, r)
		tasks := `{"tnew001": {"id": "tnew001", "title": "from old", "harness": "generic", "pane": "fake:40", "ref": {"pid": "111"},
			"owner": "fake:9", "owner_ref": {"pid": "111"}, "cwd": "/w", "brief": "", "status": "running", "started": 5000, "ended": 0,
			"summary": "", "depth": 1, "notify": "none", "closed": false, "owned": true, "seen": false, "round": 1, "launched": true,
			"project": "/w", "root": "tnew001", "tree_max": 12},
			"t000001": {"id": "t000001", "title": "stale copy", "status": "lost", "pane": "fake:5"}}`
		panes := `{"fake:40": {"harness": "generic", "label": "tnew001", "cwd": "/w", "ref": {"pid": "111"}, "owner": "fake:9", "created": 5000},
			"fake:41": {"harness": "claude", "label": "side", "cwd": "/w", "ref": {"pid": "111"}, "owner": "fake:9", "created": 5001}}`
		pending := `[{"task": "tnew001", "owner": "fake:9", "owner_ref": {"pid": "111"}, "tag": "[t]", "text": "RE: task tnew001", "created": 5100, "claimed": 0}]`
		if err := os.WriteFile(r.Path, oldBinaryWrite(t, v3, version, tasks, panes, pending), 0o644); err != nil {
			t.Fatal(err)
		}
		for round := 0; round < 2; round++ { // the merge is written, then read and written again
			if err := r.Update(func(*Data) error { return nil }); err != nil {
				t.Fatal(err)
			}
		}
		d := mustRead(t, r)
		for id, l := range base.Leases {
			got := d.Leases[id]
			a, _ := json.Marshal(l)
			b, _ := json.Marshal(got)
			if !bytes.Equal(a, b) {
				t.Errorf("version %d: lease %s changed:\n%s\n%s", version, id, a, b)
			}
		}
		if d.Scopes["s000001"] == nil || d.Jobs["t000001"].Title != "lead job" || d.Jobs["t000001"].Status != "running" {
			t.Fatalf("version %d: a version 3 record was lost or replaced", version)
		}
		if len(d.Jobs) != len(base.Jobs)+1 || d.Jobs["tnew001"] == nil || taskLease(d, "tnew001").State != "active" {
			t.Fatalf("version %d: %d jobs", version, len(d.Jobs))
		}
		if d.Created["fake:40"] == nil || d.Created["fake:40"].ForLease != taskLease(d, "tnew001").ID {
			t.Fatalf("version %d: created %+v", version, d.Created["fake:40"])
		}
		if l := paneLease(d, "fake:41"); l == nil || l.Job != "" || l.Label != "side" {
			t.Fatalf("version %d: bare lease %+v", version, l)
		}
		if len(d.Leases) != len(base.Leases)+2 || len(d.Notices) != len(base.Notices)+1 {
			t.Fatalf("version %d: %d leases (want %d), %d notices (want %d)", version, len(d.Leases), len(base.Leases)+2, len(d.Notices), len(base.Notices)+1)
		}
		raw, _ := os.ReadFile(r.Path)
		var top map[string]json.RawMessage
		json.Unmarshal(raw, &top)
		for _, k := range v2Keys {
			if _, ok := top[k]; ok {
				t.Fatalf("version %d: version 2 key %q left", version, k)
			}
		}
	}
}

// An adopted pane's record makes no creation record: such a pane stays unclosable without --force.
func TestMigrateV2AdoptedPaneNotWrittenToCreated(t *testing.T) {
	r := newRig(t)
	os.MkdirAll(r.reg.Root, 0o755)
	os.WriteFile(r.reg.Path, []byte(`{"version": 2, "tasks": {}, "pending": [],
		"panes": {"fake:1": {"harness": "generic", "label": "hand", "cwd": "/w", "ref": {}, "owner": "fake:9", "created": 1, "adopted": true}}}`), 0o644)
	d := r.data()
	if d.Created["fake:1"] != nil || paneLease(d, "fake:1") == nil || paneLease(d, "fake:1").Owner != r.owner.ID {
		t.Fatalf("created %+v lease %+v", d.Created["fake:1"], paneLease(d, "fake:1"))
	}
	_, err := r.tasks.ClosePane("fake:1", false)
	wantCode(t, err, 6)
	if r.b.Has("close", "fake:1") {
		t.Fatal("closed an adopted pane without --force")
	}
}

// A task from before the caps (root "") keeps root "" on its lease and the old rule: it may start tasks
// down to the depth ceiling.
func TestMigrateV2PreservesLegacyRootEmptyCaps(t *testing.T) {
	d := mustRead(t, writeRegistry(t, v2Fixture(t)))
	l := taskLease(d, "t000003")
	if l.Root != "" || d.TaskView("t000003").Root != "" {
		t.Fatalf("root %q", l.Root)
	}
	if c := capsOf(l, 4); c.sub != 3 || c.root != l.ID || c.treeMax != defaultTreeMax {
		t.Fatalf("caps %+v", c)
	}
}

// One pane, several tasks: at most one active lease, the open task started last; the pane's record joins
// that lease rather than making a second one.
func TestMigrateV2MultipleTasksOnSamePaneLeavesOneActiveLease(t *testing.T) {
	raw := `{"version": 2, "panes": {"fake:8": {"harness": "generic", "label": "w", "ref": {}, "owner": "fake:9", "created": 1, "worked": 9}},
		"pending": [], "tasks": {
		"ta": {"id": "ta", "pane": "fake:8", "ref": {}, "owner": "fake:9", "status": "ready", "started": 10, "round": 1},
		"tb": {"id": "tb", "pane": "fake:8", "ref": {}, "owner": "fake:9", "status": "running", "started": 20, "round": 1},
		"tc": {"id": "tc", "pane": "fake:8", "ref": {}, "owner": "fake:9", "status": "done", "started": 30, "round": 1},
		"td": {"id": "td", "pane": "fake:8", "ref": {}, "owner": "fake:9", "status": "running", "started": 40, "round": 1, "closed": true}}}`
	d := mustRead(t, writeRegistry(t, []byte(raw)))
	var active []string
	for _, l := range d.Leases {
		if l.State == "active" && l.Pane == "fake:8" {
			active = append(active, l.Job)
		}
	}
	if len(active) != 1 || active[0] != "tb" || len(d.Leases) != 4 || taskLease(d, "tb").Worked != 9 {
		t.Fatalf("active %v, %d leases", active, len(d.Leases))
	}
}

// The file a version 3 binary writes has no version 2 key: an older binary reading it finds no task, pane
// record or pending result to act on, so it delivers nothing twice (FlushPending reads "pending").
func TestV3NoticesKeyInvisibleToV2FlushPending(t *testing.T) {
	r := writeRegistry(t, v2Fixture(t))
	if err := r.Update(func(*Data) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	raw, _ := os.ReadFile(r.Path)
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"tasks", "panes", "pending"} {
		if _, ok := doc[k]; ok {
			t.Errorf("key %q written", k)
		}
	}
	if !bytes.Contains(doc["notices"], []byte("RE: task t000007")) {
		t.Fatal("the pending result is not a notice")
	}
}
