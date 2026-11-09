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
}

// Address is the ID to print and to put in message tags. iTerm2 session IDs are UUIDs and
// stand alone, so agent-msg tags keep their form; other backends need the prefix to route.
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

var Where = []string{"tab", "split", "window"}

func CheckWhere(where string) error {
	for _, w := range Where {
		if w == where {
			return nil
		}
	}
	return UsageErr("--where must be one of %s", strings.Join(Where, ", "))
}

var keyNames = []string{"enter", "escape", "tab", "backspace", "up", "down", "left", "right"}
var ctrlKey = regexp.MustCompile(`^ctrl-[a-z]$`)

func NormalizeKey(key string) (string, error) {
	k := strings.ToLower(strings.TrimSpace(key))
	for _, n := range keyNames {
		if k == n {
			return k, nil
		}
	}
	if ctrlKey.MatchString(k) {
		return k, nil
	}
	return "", UsageErr("unknown key '%s'. Use one of: %s, ctrl-<letter>", key, strings.Join(keyNames, ", "))
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
var BackendNames = []string{"iterm2", "tmux", "rex", "ghostty"}

func IsBackend(name string) bool {
	for _, n := range BackendNames {
		if n == name {
			return true
		}
	}
	return false
}

// Detect names the backend of the pane we run in. tmux first: it is the innermost when nested.
func Detect(getenv func(string) string, itermInstalled bool) string {
	switch {
	case getenv("TMUX") != "":
		return "tmux"
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
	case "ghostty":
		return ghostty{}, nil
	}
	return nil, UsageErr("unknown backend '%s'. Known: %s", name, strings.Join(BackendNames, ", "))
}
