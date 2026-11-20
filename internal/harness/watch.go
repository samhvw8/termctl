package harness

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/samhvw8/termctl/internal/term"
)

// Signal is one thing learned about a watched pane: a running task's worker (Task set), or a pane its
// owner started that no running task holds (Task "", the mark goes on the pane's record). Kind "dialog"
// carries the dialog on its screen; "clear" means its input line is back with no dialog, or the pane is
// gone. Status is the program's own reason from the terminal's status (the OSC 7501 msg) when it reported
// itself blocked; it rides along with a dialog and is never a dialog on its own.
type Signal struct {
	Task   string
	Lease  string // the lease of the pane, task or not
	Pane   string
	Kind   string
	Dialog *Dialog
	Status string
	At     float64 // when the screen was captured; 0 when unknown
	// Working: the program is working or blocked, by its own status where the terminal keeps one, else by
	// a busy pattern on its screen. A dialog is blocked whatever the status says.
	Working bool
	// Idle: a calibrated harness, not working, no dialog, its input line read and empty. Only an idle look
	// counts toward a stall; any other look ends the idle stretch.
	Idle bool
}

// Source finds signals for the panes of leases. The screen source reads each worker's screen.
type Source interface {
	Signals(leases []*Lease) []Signal
}

const (
	WatchInterval = 3.0
	StatusEvery   = 1.0 // between screen rounds, a backend with a status source is asked this often
	blockedLook   = 0.5 // the status says blocked: look again this soon when the dialog was not drawn yet
	readWorkers   = 8   // screens read at once
	minGap        = 0.5 // the least pause between rounds, however long a round took
)

// worker is a watched live pane, resolved once per round: a running task's worker, or (task nil) a pane
// the owner started that no running task holds.
type worker struct {
	lease *Lease
	task  string // the running job the lease carries, "" for a pane no running task holds
	b     term.Backend
	p    *term.Pane
	prof *Profile
}

// screenSource reads the dialog off each worker's screen, in parallel. A blocked program status (from a
// backend's StatusReader) is a hint to look again soon when no dialog is drawn yet, and its message goes
// into the notice; on its own it is never taken as a dialog.
type screenSource struct{ t *Tasks }

func (s screenSource) Signals(leases []*Lease) []Signal {
	ws := s.t.workers(leases)
	return s.t.read(ws, programStatuses(ws, nil))
}

// workers resolves the panes of launched tasks whose harness has a dialog reader. Panes are listed once
// per backend for the whole round.
func (t *Tasks) workers(leases []*Lease) []worker {
	memo := t.memo == nil
	if memo {
		t.startMemo()
		defer t.stopMemo()
	}
	var out []worker
	for _, l := range leases {
		if l == nil || l.Pane == "" || !l.Launched {
			continue
		}
		b, p, err := t.paneByID(l.Pane, l.Ref)
		if err != nil || p == nil {
			continue
		}
		prof := ForProcess(p.Process)
		if prof.ReadDialog == nil {
			continue
		}
		out = append(out, worker{lease: l, task: l.Job, b: b, p: p, prof: prof})
	}
	return out
}

// paneWorkers resolves the panes the owner started (spawn or task new) on its server that no running task
// holds, and whose harness has a dialog reader: a dialog there is marked on the pane's record. gone lists
// the recorded panes whose server answered without them that still carry a mark, so the mark can go.
func (t *Tasks) paneWorkers(d *Data, owner string, ref map[string]string) (out []worker, gone []*Lease) {
	memo := t.memo == nil
	if memo {
		t.startMemo()
		defer t.stopMemo()
	}
	byPane := map[string]*Lease{}
	var panes []string
	for _, l := range d.Leases {
		if l.State != "active" || l.Pane == "" || l.Owner != owner || l.Pane == owner || !sameServer(l.OwnerRef, ref) || paneHeld(d, l) {
			continue
		}
		if byPane[l.Pane] == nil {
			panes = append(panes, l.Pane)
		}
		if o := byPane[l.Pane]; o == nil || l.ID < o.ID {
			byPane[l.Pane] = l
		}
	}
	sort.Strings(panes)
	for _, id := range panes {
		l := byPane[id]
		b, p, err := t.paneByID(id, l.Ref)
		if err != nil { // unknown is not gone
			continue
		}
		if p == nil {
			if l.Dialog != nil {
				gone = append(gone, l)
			}
			continue
		}
		prof := ForProcess(p.Process)
		if prof.ReadDialog == nil {
			continue
		}
		out = append(out, worker{lease: l, b: b, p: p, prof: prof})
	}
	return out, gone
}


