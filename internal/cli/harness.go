package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samhvw8/termctl/internal/harness"
	"github.com/samhvw8/termctl/internal/term"
)

const harnessUsage = `usage: harness-ops [--backend NAME] [-L SOCKET] COMMAND

Talk to coding-agent panes. Put options before the target: everything after the target is text.

  ls [--json] [--all] [--mine]                panes with harness, label, folder, status; --mine: only the ones you started,
                                              with STATUS "dialog <fp>" where a dialog waits (JSON: "dialog")
  which TARGET                                resolve a target; never sends
  read TARGET [-n LINES]                      print a pane's screen (alias: capture)
  msg [--force] [--clear-first] [--again] [--defer [--wait-ack [S]]] [--timeout S] TARGET TEXT...   tagged message (alias: send).
                                              The same text to the same pane within 10 min is refused (exit 6) unless --again.
                                              --defer: prints a message ID; the receiver acks once (harness-ops ack ID) and
                                              never replies; --wait-ack waits up to S s (default 30) for the ack (exit 5 if none)
  ack ID [NOTE...]                            the receiver's one ack of a deferred task (from its own pane) or deferred message
                                              (from the pane it went to); a second ack exits 2; nothing is typed to anyone
  slash [--force] [--wait] [--timeout S] TARGET /COMMAND
  clear [--force] [--timeout S] TARGET        /clear alone, then wait for idle
  wait-idle [--timeout S] TARGET
  spawn HARNESS [--where auto|tab|split|window] [--cwd D] [--prompt T | --prompt-file F] [--label L] [--command CMD] [--timeout S]
        [--accept-trust]                      refused (exit 6) from a leaf task (--sub-depth 0). --accept-trust (spawn and task new):
                                              answer Claude Code's folder-trust prompt at startup when the folder is under a root
                                              in ~/.config/termctl/trust-roots (the user's file; none: exit 6); otherwise it stays
  attach TARGET [--name SLUG] [--about TEXT] [--stall DUR] [--sub-depth K] [--tree-max M] [--notify] [--force]
                                              attach an existing pane as a durable Scope (silent by default; 0 task slots)
  detach TARGET [--keep] [--notify]           end supervision of a scope or pane without closing it (drops unless --keep)
  report [--notify] TEXT...                   attached scope reports one line of progress, resetting its stall timer
  scope ls [--all] [--tree] [--json]          list scopes (--tree nests child tasks and sub-scopes)
  scope show (NAME | ID) [--json]             show one scope's details
  close [--force] TARGET                      only panes made by spawn or task new; never your own; refuses active scopes
  interrupt [--key K] TARGET                  stop the current turn
  key [--force] TARGET KEY...                 press keys on a pane you started or attached: a menu, a picker,
                                              shift-tab. KEY: enter escape tab shift-tab backspace delete space up down
                                              left right home end pageup pagedown f1-f12 ctrl-X alt-X (alt+x, meta-x,
                                              option-x too); never a plain digit. Refused on a dialog (use answer or
                                              deny) and on a draft (exit 4, unless --force); termctl send-key is raw
  move TARGET --to TAB                        move a pane into another tab; it keeps running, with its label and task
                                              TAB: new (a tab of its own), self, tab-of:PANE, or a tab ID from ls
  pull TARGET                                 move a pane into your tab (move --to self)
  swap A B                                    trade the places of two panes
  arrange [TAB] --layout L [--main PANE]      lay out a tab again, evenly sized (default: your tab)
                                              L: even, rows, columns, tiled, main-left, main-top
  name LABEL                                  label the current pane
  whoami
  answer TARGET N --expect FP [--persist]     choose N in the dialog FP on a pane you started/attached or a task's worker;
                                              TARGET: the pane, scope, lease, or task ID. A [persist] choice needs --persist
  deny TARGET --expect FP                     cancel the dialog FP with Esc; never presses Enter
  task new HARNESS --title T (--brief TEXT | --brief-file F) [--cwd D] [--where W] [--reuse TARGET] [--command CMD] [--max N] [--timeout S]
           [--sub-depth K] [--tree-max M] [--defer [--wait-ack [S]]] [--accept-trust]
                                              --sub-depth K: levels below the task may start (default 0: a leaf, whose own task
                                              new exits 6); a sub-task may only narrow K and M. --tree-max M: running tasks in the
                                              tree (default 12 at the root, inherited). TERMCTL_MAX_DEPTH (default 4) caps the depth.
                                              --defer: the worker acks once, then is silent; nobody is told of its result, the
                                              watcher denies its dialogs (2 denials: interrupted, blocked) and closes its pane on ok
  task ls [--all] [--tree] [--json]           your open tasks (--all: every task); --tree nests the tasks started from each
                                              task's worker under it, across owners and projects (JSON: "children")
  task show ID [--json]
  task send [--force] ID TEXT...
  task wait (ID | --any) [--timeout S] [--json]   returns when a task stops running or its worker shows a dialog
  task done ID --status ok|failed|blocked (--summary TEXT | --summary-file F)   the worker reports; the session stays open
  task progress ID --summary TEXT [--notify]  the worker (or owner) notes progress on a running task; the status stays;
                                              --notify also tells the owner (a newer progress notice replaces an unsent one)
  task accept ID [--close]                    the lead's verdict; only the owner can finish a task
  task review ID --blind --harness H [--model M] [--brief-file F]   a new session that never sees the worker's report
  task close [--force] [--tree] ID            refused until the task is accepted, unless --force; --tree also closes the
                                              tasks under it (--tree --force: your own subtree, whatever the status)
  task adopt (ID | PANE) [--force]            you become the owner: of a task (its sub-tasks stay under it), or of a pane with
                                              no task (a running task is made; the pane is told to report with task done).
                                              Refused while the old owner pane is alive, unless --force (exit 6)
  watch [--interval S]                        tell you of each new dialog on the panes you started, and of a task whose
                                              worker looks idle 10 min without done or progress (stalled); task new starts one.
                                              Also tells you once when a task below your own tasks waits on a dialog 3 min, or
                                              when one of your tasks ends while tasks under it still run
  log [--project DIR] [--task ID] [--scope NAME|ID] [--pane ID] [--since 2h|3d|DATE] [-n N] [--json]   the journal, oldest first (default: last 50)
  resume (TASK-ID | SCOPE | DIR) [--tree]     what a new session needs: a task's or scope's state, report and timeline;
                                              or a project's open tasks, scopes, notes and last entries (DIR: . for yours);
                                              --tree: one line per scope/task under it: status, latest report/progress, open dialog
  note WHAT (TEXT... | --file F | stdin)      file a text (a job brief, a plan) in your project's journal folder; prints its path
  state clean                                 remove the throwaway directory in TERMCTL_STATE_DIR; refuses the live one

--where auto (the default for spawn and task new): a new agent splits the largest pane in your tab, whatever
its folder, up to 12 panes, and evens out the sizes; then a tab of an agent you started; then a new tab beside yours.

Journal: every msg, spawn, slash, interrupt, key, close, note and task event is one line of
<state>/journal.jsonl; long messages, prompts, briefs and notes are files in <state>/projects/<project>/,
each starting with "<project path> · <unix time> · <local time> (<zone>)". <state> is TERMCTL_STATE_DIR,
else ~/.local/state/termctl.

task wait prints a DIALOG block (JSON: "event": "dialog") instead of the task table (JSON: "event": "finished")
when a worker waits on a dialog; both exit 0. A [persist] choice widens permission and needs --persist.

Exit codes: 0 ok, 1 backend error, 2 refused target/usage, 4 draft on input line (msg, slash: not typed),
5 wait timed out, or typed text stayed on the input line (it may still arrive: read, never resend),
6 refused by a safety limit, 7 no dialog on screen, or not the one --expect names.
`

