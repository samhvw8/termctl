// Package termtest holds an in-memory terminal backend and clock for tests.
package termtest

import (
	"fmt"
	"strings"

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
	Input string
	Busy  int
	Log   []string
}

type Fake struct {
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
	Ref           map[string]string
	Spawned       []string
	BackendName   string // Name() when set; default "fake"
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
	return nil
}

func (f *Fake) SendKey(p *term.Pane, key string) error {
	st := f.State[p.ID]
	f.Events = append(f.Events, Event{Kind: "key", Pane: p.ID, Text: key, Busy: st.Busy})
	switch key {
	case "enter":
		if f.DropEnters > 0 {
			f.DropEnters--
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
	}
	return nil
}

func (f *Fake) Capture(p *term.Pane, lines int) ([]string, error) {
	st := f.State[p.ID]
	f.Events = append(f.Events, Event{Kind: "capture", Pane: p.ID})
	if st.Busy > 0 {
		st.Busy--
		return append(append([]string{}, st.Log...), fmt.Sprintf("working %d", st.Busy)), nil
	}
	screen := append([]string{}, st.Log...)
	if p.Process == "fakeagent" { // spawned panes pass every profile's input-line check
		rule := strings.Repeat("─", 40)
		screen = append(screen, rule, "❯", rule, "╰─")
	}
	return append(screen, "> "+st.Input), nil
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
