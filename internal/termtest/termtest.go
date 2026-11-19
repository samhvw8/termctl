// Package termtest holds an in-memory terminal backend and clock for tests.
package termtest

import (
	"fmt"
	"strings"
	"sync"

	"github.com/samhvw8/termctl/internal/term"
)

type Clock struct{ T float64 }

func (c *Clock) Now() float64    { return c.T }
func (c *Clock) Sleep(s float64) { c.T += s }

type Event struct {
	Kind string // text, key, capture, spawn, close, label
	Pane string
	Text string
	Busy int
}

type Screen struct {
	Input  string
	Queued string // an interrupt puts this back on the input line, as Claude Code and agy do
	Busy   int
	Log    []string
	Dialog *Dialog
	hidden int // captures left before typed text shows (LateInput)
}

// Dialog is a Claude Code permission dialog drawn in place of the input box until it is answered.
type Dialog struct {
	Body      string
	Options   []string
	Selected  int  // from 1
	Answer    int  // the choice taken with Enter; -1 after Esc; 0 while it waits
	Stuck     bool // Enter and Esc do nothing, as a harness that froze
	NoArrows  bool // up and down do nothing
	HideFirst int  // this many captures do not show it yet, as a dialog still being drawn
	Lag       int  // this many captures after an arrow key still show the old selection, as Claude Code does
	shown     int
	wait      int
}

// Waiting reports whether the dialog is on screen.
func (d *Dialog) Waiting() bool { return d != nil && d.Answer == 0 }

func (d *Dialog) lines() []string {
	if d.wait > 0 {
		d.wait--
	} else {
		d.shown = d.Selected
	}
	out := []string{strings.Repeat("─", 40), " Bash command"}
	for _, ln := range strings.Split(d.Body, "\n") {
		out = append(out, " "+ln)
	}
	out = append(out, "", " Do you want to proceed?")
	for i, o := range d.Options {
		mark := "  "
		if i+1 == d.shown {
			mark = "❯ "
		}
		out = append(out, fmt.Sprintf(" %s%d. %s", mark, i+1, o))
	}
	return append(out, "", " Esc to cancel · Tab to amend")
}

type Fake struct {
	mu            sync.Mutex // Capture and ProgramStatuses run in parallel in the watcher
	Panes         []*term.Pane
	Me            *term.Pane
	State         map[string]*Screen
	Events        []Event
	Natives       map[string]string
	BusyAfter     int
	KeepClearText bool
	FailList      bool
	FailSpawn     bool
	DropEnters    int // this many Enters are ignored, as a TUI does when text is still pasting
	// LateInput: after typing, this many captures still show an empty input line, and an Enter sent
	// meanwhile is lost, as Claude Code does while it is still starting (2026-11-19, task t93a651).
	LateInput    int
	Ref          map[string]string
	Spawned      []string
	BackendName  string                        // Name() when set; default "fake"
	Statuses     map[string]term.ProgramStatus // a pane's program status
	StatusCalls  int
	CostlyStatus bool // the status read costs a process, as on Rex and hc: the watcher reads it once per round
	// History makes the fake a term.SafeHistory: a pane with Rows shows that many lines, and a Capture of
	// more reads the lines above them, as tmux's scrollback.
	History bool
}

// HistoryIsSafe makes the fake a term.SafeHistory when History is set.
func (f *Fake) HistoryIsSafe(*term.Pane) bool { return f.History }

// StatusIsFree makes the fake a term.FreeStatus unless CostlyStatus is set.
func (f *Fake) StatusIsFree() bool { return !f.CostlyStatus }

// ProgramStatuses makes the fake a term.StatusReader: one call for many panes.
func (f *Fake) ProgramStatuses(panes []*term.Pane) (map[string]term.ProgramStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.StatusCalls++
	out := map[string]term.ProgramStatus{}
	for _, p := range panes {
		if st, ok := f.Statuses[p.ID]; ok {
			out[p.ID] = st
		}
	}
	return out, nil
}

func New(panes []*term.Pane, me *term.Pane) *Fake {
	f := &Fake{Panes: panes, Me: me, State: map[string]*Screen{}, Natives: map[string]string{}, BusyAfter: 3}
	for _, p := range panes {
		f.State[p.ID] = &Screen{Log: []string{"some earlier output"}}
	}
	return f
}

