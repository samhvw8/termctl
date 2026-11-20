package harness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/samhvw8/termctl/internal/term"
)

const (
	MaxInline      = 800 // longer messages, or multi-line ones, go through a file
	enterDelay     = 0.4 // TUIs drop an Enter that arrives with the pasted text
	settle         = 0.5 // let the screen react to what we sent before judging it
	submitSettle   = 0.7 // how long a TUI gets to take a submitted line off its input
	submitRetries  = 3
	submitConfirm  = 1.0 // the first empty input line must look empty again this much later, unless the harness went busy
	startStable    = 3.0 // a starting harness must hold a stable screen this long: late startup banners redraw it
	interruptLook  = 2.0 // how long a stopped harness gets to put a queued message back on its input line
	poll           = 0.25
	DefaultTimeout = 120.0
	captureLines   = 200
)

// Clock is wall time and sleeping, in seconds.
type Clock interface {
	Now() float64
	Sleep(seconds float64)
}

// RealClock is the system clock.
type RealClock struct{}

func (RealClock) Now() float64    { return float64(time.Now().UnixNano()) / 1e9 }
func (RealClock) Sleep(s float64) { time.Sleep(time.Duration(s * float64(time.Second))) }

// Warn prints a non-fatal problem.
var Warn = func(msg string) { fmt.Fprintf(os.Stderr, "harness-ops: warning: %s\n", msg) }

// HarnessName is the profile name, or the program's name when no profile fits.
func HarnessName(p *term.Pane) string {
	if prof := ForProcess(p.Process); prof.Name != "generic" {
		return prof.Name
	}
	if p.Process != "" {
		return p.Process
	}
	return "-"
}

// Ops talks to harness panes through one backend, and records what it does in the journal.
type Ops struct {
	Backend term.Backend
	Sender  *term.Pane
	Clock   Clock
	Journal *Journal
	Task    string // the task these actions belong to, for the journal; "" for none
	Project string // the project these actions belong to; "" for the sender's folder
	From    string // who the journal names as sender when Sender is nil (a report delivered for a worker)
	Scope   string // the scope these actions belong to, for the journal; "" for none
}

// NewOps keeps its journal and files under root: the state directory when root is "".
func NewOps(b term.Backend, sender *term.Pane, clock Clock, root string) *Ops {
	if clock == nil {
		clock = RealClock{}
	}
	if root == "" {
		root = DefaultRoot()
	}
	return &Ops{Backend: b, Sender: sender, Clock: clock, Journal: &Journal{Root: root, Clock: clock}}
}

