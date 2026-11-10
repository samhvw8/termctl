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

	me := func(tab string) *Pane { return &Pane{ID: "x:me", Folder: app, Tab: tab, IsSelf: true} }
	other := func(tab string) *Pane { return &Pane{ID: "x:o", Folder: tool, Tab: tab} }

	cases := []struct {
		name  string
		panes []*Pane
		cwd   string
		want  string
	}{
		{"same project, alone in the tab", []*Pane{me("t1")}, app, "split"},
		{"same project, two in the tab", []*Pane{me("t1"), other("t1")}, app, "split"},
		{"same project, tab full", []*Pane{me("t1"), other("t1"), other("t1")}, app, "tab"},
		{"panes in other tabs do not count", []*Pane{me("t1"), other("t2"), other("t2")}, app, "split"},
		{"another project", []*Pane{me("t1")}, tool, "tab"},
		{"no caller pane", []*Pane{other("t1")}, app, "tab"},
		{"backend without tabs", []*Pane{me(""), other("")}, app, "split"},
	}
	for _, c := range cases {
		if got := PlaceIn(c.panes, c.cwd); got != c.want {
			t.Errorf("%s: PlaceIn = %s, want %s", c.name, got, c.want)
		}
	}
}

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
