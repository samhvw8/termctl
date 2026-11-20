package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/samhvw8/termctl/internal/term"
)

// Task is the CLI's and the public API's view of one job: its Job, the Lease that supervises it and the
// CreatedPane record of its pane, assembled by Data.TaskView. It is output only: never stored, and no engine
// code reads or writes through it. Its JSON is the v0.10.4 task object, key for key.
type Task struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Harness  string            `json:"harness"`
	Pane     string            `json:"pane"`
	Ref      map[string]string `json:"ref"`
	Owner    string            `json:"owner"`
	OwnerRef map[string]string `json:"owner_ref"`
	Cwd      string            `json:"cwd"`
	Brief    string            `json:"brief"`
	Status   string            `json:"status"`
	Started  float64           `json:"started"`
	Ended    float64           `json:"ended"`
	Summary  string            `json:"summary"`
	Depth    int               `json:"depth"`
	Notify   string            `json:"notify"`
	Closed   bool              `json:"closed"`
	Owned    bool              `json:"owned"`
	Seen     bool              `json:"seen"`
	Round    int               `json:"round"`    // follow-up rounds in the same session, from 1
	Launched bool              `json:"launched"` // the brief is in: a bare shell now means the harness is gone
	ExitCode *int              `json:"exit_code,omitempty"`
	ReviewOf string            `json:"review_of,omitempty"` // the task this one reviews
	Blind    bool              `json:"blind,omitempty"`     // a reviewer that must not see the worker's report
	Dialog   *DialogMark       `json:"dialog,omitempty"`    // the dialog on the worker's screen when last read
	// AnsweredAt is when answer or deny last pressed a key on the worker's dialog.
	AnsweredAt float64 `json:"answered_at,omitempty"`
	// Unreachable is when the task's server first failed to answer, 0 while it answers.
	Unreachable float64 `json:"unreachable,omitempty"`
	Project     string  `json:"project,omitempty"` // the owner's folder at task new: where the journal files it
	// Parent is the task the owner pane held when it started this one, "" for none.
	Parent string `json:"parent,omitempty"`
	// Progress is the worker's latest task progress line.
	Progress  *ProgressNote `json:"progress,omitempty"`
	IdleSince float64       `json:"idle_since,omitempty"`
	Stall     *StallMark    `json:"stall,omitempty"`
	Defer     bool          `json:"defer,omitempty"`
	AckedAt   float64       `json:"acked_at,omitempty"`
	AckNote   string        `json:"ack_note,omitempty"`
	Denied    int           `json:"denied,omitempty"`
	// Root is the top task of this task's tree (its own ID at depth 1); "" on tasks from before 2026-11-19.
	Root     string  `json:"root,omitempty"`
	SubDepth int     `json:"sub_depth,omitempty"`
	TreeMax  int     `json:"tree_max,omitempty"`
	Orphaned float64 `json:"orphaned,omitempty"`
	// Restarted is set by Reconcile when an exited task's pane is running its harness again.
	Restarted bool `json:"restarted,omitempty"`
	// Unknown is the job's fields this binary does not know, printed as they were read.
	Unknown Unknown `json:"-"`
}

// ProgressNote is a worker's latest progress line on its task.
type ProgressNote struct {
	Text    string  `json:"text"`
	At      float64 `json:"at"`
	Unknown Unknown `json:"-"`
}

// StallMark records that a running task's worker looked idle for stallAfter with no done and no
// progress, and that its owner was sent one notice of it. It goes when the worker is seen at work again,
// and on progress or done.
type StallMark struct {
	At      float64 `json:"at"`
	Unknown Unknown `json:"-"`
}

// DialogMark is the dialog on a worker's screen. It is dropped when the worker's input line is seen again
// with no dialog, so the same request asked again later is new again.
type DialogMark struct {
	Fingerprint string  `json:"fingerprint"`
	Question    string  `json:"question"`
	Since       float64 `json:"since"`
	Notified    bool    `json:"notified,omitempty"`  // the owner was sent a notice of it (watch)
	Returned    bool    `json:"returned,omitempty"`  // task wait has returned it once
	Escalated   bool    `json:"escalated,omitempty"` // the lead above the task's owner was told it waits (escalation)
	Unknown     Unknown `json:"-"`
}