// entry starts a journal entry for an action of this sender on p.
func (o *Ops) entry(event string, p *term.Pane) Entry {
	project := o.Project
	if project == "" {
		project = ProjectOf(o.Sender)
	}
	e := Entry{Event: event, Project: project, From: address(o.Sender), FromName: entryName(o.Sender),
		To: address(p), ToName: entryName(p), Task: o.Task, Scope: o.Scope}
	if o.Sender == nil {
		e.From = o.From
	}
	if p != nil {
		e.Harness = HarnessName(p)
	}
	return e
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Pick resolves a target this caller may act on. Your own pane is refused.
func (o *Ops) Pick(target string) (*term.Pane, error) {
	panes, err := o.Backend.ListPanes()
	if err != nil {
		return nil, err
	}
	p, err := term.Resolve(panes, target)
	if err != nil {
		return nil, err
	}
	if o.Sender != nil && p.ID == o.Sender.ID {
		return nil, term.TargetErr("refusing to send to your own pane")
	}
	return p, nil
}

// screenLines is how much to read of a pane to judge its screen. A backend that knows the height is
// asked for no more than the screen: reading history can scroll a full-screen app.
func screenLines(p *term.Pane) int {
	if p.Rows > 0 && p.Rows < captureLines {
		return p.Rows
	}
	return captureLines
}

func (o *Ops) draft(p *term.Pane, prof *Profile) (*string, error) {
	lines, err := o.Backend.Capture(p, screenLines(p))
	if err != nil {
		return nil, err
	}
	return prof.ReadInput(lines), nil
}

// CheckDraft refuses when the input line holds text, or a calibrated harness's line cannot be read.
func (o *Ops) CheckDraft(p *term.Pane, prof *Profile, force bool) error {
	d, err := o.draft(p, prof)
	if err != nil {
		return err
	}
	if d == nil {
		if prof.Calibrated && !force {
			return term.Errf(term.CodeDraft, "cannot see %s's input line on %s; use --force to send anyway", prof.Name, p.Address())
		}
		Warn(fmt.Sprintf("cannot read the input line of %s (%s); a draft there would be joined", p.Address(), prof.Name))
		return nil
	}
	if *d != "" && !force {
		return term.Errf(term.CodeDraft, "%s has a draft on its input line: %q; use --force to send anyway", p.Address(), term.Cut(*d, 80))
	}
	return nil
}

// assertAlone: a slash command must be the whole line. --force wipes a draft rather than joining it.
func (o *Ops) assertAlone(p *term.Pane, prof *Profile, force bool) error {
	if err := o.CheckDraft(p, prof, force); err != nil || !force {
		return err
	}
	d, err := o.draft(p, prof)
	if err != nil {
		return err
	}
	if d != nil && *d != "" {
		if err := o.Backend.SendKey(p, "ctrl-u"); err != nil {
			return err
		}
		o.Clock.Sleep(enterDelay)
		d, err = o.draft(p, prof)
		if err != nil {
			return err
		}
		if d != nil && *d != "" {
			return term.Errf(term.CodeDraft, "--force could not empty the input line of %s", p.Address())
		}
	}
	return nil
}

// Submit types one line and presses Enter. A TUI can drop an Enter that arrives while long text
// is still being taken in, so it checks that the text left the input line and presses Enter again.
func (o *Ops) Submit(p *term.Pane, line string, prof *Profile) error {
	_, err := o.submit(p, line, prof, "", false)
	return err
}

// SubmitFirst is Submit for the first line into a harness that has just started. Such a harness can
// take the typed text in late and lose the Enter sent before it (Claude Code, 2026-11-19), so the line
// counts as submitted only when the harness went busy, or the input line is still empty submitConfirm later.
func (o *Ops) SubmitFirst(p *term.Pane, line string, prof *Profile) error {
	_, err := o.submit(p, line, prof, "", true)
	return err
}

// caller names the sender to a backend that can show it, as <backend>/<id>. Only agent messages carry one:
// a tag around a slash command would stop it working.
func (o *Ops) caller() string {
	if o.Sender != nil {
		return o.Sender.Backend + "/" + term.Cut(o.Sender.Native, 8)
	}
	return "termctl/agent-ops"
}

// errCode is an error's exit code, or 0 when it carries none.
func errCode(err error) int {
	var te *term.Error
	if errors.As(err, &te) {
		return te.Code
	}
	return 0
}

// submit reports typed: the line reached the pane, so an error after it is no refusal.
func (o *Ops) submit(p *term.Pane, line string, prof *Profile, from string, starting bool) (bool, error) {
	if prof == nil {
		prof = ForProcess(p.Process)
	}
	if sub, ok := o.Backend.(term.Submitter); ok {
		if err := term.CheckText(line); err != nil {
			return false, err
		}
		if err := sub.SubmitText(p, line, from); err != nil {
			return false, err
		}
	} else {
		if err := o.Backend.SendText(p, line); err != nil {
			return false, err
		}
		o.Clock.Sleep(enterDelay)
		if err := o.Backend.SendKey(p, "enter"); err != nil {
			return true, err
		}
	}
	// For a starting harness one empty look is not proof (SubmitFirst). The first empty look counts when
	// the harness went busy, or the line is still empty submitConfirm later; after that, at once.
	enters, empty, confirmed := 0, false, !starting
	for {
		if empty {
			o.Clock.Sleep(submitConfirm)
		} else {
			o.Clock.Sleep(submitSettle)
		}
		lines, err := o.Backend.Capture(p, screenLines(p))
		if err != nil {
			return true, err
		}
		d := prof.ReadInput(lines)
		if d == nil && !confirmed && !prof.IsBusy(lines) {
			empty, confirmed = true, true // a starting harness may be redrawing: look again before giving up
			continue
		}
		if d == nil {
			Warn(fmt.Sprintf("cannot read the input line of %s (%s) after typing; the submit is not confirmed, check with read", p.Address(), prof.Name))
			return true, nil
		}
		if *d == "" {
			if empty || confirmed || prof.IsBusy(lines) {
				return true, nil
			}
			empty, confirmed = true, true
			continue
		}
		empty = false
		if enters == submitRetries {
			// Not exit 4: the text was typed, so this is no refusal. A busy harness can show a queued message
			// on its input line and still deliver it (2026-11-19: a lead resent three times on exit 4).
			return true, term.Errf(term.CodeTimeout, "%s: the text stayed on the input line after %d Enters, so the submit is not confirmed: %q. "+
				"It was typed and may still be delivered (a busy harness can hold a queued message there): read the pane, and do not send it again",
				p.Address(), enters+1, term.Cut(line, 80))
		}
		enters++
		if err := o.Backend.SendKey(p, "enter"); err != nil {
			return true, err
		}
	}
}

// Interrupt stops the harness's current turn; it returns the keys sent and whether they are known to work.
// A stopped harness can put a queued message back on its input line, unsent. That is refused (exit 4)
// with the text, so a caller does not take the text on screen for a turn in progress.
func (o *Ops) Interrupt(p *term.Pane, key string) ([]string, bool, error) {
	prof := ForProcess(p.Process)
	keys, verified := prof.InterruptKeys, prof.InterruptVerified
	if key != "" {
		keys, verified = []string{key}, true
	}
	before, err := o.draft(p, prof)
	if err != nil {
		return nil, false, err
	}
	for _, k := range keys {
		if err := o.Backend.SendKey(p, k); err != nil {
			return nil, false, err
		}
	}
	e := o.entry("interrupt", p)
	e.Text = strings.Join(keys, "+")
	o.Journal.Record(e)
	var after *string
	for waited := 0.0; waited < interruptLook; waited += poll {
		o.Clock.Sleep(poll)
		if after, err = o.draft(p, prof); err != nil {
			return keys, verified, err
		}
		if after != nil && *after != "" && (before == nil || *after != *before) {
			return keys, verified, term.Errf(term.CodeDraft, "%s stopped, but its input line now holds text that was not sent: %q. "+
				"A harness puts queued messages back there when its turn is interrupted", p.Address(), ends(strings.Join(strings.Fields(*after), " "), 60, 100))
		}
	}
	switch {
	case after == nil:
		Warn(fmt.Sprintf("cannot read the input line of %s (%s) after the interrupt; a queued message may be back on it, unsent; check with read", p.Address(), prof.Name))
	case *after != "":
		Warn(fmt.Sprintf("the input line of %s still holds a draft from before the interrupt: %q", p.Address(), term.Cut(*after, 80)))
	}
	return keys, verified, nil
}

// ends keeps the start and the end of a long text: a message's tag comes first and its point last.
func ends(s string, head, tail int) string {
	r := []rune(s)
	if len(r) <= head+tail {
		return s
	}
	return string(r[:head]) + " … " + string(r[len(r)-tail:])
}

// nativeBusy reads a terminal's own status: a working or blocked harness is not ready for input.
// blocked is hc's word for a program waiting on a person; its prompt can look idle on screen.
func nativeBusy(native string) bool {
	return strings.HasPrefix(native, "working") || strings.HasPrefix(native, "blocked")
}

// WaitIdle returns when the harness is idle: not reported working or blocked, no busy pattern, input line visible, screen stable.
// For a profile without a calibrated input reader, the terminal's own "idle", held for the profile's stable
// time, stands in for the input line and the stable screen: such a harness may draw an input line the
// generic reader does not know, or a clock that never stops.
func (o *Ops) WaitIdle(p *term.Pane, timeout float64, prof *Profile) error {
	return o.waitIdle(p, timeout, prof, false)
}

// startAsk is how long a prompt must stay on a starting harness before the wait gives up on it.
const startAsk = 2.0

// trustPrompt is the folder-trust question Claude Code and agy ask before their first start in a folder.
var trustPrompt = regexp.MustCompile(`(?i)\btrust (this folder|the files in this folder)\b`)

// startupPrompt names what a starting harness waits on, or "": the terminal's "waiting" report (iTerm2, hc)
// while the profile finds no input line on screen, else the trust question on the screen (tmux and Rex report
// no status). A native "waiting" beside a visible input line is no prompt: on 2026-11-19 iTerm2 reported agy
// waiting ("Sign in required") for over 2 s while its input line was already drawn, and task new failed.
func startupPrompt(native string, lines []string, prof *Profile) string {
	if strings.HasPrefix(native, "waiting") && (prof.ReadInput == nil || prof.ReadInput(lines) == nil) {
		if d := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(native, "waiting"), ":")); d != "" {
			return d
		}
		return "waiting"
	}
	tail := lines
	if len(tail) > 12 {
		tail = tail[len(tail)-12:]
	}
	for _, ln := range tail {
		if trustPrompt.MatchString(ln) {
			return "trust this folder"
		}
	}
	return ""
}

