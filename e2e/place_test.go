package e2e

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func gitInit(t *testing.T, dir string) string {
	t.Helper()
	for _, args := range [][]string{{"init", "-q", dir}, {"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	return dir
}

// spawned returns the new pane's tmux ID from a spawn's output.
func spawned(t *testing.T, out string) string {
	t.Helper()
	line := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("no pane in spawn output: %q", out)
	}
	return strings.Fields(parts[1])[0]
}

func windowOf(t *testing.T, pane string) (id, index string) {
	t.Helper()
	f := strings.Fields(tmux(t, "display-message", "-p", "-t", pane, "#{window_id} #{window_index}"))
	return f[0], f[1]
}

// The default --where auto: the caller's project splits its window up to three panes, then a new
// window opens right after the caller's; another project always gets that new window.
func TestSpawnAutoPlacement(t *testing.T) {
	r := newRig(t)
	app := gitInit(t, filepath.Join(r.tmp, "app"))
	tool := gitInit(t, filepath.Join(r.tmp, "tool"))
	// The caller sits right after "base", so a window at the end of the session would not be beside it.
	caller := tmux(t, "new-window", "-d", "-a", "-t", "base:0", "-c", app, "-P", "-F", "#{pane_id}", "--", "sleep 300")
	callerWin, callerIdx := windowOf(t, caller)

	spawn := func(cwd, name string) string {
		t.Helper()
		code, out, stderr := r.ops(caller, "spawn", "generic", "--cwd", cwd, "--command", fakeCmd(filepath.Join(r.tmp, name+".log")))
		if code != 0 {
			t.Fatal(code, out, stderr)
		}
		return spawned(t, out)
	}

	for i, name := range []string{"same-1", "same-2"} {
		p := spawn(app, name)
		if w, _ := windowOf(t, p); w != callerWin {
			t.Fatalf("same-project spawn %d opened in window %s, want the caller's %s", i+1, w, callerWin)
		}
	}
	// The second agent split the first one, not the caller, so the caller keeps half the window.
	f := strings.Fields(tmux(t, "display-message", "-p", "-t", caller, "#{pane_width} #{window_width}"))
	pw, _ := strconv.Atoi(f[0])
	ww, _ := strconv.Atoi(f[1])
	if pw < ww/2-1 {
		t.Fatalf("caller pane is %d of %d columns: a later split took the caller's width", pw, ww)
	}
	full := spawn(app, "same-3")
	w, idx := windowOf(t, full)
	if w == callerWin {
		t.Fatal("a fourth pane split the caller's full window")
	}
	if want := next(t, callerIdx); idx != want {
		t.Fatalf("window for a full tab at index %s, want %s (right after the caller's)", idx, want)
	}

	other := spawn(tool, "other")
	w, idx = windowOf(t, other)
	if w == callerWin {
		t.Fatal("another project split the caller's window")
	}
	if want := next(t, callerIdx); idx != want {
		t.Fatalf("another project's window at index %s, want %s (right after the caller's)", idx, want)
	}
}

func next(t *testing.T, idx string) string {
	t.Helper()
	n, err := strconv.Atoi(idx)
	if err != nil {
		t.Fatal(err)
	}
	return strconv.Itoa(n + 1)
}
