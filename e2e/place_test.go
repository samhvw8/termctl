package e2e

import (
	"fmt"
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

// The default --where auto: every new agent splits the caller's window, whatever its folder, up to
// twelve panes; then a new window opens right after the caller's. After each split tmux tiles the
// window, so twelve panes stay usable instead of twelve thin columns.
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

	for i := 1; i < 12; i++ {
		cwd := app
		if i%2 == 0 {
			cwd = tool // another repository joins the caller's window too
		}
		p := spawn(cwd, fmt.Sprintf("w%d", i))
		if w, _ := windowOf(t, p); w != callerWin {
			t.Fatalf("spawn %d opened in window %s, want the caller's %s", i, w, callerWin)
		}
	}
	sizes := strings.Fields(tmux(t, "list-panes", "-t", callerWin, "-F", "#{pane_width}x#{pane_height}"))
	if len(sizes) != 12 {
		t.Fatalf("caller's window has %d panes, want 12: %v", len(sizes), sizes)
	}
	minW, maxW, minH, maxH := 1<<30, 0, 1<<30, 0
	for _, sz := range sizes {
		var w, h int
		fmt.Sscanf(sz, "%dx%d", &w, &h)
		if w < 40 || h < 10 { // twelve side-by-side columns of a 200-column window would be 16 wide
			t.Fatalf("a pane is %s: the window was not tiled (%v)", sz, sizes)
		}
		minW, maxW, minH, maxH = min(minW, w), max(maxW, w), min(minH, h), max(maxH, h)
	}
	// tiled gives the leftover cells to the last row or column, so allow a third.
	if maxW > minW+minW/3 || maxH > minH+minH/3 {
		t.Fatalf("panes are uneven after tiling: %v", sizes)
	}

	full := spawn(tool, "w12")
	w, idx := windowOf(t, full)
	if w == callerWin {
		t.Fatal("a thirteenth pane split the caller's full window")
	}
	if want := next(t, callerIdx); idx != want {
		t.Fatalf("window for a full tab at index %s, want %s (right after the caller's)", idx, want)
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