// waitIdle is WaitIdle; starting adds the startup rule: a harness that asks something (a trust prompt) for
// startAsk seconds never gets idle by itself, so the wait ends at once instead of at its timeout.
func (o *Ops) waitIdle(p *term.Pane, timeout float64, prof *Profile, starting bool) error {
	if prof == nil {
		prof = ForProcess(p.Process)
	}
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	deadline := o.Clock.Now() + timeout
	o.Clock.Sleep(settle)
	var last []string
	first, since := true, o.Clock.Now()
	idleSince, askSince := -1.0, -1.0
	for {
		lines, err := o.Backend.Capture(p, screenLines(p))
		if err != nil {
			return err
		}
		now := o.Clock.Now()
		view := prof.StableView(lines)
		if first || !reflect.DeepEqual(view, last) {
			first, last, since = false, view, now
		}
		native := o.Backend.NativeStatus(p)
		if !strings.HasPrefix(native, "idle") {
			idleSince = -1
		} else if idleSince < 0 {
			idleSince = now
		}
		if ask := startupPrompt(native, lines, prof); starting && ask != "" {
			if askSince < 0 {
				askSince = now
			}
			if now-askSince >= startAsk {
				return term.Errf(term.CodeTimeout, "%s waits on a prompt at startup (%s) and cannot get idle by itself. "+
					"It is the user's decision: ask the user to answer it in that pane, then run task new --reuse %s "+
					"(or msg the pane, if you started it with spawn). Screen:%s", p.Address(), ask, p.Address(), screenTail(lines))
			}
			o.Clock.Sleep(poll)
			continue
		}
		askSince = -1
		need := prof.StableSeconds
		if starting {
			need = max(need, startStable)
		}
		nativeIdle := !prof.Calibrated && idleSince >= 0 && now-idleSince >= need
		promptOK := !prof.PromptRequired || prof.ReadInput(lines) != nil || nativeIdle
		stable := now-since >= need || nativeIdle
		if !nativeBusy(native) && promptOK && !prof.IsBusy(lines) && stable {
			return nil
		}
		if now >= deadline {
			return term.Errf(term.CodeTimeout, "%s still not idle after %gs (native: %s). Screen:%s", p.Address(), timeout, native, screenTail(lines))
		}
		o.Clock.Sleep(poll)
	}
}