// MakePane builds a pane with the ID fake:<n>.
func MakePane(n int, label, title, folder, process string) *term.Pane {
	if process == "" {
		process = "python3"
	}
	return &term.Pane{ID: fmt.Sprintf("fake:%d", n), Backend: "fake", Native: fmt.Sprint(n), Label: label, Title: title, Folder: folder, Process: process}
}

func (f *Fake) Name() string {
	if f.BackendName != "" {
		return f.BackendName
	}
	return "fake"
}
func (f *Fake) ListPanes() ([]*term.Pane, error) {
	if f.FailList {
		return nil, term.BackendErr("backend down")
	}
	return append([]*term.Pane{}, f.Panes...), nil
}
func (f *Fake) SelfPane() (*term.Pane, error)    { return f.Me, nil }
func (f *Fake) NativeStatus(p *term.Pane) string { return f.Natives[p.ID] }
func (f *Fake) Available() bool                  { return true }
func (f *Fake) ServerRef() map[string]string     { return f.Ref }

func (f *Fake) Spawn(where, cmd, cwd, title string) (*term.Pane, error) {
	if f.FailSpawn {
		return nil, term.BackendErr("spawn failed")
	}
	n := len(f.Panes) + len(f.Spawned) + 100
	p := &term.Pane{ID: fmt.Sprintf("fake:%d", n), Backend: "fake", Native: fmt.Sprint(n), Title: title, Folder: cwd, Process: "fakeagent"}
	f.Panes = append(f.Panes, p)
	f.State[p.ID] = &Screen{Log: []string{"some earlier output"}}
	f.Spawned = append(f.Spawned, p.ID)
	f.Events = append(f.Events, Event{Kind: "spawn", Pane: where, Text: cmd + "|" + cwd})
	return p, nil
}

// SpawnAt records the anchor as "<where>@<anchor id>". A split joins the anchor's tab; a tab gets a new one.
func (f *Fake) SpawnAt(at *term.Pane, where, cmd, cwd, title string) (*term.Pane, error) {
	p, err := f.Spawn(where, cmd, cwd, title)
	if err != nil || at == nil {
		return p, err
	}
	f.Events[len(f.Events)-1].Pane = where + "@" + at.ID
	if where == "split" {
		p.Tab = at.Tab
	} else {
		p.Tab = "tab-" + p.Native
	}
	return p, nil
}

func (f *Fake) SendText(p *term.Pane, text string) error {
	st := f.State[p.ID]
	f.Events = append(f.Events, Event{Kind: "text", Pane: p.ID, Text: text, Busy: st.Busy})
	st.Input += text
	st.hidden = f.LateInput
	return nil
}

func (f *Fake) SendKey(p *term.Pane, key string) error {
	st := f.State[p.ID]
	f.Events = append(f.Events, Event{Kind: "key", Pane: p.ID, Text: key, Busy: st.Busy})
	if d := st.Dialog; d.Waiting() {
		switch {
		case key == "up" && !d.NoArrows && d.Selected > 1:
			d.Selected, d.wait = d.Selected-1, d.Lag
		case key == "down" && !d.NoArrows && d.Selected < len(d.Options):
			d.Selected, d.wait = d.Selected+1, d.Lag
		case key == "enter" && !d.Stuck:
			d.Answer = d.Selected
		case key == "escape" && !d.Stuck:
			d.Answer = -1
		}
		return nil
	}
	switch key {
	case "enter":
		if f.DropEnters > 0 {
			f.DropEnters--
			return nil
		}
		if st.hidden > 0 { // not taken in yet: the Enter is lost
			return nil
		}
		line := st.Input
		st.Input = ""
		if line == "/clear" {
			if f.KeepClearText {
				st.Input = line
			} else {
				st.Busy = f.BusyAfter
			}
		}
	case "ctrl-u":
		st.Input = ""
	case "escape", "ctrl-c":
		if st.Queued != "" {
			st.Input, st.Queued = st.Queued, ""
		}
	}
	return nil
}

func (f *Fake) Capture(p *term.Pane, lines int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.capture(p)
	// A pane with Rows shows its last Rows lines; only a backend with safe history reads further back.
	if n := lines; p.Rows > 0 {
		if !f.History {
			n = min(n, p.Rows)
		}
		if len(out) > n {
			out = out[len(out)-n:]
		}
	}
	return out, nil
}