var (
	msgFlags   = []string{"--force", "--clear-first", "--timeout", "--backend", "-L"}
	slashFlags = []string{"--force", "--wait", "--timeout", "--backend", "-L"}
)

type row struct {
	ID      string `json:"id"`
	PaneID  string `json:"pane_id"`
	TTY     string `json:"tty"`
	Title   string `json:"title"`
	Label   string `json:"label"`
	Harness string `json:"harness"`
	Folder  string `json:"folder"`
	Self    bool   `json:"self"`
	Status  string `json:"status"`
	Tab     string `json:"tab"`               // the tab (tmux: window) ID, "" where the backend reports none
	Hidden  bool   `json:"hidden,omitempty"`  // behind a maximized pane (iTerm2)
	Dialog  string `json:"dialog,omitempty"`  // ls --mine: the fingerprint of the dialog marked open on the pane
	Scope   string `json:"scope,omitempty"`   // ls --mine: attached scope name
	Stalled bool   `json:"stalled,omitempty"` // ls --mine: whether the lease is marked stalled
}

func (a *App) printTable(panes []*term.Pane) {
	w := idWidth(panes)
	fmt.Fprintf(a.Out, "%-*s %-6s %-9s %-14s %-34s %-36s STATUS\n", w, "ID", "TAB", "HARNESS", "LABEL", "TITLE", "FOLDER")
	for _, p := range panes {
		mark := ""
		if p.IsSelf {
			mark = " (you)"
		}
		if p.Hidden {
			mark += " (hidden)"
		}
		fmt.Fprintf(a.Out, "%-*s %-6s %-9s %-14s %-34s %-36s %s\n", w, p.Short(), tabOr(p), term.Cut(harness.HarnessName(p), 9), term.Cut(p.Label, 14),
			term.Cut(term.Cut(p.Title, 28)+mark, 34), term.Cut(p.Folder, 36), term.Cut(p.Status, 60))
	}
}

func fillStatus(ops *harness.Ops, panes []*term.Pane) {
	natives := make([]string, len(panes))
	var wg sync.WaitGroup
	gate := make(chan struct{}, 8)
	for i, p := range panes {
		wg.Add(1)
		go func(i int, p *term.Pane) {
			defer wg.Done()
			gate <- struct{}{}
			natives[i] = ops.Backend.NativeStatus(p)
			<-gate
		}(i, p)
	}
	wg.Wait()
	for i, p := range panes {
		p.Status = ops.Status(p, natives[i])
	}
}

func (a *App) newOps(b term.Backend, sender *term.Pane) *harness.Ops {
	return harness.NewOps(b, sender, a.Clock, "")
}

func (a *App) openTasks(o *Options, b term.Backend) (*harness.Tasks, error) {
	sender, err := a.OpenSender(o, b)
	if err != nil {
		return nil, err
	}
	return a.tasksFor(o, b, sender), nil
}

// tasksFor is openTasks for a sender already found.
func (a *App) tasksFor(o *Options, b term.Backend, sender *term.Pane) *harness.Tasks {
	ref := map[string]string{}
	if sender != nil {
		if sender.Backend == b.Name() {
			ref = b.ServerRef()
		} else if other, err := term.Make(sender.Backend, o.Socket, ""); err == nil {
			ref = other.ServerRef()
		}
	}
	return &harness.Tasks{Reg: harness.NewRegistry(""), Backend: b, Sender: sender, SenderRef: ref, Clock: a.Clock, Factory: a.Factory}
}

// gaveWork is called when a msg or a spawn's prompt reached pane. A pane the caller started (a pair partner,
// a live-team member) is recorded as given work, so a watcher counts it working, and a watcher starts when
// none runs: the pane can stop on a dialog. Any other pane changes nothing. A failure only warns: the
// message went through.
func (a *App) gaveWork(o *Options, tasks *harness.Tasks, b term.Backend, pane *term.Pane) {
	owned, err := tasks.GaveWork(b, pane)
	if err != nil {
		fmt.Fprintf(a.Err, "harness-ops: warning: could not record the work given to %s, so no dialog watcher may run for it: %v\n", pane.Address(), err)
	}
	if owned {
		a.ensureWatcher(o, tasks)
	}
}

func (a *App) messageText(rest []string, literal bool, flags []string) (string, error) {
	if !literal {
		for _, w := range rest {
			if has(flags, w) {
				return "", term.UsageErr("%s must come before the target (to send it as text, put -- before the message)", w)
			}
		}
	}
	return strings.Join(rest, " "), nil
}

func when(ts float64) string {
	if ts == 0 {
		return "-"
	}
	return time.Unix(int64(ts), 0).Format("01-02 15:04")
}

// printWaited shows what task wait found: the task table for a finished task, a DIALOG block for a dialog.
func (a *App) printWaited(w *harness.Waited, asJSON bool) {
	if asJSON {
		event := "finished"
		if w.Dialog != nil {
			event = "dialog"
		}
		data, _ := json.MarshalIndent(map[string]any{"event": event, "task": w.Task, "dialog": w.Dialog}, "", " ")
		fmt.Fprintln(a.Out, string(data))
		return
	}
	if w.Dialog == nil {
		a.printTasks([]*harness.Task{w.Task})
		return
	}
	fmt.Fprintf(a.Out, "DIALOG task %s (%s): its worker waits for an answer\n%s", w.Task.ID, w.Task.Title, harness.DialogText(w.Task.ID, w.Dialog))
}

