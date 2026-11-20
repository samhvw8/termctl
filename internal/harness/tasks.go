package harness

import (
	"encoding/json"
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
	taskInline       = 500 // a brief up to this long is typed directly; longer goes through its file
	WaitTimeout      = 600.0
	waitPoll         = 3.0
	notifyRetry      = 10.0 // how long `task done` keeps trying before it leaves the result pending
	notifyPoll       = 2.0
	PendingTTL       = 3600.0
	idleWait         = 600.0 // how long a notice waits for its owner to look idle before it is typed anyway
	claimTTL         = 600.0 // a claim older than this belongs to a process that died; above deliveryBound
	reservationTTL   = 600.0 // a task with no pane after this long never got one
	unreachableTTL   = 600.0 // a server that has not answered for this long is gone for good
	stallAfter       = 600.0 // a running worker idle this long with no done and no progress looks stalled
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

// taskFooter ends a task's brief: its capability card, then how to report (a deferred job acks once and
// never replies). running is how many tasks of its tree run, itself included.
func taskFooter(j *Job, l *Lease, running int) string {
	how := footer(j.ID)
	if j.Defer {
		how = deferFooter(j.ID)
	}
	return roleCard(l, running) + " " + how
}

// sendFooter ends a task send: how to report this round. A deferred worker is reminded not to reply.
func sendFooter(j *Job) string {
	if j.Defer {
		return fmt.Sprintf(`deferred: do not reply; when finished, run: harness-ops task done %s --status ok|failed|blocked --summary "<one line result>"`, j.ID)
	}
	return footer(j.ID)
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
func (t *Tasks) taskOps(b term.Backend, sender *term.Pane, j *Job, l *Lease) *Ops {
	ops := t.ops(b, sender)
	ops.Task, ops.Project = j.ID, l.Project
	return ops
}

func (t *Tasks) journal() *Journal { return &Journal{Root: t.Reg.Root, Clock: t.clock()} }

// logTask logs a task event from this sender, in the task's project.
func (t *Tasks) logTask(event string, j *Job, l *Lease, status, text string) {
	if j == nil || l == nil {
		return
	}
	project := l.Project
	if project == "" {
		project = ProjectOf(t.Sender)
	}
	e := Entry{Event: event, Project: project, From: address(t.Sender), FromName: entryName(t.Sender), Task: j.ID,
		Harness: l.Harness, Status: status, Text: text}
	e.To = l.Pane
	if t.Sender != nil && t.Sender.ID == l.Pane {
		e.To = l.Owner // the worker reports to its owner
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

func (t *Tasks) find(d *Data, id string) (*Job, *Lease, error) {
	if j, ok := d.Jobs[id]; ok {
		return j, d.leaseOf(j), nil
	}
	var hits []*Job
	for k, j := range d.Jobs {
		if len(id) >= 3 && strings.HasPrefix(k, id) {
			hits = append(hits, j)
		}
	}
	if len(hits) == 1 {
		return hits[0], d.leaseOf(hits[0]), nil
	}
	word := "no task"
	if len(hits) > 1 {
		word = "several tasks"
	}
	return nil, nil, &findError{term.TargetErr("%s match '%s'", word, id), len(hits) > 1}
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
func (t *Tasks) owns(l *Lease) bool {
	return t.Sender != nil && l != nil && l.Owner == t.Sender.ID && sameServer(l.OwnerRef, t.callerRef())
}

// warnDownOnce warns once per process that another owner's task cannot be checked. Only the owner
// records an unreachable server: a process in another context may not reach a server the owner does, and
// must not lose a live task it does not own (round-5 review D4).
func (t *Tasks) warnDownOnce(id string, cause error) {
	t.downMu.Lock()
	defer t.downMu.Unlock()
	if t.downSeen[id] {
		return
	}
	if t.downSeen == nil {
		t.downSeen = map[string]bool{}
	}
	t.downSeen[id] = true
	Warn(fmt.Sprintf("cannot check task %s: %v; only its owner records that its server does not answer", id, cause))
}

// Reconcile marks active tasks whose pane is gone as lost. Unknown is not gone: a server that does not
// answer changes nothing until it has not answered for unreachableTTL; then it is gone for good, and its
// tasks are lost. Only a task's owner records that: other processes leave the task alone and warn once. "cannot check" is warned once when a task's server stops answering, not on every call.
func (t *Tasks) Reconcile() error {
	memo := t.memo == nil
	if memo {
		t.startMemo()
		defer t.stopMemo()
	}
	d, err := t.Reg.Read()
	if err != nil {
		return err
	}
	var lost, exited, back []string
	down := map[string]error{} // tasks whose server does not answer, and the first time or past the TTL
	restarted := map[string]bool{}
	now := t.clock().Now()
	for _, j := range d.Jobs {
		l := d.leaseOf(j)
		if j.Status == "exited" && !j.Closed && l.Pane != "" {
			if oj, _ := otherTaskOnPane(d, j, l); oj != nil {
				if l.Restarted {
					restarted[j.ID] = false
				}
				continue
			}
			_, p, err := t.paneByID(l.Pane, l.Ref)
			if err != nil {
				continue
			}
			if want := p != nil && harnessBack(l, p); want != l.Restarted {
				restarted[j.ID] = want
			}
			continue
		}
		if !open(j.Status) {
			continue
		}
		if j.Defer && j.Closed {
			continue // the watcher closed its pane on ok: gone on purpose, not lost
		}
		if l.Pane == "" {
			if now-j.Started > reservationTTL {
				lost = append(lost, j.ID)
			}
			continue
		}
		_, p, err := t.paneByID(l.Pane, l.Ref)
		if err != nil && !t.owns(l) {
			t.warnDownOnce(j.ID, err)
			continue
		}
		if err != nil {
			if l.Unreachable == 0 || now-l.Unreachable > unreachableTTL {
				down[j.ID] = err
			}
			continue
		}
		if l.Unreachable != 0 && t.owns(l) {
			back = append(back, j.ID)
		}
		switch {
		case p == nil:
			lost = append(lost, j.ID)
		case l.Launched && isShell(p.Process):
			exited = append(exited, j.ID)
		}
	}
	if len(lost)+len(exited)+len(back)+len(down)+len(restarted) == 0 {
		return nil
	}
	return t.Reg.Update(func(d *Data) error {
		lose := func(j *Job, l *Lease, why string) {
			now := t.clock().Now()
			j.Status, j.Ended, l.Dialog = "lost", now, nil
			if j.Summary == "" {
				j.Summary = why
			}
			if l.State == "active" {
				l.State, l.Ended = "lost", now
			}
			if l.Pane != "" {
				delete(d.Created, l.Pane)
			}
		}
		for _, id := range lost {
			if j := d.Jobs[id]; j != nil && open(j.Status) {
				lose(j, d.leaseOf(j), "its pane is gone")
			}
		}
		for _, id := range exited {
			t.markExited(d, id, nil)
		}
		for id, want := range restarted {
			if j := d.Jobs[id]; j != nil && j.Status == "exited" && !j.Closed {
				d.leaseOf(j).Restarted = want
			}
		}
		for _, id := range back {
			if j := d.Jobs[id]; j != nil && d.leaseOf(j).Unreachable != 0 {
				d.leaseOf(j).Unreachable = 0
				Warn(fmt.Sprintf("task %s: its server answers again", j.ID))
			}
		}
		for id, cause := range down { // under the lock, so two processes warn about one task once
			j := d.Jobs[id]
			if j == nil || !open(j.Status) {
				continue
			}
			l := d.leaseOf(j)
			switch {
			case l.Unreachable == 0:
				l.Unreachable = now
				Warn(fmt.Sprintf("cannot check task %s: %v; it is lost if its server does not answer for %gs", j.ID, cause, unreachableTTL))
			case now-l.Unreachable > unreachableTTL:
				lose(j, l, fmt.Sprintf("its server has not answered for %gs: %v", unreachableTTL, cause))
			}
		}
		return nil
	})
}

// harnessBack reports whether pane p is running lease l's harness again after its job was marked exited.
func harnessBack(l *Lease, p *term.Pane) bool {
	if p == nil || p.Process == "" || isShell(p.Process) {
		return false
	}
	if l.Harness == "" || l.Harness == "generic" {
		return true
	}
	return HarnessName(p) == l.Harness
}

// markExited records that the harness is gone from a live pane, and queues the owner's notice.
func (t *Tasks) markExited(d *Data, id string, code *int) {
	j := d.Jobs[id]
	if j == nil || !open(j.Status) {
		return
	}
	l := d.leaseOf(j)
	now := t.clock().Now()
	j.Status, j.Ended, j.ExitCode, j.Notify, l.Dialog, l.Restarted = "exited", now, code, "pending", nil, false
	if l.State == "active" {
		l.State, l.Ended = "exited", now
	}
	why := "the harness exited"
	if code != nil {
		why = fmt.Sprintf("the harness exited with status %d", *code)
	}
	if j.Summary == "" {
		j.Summary = why
	}
	d.Notices = append(d.Notices, &Notice{ID: newNoticeID(d), Lease: l.ID, Owner: l.Owner, OwnerRef: l.OwnerRef, Kind: "result",
		Tag:     fmt.Sprintf("[agent-ops from %s (%s) id=%s folder=%s]", j.ID, l.Harness, l.Pane, l.Cwd),
		Text:    fmt.Sprintf("RE: task %s (%s) exited: %s; its pane stays open, see it with: harness-ops read %s", j.ID, j.Title, why, j.ID),
		Created: now})
}

// Exited is called from the worker's pane when its harness ends. Reconcile is the fallback.
func (t *Tasks) Exited(id string, code int) (*Task, error) {
	var tid string
	err := t.Reg.Update(func(d *Data) error {
		j, l, err := t.find(d, id)
		if err != nil {
			return err
		}
		if t.Sender != nil && t.Sender.ID != l.Pane && t.Sender.ID != l.Owner {
			return term.TargetErr("%s is neither the worker nor the owner of task %s", t.Sender.Address(), j.ID)
		}
		tid = j.ID
		t.markExited(d, j.ID, &code)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if d, err := t.Reg.Read(); err == nil {
		j := d.Jobs[tid]
		t.logTask("task.exited", j, d.leaseOf(j), j.Status, fmt.Sprintf("the harness exited with status %d", code))
	}
	if _, err := t.FlushPending(); err != nil {
		return nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	return d.TaskView(tid), nil
}

// NewOpts are options of New.
type NewOpts struct {
	Harness, Title, Brief, BriefFile, Cwd, Where, Reuse, Command string
	MaxTasks                                                     int
	Timeout                                                      float64
	ReviewOf                                                     string
	Blind                                                        bool
	Defer                                                        bool // a deferred job: ack once, no notice, closed on ok
	SubDepth, TreeMax                                            int  // levels below it may start (0: a leaf); running tasks in its tree (0: inherit)
}

func holdsTask(d *Data, paneID string) bool {
	for _, j := range d.Jobs {
		if open(j.Status) && d.leaseOf(j).Pane == paneID {
			return true
		}
	}
	return false
}

// reserve takes a slot under the lock, before the spawn, so parallel calls cannot exceed the cap. It
// returns the task and how many tasks of its tree run, itself included (for its capability card).
func (t *Tasks) reserve(owner *term.Pane, o NewOpts, pane *term.Pane) (string, int, error) {
	limit := o.MaxTasks
	if limit == 0 {
		var err error
		if limit, err = envInt("TERMCTL_MAX_TASKS", defaultMaxTasks); err != nil {
			return "", 0, err
		}
	}
	depthLimit, err := envInt("TERMCTL_MAX_DEPTH", defaultMaxDepth)
	if err != nil {
		return "", 0, err
	}
	var id string
	inTree := 0
	err = t.Reg.Update(func(d *Data) error {
		running := 0
		var parent *Job
		for _, x := range d.Jobs {
			xl := d.leaseOf(x)
			if x.Status == "running" && xl.Owner == owner.ID {
				running++
			}
			if open(x.Status) && xl.Pane == owner.ID {
				parent = x
			}
		}
		if running >= limit {
			return term.Errf(term.CodeGuard, "%s already runs %d tasks (limit %d); use --max to raise it", owner.Address(), running, limit)
		}
		if pane != nil && holdsTask(d, pane.ID) {
			return term.Errf(term.CodeGuard, "%s already holds a running task", pane.Address())
		}
		now := t.clock().Now()
		j := &Job{ID: newJobID(d), Title: o.Title, Status: "running", Started: now, Notify: "none", Owned: pane == nil, Round: 1, ReviewOf: o.ReviewOf,
			Blind: o.Blind, Defer: o.Defer}
		l := &Lease{ID: newLeaseID(d), Owner: owner.ID, OwnerRef: t.SenderRef, Ref: t.Backend.ServerRef(), Harness: o.Harness, Cwd: o.Cwd,
			Project: ProjectOf(owner), Job: j.ID, State: "active", Started: now}
		j.Lease = l.ID
		var pl *Lease
		if parent != nil {
			pl = d.leaseOf(parent)
		}
		if err := place(d, j, l, parent, pl, o.SubDepth, o.TreeMax, depthLimit); err != nil {
			return err
		}
		if pl != nil {
			l.Parent = pl.ID
		}
		if pane != nil {
			// The pane's supervision passes to this task: a lease left on it ends (one active lease per pane).
			for _, lid := range sortedLeaseIDs(d) {
				if old := d.Leases[lid]; old.State == "active" && old.Pane == pane.ID && sameServer(old.Ref, l.Ref) {
					endLease(old, now)
				}
			}
			l.Pane = pane.ID
		}
		d.Jobs[j.ID], d.Leases[l.ID] = j, l
		id = j.ID
		inTree = runningInTree(d, rootOf(l))
		return nil
	})
	return id, inTree, err
}

// writeBrief files the brief in the task's project folder of the journal, with the footer typed after it.
func (t *Tasks) writeBrief(j *Job, l *Lease, text, foot string) (string, error) {
	return t.journal().WriteFile(l.Project, "brief", j.ID, strings.TrimRight(text, " \n\t")+"\n\n"+foot)
}

func prompt(id string, owner *term.Pane, text, brief, foot string) string {
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
	return fmt.Sprintf("[task %s from %s] %s %s", id, name, body, foot)
}

func (t *Tasks) fail(id string, cause error) {
	msg := strings.SplitN("could not start: "+cause.Error(), "\n", 2)[0]
	if err := t.Reg.Update(func(d *Data) error {
		j := d.Jobs[id]
		j.Status, j.Ended, j.Summary = "failed", t.clock().Now(), msg
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
	id, inTree, err := t.reserve(owner, o, pane)
	if err != nil {
		return nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	j := d.Jobs[id]
	l := d.leaseOf(j)
	ops.Task, ops.Project = id, l.Project
	if err := t.start(ops, owner, o, j, l, brief, taskFooter(j, l, inTree), pane); err != nil {
		t.fail(id, err)
		t.logTask("task.new", j, l, "failed", err.Error())
		return nil, err
	}
	t.WarnUncalibratedLead()
	if d, err = t.Reg.Read(); err != nil {
		return nil, err
	}
	j = d.Jobs[id]
	l = d.leaseOf(j)
	text := j.Title
	if j.ReviewOf != "" {
		text += " (reviews " + j.ReviewOf + ")"
	}
	e := Entry{Event: "task.new", Project: l.Project, From: address(owner), FromName: entryName(owner), To: l.Pane, ToName: id,
		Task: id, Harness: l.Harness, Status: j.Status, Text: text, File: j.Brief}
	t.journal().Record(e)
	return d.TaskView(id), nil
}

func (t *Tasks) start(ops *Ops, owner *term.Pane, o NewOpts, j *Job, l *Lease, brief, foot string, pane *term.Pane) error {
	id, lid := j.ID, l.ID
	path, err := t.writeBrief(j, l, brief, foot)
	if err != nil {
		return err
	}
	if err := t.Reg.Update(func(d *Data) error { d.Jobs[id].Brief = path; return nil }); err != nil {
		return err
	}
	spawned := pane == nil
	if pane == nil {
		ref := t.Backend.ServerRef()
		pane, err = ops.Spawn(o.Harness, o.Where, o.Cwd, SpawnOpts{
			Label: id, Title: paneTitle(j, l), Command: o.Command, Timeout: o.Timeout, Owned: t.owned(),
			Wrap: func(cmd string) string { return SurvivingCommand(cmd, t.exitHook(id)) },
			OnCreated: func(p *term.Pane) error {
				if t.spawnHook != nil {
					t.spawnHook(p)
				}
				return t.Reg.Update(func(d *Data) error {
					t.record(d, p, o.Harness, id, o.Cwd, ref, lid)
					d.Leases[lid].Pane = p.ID
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
		if err := term.SetTitle(t.Backend, pane, paneTitle(j, l)); err != nil {
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
	if err := submit(pane, prompt(id, owner, brief, path, foot), prof); err != nil {
		return err
	}
	return t.Reg.Update(func(d *Data) error { d.Leases[lid].Launched = true; return nil })
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

func (t *Tasks) record(d *Data, p *term.Pane, harness, label, cwd string, ref map[string]string, lease string) {
	d.Created[p.ID] = &CreatedPane{Ref: ref, Harness: harness, Label: label, Cwd: cwd, ForLease: lease, CreatedAt: t.clock().Now()}
}

// ownsPane is the active lease on a pane when the caller holds it (it started the pane, or a task of its holds
// it): the lease names the caller's pane on the caller's server, and the pane's server (paneRef) is the one
// recorded, so a lease from before a terminal server restart (a reused pane ID) counts for nothing.
func (t *Tasks) ownsPane(d *Data, pane string, paneRef map[string]string) *Lease {
	if l := activeLeaseForPane(d, pane, paneRef); t.owns(l) {
		return l
	}
	return nil
}

// Spawn starts a harness and records the pane, so close may act on it later.
func (t *Tasks) Spawn(harness, where, cwd string, opts SpawnOpts) (*term.Pane, error) {
	ref := t.Backend.ServerRef()
	opts.Owned = t.owned()
	opts.Wrap = func(cmd string) string { return SurvivingCommand(cmd, "") }
	opts.OnCreated = func(p *term.Pane) error {
		return t.Reg.Update(func(d *Data) error {
			owner := ""
			if t.Sender != nil {
				owner = t.Sender.ID
			}
			now := t.clock().Now()
			l := &Lease{ID: newLeaseID(d), Owner: owner, OwnerRef: t.SenderRef, Pane: p.ID, Ref: ref, Harness: harness, Label: opts.Label,
				Cwd: cwd, Project: ProjectOf(t.Sender), Depth: 1, TreeMax: defaultTreeMax, State: "active", Launched: true, Started: now}
			l.Root = l.ID
			for _, lid := range sortedLeaseIDs(d) { // a lease left from an earlier pane of this ID on this server
				if old := d.Leases[lid]; old.State == "active" && old.Pane == p.ID && sameServer(old.Ref, ref) {
					endLease(old, now)
				}
			}
			d.Leases[l.ID] = l
			t.record(d, p, harness, opts.Label, cwd, ref, l.ID)
			return nil
		})
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
	if t.ownsPane(d, p.ID, ref) == nil {
		return false, nil
	}
	if err := t.Reg.Update(func(d *Data) error {
		if l := t.ownsPane(d, p.ID, ref); l != nil {
			l.Worked = t.clock().Now()
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
	ref := t.Backend.ServerRef()
	for _, l := range d.Leases {
		if l.State == "active" && l.Dialog != nil && sameServer(l.Ref, ref) && t.owns(l) {
			out[l.Pane] = l.Dialog.Fingerprint
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
		if t.ownsPane(d, p.ID, t.Backend.ServerRef()) != nil {
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
	for _, l := range d.Leases {
		if l.State == "active" && l.Owner == t.Sender.ID {
			mine[l.Pane] = true
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
	ref := t.Backend.ServerRef()
	c := d.Created[p.ID] // an adopted pane was started by hand, not by harness-ops: it has none
	owned := c != nil && sameServer(c.Ref, ref) // a record from before a terminal server restart counts for nothing
	var task *Job
	var taskLease *Lease
	for _, j := range d.Jobs {
		jl := d.leaseOf(j)
		if !settled(j.Status) && !j.Closed && jl.Pane == p.ID && sameServer(jl.Ref, ref) {
			if task == nil || newerTask(j, jl, task, taskLease) {
				task, taskLease = j, jl
			}
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
		e.Task, e.Project = task.ID, d.leaseOf(task).Project
		if e.Project == "" {
			e.Project = ProjectOf(t.Sender)
		}
	}
	if force {
		e.Status = "forced"
	}
	t.journal().Record(e)
	return p, t.Reg.Update(func(d *Data) error {
		now := t.clock().Now()
		delete(d.Created, p.ID)
		for _, l := range d.Leases { // the pane is gone: its supervision ends
			if l.Pane == p.ID && sameServer(l.Ref, ref) {
				endLease(l, now)
			}
		}
		for _, j := range d.Jobs {
			if jl := d.leaseOf(j); jl.Pane == p.ID && sameServer(jl.Ref, ref) && !settled(j.Status) && !j.Closed {
				j.Closed, jl.Restarted = true, false
				if j.Status == "running" {
					j.Status, j.Ended, j.Summary = "failed", now, "its pane was closed with --force"
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
	for _, j := range d.Jobs {
		if j.Blind && d.leaseOf(j).Pane == t.Sender.ID && open(j.Status) {
			return j.ReviewOf
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
	for _, j := range d.Jobs {
		l := d.leaseOf(j)
		if j.ID != hidden && (all || (!j.Closed && (t.Sender == nil || l.Owner == t.Sender.ID))) {
			out = append(out, d.TaskView(j.ID))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started < out[j].Started })
	return out, nil
}

// Get finds one task by ID or unique prefix.
func (t *Tasks) Get(id string) (*Task, error) {
	d, j, _, err := t.getJob(id)
	if err != nil {
		return nil, err
	}
	return d.TaskView(j.ID), nil
}

// getJob is Get for engine code: the registry as read, the job and its lease.
func (t *Tasks) getJob(id string) (*Data, *Job, *Lease, error) {
	if err := t.Reconcile(); err != nil {
		return nil, nil, nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, nil, nil, err
	}
	j, l, err := t.find(d, id)
	if err != nil {
		return nil, nil, nil, err
	}
	return d, j, l, nil
}

// Show is Get for the caller's eyes: a blind reviewer cannot read the task it reviews.
func (t *Tasks) Show(id string) (*Task, error) {
	d, j, _, err := t.getJob(id)
	if err != nil {
		return nil, err
	}
	if j.ID == t.reviewedBySender(d) {
		return nil, term.TargetErr("a blind reviewer cannot read task %s", j.ID)
	}
	return d.TaskView(j.ID), nil
}

// Send messages a task's pane. New work on a finished task reopens it: the worker must report again.
func (t *Tasks) Send(id, text string, force bool) error {
	_, j, l, err := t.getJob(id)
	if err != nil {
		return err
	}
	if j.Closed {
		return term.UsageErr("task %s is closed", j.ID)
	}
	if j.Status == "lost" || j.Status == "exited" {
		return term.UsageErr("task %s is %s: there is no harness to receive this", j.ID, j.Status)
	}
	b, p, err := t.paneByID(l.Pane, l.Ref)
	if err != nil {
		return err
	}
	if p == nil {
		return term.UsageErr("task %s: its pane is gone", j.ID)
	}
	sent, err := t.taskOps(b, t.Sender, j, l).Msg(p, fmt.Sprintf("task %s: %s (%s)", j.ID, text, sendFooter(j)), MsgOpts{Force: force})
	if err != nil && !sent.Typed {
		return err
	}
	// Typed but not confirmed (exit 5) may still arrive: open the round anyway, or the worker's
	// task done for it would be refused as already reported.
	t.WarnUncalibratedLead()
	if j.Status == "running" {
		return err
	}
	if uerr := t.Reg.Update(func(d *Data) error {
		c := d.Jobs[j.ID]
		c.Status, c.Ended, c.Notify, c.Seen = "running", 0, "none", false
		c.Round++
		// A mark on the lease now is its pane's, taken while the task did not run; the task's own mark
		// starts empty, as it did when the two were kept apart.
		d.leaseOf(c).Dialog = nil
		return nil
	}); uerr != nil {
		return uerr
	}
	return err
}

func unseen(d *Data, owner string) []*Job {
	var out []*Job
	for _, j := range d.Jobs {
		if d.leaseOf(j).Owner == owner && j.Status != "running" && !j.Closed && !j.Seen {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Ended < out[k].Ended })
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
		_, j, l, err := t.getJob(id)
		if err != nil {
			return nil, err
		}
		// Waiting marks a result seen and a dialog returned, which the owner then never hears of.
		if t.Sender == nil || l.Owner != t.Sender.ID || !sameServer(l.OwnerRef, t.callerRef()) {
			return nil, term.TargetErr("only the owner of task %s can wait for it", j.ID)
		}
		id = j.ID
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
		found := ""
		if any {
			if u := unseen(d, me); len(u) > 0 {
				found = u[0].ID
			} else {
				running := false
				for _, j := range d.Jobs {
					running = running || (j.Status == "running" && d.leaseOf(j).Owner == me)
				}
				if !running {
					return nil, term.UsageErr("no running tasks and no uncollected results to wait for")
				}
			}
		} else if d.Jobs[id].Status != "running" {
			found = id
		}
		if found != "" {
			var result *Task
			err := t.Reg.Update(func(d *Data) error {
				d.Jobs[found].Seen = true
				result = d.TaskView(found)
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
	var watch []*Lease
	if id != "" {
		watch = []*Lease{d.leaseOf(d.Jobs[id])}
	} else {
		watch = runningLeases(d, owner, t.SenderRef)
	}
	if t.Sender != nil && t.Watching(t.Sender.ID) != 0 {
		var marked []*Lease
		for _, l := range watch {
			if l.Dialog != nil && !l.Dialog.Returned && !noticeDelivered(d, l) {
				marked = append(marked, l)
			}
		}
		watch = marked
	}
	sigs := t.signals(watch)
	if err := t.apply(sigs, false); err != nil {
		return nil, err
	}
	for _, s := range sigs {
		if s.Kind != "dialog" || s.Task == "" {
			continue
		}
		var result *Task
		if err := t.Reg.Update(func(d *Data) error {
			j := d.Jobs[s.Task]
			l := d.leaseOf(j)
			if j != nil && dialogOnScreen(j, l, s.Dialog.Fingerprint) && !l.Dialog.Returned && !noticeDelivered(d, l) {
				// The lead has it from task wait: the watcher must not tell it again, and a notice
				// already queued would arrive stale.
				l.Dialog.Returned, l.Dialog.Notified = true, true
				keep := d.Notices[:0]
				for _, n := range d.Notices {
					if n.Kind != "dialog" || n.Lease != l.ID || n.Dialog != s.Dialog.Fingerprint {
						keep = append(keep, n)
					}
				}
				d.Notices = keep
				result = d.TaskView(j.ID)
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
func noticeDelivered(d *Data, l *Lease) bool {
	if l.Dialog == nil || !l.Dialog.Notified {
		return false
	}
	for _, n := range d.Notices {
		if n.Kind == "dialog" && n.Lease == l.ID && n.Dialog == l.Dialog.Fingerprint {
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

func (t *Tasks) doneTag(j *Job, l *Lease, b term.Backend) (string, error) {
	if t.Sender != nil {
		return t.ops(b, t.Sender).Tag()
	}
	return fmt.Sprintf("[agent-ops from %s (%s) id=%s folder=%s]", j.ID, l.Harness, l.Pane, l.Cwd), nil
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
	var doneJob Job
	var doneLease Lease
	err := t.Reg.Update(func(d *Data) error {
		j, l, err := t.find(d, id)
		if err != nil {
			return err
		}
		if t.Sender != nil && t.Sender.ID != l.Pane && t.Sender.ID != l.Owner {
			return term.TargetErr("%s is neither the worker nor the owner of task %s", t.Sender.Address(), j.ID)
		}
		if j.Status != "running" && j.Status != "blocked" {
			return term.UsageErr("task %s is already %s", j.ID, j.Status)
		}
		j.Status, j.Ended, j.Summary, j.Notify = stored, t.clock().Now(), summary, "pending"
		l.Dialog = nil // the worker is reporting, so nothing waits on screen
		l.IdleSince, l.Stall = 0, nil
		tid = j.ID
		if j.Defer { // nobody reads a deferred job's result: recorded and journaled, never typed
			j.Notify = "none"
			doneJob, doneLease = *j, *l
			return nil
		}
		doneJob, doneLease = *j, *l
		tag, err := t.doneTag(j, l, t.Backend)
		if err != nil {
			return err
		}
		d.Notices = append(d.Notices, &Notice{ID: newNoticeID(d), Lease: l.ID, Owner: l.Owner, OwnerRef: l.OwnerRef, Tag: tag, Kind: "result",
			Text: fmt.Sprintf("RE: task %s (%s) %s%s: %s", tid, j.Title, j.Status, roundNote(j.Round), summary), Created: t.clock().Now()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	t.logTask("task.done", &doneJob, &doneLease, doneJob.Status, summary)
	deadline := t.clock().Now() + notifyRetry
	for {
		if _, err := t.FlushPending(); err != nil {
			return nil, err
		}
		d, err := t.Reg.Read()
		if err != nil {
			return nil, err
		}
		if d.Jobs[tid].Notify != "pending" || t.clock().Now() >= deadline {
			return d.TaskView(tid), nil
		}
		t.sleep(notifyPoll)
	}
}

// Progress records the worker's latest progress line on its running task, keeping its status and round. Only
// the worker (the task's pane) or the owner may. It ends a stall mark and restarts the idle timer. With
// notify it queues one notice to the owner (kind "progress") through the pending path, replacing an older
// progress notice of the task not yet being typed, so a busy owner hears only the latest.
func (t *Tasks) Progress(id, text string, notify bool) (*Task, error) {
	text = OneLine(text)
	if text == "" {
		return nil, term.UsageErr("task progress needs --summary")
	}
	var out *Task
	var outJob Job
	var outLease Lease
	err := t.Reg.Update(func(d *Data) error {
		j, l, err := t.find(d, id)
		if err != nil {
			return err
		}
		if t.Sender == nil || (t.Sender.ID != l.Pane && t.Sender.ID != l.Owner) {
			return term.TargetErr("only the worker or the owner of task %s can report its progress", j.ID)
		}
		if j.Closed {
			return term.UsageErr("task %s is closed", j.ID)
		}
		if j.Status != "running" {
			return term.UsageErr("task %s is %s: progress is for a running task", j.ID, j.Status)
		}
		now := t.clock().Now()
		l.Progress = &ProgressNote{Text: text, At: now}
		l.IdleSince, l.Stall = 0, nil
		if notify {
			keep := d.Notices[:0]
			for _, n := range d.Notices {
				if n.Kind == "progress" && n.Lease == l.ID && n.Claimed == 0 {
					continue // not being typed: the newer line replaces it
				}
				keep = append(keep, n)
			}
			d.Notices = keep
			tag, err := t.doneTag(j, l, t.Backend)
			if err != nil {
				return err
			}
			d.Notices = append(d.Notices, &Notice{ID: newNoticeID(d), Lease: l.ID, Owner: l.Owner, OwnerRef: l.OwnerRef, Tag: tag, Kind: "progress",
				Text: fmt.Sprintf("PROGRESS task %s (%s)%s: %s", j.ID, j.Title, roundNote(j.Round), text), Created: now})
		}
		out, outJob, outLease = d.TaskView(j.ID), *j, *l
		return nil
	})
	if err != nil {
		return nil, err
	}
	t.logTask("task.progress", &outJob, &outLease, outJob.Status, text)
	if notify {
		if _, err := t.FlushPending(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TaskNode is a task with the tasks started from its worker's pane (their Parent), for task ls --tree.
type TaskNode struct {
	Task     *Task
	Children []*TaskNode
}

// MarshalJSON is the task's own object with a "children" array.
func (n *TaskNode) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(n.Task)
	if err != nil {
		return nil, err
	}
	kids := n.Children
	if kids == nil {
		kids = []*TaskNode{}
	}
	c, err := json.Marshal(kids)
	if err != nil {
		return nil, err
	}
	out := append(b[:len(b)-1:len(b)-1], []byte(`,"children":`)...)
	return append(append(out, c...), '}'), nil
}

// Tree is Ls with each task's descendants nested under it, following Parent across owners and projects.
// Without all, the roots are the caller's tasks (as Ls lists them) and closed descendants are left out;
// with all, every task, as a forest whose roots have no parent among them. A blind reviewer never sees
// what it reviews.
func (t *Tasks) Tree(all bool) ([]*TaskNode, error) {
	roots, err := t.Ls(all)
	if err != nil {
		return nil, err
	}
	d, err := t.Reg.Read()
	if err != nil {
		return nil, err
	}
	hidden := t.reviewedBySender(d)
	views := d.TaskViews()
	kids := map[string][]*Task{}
	for _, x := range views {
		if x.Parent != "" && x.ID != hidden && (all || !x.Closed) {
			kids[x.Parent] = append(kids[x.Parent], x)
		}
	}
	for _, k := range kids {
		sort.Slice(k, func(i, j int) bool { return k[i].Started < k[j].Started })
	}
	if all {
		var top []*Task
		for _, x := range roots {
			if p := views[x.Parent]; p == nil || p.ID == hidden {
				top = append(top, x)
			}
		}
		roots = top
	}
	seen := map[string]bool{}
	var grow func(x *Task) *TaskNode
	grow = func(x *Task) *TaskNode {
		seen[x.ID] = true
		n := &TaskNode{Task: x}
		for _, c := range kids[x.ID] {
			if !seen[c.ID] {
				n.Children = append(n.Children, grow(c))
			}
		}
		return n
	}
	var out []*TaskNode
	for _, x := range roots {
		if !seen[x.ID] {
			out = append(out, grow(x))
		}
	}
	return out, nil
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
	if err != nil || len(d.Notices) == 0 {
		return 0, err
	}
	now := t.clock().Now()
	var claimed []Notice
	if err := t.Reg.Update(func(d *Data) error {
		keep := d.Notices[:0]
		for _, n := range d.Notices {
			liveClaim := n.Claimed != 0 && now-n.Claimed <= claimTTL && !claimerDead(n.ClaimPid)
			j := d.jobOf(d.Leases[n.Lease])
			if now-n.Created > PendingTTL && !liveClaim { // expiring a notice being typed would announce it again
				if j != nil && n.Kind == "result" {
					j.Notify = "expired"
				}
				untold(d, n)
				continue
			}
			if !noticeLive(d, n) && (n.Kind == "dialog" || !liveClaim) {
				continue // answered, ended or gone before its notice got through: telling the owner now would mislead
			}
			if n.Kind == "result" && j != nil && (j.Seen || j.Closed) && !liveClaim {
				j.Notify = "seen" // the owner took the result with task wait: the notice would wake it for nothing
				continue
			}
			if n.Claimed == 0 || now-n.Claimed > claimTTL || claimerDead(n.ClaimPid) {
				n.Claimed, n.ClaimPid = now, os.Getpid()
				claimed = append(claimed, *n)
			}
			keep = append(keep, n)
		}
		d.Notices = keep
		return nil
	}); err != nil {
		return 0, err
	}
	delivered := 0
	for _, n := range claimed {
		// Each delivery takes a second or more, so a dialog claimed above can be answered or gone by now.
		if n.Kind != "result" {
			if stale, err := t.dropStaleNotice(n); err != nil || stale {
				if err != nil {
					return delivered, err
				}
				continue
			}
		}
		outcome := t.deliver(n)
		if outcome == "delivered" {
			delivered++
		}
		if err := t.Reg.Update(func(d *Data) error {
			i := d.findNotice(n.ID)
			if i < 0 {
				return nil
			}
			q := d.Notices[i]
			if outcome == "retry" || outcome == "held" {
				q.Claimed, q.ClaimPid = 0, 0
				return nil
			}
			if j := d.jobOf(d.Leases[q.Lease]); j != nil && q.Kind == "result" {
				j.Notify = outcome
			}
			if outcome != "delivered" {
				untold(d, q)
			}
			d.Notices = append(d.Notices[:i], d.Notices[i+1:]...)
			return nil
		}); err != nil {
			return delivered, err
		}
	}
	return delivered, nil
}

// noticeLive: what a notice is about still holds. A dialog notice: its dialog is still marked on screen. A
// progress or stall notice: its task still runs, and for a stall, its stall mark is still there (the worker
// was not seen at work again, and reported no progress). A task result always holds.
func noticeLive(d *Data, n *Notice) bool {
	l := d.Leases[n.Lease]
	switch n.Kind {
	case "dialog":
		return noticeOnScreen(d, n)
	case "progress", "stall":
		j := d.jobOf(l)
		return j != nil && j.Status == "running" && !j.Closed && (n.Kind == "progress" || l.Stall != nil)
	case "escalate": // of a dialog: it still waits; of ended tasks with running sub-tasks: always
		return n.Dialog == "" || dialogOnScreen(d.jobOf(l), l, n.Dialog)
	}
	return true
}

// dropStaleNotice reads the registry right before a notice is sent, and drops the notice when what it is
// about no longer holds: a dialog answered or gone, a task that ended, a stall that ended, since it was claimed.
func (t *Tasks) dropStaleNotice(n Notice) (bool, error) {
	d, err := t.Reg.Read()
	if err != nil {
		return false, err
	}
	if noticeLive(d, &n) {
		return false, nil
	}
	return true, t.Reg.Update(func(d *Data) error {
		if i := d.findNotice(n.ID); i >= 0 {
			d.Notices = append(d.Notices[:i], d.Notices[i+1:]...)
		}
		return nil
	})
}

// untold clears the mark's notified flag when a dialog notice is dropped before it reached the owner,
// so the watcher announces the dialog again while it stays on screen. One task wait returned stays told.
func untold(d *Data, n *Notice) {
	if n.Kind != "dialog" || !noticeOnScreen(d, n) {
		return
	}
	l := d.Leases[n.Lease]
	if !(dialogOnScreen(d.jobOf(l), l, n.Dialog) && l.Dialog.Returned) {
		l.Dialog.Notified = false
	}
}

// about names what a notice is about, for warnings.
func (n Notice) about(d *Data) string {
	l := d.Leases[n.Lease]
	switch {
	case l == nil:
		return "lease " + n.Lease
	case l.Job == "":
		return "pane " + l.Pane
	}
	return "task " + l.Job
}

func (t *Tasks) deliver(n Notice) string {
	d, rerr := t.Reg.Read()
	if rerr != nil {
		d = emptyData()
	}
	about := n.about(d)
	b, pane, err := t.paneByID(n.Owner, n.OwnerRef)
	if err != nil {
		Warn(fmt.Sprintf("could not notify the owner of %s: %v", about, err))
		return "retry"
	}
	if pane == nil {
		return "owner-gone"
	}
	if n.Kind == "dialog" && !ForProcess(pane.Process).Calibrated {
		return "held" // typed blind, its letters could land in the lead's own menus: task wait returns it
	}
	ops := t.ops(b, nil)
	if l := d.Leases[n.Lease]; l != nil { // the worker's report: filed under its lease's project, task and scope
		ops.Task, ops.Project, ops.From, ops.Scope = l.Job, l.Project, l.Pane, l.Scope
	}
	// A notice goes only to an idle owner, for up to idleWait: typed into a busy Claude Code it is queued
	// on the input line, and before v0.7.2 each later look read it as unsent and typed it again (2026-11-19:
	// one dialog 8 times into a busy lead). Msg checks once, with the draft check, so a delivery makes no
	// extra backend calls (deliveryBound). Past idleWait it goes anyway: an owner that only looks busy (a
	// busy pattern left in its last output, a status stuck on working) must still hear, once.
	// A dialog notice goes at once, busy or not: the worker is stalled until the owner answers, and a lead
	// that heard nothing polled read/ls in a loop to find its dialogs (2026-11-19). It is typed once (v0.7.2).
	idleOnly := n.Kind != "dialog" && t.clock().Now()-n.Created < idleWait
	if sent, err := ops.Msg(pane, n.Text, MsgOpts{Tag: n.Tag, Retry: true, IdleOnly: idleOnly}); err != nil {
		if errors.Is(err, ErrBusy) {
			return "retry"
		}
		var te *term.Error
		if sent.Typed {
			// Typed, but not confirmed (still on the input line, or a backend error after typing): a busy
			// harness can hold a queued message there and deliver it later. A retry could type it a second
			// time, so it counts as delivered.
			Warn(fmt.Sprintf("the notice about %s was typed but not confirmed; not sent again: %v", about, err))
			return "delivered"
		}
		if !errors.As(err, &te) || te.Code != term.CodeDraft {
			Warn(fmt.Sprintf("could not notify the owner of %s: %v", about, err))
		}
		return "retry"
	}
	return "delivered"
}

// otherTaskOnPane returns another task on l's pane (same server) that still holds it (not closed, not
// settled) or was started after j; open holders come before newer settled/closed tasks.
func otherTaskOnPane(d *Data, j *Job, l *Lease) (*Job, *Lease) {
	if l.Pane == "" {
		return nil, nil
	}
	var openOther, newer *Job
	var openOtherL, newerL *Lease
	for _, y := range d.Jobs {
		yl := d.leaseOf(y)
		if y.ID == j.ID || yl.Pane != l.Pane || !sameServer(yl.Ref, l.Ref) {
			continue
		}
		if !y.Closed && !settled(y.Status) && (openOther == nil || newerTask(y, yl, openOther, openOtherL)) {
			openOther, openOtherL = y, yl
		}
		if y.Started > j.Started && (newer == nil || newerTask(y, yl, newer, newerL)) {
			newer, newerL = y, yl
		}
	}
	if openOther != nil {
		return openOther, openOtherL
	}
	return newer, newerL
}

// paneKeepReason reports why closing task j (on lease l) must leave its pane open, or "" when it may close it.
func paneKeepReason(d *Data, j *Job, l *Lease) string {
	if l.Pane == "" {
		return ""
	}
	if !createdFor(d, l) {
		return "it was not created for this task"
	}
	if other, _ := otherTaskOnPane(d, j, l); other != nil {
		if !other.Closed && !settled(other.Status) {
			return fmt.Sprintf("task %s (%s) holds it", other.ID, other.Status)
		}
		return fmt.Sprintf("newer task %s (%s) is on it", other.ID, other.Status)
	}
	return ""
}

// Close closes a task's pane. A pane the task did not create, or that another open or newer task holds, is
// left open.
func (t *Tasks) Close(id string, force bool) (*Task, error) {
	res, _, err := t.CloseWithNote(id, force)
	return res, err
}

// CloseWithNote closes a task and returns why its pane stayed open, or "" when it closed (or had none).
func (t *Tasks) CloseWithNote(id string, force bool) (*Task, string, error) {
	d, j, l, err := t.getJob(id)
	if err != nil {
		return nil, "", err
	}
	if t.isSelf(l.Pane) {
		return nil, "", term.TargetErr("refusing to close your own pane")
	}
	if !settled(j.Status) && !force {
		return nil, "", term.Errf(term.CodeGuard, "task %s is %s, not accepted; use task accept, or --force", j.ID, j.Status)
	}
	kept := paneKeepReason(d, j, l)
	if kept == "" && l.Pane != "" {
		b, p, err := t.paneByID(l.Pane, l.Ref)
		if err != nil {
			return nil, "", err
		}
		if p != nil {
			if err := b.Close(p); err != nil {
				return nil, "", err
			}
		}
	}
	var result *Task
	var cj Job
	var cl Lease
	err = t.Reg.Update(func(d *Data) error {
		c := d.Jobs[j.ID]
		l := d.leaseOf(c)
		now := t.clock().Now()
		kept = paneKeepReason(d, c, l)
		c.Closed, l.Dialog, l.Restarted = true, nil, false
		if c.Status == "running" {
			c.Status, c.Ended, c.Summary = "failed", now, "closed by its owner before it finished"
		}
		wasActive := l.State == "active"
		if kept == "" && l.Pane != "" {
			delete(d.Created, l.Pane)
			endLease(l, now)
			for _, ol := range d.Leases {
				if ol.State == "active" && ol.Pane == l.Pane && sameServer(ol.Ref, l.Ref) {
					endLease(ol, now)
				}
			}
		} else {
			endLease(l, now)
			// A pane harness-ops created that this task reused (or adopted) stays open, and stays its lead's:
			// a bare lease takes over, so ls --mine, answer and key go on working on it.
			if cp := d.Created[l.Pane]; wasActive && cp != nil && sameServer(cp.Ref, l.Ref) && activeLeaseForPane(d, l.Pane, l.Ref) == nil {
				bare := &Lease{ID: newLeaseID(d), Owner: l.Owner, OwnerRef: l.OwnerRef, Pane: l.Pane, Ref: l.Ref, Harness: l.Harness,
					Label: l.Label, Cwd: l.Cwd, Project: l.Project, Parent: l.Parent, Root: l.Root, Depth: l.Depth, SubDepth: l.SubDepth,
					TreeMax: l.TreeMax, State: "active", Launched: true, Started: now}
				d.Leases[bare.ID] = bare
			}
		}
		result, cj, cl = d.TaskView(c.ID), *c, *l
		return nil
	})
	if err == nil {
		note := ""
		if kept != "" && cl.Pane != "" {
			note = "pane left open: " + kept
		}
		t.logTask("task.close", &cj, &cl, cj.Status, note)
	}
	return result, kept, err
}

// Accept is the lead's verdict: only it makes a task done.
func (t *Tasks) Accept(id string, closeIt bool) (*Task, error) {
	_, j, l, err := t.getJob(id)
	if err != nil {
		return nil, err
	}
	if t.Sender == nil || t.Sender.ID != l.Owner {
		return nil, term.TargetErr("only the owner of task %s can accept it", j.ID)
	}
	if j.Status != "ready" {
		return nil, term.UsageErr("task %s is %s; only a ready task can be accepted", j.ID, j.Status)
	}
	if err := t.Reg.Update(func(d *Data) error {
		d.Jobs[j.ID].Status = "done"
		return nil
	}); err != nil {
		return nil, err
	}
	t.logTask("task.accept", j, l, "done", "")
	if closeIt {
		return t.Close(j.ID, false)
	}
	return t.Get(j.ID)
}

// ReviewOpts are options of Review.
type ReviewOpts struct {
	ID, Harness, Model, BriefFile, Where, Command string
	Blind                                         bool
	Timeout                                       float64
}

// stripFooter is a brief file without the footer typed after it: since 2026-11-19 the footer starts with the
// capability card ("Role: ..."), before that it was footer(id) alone.
func stripFooter(brief, id string) string {
	brief = strings.TrimSpace(brief)
	if i := strings.LastIndex(brief, "\n\nRole: "); i >= 0 {
		return strings.TrimSpace(brief[:i])
	}
	return strings.TrimSpace(strings.TrimSuffix(brief, footer(id)))
}

func (t *Tasks) blindBrief(j *Job) string {
	requirements := "(the original brief is not available; review against what the code claims to do)"
	if data, err := os.ReadFile(j.Brief); err == nil {
		requirements = stripFooter(string(data), j.ID)
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
	_, j, l, err := t.getJob(o.ID)
	if err != nil {
		return nil, err
	}
	if j.Status == "running" {
		return nil, term.UsageErr("task %s is still running; review it after the worker reports", j.ID)
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
	brief := t.blindBrief(j)
	if o.BriefFile != "" {
		data, err := os.ReadFile(ExpandHome(o.BriefFile))
		if err != nil {
			return nil, term.UsageErr("--brief-file: %v", err)
		}
		brief = string(data)
	}
	return t.New(NewOpts{Harness: o.Harness, Title: "blind review of " + j.ID, Brief: brief, Cwd: l.Cwd, Where: o.Where,
		Command: cmd, Timeout: o.Timeout, ReviewOf: j.ID, Blind: true})
}
