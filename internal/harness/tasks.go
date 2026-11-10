package harness

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/samhvw8/termctl/internal/term"
)

const (
	defaultMaxTasks  = 6
	defaultMaxDepth  = 2
	taskInline       = 500 // a brief up to this long is typed directly; longer goes through its file
	WaitTimeout      = 600.0
	waitPoll         = 3.0
	notifyRetry      = 10.0 // how long `task done` keeps trying before it leaves the result pending
	notifyPoll       = 2.0
	PendingTTL       = 3600.0
	claimTTL         = 30.0  // a claim older than this belongs to a process that died
	reservationTTL   = 600.0 // a task with no pane after this long never got one
	reuseIdleTimeout = 5.0
)

// statusOf maps what a worker reports. Nothing a worker says is final: only the lead's accept makes a task done.
var statusOf = map[string]string{"ok": "ready", "failed": "failed", "blocked": "blocked"}

// open means a harness is still expected to be in the pane.
func open(status string) bool { return status == "running" || status == "ready" || status == "blocked" }

// settled means no more lead decision is due: accepted, or nothing left to accept.
func settled(status string) bool { return status == "done" || status == "lost" }

var shells = map[string]bool{"zsh": true, "bash": true, "sh": true, "fish": true, "dash": true, "ksh": true, "tcsh": true, "csh": true, "nu": true}

func isShell(process string) bool { return shells[strings.TrimPrefix(process, "-")] }

func footer(id string) string {
	return fmt.Sprintf(`When finished, run: harness-ops task done %s --status ok|failed|blocked --summary "<one line result>". `+"`task done`"+` tells the lead you are ready; keep this session open.`, id)
}

func envInt(name string, def int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, term.UsageErr("%s must be a whole number, got %q", name, raw)
	}
	return n, nil
}

// Tasks tracks jobs given to other harness sessions, and which panes were created here.
type Tasks struct {
	Reg       *Registry
	Backend   term.Backend
	Sender    *term.Pane
	SenderRef map[string]string
	Factory   func(paneID string, ref map[string]string) (term.Backend, error)
	Clock     Clock
	Inbox     string

	spawnHook func(*term.Pane) // tests
	sleepHook func(int)        // tests
	sleeps    int
}

func (t *Tasks) clock() Clock {
	if t.Clock == nil {
		t.Clock = RealClock{}
	}
	return t.Clock
}

func (t *Tasks) sleep(s float64) {
	t.sleeps++
	if t.sleepHook != nil {
		t.sleepHook(t.sleeps)
	}
	t.clock().Sleep(s)
}

func (t *Tasks) ops(b term.Backend, sender *term.Pane) *Ops {
	return NewOps(b, sender, t.clock(), t.Inbox)
}

func (t *Tasks) backendFor(paneID string, ref map[string]string) (term.Backend, error) {
	if t.Factory != nil {
		return t.Factory(paneID, ref)
	}
	return term.Make(strings.SplitN(paneID, ":", 2)[0], "", ref["socket_path"])
}