func (a *App) printAnswered(target string, res *harness.Answered) {
	switch {
	case res.Escaped:
		fmt.Fprintf(a.Out, "denied dialog %s on %s: pressed Esc to cancel it\n", res.Dialog.Fingerprint, target)
	case res.Chosen != nil:
		fmt.Fprintf(a.Out, "answered dialog %s on %s: %d. %s\n", res.Dialog.Fingerprint, target, res.Chosen.N, res.Chosen.Text)
	}
	if res.Next != nil {
		fmt.Fprintf(a.Out, "a new dialog is on screen:\n%s", harness.DialogText(target, res.Next))
	}
}

// ensureWatcher starts a detached watcher for the caller unless one already runs (in which case it wakes
// a running watcher from slow-idle sleep). Failing to start one only warns: the task/scope itself is running.
func (a *App) ensureWatcher(o *Options, tasks *harness.Tasks) {
	if tasks.Sender == nil {
		return
	}
	if tasks.Watching(tasks.Sender.ID) != 0 {
		tasks.WakeWatcher(tasks.Sender.ID)
		return
	}
	if a.StartWatcher == nil {
		return
	}
	if err := a.StartWatcher(o, tasks.WatchLog(tasks.Sender.ID)); err != nil {
		fmt.Fprintf(a.Err, "harness-ops: warning: could not start a dialog watcher: %v; run harness-ops watch yourself\n", err)
	}
}

func (a *App) printTasks(tasks []*harness.Task) {
	fmt.Fprintf(a.Out, "%-9s %-8s %-9s %-5s %-12s %-28s SUMMARY\n", "ID", "STATUS", "HARNESS", "ROUND", "STARTED", "TITLE")
	for _, t := range tasks {
		a.printTaskRow(t, "")
	}
}

// printTree prints task nodes with each task's descendants under it, two spaces deeper per level.
func (a *App) printTree(nodes []*harness.TaskNode) {
	fmt.Fprintf(a.Out, "%-9s %-8s %-9s %-5s %-12s %-28s SUMMARY\n", "ID", "STATUS", "HARNESS", "ROUND", "STARTED", "TITLE")
	var walk func(ns []*harness.TaskNode, indent string)
	walk = func(ns []*harness.TaskNode, indent string) {
		for _, n := range ns {
			a.printTaskRow(n.Task, indent)
			walk(n.Children, indent+"  ")
		}
	}
	walk(nodes, "")
}