// programStatuses asks each backend with a StatusReader once for the status of its workers' panes (tmux
// and Ghostty have none). A failed read keeps what was read, and its error goes to warn when given.
func programStatuses(ws []worker, warn func(error)) map[string]term.ProgramStatus {
	out := map[string]term.ProgramStatus{}
	groups := map[term.Backend][]*term.Pane{}
	var order []term.Backend
	for _, w := range ws {
		if _, ok := groups[w.b]; !ok {
			order = append(order, w.b)
		}
		groups[w.b] = append(groups[w.b], w.p)
	}
	for _, b := range order {
		r, ok := b.(term.StatusReader)
		if !ok {
			continue
		}
		st, err := r.ProgramStatuses(groups[b])
		if err != nil && warn != nil {
			warn(err)
		}
		for id, s := range st {
			out[id] = s
		}
	}
	return out
}

// parallel runs fn for each index, at most readWorkers at once.
func parallel(n int, fn func(i int)) {
	var wg sync.WaitGroup
	gate := make(chan struct{}, readWorkers)
	for i := 0; i < n; i++ {
		wg.Add(1)
		gate <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-gate }()
			fn(i)
		}(i)
	}
	wg.Wait()
}

// read captures every worker's screen at once and reads its dialog. Workers whose status says blocked
// but whose screen shows no dialog yet get one more look after blockedLook.
func (t *Tasks) read(ws []worker, status map[string]term.ProgramStatus) []Signal {
	screens := make([][]string, len(ws))
	dialogs := make([]*Dialog, len(ws))
	ats := make([]float64, len(ws))
	look := func(i int) {
		ats[i] = t.clock().Now() // before the capture: an answer recorded after this time makes the read stale
		lines, err := ws[i].b.Capture(ws[i].p, screenLines(ws[i].p))
		if err != nil {
			return
		}
		screens[i], dialogs[i] = lines, readPaneDialog(ws[i].b, ws[i].p, ws[i].prof, lines)
	}
	parallel(len(ws), look)
	var again []int
	for i, w := range ws {
		if screens[i] != nil && dialogs[i] == nil && status[w.p.ID].Blocked() {
			again = append(again, i)
		}
	}
	if len(again) > 0 {
		t.clock().Sleep(blockedLook)
		parallel(len(again), func(k int) { look(again[k]) })
	}
	var out []Signal
	for i, w := range ws {
		working := false
		if st, ok := status[w.p.ID]; ok && st.State != "" {
			working = st.State == "working" || st.Blocked()
		} else if screens[i] != nil {
			working = w.prof.IsBusy(screens[i])
		}
		switch {
		case screens[i] == nil:
		case dialogs[i] != nil:
			sig := Signal{Task: w.task, Lease: w.lease.ID, Pane: w.p.ID, Kind: "dialog", Dialog: dialogs[i], At: ats[i]}
			if st := status[w.p.ID]; st.Blocked() {
				sig.Status = st.Msg
			}
			out = append(out, sig)
		case w.prof.ReadInput(screens[i]) != nil:
			idle := w.prof.Calibrated && !working && *w.prof.ReadInput(screens[i]) == ""
			out = append(out, Signal{Task: w.task, Lease: w.lease.ID, Pane: w.p.ID, Kind: "clear", At: ats[i], Working: working, Idle: idle})
		default: // no dialog and no input line: a turn in progress, or a screen this reader does not know
			out = append(out, Signal{Task: w.task, Lease: w.lease.ID, Pane: w.p.ID, Kind: "busy", At: ats[i], Working: working})
		}
	}
	return out
}

func (t *Tasks) sources() []Source { return []Source{screenSource{t}} }

func (t *Tasks) signals(leases []*Lease) []Signal {
	var out []Signal
	for _, s := range t.sources() {
		out = append(out, s.Signals(leases)...)
	}
	return out
}

// paneMemo keeps one pane list per backend server for the length of a watch round, so a round lists
// panes once per backend instead of once per task.
type paneMemo struct {
	mu    sync.Mutex
	lists map[string]*paneList
}