// CreatedPane records that harness-ops created a pane (spawn, or task new without --reuse), so close may
// act on it. Creation provenance only: an adopted pane, or one a task reused, has none, and is never
// closable without --force. Ref is the pane's server: the same pane ID on another server is another pane.
type CreatedPane struct {
	Ref       map[string]string `json:"ref"`
	Harness   string            `json:"harness"`
	Label     string            `json:"label,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	ForLease  string            `json:"for_lease,omitempty"` // the Lease it was created for: only that lease's end closes it
	CreatedAt float64           `json:"created_at"`
	Unknown   Unknown           `json:"-"`
}

// Lease is one supervision lifetime of one pane by one lead: who owns the pane, and what the watcher
// learned about it (dialog, idle time, stall, progress). At most one Lease is active per pane and server.
// It carries a Job (a task), or nothing (a bare spawned pane). Ref and OwnerRef keep the v0.10.4 task's
// null-or-object form, so a task's JSON does not change across the migration.
type Lease struct {
	ID       string            `json:"id"` // "l" + 6 hex
	Owner    string            `json:"owner"`
	OwnerRef map[string]string `json:"owner_ref"`
	Pane     string            `json:"pane,omitempty"` // "" only while task new reserves before the spawn
	Ref      map[string]string `json:"ref"`
	Harness  string            `json:"harness,omitempty"`
	Label    string            `json:"label,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
	Project  string            `json:"project,omitempty"` // the lead's folder: where the journal files it

	// Parent is the lead's own Lease when it started this one ("" for a root); Root the top Lease of the
	// tree (self at depth 1; "" only for a task from before 2026-11-19, which keeps the old depth rule).
	Parent   string `json:"parent,omitempty"`
	Root     string `json:"root,omitempty"`
	Depth    int    `json:"depth"`
	SubDepth int    `json:"sub_depth,omitempty"` // levels below this pane may start (0: a leaf)
	TreeMax  int    `json:"tree_max,omitempty"`  // running jobs allowed in this tree

	Job   string `json:"job,omitempty"`   // d.Jobs[Job]
	Scope string `json:"scope,omitempty"` // d.Scopes[Scope]

	State       string        `json:"state"` // "active", "exited", "lost", "ended"
	Launched    bool          `json:"launched,omitempty"`
	Dialog      *DialogMark   `json:"dialog,omitempty"`
	AnsweredAt  float64       `json:"answered_at,omitempty"`
	Worked      float64       `json:"worked,omitempty"`
	IdleSince   float64       `json:"idle_since,omitempty"`
	Stall       *StallMark    `json:"stall,omitempty"`
	Progress    *ProgressNote `json:"progress,omitempty"`
	Unreachable float64       `json:"unreachable,omitempty"`
	Orphaned    float64       `json:"orphaned,omitempty"`
	Restarted   bool          `json:"restarted,omitempty"`
	Told        bool          `json:"told,omitempty"`
	Started     float64       `json:"started"`
	Ended       float64       `json:"ended,omitempty"`
	Unknown     Unknown       `json:"-"`
}

// Job is one bounded work assignment: what was asked, its status and its result. Who supervises it, and
// where, is its Lease.
type Job struct {
	ID       string  `json:"id"`    // "t" + 6 hex
	Lease    string  `json:"lease"` // d.Leases[Lease]
	Title    string  `json:"title"`
	Brief    string  `json:"brief"`
	Status   string  `json:"status"` // running, ready, failed, blocked, done, exited, lost
	Round    int     `json:"round"`
	Summary  string  `json:"summary"`
	Notify   string  `json:"notify"` // none, pending, delivered, seen, expired, owner-gone
	Closed   bool    `json:"closed"`
	// Owned is historical output-only provenance ("this job created its pane") for TaskView JSON after
	// CreatedPane is deleted; safety gates always check d.Created exclusively.
	Owned    bool    `json:"owned,omitempty"`
	Seen     bool    `json:"seen,omitempty"`
	ExitCode *int    `json:"exit_code,omitempty"`
	ReviewOf string  `json:"review_of,omitempty"`
	Blind    bool    `json:"blind,omitempty"`
	Defer    bool    `json:"defer,omitempty"`
	AckedAt  float64 `json:"acked_at,omitempty"`
	AckNote  string  `json:"ack_note,omitempty"`
	Denied   int     `json:"denied,omitempty"`
	Started  float64 `json:"started"`
	Ended    float64 `json:"ended"`
	Unknown  Unknown `json:"-"`
}