func (a *App) nowUnix() float64 {
	if a.Clock != nil {
		return a.Clock.Now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

// age says how long ago a time was, in whole seconds, minutes or hours.
func (a *App) age(ts float64) string {
	s := int(a.nowUnix() - ts)
	switch {
	case s < 0:
		s = 0
	case s >= 3600:
		return fmt.Sprintf("%dh%02dm ago", s/3600, s%3600/60)
	case s >= 60:
		return fmt.Sprintf("%dm ago", s/60)
	}
	return fmt.Sprintf("%ds ago", s)
}

// progressLine is task show's progress: the latest line and its age.
func (a *App) progressLine(t *harness.Task) string {
	if t.Progress == nil {
		return ""
	}
	return fmt.Sprintf("%s (%s)", t.Progress.Text, a.age(t.Progress.At))
}

// stallLine is task show's stall: when the watcher marked a running task's worker idle too long.
func (a *App) stallLine(t *harness.Task) string {
	if t.Status != "running" || t.Stall == nil {
		return ""
	}
	return fmt.Sprintf("idle 10 min without done or progress, marked %s", a.age(t.Stall.At))
}

// role is task show's capability line: what the task may start, and its tree's limit.
func role(t *harness.Task) string {
	switch {
	case t.Root == "":
		return ""
	case t.SubDepth < 1:
		return fmt.Sprintf("leaf; tree limit %d", t.TreeMax)
	}
	return fmt.Sprintf("lead, may start tasks with --sub-depth up to %d; tree limit %d", t.SubDepth-1, t.TreeMax)
}

// deferLine is task show's deferred line: acked or not, and the ack note.
func deferLine(t *harness.Task) string {
	switch {
	case !t.Defer:
		return ""
	case t.AckedAt == 0:
		return "yes, not acked"
	case t.AckNote != "":
		return fmt.Sprintf("yes, acked %s: %s", when(t.AckedAt), t.AckNote)
	}
	return "yes, acked " + when(t.AckedAt)
}

// printTaskRow prints one task line; indent goes before the ID (task ls --tree).
func (a *App) printTaskRow(t *harness.Task, indent string) {
	extra := ""
	if t.Notify == "pending" {
		extra += " (owner not told yet)"
	}
	if t.Closed {
		extra += " [closed]"
	}
	if t.ReviewOf != "" {
		extra += " [reviews " + t.ReviewOf + "]"
	}
	if t.Dialog != nil {
		extra += " [dialog " + t.Dialog.Fingerprint + "]"
	}
	if t.Status == "exited" && t.Restarted {
		extra += " [harness back; task adopt " + t.Pane + "]"
	}
	if m := harness.AckMarks(t); m != "" {
		extra += " " + m
	}
	status, summary := t.Status, t.Summary
	if t.Status == "running" {
		if t.Stall != nil {
			status += " stalled"
		}
		if t.Progress != nil {
			summary = t.Progress.Text
		}
	}
	fmt.Fprintf(a.Out, "%-9s %-8s %-9s %-5d %-12s %-28s %s%s\n", indent+t.ID, status, t.Harness, t.Round, when(t.Started), term.Cut(t.Title, 28),
		term.Cut(strings.ReplaceAll(summary, "\n", " "), 60), extra)
}

// Harness runs the harness-ops command line and returns the exit code.
func (a *App) Harness(argv []string) int {
	code := 0
	if err := a.harness(argv); err != nil {
		code = a.fail("harness-ops", err)
	}
	a.deliverPending()
	return code
}

// deliverPending lets any harness-ops call deliver held-back task results. It never changes the exit code.
func (a *App) deliverPending() {
	reg := harness.NewRegistry("")
	d, err := reg.Read()
	if err != nil {
		fmt.Fprintf(a.Err, "harness-ops: warning: could not read pending task results: %v\n", err)
		return
	}
	if len(d.Notices) == 0 {
		return
	}
	t := &harness.Tasks{Reg: reg, Clock: a.Clock}
	if _, err := t.FlushPending(); err != nil {
		fmt.Fprintf(a.Err, "harness-ops: warning: could not deliver pending task results: %v\n", err)
	}
}

func (a *App) harness(argv []string) error {
	o := &Options{}
	head, tail, dashed := splitDoubleDash(argv)
	args, err := globals(head, o)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		fmt.Fprint(a.Err, harnessUsage)
		return term.UsageErr("a command is required")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Fprint(a.Out, harnessUsage)
		return nil
	case "task":
		return a.task(rest, tail, dashed, o)
	case "attach":
		return a.attachCmd(rest, o)
	case "detach":
		return a.detachCmd(rest, o)
	case "report":
		return a.reportCmd(rest, tail, dashed, o)
	case "scope":
		return a.scopeCmd(rest, o)
	case "state":
		if len(rest) != 1 || rest[0] != "clean" {
			return term.UsageErr("usage: state clean  (removes the directory in TERMCTL_STATE_DIR; never the live one)")
		}
		if err := harness.CleanState(os.Getenv("TERMCTL_STATE_DIR")); err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "cleaned %s\n", os.Getenv("TERMCTL_STATE_DIR"))
		return nil
	case "ls":
		p, err := parseArgs(rest, []string{"--json", "--all", "--mine"}, nil, -1)
		if err != nil {
			return err
		}
		if err := applyOptions(p, o); err != nil {
			return err
		}
		return a.ls(o, p.bools["--all"], p.bools["--json"], p.bools["--mine"])
	case "whoami", "name":
		return a.selfCmd(cmd, rest, o)
	case "watch":
		return a.watch(rest, o)
	case "answer", "deny":
		return a.answer(cmd, rest, o)
	case "log":
		return a.logCmd(rest, o)
	case "resume":
		return a.resumeCmd(rest, o)
	case "note":
		return a.noteCmd(rest, tail, dashed, o)
	case "move", "pull", "swap", "arrange":
		return a.harnessLayout(cmd, rest, o)
	case "which", "read", "capture":
		return a.lookCmd(cmd, rest, o)
	case "msg", "send":
		rest = optionalValue(rest, "--wait-ack", "30", []string{"--timeout", "--backend", "-L"}, true)
		p, err := parseArgs(rest, []string{"--force", "--clear-first", "--defer", "--again"}, []string{"--timeout", "--wait-ack"}, 1)
		if err != nil {
			return err
		}
		if err := applyOptions(p, o); err != nil {
			return err
		}
		if len(p.pos) != 1 {
			return term.UsageErr("usage: msg [options] <target> <text...>")
		}
		timeout, err := p.float("--timeout", harness.DefaultTimeout)
		if err != nil {
			return err
		}
		words, literal := attachLiteral(p.rest, tail, dashed)
		text, err := a.messageText(words, literal, msgFlags)
		if err != nil {
			return err
		}
		ops, err := a.opsFor(o, p.pos[0])
		if err != nil {
			return err
		}
		_, waitAck := p.vals["--wait-ack"]
		ackWait, err := p.float("--wait-ack", 30)
		if err != nil {
			return err
		}
		if waitAck && !p.bools["--defer"] {
			return term.UsageErr("--wait-ack goes with --defer")
		}
		pane, err := ops.Pick(p.pos[0])
		if err != nil {
			return err
		}
		tasks := a.tasksFor(o, ops.Backend, ops.Sender)
		scopeID, attachSuf := tasks.PrepareMsg(ops.Backend, pane, p.bools["--defer"])
		if scopeID != "" {
			ops.Scope = scopeID
		}
		mo := harness.MsgOpts{Force: p.bools["--force"], ClearFirst: p.bools["--clear-first"], Timeout: timeout, Again: p.bools["--again"],
			Key: harness.MsgKey(address(ops.Sender), pane.ID, text)}
		if p.bools["--defer"] {
			mo.ID = harness.NewMsgID()
			mo.Suffix = harness.DeferSuffix(mo.ID)
		} else if attachSuf != "" {
			mo.Suffix = attachSuf
		}
		sent, err := ops.Msg(pane, text, mo)
		if sent.Typed && attachSuf != "" {
			if terr := tasks.MarkScopeTold(ops.Backend, pane); terr != nil {
				fmt.Fprintf(a.Err, "harness-ops: warning: %v\n", terr)
			}
		}
		if err != nil {
			if sent.Typed { // it may still arrive: the watcher must cover the pane's dialogs
				a.gaveWork(o, tasks, ops.Backend, pane)
			}
			return err
		}
		a.gaveWork(o, tasks, ops.Backend, pane)
		if mo.ID != "" {
			fmt.Fprintln(a.Out, mo.ID)
		}
		if waitAck {
			if sent.Busy {
				a.queued(pane)
			} else {
				a.sent(pane)
			}
			if err := tasks.WaitAck(mo.ID, ackWait); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "acked %s\n", mo.ID)
			return nil
		}
		if sent.Busy {
			a.queued(pane)
			return nil
		}
		a.sent(pane)
		return nil
	case "ack":
		p, err := parseArgs(rest, nil, nil, 1)
		if err != nil {
			return err
		}
		if err := applyOptions(p, o); err != nil {
			return err
		}
		if len(p.pos) != 1 {
			return term.UsageErr("usage: ack ID [NOTE...]  (ID: a deferred task or message)")
		}
		words, _ := attachLiteral(p.rest, tail, dashed)
		b, err := a.OpenBackend(o, "")
		if err != nil {
			return err
		}
		tasks, err := a.openTasks(o, b)
		if err != nil {
			return err
		}
		what, err := tasks.Ack(p.pos[0], strings.Join(words, " "))
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "acked %s; nothing was sent: do the job, and do not reply\n", what)
		return nil
	case "slash":
		p, err := parseArgs(rest, []string{"--force", "--wait"}, []string{"--timeout"}, 1)
		if err != nil {
			return err
		}
		if err := applyOptions(p, o); err != nil {
			return err
		}
		if len(p.pos) != 1 {
			return term.UsageErr("usage: slash [options] <target> </command>")
		}
		timeout, err := p.float("--timeout", harness.DefaultTimeout)
		if err != nil {
			return err
		}
		words, literal := attachLiteral(p.rest, tail, dashed)
		text, err := a.messageText(words, literal, slashFlags)
		if err != nil {
			return err
		}
		ops, err := a.opsFor(o, p.pos[0])
		if err != nil {
			return err
		}
		pane, err := ops.Pick(p.pos[0])
		if err != nil {
			return err
		}
		if err := ops.Slash(pane, text, p.bools["--force"], p.bools["--wait"], timeout); err != nil {
			return err
		}
		a.sent(pane)
		return nil
	case "clear", "wait-idle":
		p, err := parseArgs(rest, []string{"--force"}, []string{"--timeout"}, -1)
		if err != nil {
			return err
		}
		if err := applyOptions(p, o); err != nil {
			return err
		}
		if len(p.pos) != 1 {
			return term.UsageErr("usage: %s [--timeout S] <target>", cmd)
		}
		timeout, err := p.float("--timeout", harness.DefaultTimeout)
		if err != nil {
			return err
		}
		ops, err := a.opsFor(o, p.pos[0])
		if err != nil {
			return err
		}
		pane, err := ops.Pick(p.pos[0])
		if err != nil {
			return err
		}
		if cmd == "clear" {
			if err := ops.Clear(pane, p.bools["--force"], timeout); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "cleared %s\n", term.Cut(pane.Address(), 8))
			return nil
		}
		if err := ops.WaitIdle(pane, timeout, nil); err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "idle: %s\n", pane.Address())
		return nil
	case "spawn":
		p, err := parseArgs(rest, []string{"--accept-trust"}, []string{"--where", "--cwd", "--prompt", "--prompt-file", "--label", "--command", "--timeout"}, -1)
		if err != nil {
			return err
		}
		if err := applyOptions(p, o); err != nil {
			return err
		}
		if len(p.pos) != 1 {
			return term.UsageErr("usage: spawn <harness> [options]")
		}
		timeout, err := p.float("--timeout", harness.DefaultTimeout)
		if err != nil {
			return err
		}
		where := p.vals["--where"]
		if where == "" {
			where = "auto"
		}
		b, err := a.OpenBackend(o, "")
		if err != nil {
			return err
		}
		tasks, err := a.openTasks(o, b)
		if err != nil {
			return err
		}
		trust, err := trustRoots(p.bools["--accept-trust"])
		if err != nil {
			return err
		}
		pane, err := tasks.Spawn(p.pos[0], where, cwdOr(p.vals["--cwd"]), harness.SpawnOpts{Prompt: p.vals["--prompt"], PromptFile: p.vals["--prompt-file"], Label: p.vals["--label"], Command: p.vals["--command"], Timeout: timeout, Trust: trust})
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Out, pane.ID)
		if strings.TrimSpace(p.vals["--prompt"]) != "" || p.vals["--prompt-file"] != "" {
			a.gaveWork(o, tasks, b, pane) // work starts here: a watcher, so its dialogs reach you
		} else {
			tasks.WarnUncalibratedLead()
		}
		return nil
	case "close":
		p, err := parseArgs(rest, []string{"--force"}, nil, -1)
		if err != nil {
			return err
		}
		if err := applyOptions(p, o); err != nil {
			return err
		}
		if len(p.pos) != 1 {
			return term.UsageErr("usage: close [--force] <target>")
		}
		b, err := a.OpenBackend(o, p.pos[0])
		if err != nil {
			return err
		}
		tasks, err := a.openTasks(o, b)
		if err != nil {
			return err
		}
		pane, err := tasks.ClosePane(p.pos[0], p.bools["--force"])
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "closed %s (%s, %s)\n", term.Cut(pane.Address(), 8), harness.HarnessName(pane), orFolder(pane))
		if tasks.Sender != nil && tasks.Watching(tasks.Sender.ID) != 0 {
			tasks.WakeWatcher(tasks.Sender.ID)
		}
		return nil
	case "key":
		p, err := parseArgs(rest, []string{"--force"}, nil, -1)
		if err != nil {
			return err
		}
		if err := applyOptions(p, o); err != nil {
			return err
		}
		if len(p.pos) < 2 {
			return term.UsageErr("usage: key [--force] <target> <key>...")
		}
		b, err := a.OpenBackend(o, p.pos[0])
		if err != nil {
			return err
		}
		tasks, err := a.openTasks(o, b)
		if err != nil {
			return err
		}
		res, err := tasks.Keys(p.pos[0], p.pos[1:], p.bools["--force"])
		if res != nil && len(res.Keys) > 0 {
			fmt.Fprintf(a.Out, "pressed %s on %s (%s, %s)\n", strings.Join(res.Keys, " "), term.Cut(res.Pane.Address(), 8), harness.HarnessName(res.Pane), orFolder(res.Pane))
		}
		return err
	case "interrupt":
		p, err := parseArgs(rest, nil, []string{"--key"}, -1)
		if err != nil {
			return err
		}
		if err := applyOptions(p, o); err != nil {
			return err
		}
		if len(p.pos) != 1 {
			return term.UsageErr("usage: interrupt [--key K] <target>")
		}
		ops, err := a.opsFor(o, p.pos[0])
		if err != nil {
			return err
		}
		pane, err := ops.Pick(p.pos[0])
		if err != nil {
			return err
		}
		keys, verified, err := ops.Interrupt(pane, p.vals["--key"])
		if keys != nil {
			joined := strings.Join(keys, "+")
			fmt.Fprintf(a.Out, "sent %s to %s (%s)\n", joined, term.Cut(pane.Address(), 8), harness.HarnessName(pane))
			if !verified {
				fmt.Fprintf(a.Err, "harness-ops: warning: %s has not been verified to stop %s; check with read\n", joined, harness.HarnessName(pane))
			}
		}
		return err
	}
	return term.UsageErr("unknown command '%s'", cmd)
}

