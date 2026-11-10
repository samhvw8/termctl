package term

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MaxSplit is how many panes a tab may hold before "auto" stops splitting it.
const MaxSplit = 3

// Place turns --where auto into tab or split for a new pane that runs in cwd, splitting next to the
// caller; see PlaceIn. Other values pass through. hc ignores --where, so it is never asked for its panes.
// Placement is cosmetic: a pane list that fails gives a tab and a warning, never a failed spawn.
func Place(b Backend, where, cwd string) (string, error) {
	where, _ = PlaceFor(b, where, cwd, nil)
	return where, nil
}

// PlaceFor is Place that also returns the pane to split, or beside whose tab a new tab opens.
// owned reports the panes the caller started; it may be nil.
func PlaceFor(b Backend, where, cwd string, owned func(*Pane) bool) (string, *Pane) {
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
	return PlaceIn(panes, cwd, owned)
}

// PlaceIn decides where a new pane for cwd goes:
//   - a tab with room (fewer than MaxSplit panes) that already works on cwd's project: first the
//     caller's tab when the caller is in that project, then a tab holding a pane the caller started
//     (owned) in that project. The new pane splits the newest pane there, so the caller keeps its width;
//   - otherwise a new tab, which backends open right after the caller's tab.
//
// A backend that does not report tabs splits the caller for its own project, with no cap.
func PlaceIn(panes []*Pane, cwd string, owned func(*Pane) bool) (string, *Pane) {
	var me *Pane
	for _, p := range panes {
		if p.IsSelf {
			me = p
		}
	}
	if me == nil {
		return "tab", nil
	}
	inProject := func(p *Pane) bool { return p.Folder != "" && SameProject(ExpandHome(p.Folder), cwd) }
	var tabs []string
	if inProject(me) {
		if me.Tab == "" {
			return "split", me
		}
		tabs = append(tabs, me.Tab)
	}
	if owned != nil {
		for _, p := range panes {
			if p.Tab != "" && !p.IsSelf && owned(p) && inProject(p) {
				tabs = append(tabs, p.Tab)
			}
		}
	}
	for _, tab := range tabs {
		var last *Pane
		n := 0
		for _, p := range panes {
			if p.Tab == tab {
				last = p
				n++
			}
		}
		if n < MaxSplit {
			return "split", last
		}
	}
	return "tab", me
}

// Anchored is a backend that can split a given pane, or open a tab beside a given pane's tab,
// instead of always the caller's.
type Anchored interface {
	SpawnAt(anchor *Pane, where, cmd, cwd, title string) (*Pane, error)
}

// SameProject reports whether two folders belong to one project: the same git repository
// (a worktree counts as its repository), or, outside git, the same folder.
func SameProject(a, b string) bool {
	a, b = canonical(a), canonical(b)
	if a == "" || b == "" {
		return false
	}
	ga, gb := gitCommonDir(a), gitCommonDir(b)
	if ga != "" || gb != "" {
		return ga == gb
	}
	return a == b
}

func canonical(dir string) string {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return ""
		}
		dir = wd
	}
	abs, err := filepath.Abs(ExpandHome(dir))
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// gitCommonDir is the repository's shared .git directory, the same for all its worktrees, or "".
func gitCommonDir(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	return canonical(strings.TrimSpace(string(out)))
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