// Scope is a durable workstream identity (attach). Nothing in this build makes one yet; the registry keeps them.
type Scope struct {
	ID         string  `json:"id"` // "s" + 6 hex
	Name       string  `json:"name"`
	About      string  `json:"about,omitempty"`
	Project    string  `json:"project"`
	Lease      string  `json:"lease,omitempty"`
	StallAfter float64 `json:"stall_after,omitempty"`
	Dropped    bool    `json:"dropped,omitempty"`
	Created    float64 `json:"created"`
	Unknown    Unknown `json:"-"`
}

// Notice is a message to a lead that has not reached it yet, about one Lease: a task result ("result"), or
// a "dialog", "progress", "stall", "escalate" or "adopt" notice.
type Notice struct {
	ID       string            `json:"id"`    // "n" + 6 hex
	Lease    string            `json:"lease"` // d.Leases[Lease]
	Owner    string            `json:"owner"`
	OwnerRef map[string]string `json:"owner_ref,omitempty"`
	Tag      string            `json:"tag"`
	Text     string            `json:"text"`
	Kind     string            `json:"kind"`
	Dialog   string            `json:"fingerprint,omitempty"` // the dialog a "dialog" or "escalate" notice is about
	Created  float64           `json:"created"`
	Claimed  float64           `json:"claimed,omitempty"`
	ClaimPid int               `json:"claim_pid,omitempty"` // the claimer's process, on this host
	Unknown  Unknown           `json:"-"`
}

// Data is the whole registry file, version 3. The version 2 keys ("tasks", "panes", "pending") are gone:
// an older binary reading this file sees no task, no pane record and no pending result, and keeps these
// keys as fields it does not know. migrate merges what such a binary writes back in.
type Data struct {
	Version int                     `json:"version"`
	Created map[string]*CreatedPane `json:"created"`
	Leases  map[string]*Lease       `json:"leases"`
	Jobs    map[string]*Job         `json:"jobs"`
	Scopes  map[string]*Scope       `json:"scopes"`
	Notices []*Notice               `json:"notices"`
	Unknown Unknown                 `json:"-"`
}

// Unknown holds the fields of a registry record that this binary does not know, as they were read. They
// are written back unchanged, so a newer binary's fields survive a rewrite by an older one: a version
// number does not protect them, since every version from 2 on is read (round-6 review R1).
type Unknown map[string]json.RawMessage

func (x *Data) UnmarshalJSON(b []byte) (err error) {
	type plain Data
	x.Unknown, err = decodeKeeping(b, (*plain)(x))
	return err
}

func (x Data) MarshalJSON() ([]byte, error) {
	type plain Data
	return encodeKeeping(plain(x), x.Unknown)
}

func (x *Task) UnmarshalJSON(b []byte) (err error) {
	type plain Task
	x.Unknown, err = decodeKeeping(b, (*plain)(x))
	return err
}

func (x Task) MarshalJSON() ([]byte, error) {
	type plain Task
	return encodeKeeping(plain(x), x.Unknown)
}

func (x *DialogMark) UnmarshalJSON(b []byte) (err error) {
	type plain DialogMark
	x.Unknown, err = decodeKeeping(b, (*plain)(x))
	return err
}

func (x DialogMark) MarshalJSON() ([]byte, error) {
	type plain DialogMark
	return encodeKeeping(plain(x), x.Unknown)
}

func (x *ProgressNote) UnmarshalJSON(b []byte) (err error) {
	type plain ProgressNote
	x.Unknown, err = decodeKeeping(b, (*plain)(x))
	return err
}