func (t *Tasks) find(tasks map[string]*Task, id string) (*Task, error) {
	if x, ok := tasks[id]; ok {
		return x, nil
	}
	var hits []*Task
	for k, x := range tasks {
		if len(id) >= 3 && strings.HasPrefix(k, id) {
			hits = append(hits, x)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	word := "no task"
	if len(hits) > 1 {
		word = "several tasks"
	}
	return nil, term.TargetErr("%s match '%s'", word, id)
}

// sameServer: a pane ID only means something on the server it was recorded on. Backends with a core_id
// (Herdr: it outlives `hc server restart`, under which terminals keep running) are compared on it, others on pid.
// An empty stored ref compares equal.
func sameServer(stored, current map[string]string) bool {
	if id := stored["core_id"]; id != "" {
		return current["core_id"] == id
	}
	pid := stored["pid"]
	return pid == "" || current["pid"] == pid
}

// paneByID returns the live pane, or nil if it is gone. A restarted terminal server makes the same ID a different pane.
func (t *Tasks) paneByID(paneID string, ref map[string]string) (term.Backend, *term.Pane, error) {
	b, err := t.backendFor(paneID, ref)
	if err != nil {
		return nil, nil, err
	}
	panes, err := b.ListPanes()
	if err != nil {
		return nil, nil, err
	}
	for _, p := range panes {
		if p.ID == paneID {
			if !sameServer(ref, b.ServerRef()) {
				return b, nil, nil
			}
			return b, p, nil
		}
	}
	return b, nil, nil
}

func (t *Tasks) owner() (*term.Pane, error) {
	if t.Sender == nil {
		return nil, term.UsageErr("cannot tell which pane this runs in; a task needs an owner pane")
	}
	return t.Sender, nil
}

// Reconcile marks active tasks whose pane is gone as lost. Unknown is not gone: a backend error changes nothing.
func (t *Tasks) Reconcile() error {
	d, err := t.Reg.Read()
	if err != nil {
		return err
	}
	var lost, exited []string
	now := t.clock().Now()
	for _, x := range d.Tasks {
		if !open(x.Status) {
			continue
		}
		if x.Pane == "" {
			if now-x.Started > reservationTTL {
				lost = append(lost, x.ID)
			}
			continue
		}
		_, p, err := t.paneByID(x.Pane, x.Ref)
		if err != nil {
			Warn(fmt.Sprintf("cannot check task %s: %v", x.ID, err))
			continue
		}
		switch {
		case p == nil:
			lost = append(lost, x.ID)
		case x.Launched && isShell(p.Process):
			exited = append(exited, x.ID)
		}
	}
	if len(lost)+len(exited) == 0 {
		return nil
	}
	return t.Reg.Update(func(d *Data) error {
		for _, id := range lost {
			if x := d.Tasks[id]; x != nil && open(x.Status) {
				x.Status, x.Ended = "lost", t.clock().Now()
				if x.Summary == "" {
					x.Summary = "its pane is gone"
				}
				delete(d.Panes, x.Pane)
			}
		}
		for _, id := range exited {
			t.markExited(d, id, nil)
		}
		return nil
	})
}

// markExited records that the harness is gone from a live pane, and queues the owner's notice.
func (t *Tasks) markExited(d *Data, id string, code *int) {
	x := d.Tasks[id]
	if x == nil || !open(x.Status) {
		return
	}
	x.Status, x.Ended, x.ExitCode, x.Notify = "exited", t.clock().Now(), code, "pending"
	why := "the harness exited"
	if code != nil {
		why = fmt.Sprintf("the harness exited with status %d", *code)
	}
	if x.Summary == "" {
		x.Summary = why
	}
	d.Pending = append(d.Pending, &Pending{Task: x.ID, Owner: x.Owner, OwnerRef: x.OwnerRef,
		Tag:     fmt.Sprintf("[agent-ops from %s (%s) id=%s folder=%s]", x.ID, x.Harness, x.Pane, x.Cwd),
		Text:    fmt.Sprintf("RE: task %s (%s) exited: %s; its pane stays open, see it with: harness-ops read %s", x.ID, x.Title, why, x.ID),
		Created: t.clock().Now()})
}

// Exited is called from the worker's pane when its harness ends. Reconcile is the fallback.
func (t *Tasks) Exited(id string, code int) (*Task, error) {
	var tid string
	err := t.Reg.Update(func(d *Data) error {
		x, err := t.find(d.Tasks, id)
		if err != nil {
			return err
		}
		if t.Sender != nil && t.Sender.ID != x.Pane && t.Sender.ID != x.Owner {
			return term.TargetErr("%s is neither the worker nor the owner of task %s", t.Sender.Address(), x.ID)
		}
		tid = x.ID
		t.markExited(d, x.ID, &code)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := t.FlushPending(); err != nil {
		return nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	return d.Tasks[tid], nil
}

// NewOpts are options of New.
type NewOpts struct {
	Harness, Title, Brief, BriefFile, Cwd, Where, Reuse, Command string
	MaxTasks                                                     int
	Timeout                                                      float64
	ReviewOf                                                     string
	Blind                                                        bool
}

func newID() string {
	b := make([]byte, 3)
	rand.Read(b)
	return "t" + hex.EncodeToString(b)
}

func holdsTask(d *Data, paneID string) bool {
	for _, x := range d.Tasks {
		if open(x.Status) && x.Pane == paneID {
			return true
		}
	}
	return false
}

// reserve takes a slot under the lock, before the spawn, so parallel calls cannot exceed the cap.
func (t *Tasks) reserve(owner *term.Pane, o NewOpts, pane *term.Pane) (*Task, error) {
	limit := o.MaxTasks
	if limit == 0 {
		var err error
		if limit, err = envInt("TERMCTL_MAX_TASKS", defaultMaxTasks); err != nil {
			return nil, err
		}
	}
	depthLimit, err := envInt("TERMCTL_MAX_DEPTH", defaultMaxDepth)
	if err != nil {
		return nil, err
	}
	var task *Task
	err = t.Reg.Update(func(d *Data) error {
		running := 0
		var parent *Task
		for _, x := range d.Tasks {
			if x.Status == "running" && x.Owner == owner.ID {
				running++
			}
			if open(x.Status) && x.Pane == owner.ID {
				parent = x
			}
		}
		if running >= limit {
			return term.Errf(term.CodeGuard, "%s already runs %d tasks (limit %d); use --max to raise it", owner.Address(), running, limit)
		}
		depth := 1
		if parent != nil {
			depth = parent.Depth + 1
		}
		if depth > depthLimit {
			return term.Errf(term.CodeGuard, "task depth %d is over the limit %d; set TERMCTL_MAX_DEPTH to change it", depth, depthLimit)
		}
		if pane != nil && holdsTask(d, pane.ID) {
			return term.Errf(term.CodeGuard, "%s already holds a running task", pane.Address())
		}
		task = &Task{
			ID: newID(), Title: o.Title, Harness: o.Harness, Ref: t.Backend.ServerRef(), Owner: owner.ID, OwnerRef: t.SenderRef,
			Cwd: o.Cwd, Status: "running", Started: t.clock().Now(), Depth: depth, Notify: "none", Owned: pane == nil,
			Round: 1, ReviewOf: o.ReviewOf, Blind: o.Blind,
		}
		if pane != nil {
			task.Pane = pane.ID
		}
		d.Tasks[task.ID] = task
		return nil
	})
	return task, err
}

func (t *Tasks) writeBrief(id, text string) (string, error) {
	dir := filepath.Join(t.Reg.Root, "briefs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", term.BackendErr("brief: %v", err)
	}
	path := filepath.Join(dir, id+".md")
	body := strings.TrimRight(text, " \n\t") + "\n\n" + footer(id) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", term.BackendErr("brief: %v", err)
	}
	return path, nil
}

func prompt(id string, owner *term.Pane, text, brief string) string {
	body := Flat(strings.TrimSpace(text))
	name := owner.Label
	if name == "" {
		name = owner.Title
	}
	if name == "" {
		name = HarnessName(owner)
	}
	if strings.Contains(body, "\n") || len([]rune(body)) > taskInline {
		body = "Read the file " + brief + " and do what it says."
	}
	return fmt.Sprintf("[task %s from %s] %s %s", id, name, body, footer(id))
}

func (t *Tasks) fail(id string, cause error) {
	msg := strings.SplitN("could not start: "+cause.Error(), "\n", 2)[0]
	if err := t.Reg.Update(func(d *Data) error {
		x := d.Tasks[id]
		x.Status, x.Ended, x.Summary = "failed", t.clock().Now(), msg
		return nil
	}); err != nil {
		Warn(err.Error())
	}
}

// New starts a task: it reserves a slot, spawns or reuses a pane, and submits the brief.
func (t *Tasks) New(o NewOpts) (*Task, error) {
	if (o.Brief == "") == (o.BriefFile == "") {
		return nil, term.UsageErr("use exactly one of --brief or --brief-file")
	}
	brief := o.Brief
	if o.BriefFile != "" {
		abs, err := filepath.Abs(expandHome(o.BriefFile))
		if err != nil {
			return nil, term.UsageErr("--brief-file: %v", err)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, term.UsageErr("--brief-file: no such file: %s", abs)
		}
		brief = string(data)
	}
	if strings.TrimSpace(brief) == "" {
		return nil, term.UsageErr("empty brief")
	}
	if o.Where == "" {
		o.Where = "auto"
	}
	if o.Cwd == "" {
		o.Cwd, _ = os.Getwd()
	}
	owner, err := t.owner()
	if err != nil {
		return nil, err
	}
	ops := t.ops(t.Backend, owner)
	var pane *term.Pane
	if o.Reuse != "" {
		if pane, err = ops.Pick(o.Reuse); err != nil {
			return nil, err
		}
		prof := ForProcess(pane.Process)
		if prof.Name != o.Harness {
			return nil, term.UsageErr("%s runs %s, not %s", pane.Address(), prof.Name, o.Harness)
		}
		d, err := t.Reg.Read()
		if err != nil {
			return nil, err
		}
		if holdsTask(d, pane.ID) {
			return nil, term.Errf(term.CodeGuard, "%s already holds a running task", pane.Address())
		}
		idle := reuseIdleTimeout
		if o.Timeout > 0 && o.Timeout < idle {
			idle = o.Timeout
		}
		if err := ops.WaitIdle(pane, idle, prof); err != nil {
			var te *term.Error
			if errors.As(err, &te) && te.Code == term.CodeTimeout {
				return nil, term.Errf(term.CodeGuard, "%s is not idle; a task can only reuse an idle pane", pane.Address())
			}
			return nil, err
		}
		if err := ops.CheckDraft(pane, prof, false); err != nil {
			return nil, err
		}
	}
	task, err := t.reserve(owner, o, pane)
	if err != nil {
		return nil, err
	}
	id := task.ID
	if err := t.start(ops, owner, o, id, brief, pane); err != nil {
		t.fail(id, err)
		return nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	return d.Tasks[id], nil
}

func (t *Tasks) start(ops *Ops, owner *term.Pane, o NewOpts, id, brief string, pane *term.Pane) error {
	path, err := t.writeBrief(id, brief)
	if err != nil {
		return err
	}
	if err := t.Reg.Update(func(d *Data) error { d.Tasks[id].Brief = path; return nil }); err != nil {
		return err
	}
	if pane == nil {
		ref := t.Backend.ServerRef()
		pane, err = ops.Spawn(o.Harness, o.Where, o.Cwd, SpawnOpts{
			Label: id, Title: o.Title, Command: o.Command, Timeout: o.Timeout, Owned: t.owned(),
			Wrap: func(cmd string) string { return SurvivingCommand(cmd, t.exitHook(id)) },
			OnCreated: func(p *term.Pane) error {
				if t.spawnHook != nil {
					t.spawnHook(p)
				}
				return t.Reg.Update(func(d *Data) error {
					t.record(d, p, o.Harness, id, o.Cwd, ref)
					d.Tasks[id].Pane = p.ID
					return nil
				})
			},
		})
		if err != nil {
			return err
		}
	} else {
		prof := ForProcess(pane.Process)
		if err := t.Backend.SetLabel(pane, id); err != nil { // teammates address a task by its ID
			return err
		}
		if err := ops.Clear(pane, false, o.Timeout); err != nil {
			return err
		}
		if err := ops.AssertCleared(pane, prof); err != nil {
			return err
		}
	}
	prof := Profiles[o.Harness] // a just-spawned pane's process is not known yet
	if prof == nil {
		prof = Profiles["generic"]
	}
	if err := ops.Submit(pane, prompt(id, owner, brief, path), prof); err != nil {
		return err
	}
	return t.Reg.Update(func(d *Data) error { d.Tasks[id].Launched = true; return nil })
}

// exitHook is the command the pane's shell runs when the harness ends. It must reach the same registry.
func (t *Tasks) exitHook(id string) string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	env := ""
	if d := os.Getenv("TERMCTL_STATE_DIR"); d != "" {
		env = "env TERMCTL_STATE_DIR=" + term.ShellQuote(d) + " "
	}
	return env + term.ShellQuote(exe) + " task exited " + id
}

func (t *Tasks) record(d *Data, p *term.Pane, harness, label, cwd string, ref map[string]string) {
	owner := ""
	if t.Sender != nil {
		owner = t.Sender.ID
	}
	d.Panes[p.ID] = &PaneRecord{Harness: harness, Label: label, Cwd: cwd, Ref: ref, Owner: owner, Created: t.clock().Now()}
}

// Spawn starts a harness and records the pane, so close may act on it later.
func (t *Tasks) Spawn(harness, where, cwd string, opts SpawnOpts) (*term.Pane, error) {
	ref := t.Backend.ServerRef()
	opts.Owned = t.owned()
	opts.Wrap = func(cmd string) string { return SurvivingCommand(cmd, "") }
	opts.OnCreated = func(p *term.Pane) error {
		return t.Reg.Update(func(d *Data) error { t.record(d, p, harness, opts.Label, cwd, ref); return nil })
	}
	return t.ops(t.Backend, t.Sender).Spawn(harness, where, cwd, opts)
}

// Mine keeps the panes this caller started with spawn or task new.
func (t *Tasks) Mine(panes []*term.Pane) ([]*term.Pane, error) {
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	var out []*term.Pane
	for _, p := range panes {
		if rec := d.Panes[p.ID]; rec != nil && t.Sender != nil && rec.Owner == t.Sender.ID {
			out = append(out, p)
		}
	}
	return out, nil
}

// owned reports the panes this caller started with spawn or task new, from the registry.
// A registry that cannot be read places nothing beside them; placement is cosmetic.
func (t *Tasks) owned() func(*term.Pane) bool {
	if t.Sender == nil {
		return nil
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil
	}
	mine := map[string]bool{}
	for id, rec := range d.Panes {
		if rec.Owner == t.Sender.ID {
			mine[id] = true
		}
	}
	return func(p *term.Pane) bool { return mine[p.ID] }
}

func (t *Tasks) isSelf(id string) bool { return t.Sender != nil && id != "" && id == t.Sender.ID }

func (t *Tasks) busy(b term.Backend, p *term.Pane) (bool, error) {
	if nativeBusy(b.NativeStatus(p)) {
		return true, nil
	}
	lines, err := b.Capture(p, screenLines(p))
	if err != nil {
		return false, err
	}
	return ForProcess(p.Process).IsBusy(lines), nil
}

// ClosePane closes a pane created here. Your own pane is never closed, even with force.
func (t *Tasks) ClosePane(target string, force bool) (*term.Pane, error) {
	panes, err := t.Backend.ListPanes()
	if err != nil {
		return nil, err
	}
	p, err := term.Resolve(panes, target)
	if err != nil {
		return nil, err
	}
	if p.IsSelf || t.isSelf(p.ID) {
		return nil, term.TargetErr("refusing to close your own pane")
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	rec := d.Panes[p.ID]
	owned := rec != nil
	if owned && !sameServer(rec.Ref, t.Backend.ServerRef()) {
		owned = false // the record is from before a terminal server restart
	}
	var task *Task
	for _, x := range d.Tasks {
		if !settled(x.Status) && !x.Closed && x.Pane == p.ID {
			task = x
		}
	}
	if !force {
		if !owned {
			return nil, term.Errf(term.CodeGuard, "%s was not created through spawn or task new; use --force to close it", p.Address())
		}
		if task != nil {
			return nil, term.Errf(term.CodeGuard, "%s holds task %s (%s), not accepted; use task accept, or --force", p.Address(), task.ID, task.Status)
		}
		busy, err := t.busy(t.Backend, p)
		if err != nil {
			return nil, err
		}
		if busy {
			return nil, term.Errf(term.CodeGuard, "%s is busy; use --force to close it", p.Address())
		}
	}
	if err := t.Backend.Close(p); err != nil {
		return nil, err
	}
	return p, t.Reg.Update(func(d *Data) error {
		delete(d.Panes, p.ID)
		for _, x := range d.Tasks {
			if x.Pane == p.ID && !settled(x.Status) && !x.Closed {
				x.Closed = true
				if x.Status == "running" {
					x.Status, x.Ended, x.Summary = "failed", t.clock().Now(), "its pane was closed with --force"
				}
			}
		}
		return nil
	})
}

// reviewedBySender is the task this pane is reviewing blind, or "".
func (t *Tasks) reviewedBySender(d *Data) string {
	if t.Sender == nil {
		return ""
	}
	for _, x := range d.Tasks {
		if x.Blind && x.Pane == t.Sender.ID && open(x.Status) {
			return x.ReviewOf
		}
	}
	return ""
}

// Ls lists tasks. Without all: this owner's open tasks. A blind reviewer never sees what it reviews.
func (t *Tasks) Ls(all bool) ([]*Task, error) {
	if err := t.Reconcile(); err != nil {
		return nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	hidden := t.reviewedBySender(d)
	var out []*Task
	for _, x := range d.Tasks {
		if x.ID != hidden && (all || (!x.Closed && (t.Sender == nil || x.Owner == t.Sender.ID))) {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started < out[j].Started })
	return out, nil
}

// Get finds one task by ID or unique prefix.
func (t *Tasks) Get(id string) (*Task, error) {
	if err := t.Reconcile(); err != nil {
		return nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	return t.find(d.Tasks, id)
}

// Show is Get for the caller's eyes: a blind reviewer cannot read the task it reviews.
func (t *Tasks) Show(id string) (*Task, error) {
	x, err := t.Get(id)
	if err != nil {
		return nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	if x.ID == t.reviewedBySender(d) {
		return nil, term.TargetErr("a blind reviewer cannot read task %s", x.ID)
	}
	return x, nil
}

// Send messages a task's pane. New work on a finished task reopens it: the worker must report again.
func (t *Tasks) Send(id, text string, force bool) error {
	x, err := t.Get(id)
	if err != nil {
		return err
	}
	if x.Closed {
		return term.UsageErr("task %s is closed", x.ID)
	}
	if x.Status == "lost" || x.Status == "exited" {
		return term.UsageErr("task %s is %s: there is no harness to receive this", x.ID, x.Status)
	}
	b, p, err := t.paneByID(x.Pane, x.Ref)
	if err != nil {
		return err
	}
	if p == nil {
		return term.UsageErr("task %s: its pane is gone", x.ID)
	}
	if _, err := t.ops(b, t.Sender).Msg(p, fmt.Sprintf("task %s: %s (%s)", x.ID, text, footer(x.ID)), MsgOpts{Force: force}); err != nil {
		return err
	}
	if x.Status == "running" {
		return nil
	}
	return t.Reg.Update(func(d *Data) error {
		c := d.Tasks[x.ID]
		c.Status, c.Ended, c.Notify, c.Seen = "running", 0, "none", false
		c.Round++
		return nil
	})
}

func unseen(tasks map[string]*Task, owner string) []*Task {
	var out []*Task
	for _, x := range tasks {
		if x.Owner == owner && x.Status != "running" && !x.Closed && !x.Seen {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ended < out[j].Ended })
	return out
}

// Wait returns when a task stops running. any returns each finished task once, so repeated calls walk through the results.
func (t *Tasks) Wait(id string, any bool, timeout float64) (*Task, error) {
	me := ""
	if any {
		o, err := t.owner()
		if err != nil {
			return nil, err
		}
		me = o.ID
	} else {
		x, err := t.Get(id)
		if err != nil {
			return nil, err
		}
		id = x.ID
	}
	deadline := t.clock().Now() + timeout
	for {
		if err := t.Reconcile(); err != nil {
			return nil, err
		}
		if _, err := t.FlushPending(); err != nil {
			return nil, err
		}
		d, err := t.Reg.Read()
		if err != nil {
			return nil, err
		}
		var found *Task
		if any {
			if u := unseen(d.Tasks, me); len(u) > 0 {
				found = u[0]
			} else {
				running := false
				for _, x := range d.Tasks {
					running = running || (x.Status == "running" && x.Owner == me)
				}
				if !running {
					return nil, term.UsageErr("no running tasks and no uncollected results to wait for")
				}
			}
		} else if d.Tasks[id].Status != "running" {
			found = d.Tasks[id]
		}
		if found != nil {
			var result *Task
			err := t.Reg.Update(func(d *Data) error {
				d.Tasks[found.ID].Seen = true
				result = d.Tasks[found.ID]
				return nil
			})
			return result, err
		}
		if t.clock().Now() >= deadline {
			what := "no task of yours"
			if !any {
				what = "task " + id
			}
			return nil, term.Errf(term.CodeTimeout, "%s finished within %gs", what, timeout)
		}
		t.sleep(waitPoll)
	}
}

func roundNote(round int) string {
	if round > 1 {
		return fmt.Sprintf(" round %d", round)
	}
	return ""
}

func (t *Tasks) doneTag(x *Task, b term.Backend) (string, error) {
	if t.Sender != nil {
		return t.ops(b, t.Sender).Tag()
	}
	return fmt.Sprintf("[agent-ops from %s (%s) id=%s folder=%s]", x.ID, x.Harness, x.Pane, x.Cwd), nil
}

// Done records a task's result and tells its owner, or leaves the result pending.
func (t *Tasks) Done(id, status, summary, summaryFile string) (*Task, error) {
	stored, ok := statusOf[status]
	if !ok {
		return nil, term.UsageErr("--status must be one of ok, failed, blocked")
	}
	if summaryFile != "" {
		data, err := os.ReadFile(expandHome(summaryFile))
		if err != nil {
			return nil, term.UsageErr("--summary-file: %v", err)
		}
		summary = string(data)
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return nil, term.UsageErr("task done needs --summary or --summary-file")
	}
	var tid string
	err := t.Reg.Update(func(d *Data) error {
		x, err := t.find(d.Tasks, id)
		if err != nil {
			return err
		}
		if t.Sender != nil && t.Sender.ID != x.Pane && t.Sender.ID != x.Owner {
			return term.TargetErr("%s is neither the worker nor the owner of task %s", t.Sender.Address(), x.ID)
		}
		if x.Status != "running" && x.Status != "blocked" {
			return term.UsageErr("task %s is already %s", x.ID, x.Status)
		}
		x.Status, x.Ended, x.Summary, x.Notify = stored, t.clock().Now(), summary, "pending"
		tid = x.ID
		tag, err := t.doneTag(x, t.Backend)
		if err != nil {
			return err
		}
		d.Pending = append(d.Pending, &Pending{Task: tid, Owner: x.Owner, OwnerRef: x.OwnerRef, Tag: tag,
			Text: fmt.Sprintf("RE: task %s (%s) %s%s: %s", tid, x.Title, x.Status, roundNote(x.Round), summary), Created: t.clock().Now()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	deadline := t.clock().Now() + notifyRetry
	for {
		if _, err := t.FlushPending(); err != nil {
			return nil, err
		}
		d, err := t.Reg.Read()
		if err != nil {
			return nil, err
		}
		if d.Tasks[tid].Notify != "pending" || t.clock().Now() >= deadline {
			return d.Tasks[tid], nil
		}
		t.sleep(notifyPoll)
	}
}

// FlushPending delivers held-back results. A claim under the lock stops two processes delivering the same one.
func (t *Tasks) FlushPending() (int, error) {
	d, err := t.Reg.Read()
	if err != nil || len(d.Pending) == 0 {
		return 0, err
	}
	now := t.clock().Now()
	var claimed []Pending
	if err := t.Reg.Update(func(d *Data) error {
		keep := d.Pending[:0]
		for _, p := range d.Pending {
			if now-p.Created > PendingTTL {
				if x := d.Tasks[p.Task]; x != nil {
					x.Notify = "expired"
				}
				continue
			}
			if p.Claimed == 0 || now-p.Claimed > claimTTL {
				p.Claimed = now
				claimed = append(claimed, *p)
			}
			keep = append(keep, p)
		}
		d.Pending = keep
		return nil
	}); err != nil {
		return 0, err
	}
	delivered := 0
	for _, p := range claimed {
		outcome := t.deliver(p)
		if outcome == "delivered" {
			delivered++
		}
		if err := t.Reg.Update(func(d *Data) error {
			for i, q := range d.Pending {
				if q.Task == p.Task && q.Created == p.Created {
					if outcome == "retry" {
						q.Claimed = 0
					} else {
						if x := d.Tasks[p.Task]; x != nil {
							x.Notify = outcome
						}
						d.Pending = append(d.Pending[:i], d.Pending[i+1:]...)
					}
					break
				}
			}
			return nil
		}); err != nil {
			return delivered, err
		}
	}
	return delivered, nil
}

func (t *Tasks) deliver(p Pending) string {
	b, pane, err := t.paneByID(p.Owner, p.OwnerRef)
	if err != nil {
		Warn(fmt.Sprintf("could not notify the owner of task %s: %v", p.Task, err))
		return "retry"
	}
	if pane == nil {
		return "owner-gone"
	}
	if _, err := t.ops(b, nil).Msg(pane, p.Text, MsgOpts{Tag: p.Tag}); err != nil {
		var te *term.Error
		if !errors.As(err, &te) || te.Code != term.CodeDraft {
			Warn(fmt.Sprintf("could not notify the owner of task %s: %v", p.Task, err))
		}
		return "retry"
	}
	return "delivered"
}

// Close closes a task's pane. A pane the task did not create is left open.
func (t *Tasks) Close(id string, force bool) (*Task, error) {
	x, err := t.Get(id)
	if err != nil {
		return nil, err
	}
	if t.isSelf(x.Pane) {
		return nil, term.TargetErr("refusing to close your own pane")
	}
	if !settled(x.Status) && !force {
		return nil, term.Errf(term.CodeGuard, "task %s is %s, not accepted; use task accept, or --force", x.ID, x.Status)
	}
	if x.Owned && x.Pane != "" {
		b, p, err := t.paneByID(x.Pane, x.Ref)
		if err != nil {
			return nil, err
		}
		if p != nil {
			if err := b.Close(p); err != nil {
				return nil, err
			}
		}
	}
	var result *Task
	err = t.Reg.Update(func(d *Data) error {
		c := d.Tasks[x.ID]
		c.Closed = true
		if c.Status == "running" {
			c.Status, c.Ended, c.Summary = "failed", t.clock().Now(), "closed by its owner before it finished"
		}
		if c.Owned {
			delete(d.Panes, c.Pane)
		}
		result = c
		return nil
	})
	return result, err
}

// Accept is the lead's verdict: only it makes a task done.
func (t *Tasks) Accept(id string, closeIt bool) (*Task, error) {
	x, err := t.Get(id)
	if err != nil {
		return nil, err
	}
	if t.Sender == nil || t.Sender.ID != x.Owner {
		return nil, term.TargetErr("only the owner of task %s can accept it", x.ID)
	}
	if x.Status != "ready" {
		return nil, term.UsageErr("task %s is %s; only a ready task can be accepted", x.ID, x.Status)
	}
	if err := t.Reg.Update(func(d *Data) error {
		d.Tasks[x.ID].Status = "done"
		return nil
	}); err != nil {
		return nil, err
	}
	if closeIt {
		return t.Close(x.ID, false)
	}
	return t.Get(x.ID)
}

// ReviewOpts are options of Review.
type ReviewOpts struct {
	ID, Harness, Model, BriefFile, Where, Command string
	Blind                                         bool
	Timeout                                       float64
}

func stripFooter(brief, id string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(brief), footer(id)))
}

func (t *Tasks) blindBrief(x *Task) string {
	requirements := "(the original brief is not available; review against what the code claims to do)"
	if data, err := os.ReadFile(x.Brief); err == nil {
		requirements = stripFooter(string(data), x.ID)
	}
	return "Blind review. You do not have the worker's report, and you must not look for it.\n" +
		"Inspect the changes in this folder (start with `git status` and `git diff`) against the requirements below.\n" +
		"Report a verdict, pass or fail, with concrete findings (file and line). Do not change any file.\n\nRequirements:\n\n" + requirements
}

// Review starts a new session linked to a task. A blind reviewer gets the requirements and the
// files, never the worker's report, summary or conversation. Non-blind review is task send.
func (t *Tasks) Review(o ReviewOpts) (*Task, error) {
	if !o.Blind {
		return nil, term.UsageErr("task review is for --blind review; a non-blind review goes to the worker with task send")
	}
	if o.Harness == "" {
		return nil, term.UsageErr("task review needs --harness")
	}
	prof, known := Profiles[o.Harness]
	if !known && o.Command == "" {
		return nil, term.UsageErr("unknown harness '%s'; pass --command", o.Harness)
	}
	x, err := t.Get(o.ID)
	if err != nil {
		return nil, err
	}
	if x.Status == "running" {
		return nil, term.UsageErr("task %s is still running; review it after the worker reports", x.ID)
	}
	cmd := o.Command
	if cmd == "" {
		cmd = prof.Command
		if o.Model != "" {
			cmd += " --model=" + term.ShellQuote(o.Model)
		}
		if prof.ReadOnlyFlags != "" {
			cmd += " " + prof.ReadOnlyFlags
		} else {
			Warn(fmt.Sprintf("%s has no verified read-only mode; this reviewer can change files", o.Harness))
		}
	}
	brief := t.blindBrief(x)
	if o.BriefFile != "" {
		data, err := os.ReadFile(expandHome(o.BriefFile))
		if err != nil {
			return nil, term.UsageErr("--brief-file: %v", err)
		}
		brief = string(data)
	}
	return t.New(NewOpts{Harness: o.Harness, Title: "blind review of " + x.ID, Brief: brief, Cwd: x.Cwd, Where: o.Where,
		Command: cmd, Timeout: o.Timeout, ReviewOf: x.ID, Blind: true})
}