type paneList struct {
	b     term.Backend
	panes []*term.Pane
	err   error
}

func memoKey(paneID string, ref map[string]string) string {
	keys := make([]string, 0, len(ref))
	for k := range ref {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(strings.SplitN(paneID, ":", 2)[0])
	for _, k := range keys {
		fmt.Fprintf(&b, "\x00%s=%s", k, ref[k])
	}
	return b.String()
}

func (t *Tasks) startMemo() { t.memo = &paneMemo{lists: map[string]*paneList{}} }
func (t *Tasks) stopMemo()  { t.memo = nil }

// listPanes lists the panes of the server a pane ID lives on, through the round's memo when one is on.
func (t *Tasks) listPanes(paneID string, ref map[string]string) (term.Backend, []*term.Pane, error) {
	m := t.memo
	key := memoKey(paneID, ref)
	if m != nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		if l, ok := m.lists[key]; ok {
			return l.b, l.panes, l.err
		}
	}
	b, err := t.backendFor(paneID, ref)
	if err != nil {
		return nil, nil, err
	}
	panes, err := term.ListLive(b) // a server that does not answer is unknown, not a server with no panes
	if m != nil {
		m.lists[key] = &paneList{b, panes, err}
	}
	return b, panes, err
}

// dialogOnScreen: the task's worker still waits on dialog fp. A task that is no longer running (lost,
// exited, reported) or was closed waits on nothing, whatever mark it kept.
func dialogOnScreen(j *Job, l *Lease, fp string) bool {
	return j != nil && l != nil && j.Status == "running" && !j.Closed && l.Dialog != nil && l.Dialog.Fingerprint == fp
}

// paneDialogOnScreen: the lease still marks dialog fp on its pane, and no running task holds the pane (the
// mark is the task's then).
func paneDialogOnScreen(d *Data, l *Lease, fp string) bool {
	return l != nil && l.State == "active" && l.Dialog != nil && l.Dialog.Fingerprint == fp && !paneHeld(d, l)
}

// noticeOnScreen: the dialog a dialog notice is about is still marked on screen: on its lease, by its task
// while the task runs, else as the mark of the pane. A closed task's lease marks nothing.
func noticeOnScreen(d *Data, n *Notice) bool {
	l := d.Leases[n.Lease]
	if l == nil || l.State != "active" {
		return false
	}
	if paneHeld(d, l) {
		return dialogOnScreen(d.jobOf(l), l, n.Dialog)
	}
	return paneDialogOnScreen(d, l, n.Dialog)
}

// signalMarked: the dialog of a signal is already the mark of its task or pane.
func signalMarked(d *Data, s Signal) bool {
	if s.Task != "" {
		j := d.Jobs[s.Task]
		return dialogOnScreen(j, d.leaseOf(j), s.Dialog.Fingerprint)
	}
	return paneDialogOnScreen(d, d.Leases[s.Lease], s.Dialog.Fingerprint)
}

// paneName names a pane in a notice: its label, set by the owner at spawn, else its ID. Never its title,
// which the program in the pane sets.
func paneName(id, label string) string {
	if label != "" {
		return label
	}
	return id
}

func paneTag(d *Data, l *Lease) string {
	label, harness, cwd := paneInfo(d, l)
	return fmt.Sprintf("[agent-ops from %s (%s) id=%s folder=%s]", paneName(l.Pane, label), harness, l.Pane, cwd)
}

// paneNotice is the owner's notice of a dialog on a pane it started that no running task holds. The
// commands it suggests name the pane by its ID, which answer looks up on the server its record names.
func paneNotice(d *Data, l *Lease, s Signal) string {
	label, _, _ := paneInfo(d, l)
	name := l.Pane
	if label != "" {
		name += " (" + label + ")"
	}
	return fmt.Sprintf("pane %s %s", name, dialogLine(l.Pane, s.Dialog, s.Status))
}

func dialogTag(j *Job, l *Lease) string {
	return fmt.Sprintf("[agent-ops from %s (%s) id=%s folder=%s]", j.ID, l.Harness, l.Pane, l.Cwd)
}

// dialogNotice is the owner's notice of a dialog, with the harness's own reason when its terminal kept one.
func dialogNotice(j *Job, s Signal) string {
	return fmt.Sprintf("task %s (%s) %s", j.ID, j.Title, dialogLine(j.ID, s.Dialog, s.Status))
}