func (x ProgressNote) MarshalJSON() ([]byte, error) {
	type plain ProgressNote
	return encodeKeeping(plain(x), x.Unknown)
}

func (x *StallMark) UnmarshalJSON(b []byte) (err error) {
	type plain StallMark
	x.Unknown, err = decodeKeeping(b, (*plain)(x))
	return err
}

func (x StallMark) MarshalJSON() ([]byte, error) {
	type plain StallMark
	return encodeKeeping(plain(x), x.Unknown)
}

func (x *CreatedPane) UnmarshalJSON(b []byte) (err error) {
	type plain CreatedPane
	x.Unknown, err = decodeKeeping(b, (*plain)(x))
	return err
}

func (x CreatedPane) MarshalJSON() ([]byte, error) {
	type plain CreatedPane
	return encodeKeeping(plain(x), x.Unknown)
}

func (x *Lease) UnmarshalJSON(b []byte) (err error) {
	type plain Lease
	x.Unknown, err = decodeKeeping(b, (*plain)(x))
	return err
}

func (x Lease) MarshalJSON() ([]byte, error) {
	type plain Lease
	return encodeKeeping(plain(x), x.Unknown)
}

func (x *Job) UnmarshalJSON(b []byte) (err error) {
	type plain Job
	x.Unknown, err = decodeKeeping(b, (*plain)(x))
	return err
}

func (x Job) MarshalJSON() ([]byte, error) {
	type plain Job
	return encodeKeeping(plain(x), x.Unknown)
}

func (x *Scope) UnmarshalJSON(b []byte) (err error) {
	type plain Scope
	x.Unknown, err = decodeKeeping(b, (*plain)(x))
	return err
}

func (x Scope) MarshalJSON() ([]byte, error) {
	type plain Scope
	return encodeKeeping(plain(x), x.Unknown)
}

func (x *Notice) UnmarshalJSON(b []byte) (err error) {
	type plain Notice
	x.Unknown, err = decodeKeeping(b, (*plain)(x))
	return err
}

func (x Notice) MarshalJSON() ([]byte, error) {
	type plain Notice
	return encodeKeeping(plain(x), x.Unknown)
}

// decodeKeeping decodes an object into a struct (a type without its own UnmarshalJSON) and returns the
// fields no struct field takes. encoding/json matches keys without regard to case, so this does too.
func decodeKeeping[T any](b []byte, into *T) (Unknown, error) {
	if err := json.Unmarshal(b, into); err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	known := jsonKeys(reflect.TypeFor[T]())
	var out Unknown
	for k, v := range all {
		if !known[strings.ToLower(k)] {
			if out == nil {
				out = Unknown{}
			}
			out[k] = v
		}
	}
	return out, nil
}

// jsonKeys are the lower-cased JSON names of a struct's fields.
func jsonKeys(t reflect.Type) map[string]bool {
	keys := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" || !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		keys[strings.ToLower(name)] = true
	}
	return keys
}

// encodeKeeping encodes v (a type without its own MarshalJSON) and appends the unknown fields, in key
// order, after the known ones.
func encodeKeeping(v any, extra Unknown) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return b, err
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out bytes.Buffer
	out.Write(b[:len(b)-1]) // without the closing brace
	for _, k := range keys {
		if out.Len() > 1 {
			out.WriteByte(',')
		}
		name, _ := json.Marshal(k)
		out.Write(name)
		out.WriteByte(':')
		out.Write(extra[k])
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

const registryVersion = 3

func emptyData() *Data {
	return &Data{Version: registryVersion, Created: map[string]*CreatedPane{}, Leases: map[string]*Lease{}, Jobs: map[string]*Job{},
		Scopes: map[string]*Scope{}, Notices: []*Notice{}}
}

// Registry is one JSON file, atomic writes, and one exclusive lock around each update.
type Registry struct{ Root, Path string }

// liveRoot is the real state directory. It holds the registry of tasks other agents are running.
func liveRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "termctl")
}

// DefaultRoot is TERMCTL_STATE_DIR, else the live directory. A test binary can never get the live one.
func DefaultRoot() string {
	if d := os.Getenv("TERMCTL_STATE_DIR"); d != "" {
		return d
	}
	if testing.Testing() {
		panic("tests must set TERMCTL_STATE_DIR: the default state directory is live data for other agents")
	}
	return liveRoot()
}

