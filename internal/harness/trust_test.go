package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/term"
	"github.com/samhvw8/termctl/internal/termtest"
)

func TestTrustRootsParseAndRefuseBroadPatterns(t *testing.T) {
	roots, err := ParseTrustRoots("f", "# worktrees only\n\n~/workspace/work/*/.claude/worktrees\n/srv/repos/a\n", "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(roots.Roots, ",") != "/home/u/workspace/work/*/.claude/worktrees,/srv/repos/a" {
		t.Fatalf("%q", roots.Roots)
	}
	for _, bad := range []string{"relative/dir", "~", "~/", "/", "/home/*", "/home", "/*", "[x"} {
		if _, err := ParseTrustRoots("f", bad, "/home/u"); !isCode(err, term.CodeUsage) {
			t.Errorf("%q was accepted: %v", bad, err)
		}
	}
}

func TestTrustRootsAllowOnlyFoldersUnderARoot(t *testing.T) {
	roots, _ := ParseTrustRoots("f", "~/w/*/.claude/worktrees", "/home/u")
	for dir, want := range map[string]bool{
		"/home/u/w/app/.claude/worktrees":             true,
		"/home/u/w/app/.claude/worktrees/tt1700":      true,
		"/home/u/w/app/.claude/worktrees/tt1700/sub/": true,
		"/home/u/w/app":                     false,
		"/home/u/w/app/.claude":             false,
		"/home/u/w/a/b/.claude/worktrees/x": false, // * is one folder name
		"/home/u/w/app/.claude/worktreesX":  false,
		"relative/.claude/worktrees":        false,
		"":                                  false,
	} {
		if got := roots.Allows(dir) != ""; got != want {
			t.Errorf("Allows(%q) = %v, want %v", dir, got, want)
		}
	}
	var none *TrustRoots
	if none.Allows("/home/u/w/app/.claude/worktrees") != "" {
		t.Fatal("no roots allowed a folder")
	}
}

// A folder reached through a symlink counts where it really is (macOS: /tmp is /private/tmp).
func TestTrustRootsResolveSymlinks(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real", "wt")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(base, "real"), link); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(filepath.Join(base, "real"))
	roots, err := ParseTrustRoots("f", resolved, "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if roots.Allows(filepath.Join(link, "wt")) == "" {
		t.Fatal("a symlinked path into a root was refused")
	}
}

func TestReadClaudeTrust(t *testing.T) {
	real := []string{ // Claude Code on tmux, 2026-11-18 (TestStartupPromptReadsTheRealTrustScreen)
		" Quick safety check: Is this a project you created or one you trust? (Like your",
		" own code, a well-known open source project, or work from your team). If not,",
		" take a moment to review what's in this folder first.",
		" Claude Code'll be able to read, edit, and execute files here.",
		" Security guide",
		" ❯ No, exit",
		"   Yes, I trust this folder",
		" Enter to confirm · Esc to cancel",
	}
	s := readClaudeTrust(real)
	if s == nil || s.YesSelected || s.Yes != s.No+1 {
		t.Fatalf("%+v", s)
	}
	yes := append([]string{}, real...)
	yes[5], yes[6] = "   No, exit", " ❯ Yes, I trust this folder"
	if s := readClaudeTrust(yes); s == nil || !s.YesSelected {
		t.Fatalf("%+v", s)
	}
	for name, lines := range map[string][]string{
		"no footer":      real[:7],
		"two marks":      append(append([]string{}, real[:5]...), " ❯ No, exit", " ❯ Yes, I trust this folder", real[7]),
		"mark elsewhere": append(append([]string{"❯ quoted"}, real[:5]...), "   No, exit", "   Yes, I trust this folder", real[7]),
		"swapped":        append(append([]string{}, real[:5]...), "   Yes, I trust this folder", " ❯ No, exit", real[7]),
		"input line":     {"❯ ", "? for shortcuts"},
		"quoted twice":   append(append([]string{}, real...), real[5:]...),
	} {
		if s := readClaudeTrust(lines); s != nil {
			t.Errorf("%s: read as a trust screen: %+v", name, s)
		}
	}
}

func trustRig(t *testing.T, roots string) (*rig, *TrustRoots) {
	t.Helper()
	r := newRig(t)
	tr, err := ParseTrustRoots("trust-roots", roots, "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	return r, tr
}

func keysOn(r *rig, pane string) []string {
	var out []string
	for _, k := range r.b.Typed(pane) {
		if strings.HasPrefix(k, "key:") {
			out = append(out, strings.TrimPrefix(k, "key:"))
		}
	}
	return out
}

// --accept-trust under a root: task new moves to "Yes, I trust this folder", checks it on screen, presses
// Enter, waits for the harness, and gives it the task. The journal records the folder and the root.
func TestAcceptTrustUnderARootStartsTheTask(t *testing.T) {
	r, roots := trustRig(t, "/w")
	var pane *term.Pane
	r.tasks.spawnHook = func(p *term.Pane) {
		pane = p
		r.b.State[p.ID].Trust = &termtest.Trust{Lag: 2}
	}
	x, err := r.new(t, nil, NewOpts{Harness: "claude", Trust: roots})
	if err != nil || x.Status != "running" {
		t.Fatalf("%+v %v", x, err)
	}
	if !r.b.State[pane.ID].Trust.Accepted {
		t.Fatal("the trust prompt was not accepted")
	}
	keys := strings.Join(keysOn(r, pane.ID), " ")
	if !strings.HasPrefix(keys, "down enter") || strings.ContainsAny(keys, "0123456789") {
		t.Fatalf("keys %q", keys)
	}
	entries, _, _ := (&Journal{Root: r.reg.Root}).Read(Filter{})
	found := false
	for _, e := range entries {
		if e.Event == "trust" {
			found = e.Error == "" && strings.Contains(e.Text, "/w (root /w)")
		}
	}
	if !found {
		t.Fatalf("no trust entry: %+v", entries)
	}
}

// Outside every root, or with no reader for the harness, the prompt stays for the user and no key is pressed.
func TestAcceptTrustRefusesOutsideTheRoots(t *testing.T) {
	for name, c := range map[string]struct {
		roots, harness, folder string
		code                   int
	}{
		"cwd outside":         {"/other", "claude", "", term.CodeGuard},
		"pane folder differs": {"/w", "claude", "/elsewhere", term.CodeGuard},
		"no reader (agy)":     {"/w", "agy", "", term.CodeTimeout},
	} {
		t.Run(name, func(t *testing.T) {
			r, roots := trustRig(t, c.roots)
			var pane *term.Pane
			r.tasks.spawnHook = func(p *term.Pane) {
				pane = p
				r.b.State[p.ID].Trust = &termtest.Trust{}
				if c.folder != "" {
					p.Folder = c.folder
				}
			}
			_, err := r.new(t, nil, NewOpts{Harness: c.harness, Trust: roots})
			wantCode(t, err, c.code)
			if k := keysOn(r, pane.ID); len(k) != 0 || !r.b.State[pane.ID].Trust.Waiting() {
				t.Fatalf("pressed %q", k)
			}
			if r.data().Created[pane.ID] == nil {
				t.Fatal("the pane was not kept for the user")
			}
		})
	}
}

// Without --accept-trust nothing changes: the prompt fails the start fast and stays for the user.
func TestNoTrustRootsLeaveThePromptToTheUser(t *testing.T) {
	r := newRig(t)
	var pane *term.Pane
	r.tasks.spawnHook = func(p *term.Pane) { pane = p; r.b.State[p.ID].Trust = &termtest.Trust{} }
	_, err := r.new(t, nil, NewOpts{Harness: "claude"})
	wantCode(t, err, term.CodeTimeout)
	if !strings.Contains(err.Error(), "trust this folder") || len(keysOn(r, pane.ID)) != 0 {
		t.Fatal(err, keysOn(r, pane.ID))
	}
}
