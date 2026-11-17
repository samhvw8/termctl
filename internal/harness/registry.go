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

// Task is one harness session doing one job.
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
	// AnsweredAt is when answer or deny last pressed a key on the worker's dialog. A screen read
	// taken before it is stale, and is dropped.
	AnsweredAt float64 `json:"answered_at,omitempty"`
	// Unreachable is when the task's server first failed to answer, 0 while it answers. A server that
	// stays unreachable past unreachableTTL is gone, and the task is lost.
	Unreachable float64 `json:"unreachable,omitempty"`
	Project     string  `json:"project,omitempty"` // the owner's folder at task new: where the journal files it
	Unknown     Unknown `json:"-"`
}

// DialogMark is the dialog on a worker's screen. It is dropped when the worker's input line is seen again
// with no dialog, so the same request asked again later is new again.
type DialogMark struct {
	Fingerprint string  `json:"fingerprint"`
	Question    string  `json:"question"`
	Since       float64 `json:"since"`
	Notified    bool    `json:"notified,omitempty"` // the owner was sent a notice of it (watch)
	Returned    bool    `json:"returned,omitempty"` // task wait has returned it once
	Unknown     Unknown `json:"-"`
}

// PaneRecord marks a pane as created by harness-ops (spawn or task new), so close may act on it, and
// its owner may answer its dialogs. Ref is the pane's server; OwnerRef the owner pane's server, since the
// same pane ID on another server is another pane (empty in records from before 2026-11-18: any server).
type PaneRecord struct {
	Harness  string            `json:"harness"`
	Label    string            `json:"label"`
	Cwd      string            `json:"cwd"`
	Ref      map[string]string `json:"ref"`
	Owner    string            `json:"owner"`
	OwnerRef map[string]string `json:"owner_ref,omitempty"`
	Created  float64           `json:"created"`
	// Dialog is the dialog on the pane when the watcher last read it, while no running task holds the pane
	// (then the mark is the task's). AnsweredAt is when answer or deny last pressed a key on it: a screen
	// read taken before is stale.
	Dialog     *DialogMark `json:"dialog,omitempty"`
	AnsweredAt float64     `json:"answered_at,omitempty"`
	// Worked is when the owner last gave the pane work (spawn with a prompt, msg): a watcher counts it
	// working for workGrace, since a harness takes a moment to look busy.
	Worked  float64 `json:"worked,omitempty"`
	Unknown Unknown `json:"-"`
}

// Pending is a task result that has not reached its owner yet.
type Pending struct {
	Task     string            `json:"task"`
	Owner    string            `json:"owner"`
	OwnerRef map[string]string `json:"owner_ref"`
	Tag      string            `json:"tag"`
	Text     string            `json:"text"`
	Created  float64           `json:"created"`
	Claimed  float64           `json:"claimed"`
	ClaimPid int               `json:"claim_pid,omitempty"`   // the claimer's process, on this host
	Kind     string            `json:"kind,omitempty"`        // "" for a task result; "dialog" for a dialog notice
	Dialog   string            `json:"fingerprint,omitempty"` // the dialog a "dialog" notice is about
	Pane     string            `json:"pane,omitempty"`        // a "dialog" notice of a pane no running task held: Task is ""
	Unknown  Unknown           `json:"-"`
}

// Data is the whole registry file.
type Data struct {
	Version int                    `json:"version"`
	Tasks   map[string]*Task       `json:"tasks"`
	Panes   map[string]*PaneRecord `json:"panes"`
	Pending []*Pending             `json:"pending"`
	Unknown Unknown                `json:"-"`
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

func (x *PaneRecord) UnmarshalJSON(b []byte) (err error) {
	type plain PaneRecord
	x.Unknown, err = decodeKeeping(b, (*plain)(x))
	return err
}

func (x PaneRecord) MarshalJSON() ([]byte, error) {
	type plain PaneRecord
	return encodeKeeping(plain(x), x.Unknown)
}

func (x *Pending) UnmarshalJSON(b []byte) (err error) {
	type plain Pending
	x.Unknown, err = decodeKeeping(b, (*plain)(x))
	return err
}

func (x Pending) MarshalJSON() ([]byte, error) {
	type plain Pending
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

const registryVersion = 2

func emptyData() *Data {
	return &Data{Version: registryVersion, Tasks: map[string]*Task{}, Panes: map[string]*PaneRecord{}, Pending: []*Pending{}}
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

// Read returns a consistent snapshot: writers replace the file atomically, so no lock is needed.
func (r *Registry) Read() (*Data, error) {
	raw, err := os.ReadFile(r.Path)
	if errors.Is(err, os.ErrNotExist) {
		return emptyData(), nil
	}
	if err != nil {
		return nil, term.BackendErr("task registry: %v", err)
	}
	d := emptyData()
	if err := json.Unmarshal(raw, d); err != nil {
		// Resetting would silently forget running tasks.
		return nil, term.BackendErr("task registry is corrupt: %s: %v", r.Path, err)
	}
	if d.Tasks == nil {
		d.Tasks = map[string]*Task{}
	}
	if d.Panes == nil {
		d.Panes = map[string]*PaneRecord{}
	}
	migrate(d)
	return d, nil
}

// migrate brings an older file up to date. Tasks from version 1 were all launched, with one round.
func migrate(d *Data) {
	if d.Version >= registryVersion {
		return
	}
	for _, x := range d.Tasks {
		if x.Round == 0 {
			x.Round = 1
		}
		x.Launched = x.Pane != ""
	}
	d.Version = registryVersion
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
	d, err := r.Read()
	if err != nil {
		return err
	}
	if err := fn(d); err != nil {
		return err
	}
	return r.write(d)
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
		name == journalFile || name == journalLock || name == projectsDir
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
