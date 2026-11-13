package harness

import (
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

func DefaultInbox() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "agent-ops")
}

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

// Ops talks to harness panes through one backend.
type Ops struct {
	Backend term.Backend
	Sender  *term.Pane
	Clock   Clock
	Inbox   string
}

func NewOps(b term.Backend, sender *term.Pane, clock Clock, inbox string) *Ops {
	if clock == nil {
		clock = RealClock{}
	}
	if inbox == "" {
		inbox = DefaultInbox()
	}
	return &Ops{b, sender, clock, inbox}
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
	return o.submit(p, line, prof, "")
}

// caller names the sender to a backend that can show it, as <backend>/<id>. Only agent messages carry one:
// a tag around a slash command would stop it working.
func (o *Ops) caller() string {
	if o.Sender != nil {
		return o.Sender.Backend + "/" + term.Cut(o.Sender.Native, 8)
	}
	return "termctl/agent-ops"
}

func (o *Ops) submit(p *term.Pane, line string, prof *Profile, from string) error {
	if prof == nil {
		prof = ForProcess(p.Process)
	}
	if sub, ok := o.Backend.(term.Submitter); ok {
		if err := term.CheckText(line); err != nil {
			return err
		}
		if err := sub.SubmitText(p, line, from); err != nil {
			return err
		}
	} else {
		if err := o.Backend.SendText(p, line); err != nil {
			return err
		}
		o.Clock.Sleep(enterDelay)
		if err := o.Backend.SendKey(p, "enter"); err != nil {
			return err
		}
	}
	for try := 0; ; try++ {
		o.Clock.Sleep(submitSettle)
		d, err := o.draft(p, prof)
		if err != nil {
			return err
		}
		if d == nil || *d == "" {
			return nil
		}
		if try == submitRetries {
			return term.Errf(term.CodeDraft, "%s: %q was typed but not submitted; it is still on the input line", p.Address(), term.Cut(line, 80))
		}
		if err := o.Backend.SendKey(p, "enter"); err != nil {
			return err
		}
	}
}

// Interrupt stops the harness's current turn; it returns the keys sent and whether they are known to work.
func (o *Ops) Interrupt(p *term.Pane, key string) ([]string, bool, error) {
	prof := ForProcess(p.Process)
	keys, verified := prof.InterruptKeys, prof.InterruptVerified
	if key != "" {
		keys, verified = []string{key}, true
	}
	for _, k := range keys {
		if err := o.Backend.SendKey(p, k); err != nil {
			return nil, false, err
		}
	}
	return keys, verified, nil
}

// nativeBusy reads a terminal's own status: a working or blocked harness is not ready for input.
// blocked is hc's word for a program waiting on a person; its prompt can look idle on screen.
func nativeBusy(native string) bool {
	return strings.HasPrefix(native, "working") || strings.HasPrefix(native, "blocked")
}

// WaitIdle returns when the harness is idle: not reported working or blocked, no busy pattern, input line visible, screen stable.
func (o *Ops) WaitIdle(p *term.Pane, timeout float64, prof *Profile) error {
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
	for {
		lines, err := o.Backend.Capture(p, screenLines(p))
		if err != nil {
			return err
		}
		view := prof.StableView(lines)
		if first || !reflect.DeepEqual(view, last) {
			first, last, since = false, view, o.Clock.Now()
		}
		native := o.Backend.NativeStatus(p)
		promptOK := !prof.PromptRequired || prof.ReadInput(lines) != nil
		if !nativeBusy(native) && promptOK && !prof.IsBusy(lines) && o.Clock.Now()-since >= prof.StableSeconds {
			return nil
		}
		if o.Clock.Now() >= deadline {
			tail := lines
			if len(tail) > 6 {
				tail = tail[len(tail)-6:]
			}
			var b strings.Builder
			for _, ln := range tail {
				fmt.Fprintf(&b, "\n  | %s", term.Cut(ln, 100))
			}
			return term.Errf(term.CodeTimeout, "%s still not idle after %gs (native: %s). Screen:%s", p.Address(), timeout, native, b.String())
		}
		o.Clock.Sleep(poll)
	}
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
	if err := o.Submit(p, command, prof); err != nil {
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

func (o *Ops) writeInbox(body string, to *term.Pane) (string, error) {
	if err := os.MkdirAll(o.Inbox, 0o755); err != nil {
		return "", term.BackendErr("inbox: %v", err)
	}
	me := "spawn"
	if o.Sender != nil {
		me = unsafeName.ReplaceAllString(term.Cut(o.Sender.Address(), 8), "_")
	}
	toID := unsafeName.ReplaceAllString(term.Cut(to.Address(), 8), "_")
	name := fmt.Sprintf("%s-%s-to-%s.md", time.Now().Format("20060102-150405"), me, toID)
	f := filepath.Join(o.Inbox, name)
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		return "", term.BackendErr("inbox: %v", err)
	}
	return f, nil
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
}

// Msg sends a tagged message and returns the typed line.
func (o *Ops) Msg(p *term.Pane, text string, opts MsgOpts) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", term.UsageErr("empty message")
	}
	tag := opts.Tag
	if tag == "" {
		var err error
		if tag, err = o.Tag(); err != nil {
			return "", err
		}
	}
	prof := ForProcess(p.Process)
	if opts.ClearFirst {
		if err := o.Clear(p, opts.Force, opts.Timeout); err != nil {
			return "", err
		}
		if err := o.AssertCleared(p, prof); err != nil { // even with --force
			return "", err
		}
	} else if err := o.CheckDraft(p, prof, opts.Force); err != nil {
		return "", err
	}
	var line string
	if strings.Contains(text, "\n") || len([]rune(text)) > MaxInline {
		f, err := o.writeInbox(tag+"\n\n"+text+"\n", p)
		if err != nil {
			return "", err
		}
		line = tag + " Long message; read the file " + f
	} else {
		line = tag + " " + text
	}
	line = Flat(line)
	return line, o.submit(p, line, prof, o.caller())
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
			return nil, term.UsageErr("unknown harness '%s'. Known: claude, omp, opencode, codex, gemini, aider, pi, generic; or pass --command", harness)
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
		abs, err := filepath.Abs(expandHome(opts.PromptFile))
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
	if err := o.WaitIdle(p, opts.Timeout, prof); err != nil {
		return p, err
	}
	if strings.TrimSpace(prompt) != "" {
		line, err := o.firstLine(prompt, opts.PromptFile, p)
		if err != nil {
			return p, err
		}
		if err := o.Submit(p, line, prof); err != nil {
			return p, err
		}
	}
	return p, nil
}

func (o *Ops) firstLine(prompt, promptFile string, p *term.Pane) (string, error) {
	text := strings.TrimSpace(prompt)
	if !strings.Contains(text, "\n") && len([]rune(text)) <= MaxInline {
		return Flat(text), nil
	}
	path := promptFile
	if path == "" {
		f, err := o.writeInbox(text+"\n", p)
		if err != nil {
			return "", err
		}
		path = f
	}
	return "Read the file " + path + " and follow its instructions.", nil
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

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
