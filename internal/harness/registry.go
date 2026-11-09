package harness

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"

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

func DefaultRoot() string {
	if d := os.Getenv("TERMCTL_STATE_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "termctl")
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

// Update holds the lock, runs fn on the data, and writes it back only if fn succeeds.
func (r *Registry) Update(fn func(*Data) error) error {
	if err := os.MkdirAll(r.Root, 0o755); err != nil {
		return term.BackendErr("task registry: %v", err)
	}
	lock, err := os.OpenFile(filepath.Join(r.Root, "registry.lock"), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return term.BackendErr("task registry: %v", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return term.BackendErr("task registry lock: %v", err)
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
	if _, err = f.Write(data); err == nil {
		if err = f.Sync(); err == nil {
			err = f.Close()
		}
	}
	if err != nil {
		return term.BackendErr("task registry: %v", err)
	}
	if err := os.Rename(f.Name(), r.Path); err != nil {
		return term.BackendErr("task registry: %v", err)
	}
	return nil
}