func NewRegistry(root string) *Registry {
	if root == "" {
		root = DefaultRoot()
	}
	return &Registry{root, filepath.Join(root, "registry.json")}
}

// Read returns a consistent snapshot: writers replace the file atomically, so no lock is needed. An older
// file, or one an older binary wrote into, is migrated in memory; the next Update writes it back.
func (r *Registry) Read() (*Data, error) {
	raw, err := os.ReadFile(r.Path)
	return r.decode(raw, err)
}

func (r *Registry) decode(raw []byte, readErr error) (*Data, error) {
	if errors.Is(readErr, os.ErrNotExist) {
		return emptyData(), nil
	}
	if readErr != nil {
		return nil, term.BackendErr("task registry: %v", readErr)
	}
	d := emptyData()
	if err := json.Unmarshal(raw, d); err != nil {
		// Resetting would silently forget running tasks.
		return nil, term.BackendErr("task registry is corrupt: %s: %v", r.Path, err)
	}
	if d.Created == nil {
		d.Created = map[string]*CreatedPane{}
	}
	if d.Leases == nil {
		d.Leases = map[string]*Lease{}
	}
	if d.Jobs == nil {
		d.Jobs = map[string]*Job{}
	}
	if d.Scopes == nil {
		d.Scopes = map[string]*Scope{}
	}
	if d.Notices == nil {
		d.Notices = []*Notice{}
	}
	if err := migrate(d); err != nil {
		return nil, term.BackendErr("task registry is corrupt: %s: %v", r.Path, err)
	}
	for id, j := range d.Jobs {
		if j == nil || d.Leases[j.Lease] == nil {
			return nil, term.BackendErr("task registry is corrupt: %s: job %s has no lease", r.Path, id)
		}
	}
	for id, l := range d.Leases {
		if l == nil {
			return nil, term.BackendErr("task registry is corrupt: %s: lease %s is null", r.Path, id)
		}
	}
	return d, nil
}

// LockTimeout is how long Update waits for the registry lock. An update holds it for milliseconds, so a
// lock held this long belongs to a process that hangs: fail loudly rather than hang behind it.
var LockTimeout = 20 * time.Second

const lockRetry = 20 * time.Millisecond

// Update holds the lock, runs fn on the data, and writes it back only if fn succeeds.
func (r *Registry) Update(fn func(*Data) error) error {
	if err := os.MkdirAll(r.Root, 0o755); err != nil {
		return term.BackendErr("task registry: %v", err)
	}
	path := filepath.Join(r.Root, "registry.lock")
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return term.BackendErr("task registry: %v", err)
	}
	defer lock.Close()
	deadline := time.Now().Add(LockTimeout)
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return term.BackendErr("task registry lock: %v", err)
		}
		if time.Now().After(deadline) {
			return term.BackendErr("task registry lock: %s has been held by another process for %v; a harness-ops process may hang (find it with lsof %s)", path, LockTimeout, path)
		}
		time.Sleep(lockRetry)
	}
	raw, readErr := os.ReadFile(r.Path)
	d, err := r.decode(raw, readErr)
	if err != nil {
		return err
	}
	if err := fn(d); err != nil {
		return err
	}
	if err := r.backupOld(raw); err != nil {
		return err
	}
	return r.write(d)
}

// backupPrefix starts the name of the copy of a registry from before version 3, taken before the first
// version 3 write over it. Nothing deletes one but state clean of a throwaway state directory.
const backupPrefix = "registry.v2-backup-"

