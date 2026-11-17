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

	"github.com/samhvw8/termctl/internal/term"
)

// Signal is one thing learned about a task's worker. Kind "dialog" carries the dialog on its screen;
// "clear" means its input line is back with no dialog. Status is the program's own reason from the
// terminal's status (the OSC 7501 msg) when it reported itself blocked; it rides along with a dialog
// and is never a dialog on its own.
type Signal struct {
	Task   string
	Kind   string
	Dialog *Dialog
	Status string
	At     float64 // when the screen was captured; 0 when unknown
}

// Source finds signals for tasks. The screen source reads each worker's screen.
type Source interface {
	Signals(tasks []*Task) []Signal
}

const (
	WatchInterval = 3.0
	StatusEvery   = 1.0 // between screen rounds, a backend with a status source is asked this often
	blockedLook   = 0.5 // the status says blocked: look again this soon when the dialog was not drawn yet
	readWorkers   = 8   // screens read at once
	minGap        = 0.5 // the least pause between rounds, however long a round took
)

// worker is a task's live pane, resolved once per round.
type worker struct {
	task *Task
	b    term.Backend
	p    *term.Pane
	prof *Profile
}

// screenSource reads the dialog off each worker's screen, in parallel. A blocked program status (from a
// backend's StatusReader) is a hint to look again soon when no dialog is drawn yet, and its message goes
// into the notice; on its own it is never taken as a dialog.
type screenSource struct{ t *Tasks }

func (s screenSource) Signals(tasks []*Task) []Signal {
	ws := s.t.workers(tasks)
	return s.t.read(ws, programStatuses(ws, nil))
}

// workers resolves the panes of launched tasks whose harness has a dialog reader. Panes are listed once
// per backend for the whole round.
func (t *Tasks) workers(tasks []*Task) []worker {
	memo := t.memo == nil
	if memo {
		t.startMemo()
		defer t.stopMemo()
	}
	var out []worker
	for _, x := range tasks {
		if x.Pane == "" || !x.Launched {
			continue
		}
		b, p, err := t.paneByID(x.Pane, x.Ref)
		if err != nil || p == nil {
			continue
		}
		prof := ForProcess(p.Process)
		if prof.ReadDialog == nil {
			continue
		}
		out = append(out, worker{task: x, b: b, p: p, prof: prof})
	}
	return out
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
		screens[i], dialogs[i] = lines, ws[i].prof.ReadDialog(lines)
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
		switch {
		case screens[i] == nil:
		case dialogs[i] != nil:
			sig := Signal{Task: w.task.ID, Kind: "dialog", Dialog: dialogs[i], At: ats[i]}
			if st := status[w.p.ID]; st.Blocked() {
				sig.Status = st.Msg
			}
			out = append(out, sig)
		case w.prof.ReadInput(screens[i]) != nil:
			out = append(out, Signal{Task: w.task.ID, Kind: "clear", At: ats[i]})
		}
	}
	return out
}

func (t *Tasks) sources() []Source { return []Source{screenSource{t}} }