// trustRootsFile is the user's list of folders whose trust prompt --accept-trust may answer:
// $TERMCTL_TRUST_FILE, else $XDG_CONFIG_HOME/termctl/trust-roots, else ~/.config/termctl/trust-roots.
func trustRootsFile() string {
	if f := os.Getenv("TERMCTL_TRUST_FILE"); f != "" {
		return f
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "termctl", "trust-roots")
	}
	return filepath.Join(harness.ExpandHome("~"), ".config", "termctl", "trust-roots")
}

// trustRoots loads the roots for --accept-trust; without the flag it reads nothing. A missing or empty file
// refuses the flag (exit 6): the grant is the user's to write.
func trustRoots(accept bool) (*harness.TrustRoots, error) {
	if !accept {
		return nil, nil
	}
	file := trustRootsFile()
	t, err := harness.LoadTrustRoots(file, harness.ExpandHome("~"))
	if err != nil {
		return nil, err
	}
	if t == nil || len(t.Roots) == 0 {
		return nil, term.Errf(term.CodeGuard, "--accept-trust needs trust roots: the user writes the folders whose trust prompt may be answered, one absolute pattern per line (* matches within one folder name), to %s", file)
	}
	return t, nil
}

func cwdOr(cwd string) string {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return cwd
}

func orFolder(p *term.Pane) string {
	if p.Title != "" {
		return p.Title
	}
	return p.Folder
}

