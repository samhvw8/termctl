package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	// AnsweredAt is when task answer or deny last pressed a key on the worker's dialog. A screen read
	// taken before it is stale, and is dropped.
	AnsweredAt float64 `json:"answered_at,omitempty"`
}

// DialogMark is the dialog on a worker's screen. It is dropped when the worker's input line is seen again
// with no dialog, so the same request asked again later is new again.
type DialogMark struct {
	Fingerprint string  `json:"fingerprint"`
	Question    string  `json:"question"`
	Since       float64 `json:"since"`
	Notified    bool    `json:"notified,omitempty"` // the owner was sent a notice of it (watch)
	Returned    bool    `json:"returned,omitempty"` // task wait has returned it once
}

// PaneRecord marks a pane as created by harness-ops, so close may act on it.
type PaneRecord struct {
	Harness string            `json:"harness"`
	Label   string            `json:"label"`
	Cwd     string            `json:"cwd"`
	Ref     map[string]string `json:"ref"`
	Owner   string            `json:"owner"`
	Created float64           `json:"created"`
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
	Kind     string            `json:"kind,omitempty"`        // "" for a task result; "dialog" for a dialog notice
	Dialog   string            `json:"fingerprint,omitempty"` // the dialog a "dialog" notice is about
}

// Data is the whole registry file.
type Data struct {
	Version int                    `json:"version"`
	Tasks   map[string]*Task       `json:"tasks"`
	Panes   map[string]*PaneRecord `json:"panes"`
	Pending []*Pending             `json:"pending"`
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
	return name == "registry.json" || name == "registry.lock" || name == "briefs" || name == "watch" || strings.HasPrefix(name, ".registry-")
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
