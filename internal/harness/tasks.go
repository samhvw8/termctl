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
	"sync"
	"syscall"

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
	claimTTL         = 600.0 // a claim older than this belongs to a process that died; above deliveryBound
	reservationTTL   = 600.0 // a task with no pane after this long never got one
	unreachableTTL   = 600.0 // a server that has not answered for this long is gone for good
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

	memo      *paneMemo // one pane list per backend server while a watch round runs
	downMu    sync.Mutex
	downSeen  map[string]bool  // tasks of other owners whose server this process could not reach, warned once
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
	return NewOps(b, sender, t.clock(), t.Reg.Root)
}

// taskOps is ops whose journal entries belong to task x.
func (t *Tasks) taskOps(b term.Backend, sender *term.Pane, x *Task) *Ops {
	ops := t.ops(b, sender)
	ops.Task, ops.Project = x.ID, x.Project
	return ops
}

func (t *Tasks) journal() *Journal { return &Journal{Root: t.Reg.Root, Clock: t.clock()} }

// logTask logs a task event from this sender, in the task's project.
func (t *Tasks) logTask(event string, x *Task, status, text string) {
	if x == nil {
		return
	}
	project := x.Project
	if project == "" {
		project = ProjectOf(t.Sender)
	}
	e := Entry{Event: event, Project: project, From: address(t.Sender), FromName: entryName(t.Sender), Task: x.ID,
		Harness: x.Harness, Status: status, Text: text}
	e.To = x.Pane
	if t.Sender != nil && t.Sender.ID == x.Pane {
		e.To = x.Owner // the worker reports to its owner
	}
	t.journal().Record(e)
}

func (t *Tasks) backendFor(paneID string, ref map[string]string) (term.Backend, error) {
	if t.Factory != nil {
		return t.Factory(paneID, ref)
	}
	return term.Make(strings.SplitN(paneID, ":", 2)[0], "", ref["socket_path"])
}

// errSeveral marks a task ID prefix that matches more than one task, as against none.
var errSeveral = errors.New("several tasks match")

// findError is find's refusal: a *term.Error (exit 2) that also says whether several tasks matched.
type findError struct {
	err     *term.Error
	several bool
}