func (a *App) queued(pane *term.Pane) {
	fmt.Fprintf(a.Out, "queued for %s (%s, %s): it is working and may hold the message until its turn ends; "+
		"an interrupt would put it back on the input line unsent\n", term.Cut(pane.Address(), 8), harness.HarnessName(pane), orFolder(pane))
}

func address(p *term.Pane) string {
	if p == nil {
		return ""
	}
	return p.Address()
}

func (a *App) sent(p *term.Pane) {
	fmt.Fprintf(a.Out, "sent to %s (%s, %s)\n", term.Cut(p.Address(), 8), harness.HarnessName(p), orFolder(p))
}

func (a *App) opsFor(o *Options, target string) (*harness.Ops, error) {
	b, err := a.OpenBackend(o, target)
	if err != nil {
		return nil, err
	}
	sender, err := a.OpenSender(o, b)
	if err != nil {
		return nil, err
	}
	return a.newOps(b, sender), nil
}

func (a *App) ls(o *Options, all, asJSON, mine bool) error {
	names := []string{""}
	if all {
		names = nil
		for _, n := range term.BackendNames {
			if n != "ghostty" {
				names = append(names, n)
			}
		}
	}
	var panes []*term.Pane
	dialogs := map[string]string{}
	scopes := map[string]string{}
	stalls := map[string]bool{}
	for _, name := range names {
		opt := *o
		if name != "" {
			opt.Backend = name
		}
		b, err := a.OpenBackend(&opt, "")
		if err != nil {
			return err
		}
		if all && !b.Available() {
			continue
		}
		found, err := b.ListPanes()
		if err != nil {
			return err
		}
		var mineEntries []harness.MineEntry
		if mine {
			tasks, err := a.openTasks(&opt, b)
			if err != nil {
				return err
			}
			if tasks.Sender == nil {
				return term.UsageErr("ls --mine: cannot tell which pane this runs in")
			}
			mineEntries, err = tasks.MineInfo(found)
			if err != nil {
				return err
			}
			found = make([]*term.Pane, 0, len(mineEntries))
			for _, me := range mineEntries {
				found = append(found, me.Pane)
			}
		}
		fillStatus(a.newOps(b, nil), found)
		if mine {
			for _, me := range mineEntries {
				p := me.Pane
				if me.Dialog != "" {
					p.Status, dialogs[p.ID] = "dialog "+me.Dialog, me.Dialog
				} else if me.Stalled {
					p.Status = "stalled"
				}
				if me.Scope != "" {
					scopes[p.ID] = me.Scope
					p.Status += " (scope " + me.Scope + ")"
				}
				if me.Stalled {
					stalls[p.ID] = true
				}
			}
		}
		panes = append(panes, found...)
	}
	if asJSON {
		rows := []row{}
		for _, p := range panes {
			rows = append(rows, row{p.Address(), p.ID, p.TTY, p.Title, p.Label, harness.HarnessName(p), p.Folder, p.IsSelf, p.Status, p.Tab, p.Hidden, dialogs[p.ID], scopes[p.ID], stalls[p.ID]})
		}
		data, _ := json.MarshalIndent(rows, "", " ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	a.printTable(panes)
	return nil
}

func (a *App) selfCmd(cmd string, rest []string, o *Options) error {
	p, err := parseArgs(rest, nil, nil, -1)
	if err != nil {
		return err
	}
	if err := applyOptions(p, o); err != nil {
		return err
	}
	if cmd == "name" && (len(p.pos) != 1 || !labelRe.MatchString(p.pos[0])) {
		return term.UsageErr("usage: name <label>  (letters, digits, . _ -; max 40)")
	}
	b, err := a.OpenBackend(o, "")
	if err != nil {
		return err
	}
	if cmd == "whoami" {
		me, err := a.OpenSender(o, b)
		if err != nil {
			return err
		}
		if me == nil {
			return term.UsageErr("cannot tell which pane this runs in")
		}
		fillStatus(a.newOps(b, nil), []*term.Pane{me})
		a.printTable([]*term.Pane{me})
		if line := a.tasksFor(o, b, me).SupervisionLine(); line != "" {
			fmt.Fprintln(a.Out, line)
		}
		return nil
	}
	me, err := b.SelfPane()
	if err != nil {
		return err
	}
	if me == nil {
		return term.UsageErr("cannot tell which pane this runs in")
	}
	if err := b.SetLabel(me, p.pos[0]); err != nil {
		return err
	}
	if err := term.SetTitle(b, me, p.pos[0]); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "labelled this pane: %s\n", p.pos[0])
	return nil
}

func (a *App) lookCmd(cmd string, rest []string, o *Options) error {
	p, err := parseArgs(rest, nil, []string{"-n"}, -1)
	if err != nil {
		return err
	}
	if err := applyOptions(p, o); err != nil {
		return err
	}
	if len(p.pos) != 1 {
		return term.UsageErr("usage: %s <target>", cmd)
	}
	b, err := a.OpenBackend(o, p.pos[0])
	if err != nil {
		return err
	}
	panes, err := b.ListPanes()
	if err != nil {
		return err
	}
	pane, err := term.Resolve(panes, p.pos[0])
	if err != nil {
		return err
	}
	if cmd == "which" {
		fillStatus(a.newOps(b, nil), []*term.Pane{pane})
		a.printTable([]*term.Pane{pane})
		return nil
	}
	n, err := p.int("-n", 40)
	if err != nil {
		return err
	}
	lines, err := b.Capture(pane, n)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "--- %s %s %s %s ---\n%s\n", term.Cut(pane.Address(), 8), harness.HarnessName(pane), pane.Title, pane.Folder, strings.Join(lines, "\n"))
	return nil
}

