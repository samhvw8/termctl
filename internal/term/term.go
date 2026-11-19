// Package term is Layer 1: panes, text, keys and capture through backends.
// It knows nothing about coding-agent harnesses.
package term

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Error carries a process exit code. The codes are part of the CLI contract.
type Error struct {
	Code int
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

const (
	CodeBackend = 1
	CodeUsage   = 2 // also a refused target
	CodeDraft   = 4
	CodeTimeout = 5
	CodeGuard   = 6
	CodeDialog  = 7 // no dialog on screen, or a different one than expected
)

func Errf(code int, format string, a ...any) *Error { return &Error{code, fmt.Sprintf(format, a...)} }
func BackendErr(format string, a ...any) *Error     { return Errf(CodeBackend, format, a...) }
func UsageErr(format string, a ...any) *Error       { return Errf(CodeUsage, format, a...) }
func TargetErr(format string, a ...any) *Error      { return Errf(CodeUsage, format, a...) }

// Pane is one terminal pane. ID is "<backend>:<native>".
type Pane struct {
	ID, Backend, Native, TTY, Title, Label string
	Process                                string // main program on the pane (a harness if one runs there)
	Folder                                 string
	IsSelf                                 bool
	Status                                 string
	Rows                                   int    // screen height, when the backend reports it; 0 otherwise
	Width, Height                          int    // pane size in cells for placement, when the backend reports it; 0 otherwise
	Tab                                    string // the tab (tmux: window) holding the pane, when the backend reports it
	Hidden                                 bool   // behind a maximized pane of its tab (iTerm2): still running and reachable, not on screen
}

// Address is the ID to print and to put in message tags. iTerm2 session IDs are UUIDs and
// stand alone, so message tags keep their form; other backends need the prefix to route.
func (p *Pane) Address() string {
	if p.Backend == "iterm2" {
		return p.Native
	}
	return p.ID
}

// Short is the ID shown in tables.
func (p *Pane) Short() string {
	if p.Backend == "iterm2" {
		return Cut(p.Native, 8)
	}
	return p.ID
}

// Cut returns at most n runes of s.
func Cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Backend is everything Layer 2 may ask of a terminal.
type Backend interface {
	Name() string
	ListPanes() ([]*Pane, error)
	Spawn(where, cmd, cwd, title string) (*Pane, error)
	SendText(p *Pane, text string) error // literal; never submits
	SendKey(p *Pane, key string) error
	Capture(p *Pane, lines int) ([]string, error)
	Close(p *Pane) error
	SetLabel(p *Pane, label string) error
	SelfPane() (*Pane, error)
	NativeStatus(p *Pane) string // the terminal's own busy/idle report, or ""
	Available() bool
	ServerRef() map[string]string // enough to reopen this server and to notice a restart
}

// ProgramStatus is what the program in a pane last reported about itself (OSC 7501 and alike), as the
// terminal keeps it. State is the terminal's word, lower case: idle, working, done, blocked, error.
// iTerm2's "waiting" (its name for an OSC 7501 "blocked") is given as "blocked". Kind and Msg are the
// program's own reason ("permission", the request), when the terminal keeps them.
type ProgramStatus struct {
	State, Kind, Msg string
}

// Blocked says the program reported that it waits on a person. It is a hint, never proof of a dialog.
func (s ProgramStatus) Blocked() bool { return s.State == "blocked" }

// LiveLister is an optional backend interface for a terminal server that may not be running (hc's core).
// Its ListPanes returns no panes and no error then, so ls reports nothing and starts nothing; ListLive
// fails instead, so a caller that recorded a pane can tell "gone" from "cannot tell".
type LiveLister interface {
	ListLive() ([]*Pane, error)
}

// ListLive lists a backend's panes, and fails when its server does not answer (see LiveLister).
func ListLive(b Backend) ([]*Pane, error) {
	if l, ok := b.(LiveLister); ok {
		return l.ListLive()
	}
	return b.ListPanes()
}

// StatusReader is an optional backend interface: the program status of many panes in one call. The cost
// differs (FreeStatus marks the backends whose read starts no process): iTerm2 makes one websocket call
// per pane, hc runs one `hc ls` plus one `hc get` per blocked pane, Rex one `rex do`. Panes the terminal
// knows no status for, or that ended since the list, are left out of the map, which is keyed by Pane.ID.
// On another failure the map holds what was read and the error says what failed. tmux and Ghostty have
// none: tmux does not answer OSC 7501, so agents do not send it there.
type StatusReader interface {
	ProgramStatuses(panes []*Pane) (map[string]ProgramStatus, error)
}

// FreeStatus marks a StatusReader whose read starts no process: iTerm2 asks over its open websocket.
// The watcher polls only these between screen rounds. Rex (a rex do process per read) and hc (an hc ls)
// are read once per round, so a watcher never starts a process every second.
type FreeStatus interface {
	StatusIsFree() bool
}

// SafeHistory marks a backend whose Capture of more lines than the screen (Pane.Rows) reads the scrollback
// above the screen. HistoryIsSafe says whether that read leaves the program in p undisturbed right now.
// tmux (capture-pane -S), iTerm2 (GetBufferRequest trailing_lines) and Rex (rex capture) read the
// terminal's own copy of the screen and scrollback, whatever the program, so they always answer true. hc
// reads a full-screen app's history by scrolling it with mouse-wheel input, except while the program
// reports working, blocked or error, so it answers per pane from that report.
type SafeHistory interface {
	HistoryIsSafe(p *Pane) bool
}

// Submitter is for a backend that cannot type without pressing Enter (hc). A backend that has it
// is sent whole lines through SubmitText; its SendText must refuse rather than submit.
// from names the sender of an agent message, as <backend>/<id>, and is empty for anything else.
type Submitter interface {
	SubmitText(p *Pane, text, from string) error
}

// Where lists the --where values. "auto" is resolved by Place before a backend sees it.
var Where = []string{"auto", "tab", "split", "window"}

func CheckWhere(where string) error {
	for _, w := range Where {
		if w == where {
			return nil
		}
	}
	return UsageErr("--where must be one of %s", strings.Join(Where, ", "))
}

// CheckText refuses line breaks: send-text never submits, and one would act as Enter.
func CheckText(text string) error {
	if strings.ContainsAny(text, "\r\n") {
		return UsageErr("text must not contain line breaks; send-text never submits (use send-key enter)")
	}
	return nil
}

// TrimScreen drops trailing blank lines and returns the last n. iTerm2 gives NUL for cells a TUI never wrote.
func TrimScreen(lines []string, n int) []string {
	out := make([]string, len(lines))
	for i, ln := range lines {
		out[i] = strings.TrimRight(strings.ReplaceAll(ln, "\x00", " "), " \t\r")
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

var (
	titleGlyphs = regexp.MustCompile(`[\x{2800}-\x{28ff}\x{e000}-\x{f8ff}\x{f0000}-\x{10ffff}]`)
	ompPrefix   = regexp.MustCompile(`^\s*π\s*>?\s*`)
	spaces      = regexp.MustCompile(`\s+`)
	shellSafe   = regexp.MustCompile(`^[\w@%+=:,./-]+$`)
)

// CleanTitle drops spinner and icon-font glyphs that harnesses put in titles.
func CleanTitle(t string) string {
	t = titleGlyphs.ReplaceAllString(t, "")
	t = ompPrefix.ReplaceAllString(t, "")
	return strings.TrimSpace(spaces.ReplaceAllString(t, " "))
}

func Tilde(p string) string {
	home, err := os.UserHomeDir()
	if err == nil && (p == home || strings.HasPrefix(p, home+"/")) {
		return "~" + p[len(home):]
	}
	return p
}

// ShellQuote quotes one word for a POSIX shell.
func ShellQuote(s string) string {
	if s != "" && shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// Resolve picks one pane. The first rule with matches wins.
func Resolve(panes []*Pane, target string) (*Pane, error) {
	t := strings.ToLower(strings.TrimSpace(target))
	has4 := len([]rune(t)) >= 4
	base := func(p *Pane) string {
		if p.Folder == "" {
			return ""
		}
		return strings.ToLower(filepath.Base(p.Folder))
	}
	rules := []struct {
		name string
		ok   func(*Pane) bool
	}{
		{"id", func(p *Pane) bool { return strings.ToLower(p.ID) == t || (has4 && strings.ToLower(p.Native) == t) }},
		{"label", func(p *Pane) bool { return strings.ToLower(p.Label) == t }},
		{"title", func(p *Pane) bool { return strings.ToLower(p.Title) == t }},
		{"folder", func(p *Pane) bool { return p.Folder != "" && base(p) == t }},
		{"title contains", func(p *Pane) bool { return strings.Contains(strings.ToLower(p.Title), t) }},
		{"id prefix", func(p *Pane) bool {
			return has4 && (strings.HasPrefix(strings.ToLower(p.Native), t) || strings.HasPrefix(strings.ToLower(p.ID), t))
		}},
	}
	for _, r := range rules {
		var hits []*Pane
		for _, p := range panes {
			if r.ok(p) {
				hits = append(hits, p)
			}
		}
		if len(hits) == 1 {
			return hits[0], nil
		}
		if len(hits) > 1 {
			var b strings.Builder
			for _, p := range hits {
				fmt.Fprintf(&b, "\n  %-9s %-8s %-40s %s", p.Short(), p.Process, Cut(p.Title, 40), p.Folder)
			}
			return nil, TargetErr("'%s' matches %d panes by %s; use a label or an ID prefix:%s", target, len(hits), r.name, b.String())
		}
	}
	return nil, TargetErr("no pane matches '%s'. Run: ls", target)
}

// BackendNames lists known backends.
var BackendNames = []string{"iterm2", "tmux", "rex", "hc", "ghostty"}

func IsBackend(name string) bool {
	for _, n := range BackendNames {
		if n == name {
			return true
		}
	}
	return false
}

// Detect names the backend of the pane we run in. tmux first: it is the innermost when nested.
// hc comes before iTerm2: a Herdr terminal can inherit ITERM_SESSION_ID from the core's environment.
func Detect(getenv func(string) string, itermInstalled bool) string {
	switch {
	case getenv("TMUX") != "":
		return "tmux"
	case getenv("HC_TERMINAL_ID") != "":
		return "hc"
	case getenv("REX_SESSION") != "":
		return "rex"
	case getenv("ITERM_SESSION_ID") != "":
		return "iterm2"
	case itermInstalled:
		return "iterm2"
	}
	return ""
}

// DetectEnv is Detect on the real environment.
func DetectEnv() string {
	_, err := os.Stat(itermApp)
	return Detect(os.Getenv, err == nil)
}

// Make builds a backend. iTerm2 returns one instance per process, sharing one connection.
func Make(name, socket, socketPath string) (Backend, error) {
	switch name {
	case "iterm2":
		return theITerm2(), nil
	case "tmux":
		return &TmuxBackend{Socket: socket, SocketPath: socketPath}, nil
	case "rex":
		return NewRex(), nil
	case "hc":
		return NewHc(socketPath), nil
	case "ghostty":
		return ghostty{}, nil
	}
	return nil, UsageErr("unknown backend '%s'. Known: %s", name, strings.Join(BackendNames, ", "))
}