func (t *Tasks) signals(tasks []*Task) []Signal {
	var out []Signal
	for _, s := range t.sources() {
		out = append(out, s.Signals(tasks)...)
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
func dialogOnScreen(x *Task, fp string) bool {
	return x != nil && x.Status == "running" && !x.Closed && x.Dialog != nil && x.Dialog.Fingerprint == fp
}

func dialogTag(x *Task) string {
	return fmt.Sprintf("[agent-ops from %s (%s) id=%s folder=%s]", x.ID, x.Harness, x.Pane, x.Cwd)
}

// dialogNotice is the owner's notice of a dialog, with the harness's own reason when its terminal kept one.
func dialogNotice(x *Task, s Signal) string {
	return fmt.Sprintf("task %s (%s) %s", x.ID, x.Title, dialogLine(x.ID, s.Dialog, s.Status))
}

// apply records signals under the lock: a new dialog replaces the mark, "clear" drops it. With notify,
// a dialog the owner has not been told about gets a pending notice, once per dialog while it stays on screen.
// A signal read before the task's last answer is stale: it would bring the answered dialog back.
func (t *Tasks) apply(sigs []Signal, notify bool) error {
	if len(sigs) == 0 {
		return nil
	}
	return t.Reg.Update(func(d *Data) error {
		now := t.clock().Now()
		for _, s := range sigs {
			x := d.Tasks[s.Task]
			if x == nil || !open(x.Status) || (s.At > 0 && s.At < x.AnsweredAt) {
				continue
			}
			switch s.Kind {
			case "clear":
				x.Dialog = nil
			case "dialog":
				if x.Dialog == nil || x.Dialog.Fingerprint != s.Dialog.Fingerprint {
					x.Dialog = &DialogMark{Fingerprint: s.Dialog.Fingerprint, Question: s.Dialog.Question, Since: now}
				}
				if notify && !x.Dialog.Notified {
					x.Dialog.Notified = true
					d.Pending = append(d.Pending, &Pending{Task: x.ID, Owner: x.Owner, OwnerRef: x.OwnerRef, Tag: dialogTag(x),
						Text: dialogNotice(x, s), Created: now, Kind: "dialog", Dialog: s.Dialog.Fingerprint})
				}
			}
		}
		return nil
	})
}

// runningTasks lists the owner's running tasks. Only these can show a live dialog: a ready, failed or
// blocked worker has reported, and task send makes it running again.
func runningTasks(d *Data, owner string, ref map[string]string) (running []*Task) {
	for _, x := range d.Tasks {
		if x.Owner == owner && sameServer(x.OwnerRef, ref) && !x.Closed && x.Status == "running" {
			running = append(running, x)
		}
	}
	return running
}

// holdsNotice reports whether a notice for the owner is still waiting to be delivered.
func holdsNotice(d *Data, owner string, ref map[string]string) bool {
	for _, p := range d.Pending {
		if p.Owner == owner && sameServer(p.OwnerRef, ref) {
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

// WatchOpts are options of Watch. Rounds stops after that many screen rounds (tests); 0 runs until no
// task is open. StatusEvery is how often, between rounds, backends with a status source are asked
// (0: the default; negative: never). Restart, checked each round, makes Watch return ErrRestart.
type WatchOpts struct {
	Interval    float64
	StatusEvery float64
	Rounds      int
	Log         func(string)
	Restart     func() bool
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
	w := &watchState{t: t, say: say, seen: map[string]term.ProgramStatus{}}
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
		d, err := t.Reg.Read()
		if err != nil {
			t.stopMemo()
			return err
		}
		if _, p, err := t.paneByID(owner.ID, t.SenderRef); err == nil && p == nil {
			t.stopMemo()
			say("the owner pane is gone; stopping")
			return nil
		}
		running := runningTasks(d, owner.ID, t.SenderRef)
		if len(running) == 0 && !holdsNotice(d, owner.ID, t.SenderRef) {
			t.stopMemo()
			// A task created after the read above found this lock held, so it started no watcher. Look
			// again once the lock is free, and serve what came, unless a new watcher took the lock first.
			lock.Close()
			if d, err = t.Reg.Read(); err != nil {
				return err
			}
			if len(runningTasks(d, owner.ID, t.SenderRef)) == 0 && !holdsNotice(d, owner.ID, t.SenderRef) {
				say("no running task and no held notice left; stopping")
				return nil
			}
			if lock, err = t.lockWatcher(owner.ID); err != nil {
				if errors.Is(err, ErrWatching) {
					say("a task came while stopping, and another watcher serves it; stopping")
					return nil
				}
				return err
			}
			lock.Truncate(0)
			lock.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
			say("a task came while stopping; watching again")
			continue
		}
		w.ws = t.workers(running)
		status := programStatuses(w.ws, w.statusErr)
		w.remember(status)
		w.handle(d, t.read(w.ws, status))
		t.stopMemo()
		if o.Rounds > 0 && round >= o.Rounds {
			return nil
		}
		// Rounds start every Interval: the time a round took (lists, reads, notices) is not added on top.
		w.between(max(o.Interval-(t.clock().Now()-start), minGap), o.StatusEvery)
	}
}

// watchState is what a watcher keeps between rounds: the workers of the last round and the status each
// had, so that a status poll can tell which worker turned blocked.
type watchState struct {
	t       *Tasks
	say     func(string)
	ws      []worker
	seen    map[string]term.ProgramStatus
	lastErr string
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
		if s.Kind == "dialog" && !dialogOnScreen(d.Tasks[s.Task], s.Dialog.Fingerprint) {
			w.say(fmt.Sprintf("task %s: dialog %s: %s", s.Task, s.Dialog.Fingerprint, s.Dialog.Question))
		}
	}
	if err := w.t.apply(sigs, true); err != nil {
		w.say("record: " + err.Error())
	}
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
		clock.Sleep(interval)
		return
	}
	end := clock.Now() + interval // a deadline: time spent reading and announcing between polls counts too
	for {
		left := end - clock.Now()
		if left <= 0 {
			return
		}
		clock.Sleep(min(statusEvery, left))
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