// apply records signals under the lock: a new dialog replaces the mark, "clear" drops it. With notify,
// a dialog the owner has not been told about gets a pending notice, once per dialog while it stays on screen.
// A signal read before the task's last answer is stale: it would bring the answered dialog back.
func (t *Tasks) apply(sigs []Signal, notify bool) error {
	if len(sigs) == 0 {
		return nil
	}
	now := t.clock().Now()
	// Look on a read first, as escalate does: a round whose signals change no mark, idle time or notice
	// writes nothing (a write is a lock and two syncs, every few seconds per watcher).
	d, err := t.Reg.Read()
	if err != nil {
		return err
	}
	if !t.applyOn(d, sigs, notify, now) {
		return nil
	}
	return t.Reg.Update(func(d *Data) error {
		t.applyOn(d, sigs, notify, now)
		return nil
	})
}

// applyOn is apply on d and reports whether d changed.
func (t *Tasks) applyOn(d *Data, sigs []Signal, notify bool, now float64) bool {
	changed := false
	for _, s := range sigs {
		if s.Task == "" {
			if t.applyPane(d, s, notify, now) {
				changed = true
			}
			continue
		}
		j := d.Jobs[s.Task]
		if j == nil || !open(j.Status) {
			continue
		}
		l := d.leaseOf(j)
		if s.At > 0 && s.At < l.AnsweredAt {
			continue
		}
		if notify && j.Status == "running" && !j.Closed {
			if trackIdle(d, j, l, s, now) {
				changed = true
			}
		}
		switch s.Kind {
		case "clear":
			if l.Dialog != nil {
				l.Dialog = nil
				changed = true
			}
		case "dialog":
			if l.Dialog == nil || l.Dialog.Fingerprint != s.Dialog.Fingerprint {
				l.Dialog = &DialogMark{Fingerprint: s.Dialog.Fingerprint, Question: s.Dialog.Question, Since: now}
				changed = true
			}
			if notify && !l.Dialog.Notified && !j.Defer { // a deferred job's dialogs are denied, never told (denyDeferred)
				l.Dialog.Notified = true
				d.Notices = append(d.Notices, &Notice{ID: newNoticeID(d), Lease: l.ID, Owner: l.Owner, OwnerRef: l.OwnerRef, Tag: dialogTag(j, l),
					Text: dialogNotice(j, s), Created: now, Kind: "dialog", Dialog: s.Dialog.Fingerprint})
				changed = true
			}
		}
	}
	return changed
}

// trackIdle times a running task's idle stretch from the watcher's own screen reads (no extra backend call).
// A look that is not idle ends the stretch and its stall mark. Once the worker has looked idle for stallAfter,
// counted from the later of the stretch's start and its last progress, the task gets a stall mark and its
// owner one notice (kind "stall"); the mark keeps a second one from going out in the same stretch.
func trackIdle(d *Data, j *Job, l *Lease, s Signal, now float64) bool {
	if !s.Idle {
		if l.IdleSince != 0 || l.Stall != nil {
			l.IdleSince, l.Stall = 0, nil
			return true
		}
		return false
	}
	if l.IdleSince == 0 {
		v := now
		if s.At > 0 {
			v = s.At
		}
		l.IdleSince = v
		return v != 0
	}
	// A lead that waits on a working sub-task is not stalled (2026-11-19: a lead flagged twice while its
	// worker built). Its idle stretch starts again once no sub-task works. A sub-task that is stalled itself
	// does not cover for it: then the lead's notice names it.
	working, stalled := childWork(d, l.ID)
	if working {
		if l.IdleSince != now {
			l.IdleSince = now
			return true
		}
		return false
	}
	since := l.IdleSince
	if l.Progress != nil && l.Progress.At > since {
		since = l.Progress.At
	}
	if l.Stall != nil || now-since < stallAfter {
		return false
	}
	l.Stall = &StallMark{At: now}
	text := fmt.Sprintf("task %s (%s) looks stalled: idle for %d min without task done or progress. read it: harness-ops read %s",
		j.ID, j.Title, int(stallAfter/60), j.ID)
	if len(stalled) > 0 {
		text += "; its sub-tasks look stalled too: " + strings.Join(stalled, ", ")
	}
	d.Notices = append(d.Notices, &Notice{ID: newNoticeID(d), Lease: l.ID, Owner: l.Owner, OwnerRef: l.OwnerRef, Tag: dialogTag(j, l), Kind: "stall", Created: now, Text: text})
	return true
}