func (f *Fake) capture(p *term.Pane) []string {
	st := f.State[p.ID]
	f.Events = append(f.Events, Event{Kind: "capture", Pane: p.ID})
	if st.Busy > 0 {
		st.Busy--
		return append(append([]string{}, st.Log...), fmt.Sprintf("working %d", st.Busy))
	}
	screen := append([]string{}, st.Log...)
	input := st.Input
	if st.hidden > 0 {
		st.hidden--
		input = ""
	}
	if d := st.Dialog; d.Waiting() {
		if d.HideFirst > 0 {
			d.HideFirst--
			return append(screen, "drawing…")
		}
		return append(screen, d.lines()...)
	}
	if p.Process == "omp" { // omp's input line, with what is typed on it
		return append(screen, "╰─ "+input)
	}
	if p.Process == "fakeagent" || p.Process == "claude" { // spawned panes pass every profile's input-line check
		rule := strings.Repeat("─", 40)
		screen = append(screen, rule, "❯ "+input, rule, "╰─")
	}
	return append(screen, "> "+input)
}

func (f *Fake) Close(p *term.Pane) error {
	f.Events = append(f.Events, Event{Kind: "close", Pane: p.ID})
	kept := f.Panes[:0]
	for _, q := range f.Panes {
		if q.ID != p.ID {
			kept = append(kept, q)
		}
	}
	f.Panes = kept
	return nil
}

func (f *Fake) SetLabel(p *term.Pane, label string) error {
	f.Events = append(f.Events, Event{Kind: "label", Pane: p.ID, Text: label})
	p.Label = label
	return nil
}

// Captures counts the screen reads of a pane.
func (f *Fake) Captures(pane string) int {
	n := 0
	for _, e := range f.Events {
		if e.Kind == "capture" && e.Pane == pane {
			n++
		}
	}
	return n
}

// Typed returns the text and key events sent to a pane, as "text:..." and "key:...".
func (f *Fake) Typed(pane string) []string {
	var out []string
	for _, e := range f.Events {
		if (e.Kind == "text" || e.Kind == "key") && e.Pane == pane {
			out = append(out, e.Kind+":"+e.Text)
		}
	}
	return out
}

// Texts returns only the typed text.
func (f *Fake) Texts(pane string) []string {
	var out []string
	for _, e := range f.Events {
		if e.Kind == "text" && e.Pane == pane {
			out = append(out, e.Text)
		}
	}
	return out
}

func (f *Fake) Has(kind, pane string) bool {
	for _, e := range f.Events {
		if e.Kind == kind && e.Pane == pane {
			return true
		}
	}
	return false
}

func Contains(xs []string, sub string) bool {
	for _, x := range xs {
		if strings.Contains(x, sub) {
			return true
		}
	}
	return false
}

func (f *Fake) find(id string) *term.Pane {
	for _, p := range f.Panes {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// MovePane puts p in anchor's tab, recorded as "move" with the anchor's ID as text.
func (f *Fake) MovePane(p, anchor *term.Pane) error {
	f.Events = append(f.Events, Event{Kind: "move", Pane: p.ID, Text: anchor.ID})
	f.find(p.ID).Tab = anchor.Tab
	return nil
}

// BreakPane gives p a tab of its own, "tab-<native>".
func (f *Fake) BreakPane(p *term.Pane) error {
	f.Events = append(f.Events, Event{Kind: "break", Pane: p.ID})
	f.find(p.ID).Tab = "tab-" + p.Native
	return nil
}

// SwapPanes trades the tabs and list positions of two panes.
func (f *Fake) SwapPanes(a, b *term.Pane) error {
	f.Events = append(f.Events, Event{Kind: "swap", Pane: a.ID, Text: b.ID})
	i, j := -1, -1
	for k, p := range f.Panes {
		switch p.ID {
		case a.ID:
			i = k
		case b.ID:
			j = k
		}
	}
	f.Panes[i].Tab, f.Panes[j].Tab = f.Panes[j].Tab, f.Panes[i].Tab
	f.Panes[i], f.Panes[j] = f.Panes[j], f.Panes[i]
	return nil
}

// Arrange records "arrange" on the tab, with "<layout>|<main ID>" as text.
func (f *Fake) Arrange(tab, layout string, main *term.Pane) error {
	id := ""
	if main != nil {
		id = main.ID
	}
	f.Events = append(f.Events, Event{Kind: "arrange", Pane: tab, Text: layout + "|" + id})
	return nil
}

// Basic hides every optional interface of a backend (Mover, Swapper, Anchored, ...), as a backend
// that implements only term.Backend.
type Basic struct{ term.Backend }
