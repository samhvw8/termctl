package term

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MaxSplit is how many panes a tab may hold before "auto" stops splitting it.
const MaxSplit = 12

// Place turns --where auto into tab or split for a new pane, splitting in the caller's tab; see PlaceIn.
// Other values pass through. hc ignores --where, so it is never asked for its panes.
// Placement is cosmetic: a pane list that fails gives a tab and a warning, never a failed spawn.
func Place(b Backend, where string) (string, error) {
	where, _ = PlaceFor(b, where, nil)
	return where, nil
}

// PlaceFor is Place that also returns the pane to split, or beside whose tab a new tab opens.
// owned reports the panes the caller started; it may be nil.
func PlaceFor(b Backend, where string, owned func(*Pane) bool) (string, *Pane) {
	if where != "auto" {
		return where, nil
	}
	if b.Name() == "hc" {
		return "tab", nil
	}
	panes, err := b.ListPanes()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: placement: %v; opening a new tab\n", err)
		return "tab", nil
	}
	return PlaceIn(panes, owned)
}

// PlaceIn decides where a new pane goes, whatever its folder:
//   - a tab with room (fewer than MaxSplit panes): first the caller's tab, then a tab holding a pane
//     the caller started (owned). The new pane splits the largest pane there (the newest on a tie,
//     or when the backend reports no sizes), so the panes stay even;
//   - otherwise a new tab, which backends open right after the caller's tab.
//
// A backend that does not report tabs splits the caller, with no cap.
func PlaceIn(panes []*Pane, owned func(*Pane) bool) (string, *Pane) {
	var me *Pane
	for _, p := range panes {
		if p.IsSelf {
			me = p
		}
	}
	if me == nil {
		return "tab", nil
	}
	if me.Tab == "" {
		return "split", me
	}
	tabs := []string{me.Tab}
	if owned != nil {
		for _, p := range panes {
			if p.Tab != "" && !p.IsSelf && owned(p) {
				tabs = append(tabs, p.Tab)
			}
		}
	}
	for _, tab := range tabs {
		var best *Pane
		n := 0
		for _, p := range panes {
			if p.Tab != tab {
				continue
			}
			n++
			if best == nil || p.Width*p.Height >= best.Width*best.Height {
				best = p
			}
		}
		if n < MaxSplit {
			return "split", best
		}
	}
	return "tab", me
}

// SideBySide reports whether splitting a pane of cols x rows cells should put the new pane beside it
// (true) or below it (false): the split cuts the long side. A cell is about twice as tall as it is
// wide, so a pane is wider than tall when cols >= 2*rows. Unknown sizes split side by side.
func SideBySide(cols, rows int) bool {
	if cols <= 0 || rows <= 0 {
		return true
	}
	return cols >= 2*rows
}

// Anchored is a backend that can split a given pane, or open a tab beside a given pane's tab,
// instead of always the caller's.
type Anchored interface {
	SpawnAt(anchor *Pane, where, cmd, cwd, title string) (*Pane, error)
}

// ExpandHome turns a leading ~ into the home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