// childWork: whether a running, not closed sub-task of the lead's lease is not marked stalled (so the lead
// waits on it), and the IDs of the running sub-tasks that are.
func childWork(d *Data, parentLease string) (working bool, stalled []string) {
	for _, c := range d.Leases {
		j := d.jobOf(c)
		if c.Parent != parentLease || j == nil || j.Status != "running" || j.Closed {
			continue
		}
		if c.Stall != nil {
			stalled = append(stalled, j.ID)
		} else {
			working = true
		}
	}
	sort.Strings(stalled)
	return working, stalled
}

// applyPane records a signal of a pane no running task holds on the pane's lease, as apply does on a task.
func (t *Tasks) applyPane(d *Data, s Signal, notify bool, now float64) bool {
	l := d.Leases[s.Lease]
	if l == nil || l.State != "active" || paneHeld(d, l) || (s.At > 0 && s.At < l.AnsweredAt) {
		return false
	}
	changed := false
	switch s.Kind {
	case "clear":
		if l.Dialog != nil {
			l.Dialog = nil
			changed = true
		}
	case "dialog":
		if l.Dialog == nil || l.Dialog.Fingerprint != s.Dialog.Fingerprint {
			l.Dialog = &DialogMark{Fingerprint: s.Dialog.Fingerprint, Question: s.Dialog.Question, Since: now}
			changed = true
		}
		if notify && !l.Dialog.Notified {
			l.Dialog.Notified = true
			ref := l.OwnerRef
			if ref == nil { // a lease from before owner_ref: the watcher serves its owner on its own server
				ref = t.SenderRef
			}
			d.Notices = append(d.Notices, &Notice{ID: newNoticeID(d), Lease: l.ID, Owner: l.Owner, OwnerRef: ref, Tag: paneTag(d, l),
				Text: paneNotice(d, l, s), Created: now, Kind: "dialog", Dialog: s.Dialog.Fingerprint})
			changed = true
		}
	}
	return changed
}

// runningLeases lists the leases of the owner's running tasks. Only these can show a live dialog: a ready,
// failed or blocked worker has reported, and task send makes it running again.
func runningLeases(d *Data, owner string, ref map[string]string) (running []*Lease) {
	for _, j := range d.Jobs {
		l := d.leaseOf(j)
		if l.Owner == owner && sameServer(l.OwnerRef, ref) && !j.Closed && j.Status == "running" {
			running = append(running, l)
		}
	}
	return running
}

// holdsNotice reports whether a notice for the owner is still waiting to be delivered.
func holdsNotice(d *Data, owner string, ref map[string]string) bool {
	for _, n := range d.Notices {
		if n.Owner == owner && sameServer(n.OwnerRef, ref) {
			return true
		}
	}
	return false
}

// ErrWatching means another watcher already serves this owner.
var ErrWatching = errors.New("a watcher already runs for this owner")

// ErrRestart means WatchOpts.Restart asked the watcher to stop so its process can start again: its
// executable changed. The lock is released when Watch returns it.
var ErrRestart = errors.New("the watcher's executable changed")

// watchPaths are keyed by the owner pane and its server: the same pane ID on another server is another owner.
func (t *Tasks) watchPaths(owner string) (lock, log string) {
	name := owner
	if k := serverKey(t.SenderRef); k != "" {
		name += "@" + k
	}
	name = unsafeName.ReplaceAllString(name, "_")
	dir := filepath.Join(t.Reg.Root, "watch")
	return filepath.Join(dir, name+".lock"), filepath.Join(dir, name+".log")
}

// WatchLog is where a detached watcher writes, in the state directory.
func (t *Tasks) WatchLog(owner string) string {
	_, log := t.watchPaths(owner)
	return log
}

const (
	logMax  = 1 << 20   // a watcher log over this is cut when a watcher takes the owner's lock
	logKeep = 256 << 10 // to its last lines that fit this
)