// backupOld copies raw (read under the registry lock), byte for byte, before the first write over a file
// from before version 3, unless a copy is there already. Only called once fn succeeds.
func (r *Registry) backupOld(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	var head struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(raw, &head) != nil || head.Version >= registryVersion {
		return nil
	}
	if old, _ := filepath.Glob(filepath.Join(r.Root, backupPrefix+"*.json")); len(old) > 0 {
		return nil
	}
	// Written in full and synced under a temporary name first: a copy that is there is a whole one.
	f, err := os.CreateTemp(r.Root, ".registry-backup-")
	if err != nil {
		return term.BackendErr("task registry: cannot back up %s before migrating it: %v", r.Path, err)
	}
	defer os.Remove(f.Name())
	_, err = f.Write(raw)
	if err == nil {
		err = syncFile(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return term.BackendErr("task registry: cannot back up %s before migrating it: %v", r.Path, err)
	}
	stamp := time.Now().Unix()
	for n := 1; ; n++ {
		name := fmt.Sprintf("%s%d.json", backupPrefix, stamp)
		if n > 1 {
			name = fmt.Sprintf("%s%d-%d.json", backupPrefix, stamp, n)
		}
		err := os.Link(f.Name(), filepath.Join(r.Root, name)) // fails on a name that is taken, as O_EXCL does
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return term.BackendErr("task registry: cannot back up %s before migrating it: %v", r.Path, err)
		}
		if dir, derr := os.Open(r.Root); derr == nil {
			if serr := syncFile(dir); serr != nil {
				Warn(fmt.Sprintf("task registry: backup %s created but syncing %s failed: %v", name, r.Root, serr))
			}
			dir.Close()
		}
		return nil
	}
}

// syncFile flushes a file or a directory to disk; tests replace it.
var syncFile = (*os.File).Sync

func (r *Registry) write(d *Data) error {
	data, err := json.MarshalIndent(d, "", " ")
	if err != nil {
		return term.BackendErr("task registry: %v", err)
	}
	f, err := os.CreateTemp(r.Root, ".registry-")
	if err != nil {
		return term.BackendErr("task registry: %v", err)
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = syncFile(f)
	}
	if cerr := f.Close(); err == nil { // closed on every path: a watcher writes for hours
		err = cerr
	}
	if err != nil {
		return term.BackendErr("task registry: %v", err)
	}
	if err := os.Rename(f.Name(), r.Path); err != nil {
		return term.BackendErr("task registry: %v", err)
	}
	// The rename is on disk only once the directory is: without this sync a crash can bring the old file
	// back. The update has happened either way, so a failed sync is a warning, not a failed update.
	dir, err := os.Open(r.Root)
	if err == nil {
		err = syncFile(dir)
		dir.Close()
	}
	if err != nil {
		Warn(fmt.Sprintf("task registry: the update is written but may not survive a crash: syncing %s: %v", r.Root, err))
	}
	return nil
}

// realPath resolves symlinks on the longest part of path that exists.
func realPath(path string) string {
	path = filepath.Clean(path)
	rest := ""
	for p := path; ; p = filepath.Dir(p) {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(resolved, rest)
		}
		if p == filepath.Dir(p) {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
	}
}

func isStateFile(name string) bool {
	return name == "registry.json" || name == "registry.lock" || name == "briefs" || name == "watch" || strings.HasPrefix(name, ".registry-") ||
		name == journalFile || name == journalLock || name == projectsDir ||
		(strings.HasPrefix(name, backupPrefix) && strings.HasSuffix(name, ".json"))
}

// CleanState removes a throwaway state directory. It refuses the live directory, anything
// containing it or inside it, and any directory that holds something other than termctl state.
func CleanState(root string) error {
	if root == "" {
		return term.Errf(term.CodeGuard, "set TERMCTL_STATE_DIR to the directory to clean; the default state directory is live data and is never cleaned")
	}
	target, live := realPath(root), realPath(liveRoot())
	if target == live || strings.HasPrefix(live+"/", strings.TrimSuffix(target, "/")+"/") || strings.HasPrefix(target, live+"/") {
		return term.Errf(term.CodeGuard, "refusing to clean %s: the live state directory holds other agents' tasks", root)
	}
	entries, err := os.ReadDir(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return term.BackendErr("state clean: %v", err)
	}
	for _, e := range entries {
		if !isStateFile(e.Name()) {
			return term.Errf(term.CodeGuard, "refusing to clean %s: %s is not termctl state", root, e.Name())
		}
	}
	if err := os.RemoveAll(target); err != nil {
		return term.BackendErr("state clean: %v", err)
	}
	return nil
}