func (a *App) task(rest, tail []string, dashed bool, o *Options) error {
	if len(rest) == 0 {
		return term.UsageErr("usage: task new|ls|show|send|wait|done|progress|accept|review|close|adopt")
	}
	cmd, rest := rest[0], rest[1:]
	if cmd == "answer" || cmd == "deny" {
		return term.UsageErr("task %s was removed in v0.7.0; use harness-ops %s", cmd, cmd)
	}
	bools, vals, restAfter := []string{}, []string{}, -1
	switch cmd {
	case "new":
		rest = optionalValue(rest, "--wait-ack", "30", []string{"--title", "--brief", "--brief-file", "--cwd", "--where", "--reuse", "--command",
			"--max", "--timeout", "--sub-depth", "--tree-max", "--backend", "-L"}, false)
		bools = []string{"--defer", "--accept-trust"}
		vals = []string{"--title", "--brief", "--brief-file", "--cwd", "--where", "--reuse", "--command", "--max", "--timeout", "--wait-ack", "--sub-depth", "--tree-max"}
	case "ls":
		bools = []string{"--all", "--tree", "--json"}
	case "show":
		bools = []string{"--json"}
	case "send":
		bools, restAfter = []string{"--force"}, 1
	case "wait":
		bools, vals = []string{"--any", "--json"}, []string{"--timeout"}
	case "done":
		vals = []string{"--status", "--summary", "--summary-file"}
	case "progress":
		bools, vals = []string{"--notify"}, []string{"--summary"}
	case "close":
		bools = []string{"--force", "--tree"}
	case "adopt":
		bools = []string{"--force"}
	case "accept":
		bools = []string{"--close"}
	case "review":
		bools, vals = []string{"--blind"}, []string{"--harness", "--model", "--brief-file", "--where", "--command", "--timeout"}
	case "exited":
		vals = []string{"--code"}
	default:
		return term.UsageErr("unknown task command '%s'", cmd)
	}
	p, err := parseArgs(rest, bools, vals, restAfter)
	if err != nil {
		return err
	}
	if err := applyOptions(p, o); err != nil {
		return err
	}
	b, err := a.OpenBackend(o, p.vals["--reuse"])
	if err != nil {
		return err
	}
	tasks, err := a.openTasks(o, b)
	if err != nil {
		return err
	}
	arg := func() (string, error) {
		if len(p.pos) != 1 {
			return "", term.UsageErr("task %s needs a task id", cmd)
		}
		return p.pos[0], nil
	}
	switch cmd {
	case "new":
		if len(p.pos) != 1 {
			return term.UsageErr("usage: task new <harness> --title T (--brief TEXT | --brief-file F)")
		}
		if p.vals["--title"] == "" {
			return term.UsageErr("task new needs --title")
		}
		timeout, err := p.float("--timeout", harness.DefaultTimeout)
		if err != nil {
			return err
		}
		maxTasks, err := p.int("--max", 0)
		if err != nil {
			return err
		}
		subDepth, err := p.int("--sub-depth", 0)
		if err != nil {
			return err
		}
		treeMax, err := p.int("--tree-max", 0)
		if err != nil {
			return err
		}
		_, waitAck := p.vals["--wait-ack"]
		ackWait, err := p.float("--wait-ack", 30)
		if err != nil {
			return err
		}
		if waitAck && !p.bools["--defer"] {
			return term.UsageErr("--wait-ack goes with --defer")
		}
		trust, err := trustRoots(p.bools["--accept-trust"])
		if err != nil {
			return err
		}
		t, err := tasks.New(harness.NewOpts{Harness: p.pos[0], Title: p.vals["--title"], Brief: p.vals["--brief"], BriefFile: p.vals["--brief-file"],
			Cwd: cwdOr(p.vals["--cwd"]), Where: p.vals["--where"], Reuse: p.vals["--reuse"], Command: p.vals["--command"], MaxTasks: maxTasks, Timeout: timeout,
			Defer: p.bools["--defer"], SubDepth: subDepth, TreeMax: treeMax, Trust: trust})
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Out, t.ID)
		a.ensureWatcher(o, tasks)
		if waitAck {
			if err := tasks.WaitAck(t.ID, ackWait); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "acked %s\n", t.ID)
		}
	case "ls":
		if p.bools["--tree"] {
			nodes, err := tasks.Tree(p.bools["--all"])
			if err != nil {
				return err
			}
			if p.bools["--json"] {
				if nodes == nil {
					nodes = []*harness.TaskNode{}
				}
				data, _ := json.MarshalIndent(nodes, "", " ")
				fmt.Fprintln(a.Out, string(data))
			} else {
				a.printTree(nodes)
			}
			return nil
		}
		found, err := tasks.Ls(p.bools["--all"])
		if err != nil {
			return err
		}
		if p.bools["--json"] {
			if found == nil {
				found = []*harness.Task{}
			}
			data, _ := json.MarshalIndent(found, "", " ")
			fmt.Fprintln(a.Out, string(data))
		} else {
			a.printTasks(found)
		}
	case "show":
		id, err := arg()
		if err != nil {
			return err
		}
		t, err := tasks.Show(id)
		if err != nil {
			return err
		}
		if p.bools["--json"] {
			data, _ := json.MarshalIndent(t, "", " ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		restarted := ""
		if t.Status == "exited" && t.Restarted {
			restarted = fmt.Sprintf("harness is back in %s; run harness-ops task adopt %s", t.Pane, t.Pane)
		}
		for _, kv := range [][2]string{{"id", t.ID}, {"status", t.Status}, {"round", fmt.Sprint(t.Round)}, {"title", t.Title}, {"harness", t.Harness}, {"reviews", t.ReviewOf}, {"pane", t.Pane}, {"owner", t.Owner},
			{"cwd", t.Cwd}, {"brief", t.Brief}, {"depth", fmt.Sprint(t.Depth)}, {"parent", t.Parent}, {"notify", t.Notify}, {"summary", t.Summary},
			{"progress", a.progressLine(t)}, {"stalled", a.stallLine(t)}, {"restarted", restarted}, {"root", t.Root}, {"role", role(t)}, {"deferred", deferLine(t)},
			{"started", when(t.Started)}, {"ended", when(t.Ended)}} {
			if (kv[0] == "parent" || kv[0] == "progress" || kv[0] == "stalled" || kv[0] == "restarted" || kv[0] == "root" || kv[0] == "role" || kv[0] == "deferred") && kv[1] == "" {
				continue
			}
			fmt.Fprintf(a.Out, "%-9s %s\n", kv[0]+":", kv[1])
		}
	case "send":
		id, err := arg()
		if err != nil {
			return err
		}
		words, literal := attachLiteral(p.rest, tail, dashed)
		text, err := a.messageText(words, literal, []string{"--force", "--backend", "-L"})
		if err != nil {
			return err
		}
		if err := tasks.Send(id, text, p.bools["--force"]); err != nil {
			var te *term.Error
			if errors.As(err, &te) && te.Code == term.CodeTimeout { // typed, may still arrive: the round is open
				a.ensureWatcher(o, tasks)
			}
			return err
		}
		fmt.Fprintf(a.Out, "sent to task %s\n", id)
		a.ensureWatcher(o, tasks)
	case "wait":
		timeout, err := p.float("--timeout", harness.WaitTimeout)
		if err != nil {
			return err
		}
		id := ""
		if len(p.pos) > 0 {
			id = p.pos[0]
		}
		if (id != "") == p.bools["--any"] {
			return term.UsageErr("task wait needs a task id or --any, not both")
		}
		w, err := tasks.Wait(id, p.bools["--any"], timeout)
		if err != nil {
			return err
		}
		a.printWaited(w, p.bools["--json"])
	case "done":
		id, err := arg()
		if err != nil {
			return err
		}
		t, err := tasks.Done(id, p.vals["--status"], p.vals["--summary"], p.vals["--summary-file"])
		if err != nil {
			return err
		}
		note := map[string]string{"delivered": "owner told; keep this session open", "pending": "owner busy: notification pending; keep this session open", "owner-gone": "owner pane is gone", "seen": "owner took it with task wait"}[t.Notify]
		if t.Defer {
			note = "deferred: recorded, nobody is told; do not reply"
		}
		if note == "" {
			note = t.Notify
		}
		fmt.Fprintf(a.Out, "task %s: %s (%s)\n", t.ID, t.Status, note)
	case "progress":
		id, err := arg()
		if err != nil {
			return err
		}
		t, err := tasks.Progress(id, p.vals["--summary"], p.bools["--notify"])
		if err != nil {
			return err
		}
		note := ""
		if p.bools["--notify"] {
			note = " (owner notice queued)"
		}
		fmt.Fprintf(a.Out, "task %s: progress noted%s\n", t.ID, note)
	case "accept":
		id, err := arg()
		if err != nil {
			return err
		}
		t, err := tasks.Accept(id, p.bools["--close"])
		if err != nil {
			return err
		}
		state := "accepted"
		if t.Closed {
			state = "accepted and closed"
		}
		fmt.Fprintf(a.Out, "%s task %s\n", state, t.ID)
	case "review":
		id, err := arg()
		if err != nil {
			return err
		}
		timeout, err := p.float("--timeout", harness.DefaultTimeout)
		if err != nil {
			return err
		}
		t, err := tasks.Review(harness.ReviewOpts{ID: id, Blind: p.bools["--blind"], Harness: p.vals["--harness"], Model: p.vals["--model"],
			BriefFile: p.vals["--brief-file"], Where: p.vals["--where"], Command: p.vals["--command"], Timeout: timeout})
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Out, t.ID)
		a.ensureWatcher(o, tasks) // the reviewer is a running task, and can stop on a dialog too
	case "adopt":
		id, err := arg()
		if err != nil {
			return err
		}
		res, err := tasks.Adopt(id, p.bools["--force"])
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Out, res.Task.ID)
		switch {
		case res.New:
			fmt.Fprintf(a.Out, "adopted %s as task %s; it was told to report to you with task done\n", res.Task.Pane, res.Task.ID)
		case res.OldOwner == res.Task.Owner:
			fmt.Fprintf(a.Out, "reopened task %s (round %d); it reports to you now\n", res.Task.ID, res.Task.Round)
		default:
			fmt.Fprintf(a.Out, "adopted task %s from %s; it reports to you now\n", res.Task.ID, orDash(res.OldOwner))
		}
		a.ensureWatcher(o, tasks)
	case "exited":
		id, err := arg()
		if err != nil {
			return err
		}
		code, err := p.int("--code", 0)
		if err != nil {
			return err
		}
		t, err := tasks.Exited(id, code)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "task %s: %s\n", t.ID, t.Status)
	case "close":
		id, err := arg()
		if err != nil {
			return err
		}
		if p.bools["--tree"] {
			closed, err := tasks.CloseTree(id, p.bools["--force"])
			for _, c := range closed {
				fmt.Fprintf(a.Out, "closed task %s (%s)\n", c.ID, c.Status)
			}
			return err
		}
		t, kept, err := tasks.CloseWithNote(id, p.bools["--force"])
		if err != nil {
			return err
		}
		suffix := ""
		if kept != "" {
			suffix = " (pane left open: " + kept + ")"
		}
		fmt.Fprintf(a.Out, "closed task %s%s\n", t.ID, suffix)
	}
	return nil
}

