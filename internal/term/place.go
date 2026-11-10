package term

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MaxSplit is how many panes a tab may hold before "auto" stops splitting it.
const MaxSplit = 3

// Place turns --where auto into tab or split for a new pane that runs in cwd:
//   - the same project as the caller's pane, and its tab has fewer than MaxSplit panes: split;
//   - anything else (another project, a full tab, no caller pane): tab, which backends open next to the caller's tab.
//
// Other values pass through. hc ignores --where, so it is never asked for its panes.
func Place(b Backend, where, cwd string) (string, error) {
	if where != "auto" {
		return where, nil
	}
	if b.Name() == "hc" {
		return "tab", nil
	}
	panes, err := b.ListPanes()
	if err != nil {
		return "", err
	}
	return PlaceIn(panes, cwd), nil
}

// PlaceIn is Place for a known pane list.
func PlaceIn(panes []*Pane, cwd string) string {
	var me *Pane
	for _, p := range panes {
		if p.IsSelf {
			me = p
		}
	}
	if me == nil || me.Folder == "" || !SameProject(ExpandHome(me.Folder), cwd) {
		return "tab"
	}
	if me.Tab == "" {
		return "split" // the backend cannot say which panes share the tab
	}
	n := 0
	for _, p := range panes {
		if p.Tab == me.Tab {
			n++
		}
	}
	if n >= MaxSplit {
		return "tab"
	}
	return "split"
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
