package term

import (
	"strings"
)

// Moving and arranging panes. Each capability is an optional interface, like Submitter and
// Anchored: a backend without it is refused with an error that names the backend, before anything
// changes. None of them restarts a program: a moved pane keeps its process, ID, label and scrollback.

// Mover moves a live pane into another tab by splitting anchor there. The split cuts anchor's long
// side (SideBySide), and the backend evens out the destination tab as it does after a spawn.
type Mover interface {
	MovePane(p, anchor *Pane) error
}

// Breaker moves a live pane out into a new tab of its own, opened right after the tab it leaves.
type Breaker interface {
	BreakPane(p *Pane) error
}

// Swapper trades the places of two live panes, in one tab or across tabs.
type Swapper interface {
	SwapPanes(a, b *Pane) error
}

// Arranger lays out every pane of a tab again, evenly sized. For the main-* layouts, main is the
// pane that gets the big half; nil leaves the tab's first pane there.
type Arranger interface {
	Arrange(tab, layout string, main *Pane) error
}

// Layouts lists the arrange --layout values. even keeps the tab's shape where the backend can
// (iTerm2) and tiles it where it cannot (tmux), as a spawn does.
var Layouts = []string{"even", "rows", "columns", "tiled", "main-left", "main-top"}

func CheckLayout(layout string) error {
	for _, l := range Layouts {
		if l == layout {
			return nil
		}
	}
	return UsageErr("--layout must be one of %s", strings.Join(Layouts, ", "))
}

func hasMainPane(layout string) bool { return strings.HasPrefix(layout, "main-") }

// TabTargets describes a tab target, for messages.
const TabTargets = "self, tab-of:<pane>, or a tab ID from ls"

// cannotArrange says why a backend has none of the interfaces above.
var cannotArrange = map[string]string{
	"hc":      "herdr-core has no tabs or splits; the Herdr app places its terminals",
	"ghostty": "Ghostty has no control API yet",
	"rex":     "Rex's CLI has no command for it",
}

func notSupported(b Backend, what string) *Error {
	why := cannotArrange[b.Name()]
	if why == "" {
		why = "this backend does not implement it"
	}
	return BackendErr("not supported on %s: cannot %s (%s); nothing was changed", b.Name(), what, why)
}

// ResolveTab names a tab: "self" is the caller's tab, "tab-of:<target>" the tab of the pane that
// target resolves to, anything else a tab ID as ls shows it. Tab IDs, not positions: a position
// shifts when another tab opens or closes. It returns the tab and the pane that named it (nil for
// an ID). On a backend that reports no tabs (Rex) the tab is "" and only the pane stands for it.
func ResolveTab(panes []*Pane, target string) (string, *Pane, error) {
	var at *Pane
	switch {
	case target == "":
		return "", nil, UsageErr("name a tab: %s", TabTargets)
	case target == "self":
		for _, p := range panes {
			if p.IsSelf {
				at = p
			}
		}
		if at == nil {
			return "", nil, UsageErr("cannot tell which pane this runs in, so 'self' names no tab")
		}
	case strings.HasPrefix(target, "tab-of:"):
		p, err := Resolve(panes, strings.TrimPrefix(target, "tab-of:"))
		if err != nil {
			return "", nil, err
		}
		at = p
	default:
		for _, p := range panes {
			if p.Tab != "" && p.Tab == target {
				return target, nil, nil
			}
		}
		return "", nil, TargetErr("no tab '%s'. Name a tab as %s; ls shows the tab of each pane", target, TabTargets)
	}
	return at.Tab, at, nil
}

// TabPanes returns the panes of a tab, in the backend's order.
func TabPanes(panes []*Pane, tab string) []*Pane {
	var out []*Pane
	for _, p := range panes {
		if tab != "" && p.Tab == tab {
			out = append(out, p)
		}
	}
	return out
}

// largest is the pane a move splits: the largest in the tab, the last on a tie, as PlaceIn picks.
func largest(panes []*Pane) *Pane {
	var best *Pane
	for _, p := range panes {
		if best == nil || p.Width*p.Height >= best.Width*best.Height {
			best = p
		}
	}
	return best
}

// Moved says where Move put a pane.
type Moved struct {
	Pane   *Pane // the pane as listed after the move: same ID, its new tab
	New    bool  // it got a new tab of its own
	Anchor *Pane // the pane it was split from, when it joined a tab
}

