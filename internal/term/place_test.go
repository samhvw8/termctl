package term

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestSameProject(t *testing.T) {
	root := t.TempDir()
	app := gitRepo(t, filepath.Join(root, "app"))
	tool := gitRepo(t, filepath.Join(root, "tool"))
	sub := filepath.Join(app, "src", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "app-wt")
	if out, err := exec.Command("git", "-C", app, "worktree", "add", "-q", wt).CombinedOutput(); err != nil {
		t.Fatalf("worktree: %v\n%s", err, out)
	}
	plain := filepath.Join(root, "notes")
	if err := os.MkdirAll(filepath.Join(plain, "a"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"same repo root", app, app, true},
		{"subfolder of the repo", app, sub, true},
		{"worktree of the repo", app, wt, true},
		{"another repo", app, tool, false},
		{"repo and a plain folder", app, plain, false},
		{"plain folder, itself", plain, plain, true},
		{"plain folder and its child", plain, filepath.Join(plain, "a"), false},
		{"missing folder", app, filepath.Join(root, "nope"), false},
	}
	for _, c := range cases {
		if got := SameProject(c.a, c.b); got != c.want {
			t.Errorf("%s: SameProject(%s, %s) = %v, want %v", c.name, c.a, c.b, got, c.want)
		}
	}
}

func TestPlaceIn(t *testing.T) {
	root := t.TempDir()
	app := gitRepo(t, filepath.Join(root, "app"))
	tool := gitRepo(t, filepath.Join(root, "tool"))
	home := filepath.Join(root, "home") // not a git repo
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}

	me := func(dir, tab string) *Pane { return &Pane{ID: "x:me", Folder: dir, Tab: tab, IsSelf: true} }
	pane := func(id, dir, tab string) *Pane { return &Pane{ID: "x:" + id, Folder: dir, Tab: tab} }
	owns := func(ids ...string) func(*Pane) bool {
		return func(p *Pane) bool {
			for _, id := range ids {
				if p.ID == "x:"+id {
					return true
				}
			}
			return false
		}
	}

	cases := []struct {
		name   string
		panes  []*Pane
		owned  func(*Pane) bool
		cwd    string
		where  string
		anchor string // ID of the pane split, or beside whose tab the new tab opens
	}{
		{"same project, alone in the tab", []*Pane{me(app, "t1")}, nil, app, "split", "x:me"},
		{"same project: split the newest pane, not the caller", []*Pane{me(app, "t1"), pane("w1", app, "t1")}, nil, app, "split", "x:w1"},
		{"same project, tab full", []*Pane{me(app, "t1"), pane("a", tool, "t1"), pane("b", tool, "t1")}, nil, app, "tab", "x:me"},
		{"panes in other tabs do not count", []*Pane{me(app, "t1"), pane("a", tool, "t2"), pane("b", tool, "t2")}, nil, app, "split", "x:me"},
		{"another project", []*Pane{me(app, "t1")}, nil, tool, "tab", "x:me"},
		{"no caller pane", []*Pane{pane("a", app, "t1")}, nil, app, "tab", ""},
		{"backend without tabs", []*Pane{me(app, ""), pane("a", tool, "")}, nil, app, "split", "x:me"},
		{"caller outside the project joins its own worker's tab",
			[]*Pane{me(home, "t1"), pane("w1", app, "t2")}, owns("w1"), app, "split", "x:w1"},
		{"a worker of someone else is left alone",
			[]*Pane{me(home, "t1"), pane("w1", app, "t2")}, owns(), app, "tab", "x:me"},
		{"owned worker in another project is left alone",
			[]*Pane{me(home, "t1"), pane("w1", tool, "t2")}, owns("w1"), app, "tab", "x:me"},
		{"caller tab full: continue in an owned tab with room",
			[]*Pane{me(app, "t1"), pane("w1", app, "t1"), pane("w2", app, "t1"), pane("w3", app, "t2")}, owns("w1", "w2", "w3"), app, "split", "x:w3"},
		{"every tab full: new tab beside the caller",
			[]*Pane{me(app, "t1"), pane("w1", app, "t1"), pane("w2", app, "t1"), pane("w3", app, "t2"), pane("w4", app, "t2"), pane("w5", app, "t2")},
			owns("w1", "w2", "w3", "w4", "w5"), app, "tab", "x:me"},
	}
	for _, c := range cases {
		where, anchor := PlaceIn(c.panes, c.cwd, c.owned)
		got := ""
		if anchor != nil {
			got = anchor.ID
		}
		if where != c.where || got != c.anchor {
			t.Errorf("%s: PlaceIn = %s at %q, want %s at %q", c.name, where, got, c.where, c.anchor)
		}
	}
}

type failingLister struct{ Backend }

func (failingLister) Name() string                { return "iterm2" }
func (failingLister) ListPanes() ([]*Pane, error) { return nil, BackendErr("a session closed") }

// Placement is cosmetic: a pane list that fails must not fail the spawn.
func TestPlaceFallsBackToTabWhenListingFails(t *testing.T) {
	where, err := Place(failingLister{}, "auto", "/")
	if err != nil || where != "tab" {
		t.Fatalf("Place = %s, %v; want tab, nil", where, err)
	}
}

func TestPlaceOnHcIsTab(t *testing.T) {
	if where, err := Place(hcNamed{}, "auto", "/"); err != nil || where != "tab" {
		t.Fatalf("Place = %s, %v", where, err)
	}
}

type hcNamed struct{ Backend }

func (hcNamed) Name() string { return "hc" }

func TestPlacePassesExplicitWhere(t *testing.T) {
	for _, w := range []string{"tab", "split", "window"} {
		if got, err := Place(nil, w, "/"); err != nil || got != w {
			t.Errorf("Place(%s) = %s, %v", w, got, err)
		}
	}
}

func TestCheckWhereAcceptsAuto(t *testing.T) {
	if err := CheckWhere("auto"); err != nil {
		t.Fatal(err)
	}
}
