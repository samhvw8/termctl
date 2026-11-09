package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/samhvw8/termctl/internal/harness"
	"github.com/samhvw8/termctl/internal/term"
)

const harnessUsage = `usage: harness-ops [--backend NAME] [-L SOCKET] COMMAND

Talk to coding-agent panes. Put options before the target: everything after the target is text.

  ls [--json] [--all]                         panes with harness, label, folder, status
  which TARGET                                resolve a target; never sends
  read TARGET [-n LINES]                      print a pane's screen (alias: capture)
  msg [--force] [--clear-first] [--timeout S] TARGET TEXT...   tagged message (alias: send)
  slash [--force] [--wait] [--timeout S] TARGET /COMMAND
  clear [--force] [--timeout S] TARGET        /clear alone, then wait for idle
  wait-idle [--timeout S] TARGET
  spawn HARNESS [--where tab|split|window] [--cwd D] [--prompt T | --prompt-file F] [--label L] [--command CMD] [--timeout S]
  close [--force] TARGET                      only panes made by spawn or task new; never your own
  interrupt [--key K] TARGET                  stop the current turn
  name LABEL                                  label the current pane
  whoami
  task new HARNESS --title T (--brief TEXT | --brief-file F) [--cwd D] [--where W] [--reuse TARGET] [--command CMD] [--max N] [--timeout S]
  task ls [--all] [--json]
  task show ID [--json]
  task send [--force] ID TEXT...
  task wait (ID | --any) [--timeout S]
  task done ID --status ok|failed|blocked (--summary TEXT | --summary-file F)
  task close [--force] ID

Exit codes: 0 ok, 1 backend error, 2 refused target/usage, 4 draft on input line, 5 wait timed out, 6 refused by a safety limit.
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
}

func (a *App) printTable(panes []*term.Pane) {
	w := idWidth(panes)
	fmt.Fprintf(a.Out, "%-*s %-9s %-14s %-34s %-36s STATUS\n", w, "ID", "HARNESS", "LABEL", "TITLE", "FOLDER")
	for _, p := range panes {
		mark := ""
		if p.IsSelf {
			mark = " (you)"
		}
		fmt.Fprintf(a.Out, "%-*s %-9s %-14s %-34s %-36s %s\n", w, p.Short(), term.Cut(harness.HarnessName(p), 9), term.Cut(p.Label, 14),
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
	return harness.NewOps(b, sender, a.Clock, a.Inbox)
}

func (a *App) openTasks(o *Options, b term.Backend) (*harness.Tasks, error) {
	sender, err := a.OpenSender(o, b)
	if err != nil {
		return nil, err
	}
	ref := map[string]string{}
	if sender != nil {
		if sender.Backend == b.Name() {
			ref = b.ServerRef()
		} else if other, err := term.Make(sender.Backend, o.Socket, ""); err == nil {
			ref = other.ServerRef()
		}
	}
	return &harness.Tasks{Reg: harness.NewRegistry(""), Backend: b, Sender: sender, SenderRef: ref, Clock: a.Clock, Inbox: a.Inbox}, nil
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

func (a *App) printTasks(tasks []*harness.Task) {
	fmt.Fprintf(a.Out, "%-9s %-8s %-9s %-12s %-28s SUMMARY\n", "ID", "STATUS", "HARNESS", "STARTED", "TITLE")
	for _, t := range tasks {
		extra := ""
		if t.Notify == "pending" {
			extra += " (owner not told yet)"
		}
		if t.Closed {
			extra += " [closed]"
		}
		fmt.Fprintf(a.Out, "%-9s %-8s %-9s %-12s %-28s %s%s\n", t.ID, t.Status, t.Harness, when(t.Started), term.Cut(t.Title, 28),
			term.Cut(strings.ReplaceAll(t.Summary, "\n", " "), 60), extra)
	}
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
	if len(d.Pending) == 0 {
		return
	}
	t := &harness.Tasks{Reg: reg, Clock: a.Clock, Inbox: a.Inbox}
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
	case "ls":
		p, err := parseArgs(rest, []string{"--json", "--all"}, nil, -1)
		if err != nil {
			return err
		}
		if err := applyOptions(p, o); err != nil {
			return err
		}
		return a.ls(o, p.bools["--all"], p.bools["--json"])
	case "whoami", "name":
		return a.selfCmd(cmd, rest, o)
	case "which", "read", "capture":
		return a.lookCmd(cmd, rest, o)
	case "msg", "send":
		p, err := parseArgs(rest, []string{"--force", "--clear-first"}, []string{"--timeout"}, 1)
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
		pane, err := ops.Pick(p.pos[0])
		if err != nil {
			return err
		}
		if _, err := ops.Msg(pane, text, harness.MsgOpts{Force: p.bools["--force"], ClearFirst: p.bools["--clear-first"], Timeout: timeout}); err != nil {
			return err
		}
		a.sent(pane)
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
		p, err := parseArgs(rest, nil, []string{"--where", "--cwd", "--prompt", "--prompt-file", "--label", "--command", "--timeout"}, -1)
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
			where = "tab"
		}
		b, err := a.OpenBackend(o, "")
		if err != nil {
			return err
		}
		tasks, err := a.openTasks(o, b)
		if err != nil {
			return err
		}
		pane, err := tasks.Spawn(p.pos[0], where, cwdOr(p.vals["--cwd"]), harness.SpawnOpts{Prompt: p.vals["--prompt"], PromptFile: p.vals["--prompt-file"], Label: p.vals["--label"], Command: p.vals["--command"], Timeout: timeout})
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Out, pane.ID)
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
		return nil
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
		if err != nil {
			return err
		}
		joined := strings.Join(keys, "+")
		fmt.Fprintf(a.Out, "sent %s to %s (%s)\n", joined, term.Cut(pane.Address(), 8), harness.HarnessName(pane))
		if !verified {
			fmt.Fprintf(a.Err, "harness-ops: warning: %s has not been verified to stop %s; check with read\n", joined, harness.HarnessName(pane))
		}
		return nil
	}
	return term.UsageErr("unknown command '%s'", cmd)
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

func (a *App) ls(o *Options, all, asJSON bool) error {
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
		fillStatus(a.newOps(b, nil), found)
		panes = append(panes, found...)
	}
	if asJSON {
		rows := []row{}
		for _, p := range panes {
			rows = append(rows, row{p.Address(), p.ID, p.TTY, p.Title, p.Label, harness.HarnessName(p), p.Folder, p.IsSelf, p.Status})
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
	me, err := b.SelfPane()
	if err != nil {
		return err
	}
	if me == nil {
		return term.UsageErr("cannot tell which pane this runs in")
	}
	if cmd == "whoami" {
		fillStatus(a.newOps(b, nil), []*term.Pane{me})
		a.printTable([]*term.Pane{me})
		return nil
	}
	if err := b.SetLabel(me, p.pos[0]); err != nil {
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
		return term.UsageErr("usage: task new|ls|show|send|wait|done|close")
	}
	cmd, rest := rest[0], rest[1:]
	bools, vals, restAfter := []string{}, []string{}, -1
	switch cmd {
	case "new":
		vals = []string{"--title", "--brief", "--brief-file", "--cwd", "--where", "--reuse", "--command", "--max", "--timeout"}
	case "ls":
		bools = []string{"--all", "--json"}
	case "show":
		bools = []string{"--json"}
	case "send":
		bools, restAfter = []string{"--force"}, 1
	case "wait":
		bools, vals = []string{"--any"}, []string{"--timeout"}
	case "done":
		vals = []string{"--status", "--summary", "--summary-file"}
	case "close":
		bools = []string{"--force"}
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
		t, err := tasks.New(harness.NewOpts{Harness: p.pos[0], Title: p.vals["--title"], Brief: p.vals["--brief"], BriefFile: p.vals["--brief-file"],
			Cwd: cwdOr(p.vals["--cwd"]), Where: p.vals["--where"], Reuse: p.vals["--reuse"], Command: p.vals["--command"], MaxTasks: maxTasks, Timeout: timeout})
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Out, t.ID)
	case "ls":
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
		t, err := tasks.Get(id)
		if err != nil {
			return err
		}
		if p.bools["--json"] {
			data, _ := json.MarshalIndent(t, "", " ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		for _, kv := range [][2]string{{"id", t.ID}, {"status", t.Status}, {"title", t.Title}, {"harness", t.Harness}, {"pane", t.Pane}, {"owner", t.Owner},
			{"cwd", t.Cwd}, {"brief", t.Brief}, {"depth", fmt.Sprint(t.Depth)}, {"notify", t.Notify}, {"summary", t.Summary}, {"started", when(t.Started)}, {"ended", when(t.Ended)}} {
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
			return err
		}
		fmt.Fprintf(a.Out, "sent to task %s\n", id)
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
		t, err := tasks.Wait(id, p.bools["--any"], timeout)
		if err != nil {
			return err
		}
		a.printTasks([]*harness.Task{t})
	case "done":
		id, err := arg()
		if err != nil {
			return err
		}
		t, err := tasks.Done(id, p.vals["--status"], p.vals["--summary"], p.vals["--summary-file"])
		if err != nil {
			return err
		}
		note := map[string]string{"delivered": "owner told", "pending": "owner busy: notification pending", "owner-gone": "owner pane is gone"}[t.Notify]
		if note == "" {
			note = t.Notify
		}
		fmt.Fprintf(a.Out, "task %s: %s (%s)\n", t.ID, t.Status, note)
	case "close":
		id, err := arg()
		if err != nil {
			return err
		}
		t, err := tasks.Close(id, p.bools["--force"])
		if err != nil {
			return err
		}
		suffix := ""
		if !t.Owned {
			suffix = " (pane left open: it was not created for this task)"
		}
		fmt.Fprintf(a.Out, "closed task %s%s\n", t.ID, suffix)
	}
	return nil
}