func (e *findError) Error() string { return e.err.Error() }
func (e *findError) Unwrap() []error {
	if e.several {
		return []error{e.err, errSeveral}
	}
	return []error{e.err}
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
	return nil, &findError{term.TargetErr("%s match '%s'", word, id), len(hits) > 1}
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
	b, panes, err := t.listPanes(paneID, ref)
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

// callerRef is the server of the caller's own pane.
func (t *Tasks) callerRef() map[string]string {
	if t.SenderRef != nil {
		return t.SenderRef
	}
	return t.Backend.ServerRef()
}

// serverKey names the server a ref belongs to, as sameServer compares it: core_id, else pid.
func serverKey(ref map[string]string) string {
	if id := ref["core_id"]; id != "" {
		return id
	}
	return ref["pid"]
}

func (t *Tasks) owner() (*term.Pane, error) {
	if t.Sender == nil {
		return nil, term.UsageErr("cannot tell which pane this runs in; a task needs an owner pane")
	}
	return t.Sender, nil
}

// owns: the caller is the task's owner pane, on the owner's server.
func (t *Tasks) owns(x *Task) bool {
	return t.Sender != nil && x.Owner == t.Sender.ID && sameServer(x.OwnerRef, t.callerRef())
}

// warnDownOnce warns once per process that another owner's task cannot be checked. Only the owner
// records an unreachable server: a process in another context may not reach a server the owner does, and
// must not lose a live task it does not own (round-5 review D4).
func (t *Tasks) warnDownOnce(x *Task, cause error) {
	t.downMu.Lock()
	defer t.downMu.Unlock()
	if t.downSeen[x.ID] {
		return
	}
	if t.downSeen == nil {
		t.downSeen = map[string]bool{}
	}
	t.downSeen[x.ID] = true
	Warn(fmt.Sprintf("cannot check task %s: %v; only its owner records that its server does not answer", x.ID, cause))
}

// Reconcile marks active tasks whose pane is gone as lost. Unknown is not gone: a server that does not
// answer changes nothing until it has not answered for unreachableTTL; then it is gone for good, and its
// tasks are lost. Only a task's owner records that: other processes leave the task alone and warn once. "cannot check" is warned once when a task's server stops answering, not on every call.
func (t *Tasks) Reconcile() error {
	d, err := t.Reg.Read()
	if err != nil {
		return err
	}
	var lost, exited, back []string
	down := map[string]error{} // tasks whose server does not answer, and the first time or past the TTL
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
		if err != nil && !t.owns(x) {
			t.warnDownOnce(x, err)
			continue
		}
		if err != nil {
			if x.Unreachable == 0 || now-x.Unreachable > unreachableTTL {
				down[x.ID] = err
			}
			continue
		}
		if x.Unreachable != 0 && t.owns(x) {
			back = append(back, x.ID)
		}
		switch {
		case p == nil:
			lost = append(lost, x.ID)
		case x.Launched && isShell(p.Process):
			exited = append(exited, x.ID)
		}
	}
	if len(lost)+len(exited)+len(back)+len(down) == 0 {
		return nil
	}
	return t.Reg.Update(func(d *Data) error {
		lose := func(x *Task, why string) {
			x.Status, x.Ended, x.Dialog = "lost", t.clock().Now(), nil
			if x.Summary == "" {
				x.Summary = why
			}
			delete(d.Panes, x.Pane)
		}
		for _, id := range lost {
			if x := d.Tasks[id]; x != nil && open(x.Status) {
				lose(x, "its pane is gone")
			}
		}
		for _, id := range exited {
			t.markExited(d, id, nil)
		}
		for _, id := range back {
			if x := d.Tasks[id]; x != nil && x.Unreachable != 0 {
				x.Unreachable = 0
				Warn(fmt.Sprintf("task %s: its server answers again", x.ID))
			}
		}
		for id, cause := range down { // under the lock, so two processes warn about one task once
			x := d.Tasks[id]
			switch {
			case x == nil || !open(x.Status):
			case x.Unreachable == 0:
				x.Unreachable = now
				Warn(fmt.Sprintf("cannot check task %s: %v; it is lost if its server does not answer for %gs", x.ID, cause, unreachableTTL))
			case now-x.Unreachable > unreachableTTL:
				lose(x, fmt.Sprintf("its server has not answered for %gs: %v", unreachableTTL, cause))
			}
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
	x.Status, x.Ended, x.ExitCode, x.Notify, x.Dialog = "exited", t.clock().Now(), code, "pending", nil
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
	if d, err := t.Reg.Read(); err == nil {
		t.logTask("task.exited", d.Tasks[tid], d.Tasks[tid].Status, fmt.Sprintf("the harness exited with status %d", code))
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
			Round: 1, ReviewOf: o.ReviewOf, Blind: o.Blind, Project: ProjectOf(owner),
		}
		if pane != nil {
			task.Pane = pane.ID
		}
		d.Tasks[task.ID] = task
		return nil
	})
	return task, err
}

// writeBrief files the brief in the task's project folder of the journal.
func (t *Tasks) writeBrief(x *Task, text string) (string, error) {
	return t.journal().WriteFile(x.Project, "brief", x.ID, strings.TrimRight(text, " \n\t")+"\n\n"+footer(x.ID))
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

// uncalibratedLead is the note for a lead whose harness has no calibrated input reader, or "": the watcher
// types it no dialog notice (its letters could land in the lead's own menus), and task wait returns them.
func uncalibratedLead(lead *term.Pane) string {
	if lead == nil || ForProcess(lead.Process).Calibrated {
		return ""
	}
	return fmt.Sprintf("%s runs %s, whose input line is not calibrated: dialog notices are not typed to it; task wait returns its workers' dialogs, and ls --mine shows a dialog open on a pane it started",
		lead.Address(), HarnessName(lead))
}

// WarnUncalibratedLead warns once when the caller's harness is not typed dialog notices (round-6 review R4).
func (t *Tasks) WarnUncalibratedLead() {
	if note := uncalibratedLead(t.Sender); note != "" {
		Warn(note)
	}
}

// New starts a task: it reserves a slot, spawns or reuses a pane, and submits the brief.
func (t *Tasks) New(o NewOpts) (*Task, error) {
	if (o.Brief == "") == (o.BriefFile == "") {
		return nil, term.UsageErr("use exactly one of --brief or --brief-file")
	}
	brief := o.Brief
	if o.BriefFile != "" {
		abs, err := filepath.Abs(ExpandHome(o.BriefFile))
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
	ops.Task, ops.Project = id, task.Project
	if err := t.start(ops, owner, o, task, brief, pane); err != nil {
		t.fail(id, err)
		t.logTask("task.new", task, "failed", err.Error())
		return nil, err
	}
	t.WarnUncalibratedLead()
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	x := d.Tasks[id]
	text := x.Title
	if x.ReviewOf != "" {
		text += " (reviews " + x.ReviewOf + ")"
	}
	e := Entry{Event: "task.new", Project: x.Project, From: address(owner), FromName: entryName(owner), To: x.Pane, ToName: id,
		Task: id, Harness: x.Harness, Status: x.Status, Text: text, File: x.Brief}
	t.journal().Record(e)
	return x, nil
}

func (t *Tasks) start(ops *Ops, owner *term.Pane, o NewOpts, task *Task, brief string, pane *term.Pane) error {
	id := task.ID
	path, err := t.writeBrief(task, brief)
	if err != nil {
		return err
	}
	if err := t.Reg.Update(func(d *Data) error { d.Tasks[id].Brief = path; return nil }); err != nil {
		return err
	}
	spawned := pane == nil
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
	submit := ops.Submit
	if spawned {
		submit = ops.SubmitFirst // a harness that has just started can lose the first Enter
	}
	if err := submit(pane, prompt(id, owner, brief, path), prof); err != nil {
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
	d.Panes[p.ID] = &PaneRecord{Harness: harness, Label: label, Cwd: cwd, Ref: ref, Owner: owner, OwnerRef: t.SenderRef, Created: t.clock().Now()}
}

// ownsRecord: the caller started the pane the record is of: the record names the caller's pane on the
// caller's server, and the pane's server (paneRef) is the one recorded, so a record from before a terminal
// server restart (a reused pane ID) counts for nothing.
func (t *Tasks) ownsRecord(rec *PaneRecord, paneRef map[string]string) bool {
	return rec != nil && t.Sender != nil && rec.Owner == t.Sender.ID && sameServer(rec.OwnerRef, t.callerRef()) && sameServer(rec.Ref, paneRef)
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

// GaveWork records that the caller gave work to p (spawn with a prompt, msg) when p is a pane it started:
// a watcher then counts p working for workGrace, and its stop check after letting go of its lock sees the
// work. It reports whether p is the caller's, and warns once, as task new does, when the caller's harness
// is not typed dialog notices. b is the backend p is on. A pane that is not the caller's is not written.
func (t *Tasks) GaveWork(b term.Backend, p *term.Pane) (bool, error) {
	if t.Sender == nil || t.isSelf(p.ID) {
		return false, nil
	}
	d, err := t.Reg.Read()
	if err != nil {
		return false, err
	}
	ref := b.ServerRef()
	if !t.ownsRecord(d.Panes[p.ID], ref) {
		return false, nil
	}
	if err := t.Reg.Update(func(d *Data) error {
		if rec := d.Panes[p.ID]; t.ownsRecord(rec, ref) {
			rec.Worked = t.clock().Now()
		}
		return nil
	}); err != nil {
		return true, err
	}
	t.WarnUncalibratedLead()
	return true, nil
}

// OpenDialogs maps each pane this caller started (on the caller's backend) to the fingerprint of the dialog
// marked open on it: the mark of the running task that holds the pane, else the pane record's. This is how
// a lead whose harness is not typed dialog notices sees a held one on a spawned pane (ls --mine).
func (t *Tasks) OpenDialogs() (map[string]string, error) {
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for id, rec := range d.Panes {
		if !t.ownsRecord(rec, t.Backend.ServerRef()) {
			continue
		}
		if rec.Dialog != nil && !paneHeld(d, id) {
			out[id] = rec.Dialog.Fingerprint
		}
	}
	for _, x := range d.Tasks {
		if x.Dialog != nil && dialogOnScreen(x, x.Dialog.Fingerprint) && t.ownsRecord(d.Panes[x.Pane], t.Backend.ServerRef()) {
			out[x.Pane] = x.Dialog.Fingerprint
		}
	}
	return out, nil
}

// Mine keeps the panes this caller started with spawn or task new.
func (t *Tasks) Mine(panes []*term.Pane) ([]*term.Pane, error) {
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	var out []*term.Pane
	for _, p := range panes {
		if t.ownsRecord(d.Panes[p.ID], t.Backend.ServerRef()) {
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
	e := Entry{Event: "close", Project: ProjectOf(t.Sender), From: address(t.Sender), FromName: entryName(t.Sender),
		To: p.Address(), ToName: entryName(p), Harness: HarnessName(p)}
	if task != nil {
		e.Task, e.Project = task.ID, task.Project
		if e.Project == "" {
			e.Project = ProjectOf(t.Sender)
		}
	}
	if force {
		e.Status = "forced"
	}
	t.journal().Record(e)
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
	sent, err := t.taskOps(b, t.Sender, x).Msg(p, fmt.Sprintf("task %s: %s (%s)", x.ID, text, footer(x.ID)), MsgOpts{Force: force})
	if err != nil && !sent.Typed {
		return err
	}
	// Typed but not confirmed (exit 5) may still arrive: open the round anyway, or the worker's
	// task done for it would be refused as already reported.
	t.WarnUncalibratedLead()
	if x.Status == "running" {
		return err
	}
	if uerr := t.Reg.Update(func(d *Data) error {
		c := d.Tasks[x.ID]
		c.Status, c.Ended, c.Notify, c.Seen = "running", 0, "none", false
		c.Round++
		return nil
	}); uerr != nil {
		return uerr
	}
	return err
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

// Waited is what Wait found: a task that stopped running, or, with Dialog set, a running task whose
// worker shows a dialog.
type Waited struct {
	Task   *Task
	Dialog *Dialog
}

// Wait returns when a task stops running, or when a waited task's worker shows a dialog. any returns each
// finished task once and each dialog once while it stays on screen, so repeated calls walk through them.
func (t *Tasks) Wait(id string, any bool, timeout float64) (*Waited, error) {
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
		// Waiting marks a result seen and a dialog returned, which the owner then never hears of.
		if t.Sender == nil || x.Owner != t.Sender.ID || !sameServer(x.OwnerRef, t.callerRef()) {
			return nil, term.TargetErr("only the owner of task %s can wait for it", x.ID)
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
			return &Waited{Task: result}, err
		}
		if w, err := t.waitDialog(d, me, id); w != nil || err != nil {
			return w, err
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

// waitDialog reads the screens of the waited running tasks and returns one dialog not yet returned. When
// a watcher serves the caller it reads every screen each round and keeps the marks, so only a worker whose
// mark shows a dialog not returned yet is read, to get that dialog in full.
func (t *Tasks) waitDialog(d *Data, owner, id string) (*Waited, error) {
	var watch []*Task
	if id != "" {
		watch = []*Task{d.Tasks[id]}
	} else {
		watch = runningTasks(d, owner, t.SenderRef)
	}
	if t.Sender != nil && t.Watching(t.Sender.ID) != 0 {
		var marked []*Task
		for _, x := range watch {
			if x.Dialog != nil && !x.Dialog.Returned && !noticeDelivered(d, x) {
				marked = append(marked, x)
			}
		}
		watch = marked
	}
	sigs := t.signals(watch)
	if err := t.apply(sigs, false); err != nil {
		return nil, err
	}
	for _, s := range sigs {
		if s.Kind != "dialog" {
			continue
		}
		var result *Task
		if err := t.Reg.Update(func(d *Data) error {
			x := d.Tasks[s.Task]
			if x != nil && dialogOnScreen(x, s.Dialog.Fingerprint) && !x.Dialog.Returned && !noticeDelivered(d, x) {
				// The lead has it from task wait: the watcher must not tell it again, and a notice
				// already queued would arrive stale.
				x.Dialog.Returned, x.Dialog.Notified = true, true
				keep := d.Pending[:0]
				for _, p := range d.Pending {
					if p.Kind != "dialog" || p.Task != x.ID || p.Dialog != s.Dialog.Fingerprint {
						keep = append(keep, p)
					}
				}
				d.Pending = keep
				result = x
			}
			return nil
		}); err != nil {
			return nil, err
		}
		if result != nil {
			return &Waited{Task: result, Dialog: s.Dialog}, nil
		}
	}
	return nil, nil
}

// noticeDelivered: the owner was told of the task's marked dialog, and no notice of it is still queued. The
// lead has it from the watcher, so task wait does not return it again.
func noticeDelivered(d *Data, x *Task) bool {
	if x.Dialog == nil || !x.Dialog.Notified {
		return false
	}
	for _, p := range d.Pending {
		if p.Kind == "dialog" && p.Task == x.ID && p.Dialog == x.Dialog.Fingerprint {
			return false
		}
	}
	return true
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
		data, err := os.ReadFile(ExpandHome(summaryFile))
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
	var done Task
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
		x.Dialog = nil // the worker is reporting, so nothing waits on screen
		tid = x.ID
		done = *x
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
	t.logTask("task.done", &done, done.Status, summary)
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

// deliveryBound is the longest one delivery can take while every backend call stays inside its deadline
// (term.CallTimeout; hc send term.SendTimeout): 543 s. A slower call fails and ends the delivery. claimTTL
// must stay above it, or a second flusher takes over a notice still being delivered and sends it twice.
// In order, a delivery makes these calls:
//   - the owner's pane list: one call, but on hc an hc ls plus an hc get per terminal, 8 at once; 5 calls
//     cover 32 terminals (tmux and iTerm2 add ps and lsof: 3 calls);
//   - the server check, a draft read, a status read and a busy read: 4;
//   - the send: one hc send, or paste and Enter (2 calls);
//   - up to submitRetries+1 reads of the input line, with an Enter between two: 7;
//   - and the pauses: enterDelay once, submitSettle before each read.
func deliveryBound() float64 {
	call := term.CallTimeout.Seconds()
	calls := 5.0 + 4 + float64(2*submitRetries+1)
	send := max(term.SendTimeout.Seconds(), 2*call)
	return calls*call + send + enterDelay + float64(submitRetries+1)*submitSettle
}

// claimerDead: the process that claimed a notice is gone, so its claim is taken at once instead of after
// claimTTL. The state directory is local, so the pid is of this host. A claim with no pid (an older binary's)
// waits out claimTTL, and so does one whose process may live (any answer but "no such process").
func claimerDead(pid int) bool {
	return pid > 0 && errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
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
				if x := d.Tasks[p.Task]; x != nil && p.Kind == "" {
					x.Notify = "expired"
				}
				untold(d, p)
				continue
			}
			if p.Kind == "dialog" && !noticeOnScreen(d, p) {
				continue // answered or gone before its notice got through: telling the owner now would mislead
			}
			if p.Claimed == 0 || now-p.Claimed > claimTTL || claimerDead(p.ClaimPid) {
				p.Claimed, p.ClaimPid = now, os.Getpid()
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
		// Each delivery takes a second or more, so a dialog claimed above can be answered or gone by now.
		if p.Kind == "dialog" {
			if stale, err := t.dropStaleDialogNotice(p); err != nil || stale {
				if err != nil {
					return delivered, err
				}
				continue
			}
		}
		outcome := t.deliver(p)
		if outcome == "delivered" {
			delivered++
		}
		if err := t.Reg.Update(func(d *Data) error {
			for i, q := range d.Pending {
				if q.Task == p.Task && q.Pane == p.Pane && q.Created == p.Created {
					if outcome == "retry" || outcome == "held" {
						q.Claimed, q.ClaimPid = 0, 0
					} else {
						if x := d.Tasks[p.Task]; x != nil && p.Kind == "" {
							x.Notify = outcome
						}
						if outcome != "delivered" {
							untold(d, q)
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

// dropStaleDialogNotice reads the task's dialog mark right before its notice is sent, and drops the notice
// when the dialog is no longer on screen: answered, gone, or the task ended since it was claimed.
func (t *Tasks) dropStaleDialogNotice(p Pending) (bool, error) {
	d, err := t.Reg.Read()
	if err != nil {
		return false, err
	}
	if noticeOnScreen(d, &p) {
		return false, nil
	}
	return true, t.Reg.Update(func(d *Data) error {
		for i, q := range d.Pending {
			if q.Task == p.Task && q.Pane == p.Pane && q.Created == p.Created {
				d.Pending = append(d.Pending[:i], d.Pending[i+1:]...)
				break
			}
		}
		return nil
	})
}

// untold clears the mark's notified flag when a dialog notice is dropped before it reached the owner,
// so the watcher announces the dialog again while it stays on screen. One task wait returned stays told.
func untold(d *Data, p *Pending) {
	if p.Kind != "dialog" || !noticeOnScreen(d, p) {
		return
	}
	if p.Task == "" {
		d.Panes[p.Pane].Dialog.Notified = false
	} else if x := d.Tasks[p.Task]; !x.Dialog.Returned {
		x.Dialog.Notified = false
	}
}

// about names what a notice is about, for warnings.
func (p Pending) about() string {
	if p.Task == "" && p.Pane != "" {
		return "pane " + p.Pane
	}
	return "task " + p.Task
}

func (t *Tasks) deliver(p Pending) string {
	b, pane, err := t.paneByID(p.Owner, p.OwnerRef)
	if err != nil {
		Warn(fmt.Sprintf("could not notify the owner of %s: %v", p.about(), err))
		return "retry"
	}
	if pane == nil {
		return "owner-gone"
	}
	if p.Kind == "dialog" && !ForProcess(pane.Process).Calibrated {
		return "held" // typed blind, its letters could land in the lead's own menus: task wait returns it
	}
	ops := t.ops(b, nil)
	ops.Task = p.Task
	if d, err := t.Reg.Read(); err == nil && d.Tasks[p.Task] != nil {
		ops.Project, ops.From = d.Tasks[p.Task].Project, d.Tasks[p.Task].Pane // the worker's report
	}
	if sent, err := ops.Msg(pane, p.Text, MsgOpts{Tag: p.Tag, Retry: true}); err != nil {
		var te *term.Error
		if sent.Typed {
			// Typed, but not confirmed (still on the input line, or a backend error after typing): a busy
			// harness can hold a queued message there and deliver it later. A retry could type it a second
			// time, so it counts as delivered.
			Warn(fmt.Sprintf("the notice about %s was typed but not confirmed; not sent again: %v", p.about(), err))
			return "delivered"
		}
		if !errors.As(err, &te) || te.Code != term.CodeDraft {
			Warn(fmt.Sprintf("could not notify the owner of %s: %v", p.about(), err))
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
		c.Closed, c.Dialog = true, nil
		if c.Status == "running" {
			c.Status, c.Ended, c.Summary = "failed", t.clock().Now(), "closed by its owner before it finished"
		}
		if c.Owned {
			delete(d.Panes, c.Pane)
		}
		result = c
		return nil
	})
	if err == nil {
		t.logTask("task.close", result, result.Status, "")
	}
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
	t.logTask("task.accept", x, "done", "")
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
		data, err := os.ReadFile(ExpandHome(o.BriefFile))
		if err != nil {
			return nil, term.UsageErr("--brief-file: %v", err)
		}
		brief = string(data)
	}
	return t.New(NewOpts{Harness: o.Harness, Title: "blind review of " + x.ID, Brief: brief, Cwd: x.Cwd, Where: o.Where,
		Command: cmd, Timeout: o.Timeout, ReviewOf: x.ID, Blind: true})
}