// answer runs answer and deny. TARGET is a task ID
// or a pane the caller started; a pane target also selects its backend.
func (a *App) answer(cmd string, rest []string, o *Options) error {
	bools := []string{}
	want := 1
	if cmd == "answer" {
		bools, want = []string{"--persist"}, 2
	}
	p, err := parseArgs(rest, bools, []string{"--expect"}, -1)
	if err != nil {
		return err
	}
	if err := applyOptions(p, o); err != nil {
		return err
	}
	if len(p.pos) != want {
		return term.UsageErr("usage: answer TARGET N --expect FP [--persist] | deny TARGET --expect FP")
	}
	if p.vals["--expect"] == "" {
		return term.UsageErr("%s needs --expect FP, the fingerprint of the dialog you read (from the notice or task wait): a key must never land on a dialog you have not seen", cmd)
	}
	opts := harness.AnswerOpts{Deny: cmd == "deny", Expect: p.vals["--expect"], Persist: p.bools["--persist"]}
	if cmd == "answer" {
		if opts.N, err = strconv.Atoi(p.pos[1]); err != nil || opts.N < 1 {
			return term.UsageErr("answer: the choice must be a number from the dialog, got %q", p.pos[1])
		}
	}
	b, err := a.OpenBackend(o, p.pos[0])
	if err != nil {
		return err
	}
	tasks, err := a.openTasks(o, b)
	if err != nil {
		return err
	}
	res, _, err := tasks.Answer(p.pos[0], opts)
	if res != nil {
		a.printAnswered(p.pos[0], res)
	}
	return err
}