// screenTail is the last lines of a screen for an error message.
func screenTail(lines []string) string {
	tail := lines
	if len(tail) > 6 {
		tail = tail[len(tail)-6:]
	}
	var b strings.Builder
	for _, ln := range tail {
		fmt.Fprintf(&b, "\n  | %s", term.Cut(ln, 100))
	}
	return b.String()
}

var slashRe = regexp.MustCompile(`^/\S+( [^\n\r]*)?$`)

// Slash sends a slash command alone on its line.
func (o *Ops) Slash(p *term.Pane, command string, force, wait bool, timeout float64) error {
	command = strings.TrimSpace(command)
	if !slashRe.MatchString(command) {
		return term.UsageErr("a slash command is one line starting with '/': got %q", command)
	}
	prof := ForProcess(p.Process)
	if !prof.Slash {
		return term.UsageErr("%s has no slash commands", prof.Name)
	}
	if err := o.assertAlone(p, prof, force); err != nil {
		return err
	}
	err := o.Submit(p, command, prof)
	e := o.entry("slash", p)
	e.Text, e.Error = command, errText(err)
	o.Journal.Record(e)
	if err != nil {
		return err
	}
	if wait {
		return o.WaitIdle(p, timeout, prof)
	}
	return nil
}

// Clear sends /clear and waits until the harness is idle.
func (o *Ops) Clear(p *term.Pane, force bool, timeout float64) error {
	return o.Slash(p, "/clear", force, true, timeout)
}