// trimLog cuts a watcher log over logMax to its last whole lines within logKeep, in place: it copies the
// tail, truncates the file and writes the tail back. Watchers write their log with O_APPEND, so one that
// has it open goes on at the new end; a rename left such a watcher writing into the unlinked old file
// (round-5 review D3). Only the watcher that holds the owner's lock cuts it. A line written between the
// read and the truncate is lost.
func trimLog(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() <= logMax {
		return err
	}
	tail := make([]byte, logKeep)
	n, err := f.ReadAt(tail, st.Size()-logKeep)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	tail = tail[:n]
	if i := bytes.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.WriteAt(tail, 0); err != nil {
		return err
	}
	return f.Close()
}

// lockWatcher takes the owner's watcher lock without waiting. It holds while the returned file is open.
func (t *Tasks) lockWatcher(owner string) (*os.File, error) {
	path, _ := t.watchPaths(owner)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, term.BackendErr("watch: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, term.BackendErr("watch: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrWatching
		}
		return nil, term.BackendErr("watch lock: %v", err)
	}
	return f, nil
}

// Watching reports the pid of the watcher that serves owner, or 0 when none runs.
func (t *Tasks) Watching(owner string) int {
	f, err := t.lockWatcher(owner)
	if err == nil {
		f.Close()
		return 0
	}
	if !errors.Is(err, ErrWatching) {
		return 0
	}
	path, _ := t.watchPaths(owner)
	data, _ := os.ReadFile(path)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if pid == 0 {
		pid = -1
	}
	return pid
}

// WakeWatcher asks the watcher that serves owner, if one runs, to start its next round now (SIGUSR1; a
// watcher of v0.10.x has no handler for it, and SIGUSR1 ends a process by default: wake only a watcher
// started by this build). It reports whether a signal was sent.
func (t *Tasks) WakeWatcher(owner string) bool {
	pid := t.Watching(owner)
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, syscall.SIGUSR1) == nil
}

// WatchOpts are options of Watch. Rounds stops after that many screen rounds (tests); 0 runs until no
// task is open. StatusEvery is how often, between rounds, backends with a status source are asked
// (0: the default; negative: never). Restart, checked each round, makes Watch return ErrRestart.
type WatchOpts struct {
	Interval    float64
	StatusEvery float64
	Rounds      int
	Log         func(string)
	Restart     func() bool
	// Wake, when set, ends the wait between two rounds at once (harness-ops watch: SIGUSR1, see WakeWatcher).
	Wake <-chan struct{}
}