// Move moves p to the tab named by to, a tab target or "new", and returns it as listed afterwards.
// It refuses a move that changes nothing. The pane's program keeps running: a busy pane may move.
func Move(b Backend, panes []*Pane, p *Pane, to string) (*Moved, error) {
	if to == "new" {
		br, ok := b.(Breaker)
		if !ok {
			return nil, notSupported(b, "move a pane to a new tab")
		}
		if p.Tab != "" && len(TabPanes(panes, p.Tab)) == 1 {
			return nil, UsageErr("%s is already alone in its tab", p.Address())
		}
		before := *p
		if err := br.BreakPane(p); err != nil {
			return nil, err
		}
		fresh, err := settle(b, before)
		if err != nil {
			return nil, err
		}
		return &Moved{Pane: fresh[0], New: true}, nil
	}
	m, ok := b.(Mover)
	if !ok {
		return nil, notSupported(b, "move a pane to another tab")
	}
	tab, at, err := ResolveTab(panes, to)
	if err != nil {
		return nil, err
	}
	anchor := at
	if tab != "" {
		if tab == p.Tab {
			return nil, UsageErr("%s is already in tab %s; use swap or arrange to change its place there", p.Address(), tab)
		}
		anchor = largest(TabPanes(panes, tab))
	}
	if anchor == nil || anchor.ID == p.ID {
		return nil, UsageErr("cannot move %s beside itself", p.Address())
	}
	before := *p
	if err := m.MovePane(p, anchor); err != nil {
		return nil, err
	}
	fresh, err := settle(b, before)
	if err != nil {
		return nil, err
	}
	return &Moved{Pane: fresh[0], Anchor: anchor}, nil
}

// Swap trades the places of a and c and returns both as listed afterwards.
func Swap(b Backend, a, c *Pane) ([]*Pane, error) {
	s, ok := b.(Swapper)
	if !ok {
		return nil, notSupported(b, "swap panes")
	}
	if a.ID == c.ID {
		return nil, UsageErr("cannot swap %s with itself", a.Address())
	}
	before := []Pane{*a, *c}
	if err := s.SwapPanes(a, c); err != nil {
		return nil, err
	}
	return settle(b, before...)
}

// Arranged says what Arrange laid out.
type Arranged struct {
	Tab   string
	Panes int
	Main  *Pane
}

// Arrange lays out the tab named by tabTarget (a tab target; "" is the caller's tab). mainTarget
// names the main pane of a main-* layout; without it the caller's pane is main when it is in the
// tab, else the tab's first pane. A tab with one pane is left as it is.
func Arrange(b Backend, panes []*Pane, tabTarget, layout, mainTarget string) (*Arranged, error) {
	ar, ok := b.(Arranger)
	if !ok {
		return nil, notSupported(b, "arrange a tab")
	}
	if err := CheckLayout(layout); err != nil {
		return nil, err
	}
	if tabTarget == "" {
		tabTarget = "self"
	}
	tab, _, err := ResolveTab(panes, tabTarget)
	if err != nil {
		return nil, err
	}
	if tab == "" {
		return nil, notSupported(b, "arrange a tab")
	}
	in := TabPanes(panes, tab)
	var main *Pane
	switch {
	case mainTarget != "":
		if !hasMainPane(layout) {
			return nil, UsageErr("--main needs a main-left or main-top layout")
		}
		if main, err = Resolve(panes, mainTarget); err != nil {
			return nil, err
		}
		if main.Tab != tab {
			return nil, UsageErr("%s is not in tab %s", main.Address(), tab)
		}
	case hasMainPane(layout):
		for _, p := range in {
			if p.IsSelf {
				main = p
			}
		}
	}
	done := &Arranged{Tab: tab, Panes: len(in), Main: main}
	if len(in) < 2 {
		return done, nil
	}
	return done, ar.Arrange(tab, layout, main)
}

// settle lists the panes again after a change; moved are the panes as they were before it. Each
// must still be there under its ID, which is what keeps a harness's task and registry record
// valid; a label the change dropped is put back.
func settle(b Backend, moved ...Pane) ([]*Pane, error) {
	panes, err := b.ListPanes()
	if err != nil {
		return nil, err
	}
	out := make([]*Pane, len(moved))
	for i, p := range moved {
		for _, q := range panes {
			if q.ID == p.ID {
				out[i] = q
			}
		}
		if out[i] == nil {
			return nil, BackendErr("%s is no longer listed under its ID after the change; check ls", p.Address())
		}
		if p.Label != "" && out[i].Label != p.Label {
			if err := b.SetLabel(out[i], p.Label); err != nil {
				return nil, err
			}
			out[i].Label = p.Label
		}
	}
	return out, nil
}