// Tag builds the sender tag of a message.
func (o *Ops) Tag() (string, error) {
	me := o.Sender
	if me == nil {
		return "", term.UsageErr("cannot tell which pane this runs in; run inside a terminal pane (backend env vars not set)")
	}
	harness := HarnessName(me)
	name := me.Label
	if name == "" {
		name = me.Title
	}
	if name == "" {
		name = harness
	}
	return fmt.Sprintf("[agent-ops from %s (%s) id=%s folder=%s]", name, harness, me.Address(), me.Folder), nil
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// writeFile puts a long text for pane to read into the sender's project folder of the journal.
func (o *Ops) writeFile(kind, body string, to *term.Pane) (string, error) {
	me := "spawn"
	if o.Sender != nil {
		me = term.Cut(o.Sender.Address(), 8)
	}
	return o.Journal.WriteFile(o.entry("", nil).Project, kind, me+"-to-"+term.Cut(to.Address(), 8), body)
}

// AssertCleared: leftover text means /clear never ran; typing now would rebuild the joined line.
func (o *Ops) AssertCleared(p *term.Pane, prof *Profile) error {
	left, err := o.draft(p, prof)
	if err != nil {
		return err
	}
	if left != nil && *left != "" {
		return term.Errf(term.CodeDraft, "input line of %s still holds %q after /clear; message not sent", p.Address(), term.Cut(*left, 80))
	}
	return nil
}

// MsgOpts are options of Msg. Tag overrides the sender tag (for held-back task results).
type MsgOpts struct {
	Force, ClearFirst bool
	Timeout           float64
	Tag               string
	Retry             bool // a failure will be tried again: the journal records only the delivery
	IdleOnly          bool // refuse a busy harness with ErrBusy, nothing typed (notices)
	// Key is the message's idempotency key (MsgKey): the same key in a msg entry of the last keyWindow
	// refuses the send (exit 6) unless Again. "" for none: notices and task send are not keyed.
	Key    string
	Again  bool
	ID     string // a deferred message's ID, kept in its journal entry
	Suffix string // added to the typed line after the text (or the file it points to); not journaled
}

// keyWindow is how long a msg's idempotency key refuses the same message to the same pane.
const keyWindow = 600

// ErrBusy is Msg's refusal of a busy harness under IdleOnly: nothing was typed.
var ErrBusy = errors.New("the harness is busy; nothing typed")

// Sent is what Msg typed. Busy: the harness was working, so it may hold the message until its turn ends.
type Sent struct {
	Line  string
	Busy  bool
	Typed bool // the line was typed: an error after it is no refusal, and a retry could deliver it twice
}

// Msg sends a tagged message.
func (o *Ops) Msg(p *term.Pane, text string, opts MsgOpts) (Sent, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Sent{}, term.UsageErr("empty message")
	}
	tag := opts.Tag
	if tag == "" {
		var err error
		if tag, err = o.Tag(); err != nil {
			return Sent{}, err
		}
	}
	if opts.Key != "" && !opts.Again {
		now := int64(o.Clock.Now())
		if prev, err := o.Journal.FindKey(opts.Key, now-keyWindow); err != nil {
			return Sent{}, err
		} else if prev != nil {
			return Sent{}, term.Errf(term.CodeGuard, "the same message went to %s at %s (%ds ago); not sent again. Pass --again to send it anyway",
				p.Address(), time.Unix(prev.T, 0).Format("15:04:05"), now-prev.T)
		}
	}
	prof := ForProcess(p.Process)
	if opts.ClearFirst {
		if err := o.Clear(p, opts.Force, opts.Timeout); err != nil {
			if c := errCode(err); c != 0 {
				return Sent{}, term.Errf(c, "%v (that was /clear; the message itself was not typed)", err)
			}
			return Sent{}, err
		}
		if err := o.AssertCleared(p, prof); err != nil { // even with --force
			return Sent{}, err
		}
	} else if err := o.CheckDraft(p, prof, opts.Force); err != nil {
		return Sent{}, err
	}
	busy, err := o.busy(p, prof)
	if err != nil {
		return Sent{}, err
	}
	if busy && opts.IdleOnly {
		return Sent{Busy: true}, ErrBusy
	}
	var line, file string
	if strings.Contains(text, "\n") || len([]rune(text)) > MaxInline {
		if file, err = o.writeFile("msg", tag+"\n\n"+text+"\n", p); err != nil {
			return Sent{}, err
		}
		line = tag + " Long message; read the file " + file
	} else {
		line = tag + " " + text
	}
	line = Flat(line + opts.Suffix)
	typed, err := o.submit(p, line, prof, o.caller(), false)
	e := o.entry("msg", p)
	e.Text, e.File, e.Error, e.ID = text, file, errText(err), opts.ID
	if typed {
		e.Key = opts.Key // an error before typing sent nothing: the same message may go again
	}
	if file != "" {
		e.Text = preview(text)
	}
	if busy {
		e.Status = "queued"
	}
	unconfirmed := typed && errCode(err) == term.CodeTimeout
	if unconfirmed {
		e.Status = "unconfirmed"
	}
	if err == nil || !opts.Retry || unconfirmed { // a retried delivery records only its last try
		o.Journal.Record(e)
	}
	return Sent{line, busy, typed}, err
}