// Watch polls the owner's running workers, tells the owner of each new dialog through the pending path
// (draft-safe, claimed under the lock), and delivers held-back results. Every Interval (start to start) it reads every
// worker's screen (in parallel, panes listed once per backend). In between, every StatusEvery, it asks
// backends with a status source for their workers' program status, and a worker that turned blocked
// gets its screen read at once. It returns when the owner pane is gone, or when the owner has no running
// task and no notice waiting to be delivered; with ErrWatching when another watcher serves the owner, or
// with ErrRestart.
func (t *Tasks) Watch(o WatchOpts) error {
	owner, err := t.owner()
	if err != nil {
		return err
	}
	if o.Interval <= 0 {
		o.Interval = WatchInterval
	}
	if o.StatusEvery == 0 {
		o.StatusEvery = StatusEvery
	}
	say := o.Log
	if say == nil {
		say = func(string) {}
	}
	lock, err := t.lockWatcher(owner.ID)
	if err != nil {
		return err
	}
	defer func() { lock.Close() }() // the lock in hand when Watch returns: it is taken again below
	lock.Truncate(0)
	lock.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	if err := trimLog(t.WatchLog(owner.ID)); err != nil { // under the lock: no other watcher of this owner runs
		say("could not cut this log: " + err.Error())
	}
	say(fmt.Sprintf("watching the tasks of %s (pid %d)", owner.Address(), os.Getpid()))
	if note := uncalibratedLead(owner); note != "" {
		say(note)
	}
	w := &watchState{t: t, say: say, seen: map[string]term.ProgramStatus{}, wake: o.Wake}
	for round := 1; ; round++ {
		start := t.clock().Now()
		if o.Restart != nil && o.Restart() {
			say("the executable changed; restarting")
			return ErrRestart
		}
		if _, err := os.Stat(t.Reg.Path); errors.Is(err, os.ErrNotExist) {
			say("the registry is gone; stopping")
			return nil
		}
		t.startMemo()
		if err := t.Reconcile(); err != nil {
			say("reconcile: " + err.Error())
		}
		t.closeDeferred(owner.ID, say)
		if err := t.escalate(owner.ID); err != nil {
			say("escalate: " + err.Error())
		}
		d, err := t.Reg.Read()
		if err != nil {
			t.stopMemo()
			return err
		}
		if _, p, err := t.paneByID(owner.ID, t.SenderRef); err == nil && p == nil {
			// A deferred job outlives its owner: stay while one runs (its dialogs are denied) or one that
			// reported ok still has its pane open, and only then.
			if !deferredLeft(d, owner.ID, t.SenderRef) {
				t.stopMemo()
				say("the owner pane is gone; stopping")
				return nil
			}
			if !w.ownerGone {
				w.ownerGone = true
				say("the owner pane is gone; staying for its deferred tasks")
			}
		}
		running := runningLeases(d, owner.ID, t.SenderRef)
		panes, gone := t.paneWorkers(d, owner.ID, t.SenderRef)
		idle := len(running) == 0 && len(panes) == 0 && len(gone) == 0 && !t.keepOn(d, owner.ID)
		if !idle {
			w.ws = append(t.workers(running), panes...)
			status := programStatuses(w.ws, w.statusErr)
			w.remember(status)
			sigs := t.read(w.ws, status)
			for _, l := range gone {
				sigs = append(sigs, Signal{Lease: l.ID, Pane: l.Pane, Kind: "clear"})
			}
			w.handle(d, sigs)
			atWork := panesAtWork(sigs)
			if atWork { // also while tasks run: a member at work beside them must not stop the watcher with the last task
				w.quiet, w.worked = 0, true
			}
			if len(running) == 0 {
				idle = w.quietRound(atWork || t.keepOn(nil, owner.ID))
			}
		}
		t.stopMemo()
		if idle {
			// Work given after the read above (a task new, task send, or a msg to one of the owner's panes)
			// found this lock held, so it started no watcher. Look again once the lock is free, and serve
			// what came, unless a new watcher took the lock first.
			lock.Close()
			if !t.keepOn(nil, owner.ID) {
				say("no running task and no held notice left, and no pane of yours at work; stopping")
				return nil
			}
			if lock, err = t.lockWatcher(owner.ID); err != nil {
				if errors.Is(err, ErrWatching) {
					say("work came while stopping, and another watcher serves it; stopping")
					return nil
				}
				return err
			}
			lock.Truncate(0)
			lock.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
			say("work came while stopping; watching again")
			w.quiet = 0
			continue
		}
		if o.Rounds > 0 && round >= o.Rounds {
			return nil
		}
		// Rounds start every Interval: the time a round took (lists, reads, notices) is not added on top.
		w.between(max(o.Interval-(t.clock().Now()-start), minGap), o.StatusEvery)
	}
}

// keepOn: work the watcher must stay for whatever the screens show: a running task, a notice still
// waiting for the owner, or a pane of the owner's given work (spawn --prompt, msg) less than workGrace ago,
// which may not look busy yet. d nil reads the registry; a read that fails keeps the watcher on.
func (t *Tasks) keepOn(d *Data, owner string) bool {
	if d == nil {
		var err error
		if d, err = t.Reg.Read(); err != nil {
			return true
		}
	}
	if len(runningLeases(d, owner, t.SenderRef)) > 0 || holdsNotice(d, owner, t.SenderRef) || deferredLeft(d, owner, t.SenderRef) {
		return true
	}
	now := t.clock().Now()
	for _, l := range d.Leases {
		if l.State == "active" && l.Owner == owner && l.Pane != owner && sameServer(l.OwnerRef, t.SenderRef) && l.Worked > 0 && now-l.Worked < workGrace {
			return true
		}
	}
	return false
}

const (
	// workGrace is how long a pane given work counts as working: a harness takes a moment to show it is busy.
	workGrace = 15.0
	// quietRounds is how many rounds in a row the owner's panes must look idle, once one was seen at work,
	// before the watcher stops: one idle look can fall between two steps of a turn.
	quietRounds = 3
)

// panesAtWork: a pane of the owner's that no running task holds is working or blocked: by its program
// status where the terminal keeps one, else by its screen (a busy pattern), and always with a dialog on it.
func panesAtWork(sigs []Signal) bool {
	for _, s := range sigs {
		if s.Task == "" && (s.Kind == "dialog" || s.Working) {
			return true
		}
	}
	return false
}