// busy is one look: the terminal reports the harness working or blocked, or a busy pattern is on screen.
func (o *Ops) busy(p *term.Pane, prof *Profile) (bool, error) {
	if nativeBusy(o.Backend.NativeStatus(p)) {
		return true, nil
	}
	lines, err := o.Backend.Capture(p, screenLines(p))
	if err != nil {
		return false, err
	}
	return prof.IsBusy(lines), nil
}

// SurvivingCommand runs cmd and leaves a login shell in the pane afterwards, so a crash leaves
// its error on screen. hook, if set, is told the exit status; its own failure must not cost the shell.
func SurvivingCommand(cmd, hook string) string {
	tail := "s=$?; "
	if hook != "" {
		tail += hook + ` --code "$s" >/dev/null 2>&1; `
	}
	tail += `printf '\n[termctl: harness exited with status %s; this shell stays open]\n' "$s"; exec "${SHELL:-/bin/sh}" -l`
	return cmd + "; " + tail
}

// Flat removes what would end or corrupt a typed line.
func Flat(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r", " "), "\x1b", "")
}

// SpawnOpts are options of Spawn. OnCreated runs before the wait, so a harness that never gets ready stays closable.
type SpawnOpts struct {
	Prompt, PromptFile, Label, Command, Title string
	Timeout                                   float64
	OnCreated                                 func(*term.Pane) error
	Wrap                                      func(string) string   // final form of the command; nil leaves it as is
	Owned                                     func(*term.Pane) bool // panes the caller started, for --where auto; may be nil
}

// hcEnvReset removes what a Herdr core started from an agent session passes on to everything it runs.
// CLAUDE_CODE_CHILD_SESSION is the minimal set, checked on real Claude Code: inherited, it prints
// "Transcript saving is off"; with this one variable unset, the warning is gone.
const hcEnvReset = "unset CLAUDE_CODE_CHILD_SESSION; "

// Spawn starts a harness, waits until ready, and submits the first prompt.
func (o *Ops) Spawn(harness, where, cwd string, opts SpawnOpts) (*term.Pane, error) {
	if opts.Prompt != "" && opts.PromptFile != "" {
		return nil, term.UsageErr("use --prompt or --prompt-file, not both")
	}
	prof, known := Profiles[harness]
	if !known {
		if opts.Command == "" {
			return nil, term.UsageErr("unknown harness '%s'. Known: claude, omp, opencode, codex, gemini, agy, aider, pi, generic; or pass --command", harness)
		}
		prof = Profiles["generic"]
	}
	cmd := opts.Command
	if cmd == "" {
		cmd = prof.Command
	}
	if cmd == "" {
		return nil, term.UsageErr("harness '%s' has no default command; pass --command", harness)
	}
	if opts.Wrap != nil {
		cmd = opts.Wrap(cmd)
	}
	if o.Backend.Name() == "hc" {
		cmd = hcEnvReset + cmd
	}
	prompt := opts.Prompt
	if opts.PromptFile != "" {
		abs, err := filepath.Abs(ExpandHome(opts.PromptFile))
		if err != nil {
			return nil, term.UsageErr("--prompt-file: %v", err)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, term.UsageErr("--prompt-file: no such file: %s", abs)
		}
		prompt, opts.PromptFile = string(data), abs
	}
	title := opts.Title
	if title == "" {
		title = opts.Label
	}
	var at *term.Pane
	if where == "auto" {
		where, at = term.PlaceFor(o.Backend, where, opts.Owned)
	}
	var p *term.Pane
	var err error
	if a, ok := o.Backend.(term.Anchored); ok && at != nil {
		p, err = a.SpawnAt(at, where, cmd, cwd, title)
	} else {
		p, err = o.Backend.Spawn(where, cmd, cwd, title)
	}
	if err != nil {
		return nil, err
	}
	if opts.OnCreated != nil {
		if err := opts.OnCreated(p); err != nil {
			return p, err
		}
	}
	if opts.Label != "" {
		if err := o.Backend.SetLabel(p, opts.Label); err != nil {
			return p, err
		}
	}
	e := o.entry("spawn", p)
	e.Harness = harness
	defer func() { o.Journal.Record(e) }()
	if err := o.waitIdle(p, opts.Timeout, prof, true); err != nil {
		e.Error = err.Error()
		return p, err
	}
	if strings.TrimSpace(prompt) != "" {
		line, file, err := o.firstLine(prompt, opts.PromptFile, p)
		e.Text, e.File = preview(prompt), file
		if file == "" {
			e.Text = strings.TrimSpace(prompt)
		}
		if err != nil {
			e.Error = err.Error()
			return p, err
		}
		if err := o.SubmitFirst(p, line, prof); err != nil {
			e.Error = err.Error()
			return p, err
		}
	}
	return p, nil
}

// firstLine is the line that starts a spawned harness, and the file holding the prompt, if any.
func (o *Ops) firstLine(prompt, promptFile string, p *term.Pane) (string, string, error) {
	text := strings.TrimSpace(prompt)
	if !strings.Contains(text, "\n") && len([]rune(text)) <= MaxInline {
		return Flat(text), promptFile, nil
	}
	path := promptFile
	if path == "" {
		f, err := o.writeFile("prompt", text+"\n", p)
		if err != nil {
			return "", "", err
		}
		path = f
	}
	return "Read the file " + path + " and follow its instructions.", path, nil
}

// Status is one-shot status for `ls`: the terminal's own report, else a single screen sample.
func (o *Ops) Status(p *term.Pane, native string) string {
	if native != "" {
		return native
	}
	prof := ForProcess(p.Process)
	if prof.Name == "generic" {
		return "?"
	}
	lines, err := o.Backend.Capture(p, screenLines(p))
	if err != nil {
		return "?"
	}
	if prof.IsBusy(lines) {
		return "working"
	}
	if prof.ReadInput(lines) != nil && prof.PromptRequired {
		return "idle"
	}
	return "?"
}

// ExpandHome turns a leading ~ (alone or as ~/...) into the home folder. Backends show folders with
// term.Tilde, so a pane in home has the folder "~".
func ExpandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[1:])
		}
	}
	return path
}