// quietRound counts a round with no running task and says whether the watcher may stop: nothing keeps it
// on (busy: a pane at work, a held notice, work given less than workGrace ago), and, once a pane was seen
// at work, quietRounds such rounds have passed in a row.
func (w *watchState) quietRound(busy bool) bool {
	if busy {
		w.quiet, w.worked = 0, true
		return false
	}
	w.quiet++
	return !w.worked || w.quiet >= quietRounds
}

// watchState is what a watcher keeps between rounds: the workers of the last round and the status each
// had, so that a status poll can tell which worker turned blocked.
type watchState struct {
	t         *Tasks
	say       func(string)
	ws        []worker
	seen      map[string]term.ProgramStatus
	lastErr   string
	quiet     int  // rounds in a row with no running task and no pane at work
	worked    bool // a pane of the owner's was seen at work since the watcher started
	ownerGone bool // said once: the owner pane is gone, the watcher stays for deferred tasks
	wake      <-chan struct{}
}

// nap sleeps s seconds, and reports false when a wake (WatchOpts.Wake) cut it short. Only the real clock
// can be cut short; with another clock a wake already sent ends the wait before it starts.
func (w *watchState) nap(s float64) bool {
	if w.wake == nil {
		w.t.clock().Sleep(s)
		return true
	}
	select {
	case <-w.wake:
		return false
	default:
	}
	if _, real := w.t.clock().(RealClock); !real {
		w.t.clock().Sleep(s)
		return true
	}
	timer := time.NewTimer(time.Duration(s * float64(time.Second)))
	defer timer.Stop()
	select {
	case <-w.wake:
		return false
	case <-timer.C:
		return true
	}
}

// statusErr logs a failed status read once, until a different one comes: the read repeats every second.
func (w *watchState) statusErr(err error) {
	if msg := err.Error(); msg != w.lastErr {
		w.lastErr = msg
		w.say("status: " + msg)
	}
}

func (w *watchState) remember(status map[string]term.ProgramStatus) {
	for _, x := range w.ws {
		w.seen[x.p.ID] = status[x.p.ID]
	}
}

// handle logs new dialogs, records the signals and delivers what is pending.
func (w *watchState) handle(d *Data, sigs []Signal) {
	for _, s := range sigs {
		if s.Kind == "dialog" && !signalMarked(d, s) {
			what := "task " + s.Task
			if s.Task == "" {
				what = "pane " + s.Pane
			}
			w.say(fmt.Sprintf("%s: dialog %s: %s", what, s.Dialog.Fingerprint, s.Dialog.Question))
		}
	}
	if err := w.t.apply(sigs, true); err != nil {
		w.say("record: " + err.Error())
	}
	w.t.denyDeferred(sigs, w.say)
	if n, err := w.t.FlushPending(); err != nil {
		w.say("deliver: " + err.Error())
	} else if n > 0 {
		w.say(fmt.Sprintf("delivered %d notice(s)", n))
	}
}

// between waits out the interval. Workers on a backend whose status read is free (term.FreeStatus) are
// asked for their status every statusEvery; one that turned blocked (or blocked with a new message) has its screen read at once.
func (w *watchState) between(interval, statusEvery float64) {
	clock := w.t.clock()
	var polled []worker
	for _, x := range w.ws {
		_, reads := x.b.(term.StatusReader)
		if f, ok := x.b.(term.FreeStatus); reads && ok && f.StatusIsFree() {
			polled = append(polled, x)
		}
	}
	if statusEvery < 0 || len(polled) == 0 || statusEvery >= interval {
		w.nap(interval)
		return
	}
	end := clock.Now() + interval // a deadline: time spent reading and announcing between polls counts too
	for {
		left := end - clock.Now()
		if left <= 0 {
			return
		}
		if !w.nap(min(statusEvery, left)) {
			return
		}
		if clock.Now() >= end {
			return
		}
		status := programStatuses(polled, w.statusErr)
		var turned []worker
		for _, x := range polled {
			now, before := status[x.p.ID], w.seen[x.p.ID]
			if now.Blocked() && (!before.Blocked() || now.Msg != before.Msg) {
				turned = append(turned, x)
			}
			w.seen[x.p.ID] = now
		}
		if len(turned) == 0 {
			continue
		}
		d, err := w.t.Reg.Read()
		if err != nil {
			continue
		}
		w.handle(d, w.t.read(turned, status))
	}
}
