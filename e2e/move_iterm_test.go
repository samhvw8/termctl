package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samhvw8/termctl/internal/term"
)

// TestMoveOnITerm2 is opt-in (TERMCTL_ITERM_TEST=1). It opens two scratch tabs of long sleeps and
// acts only on the sessions it created, by full ID: move to another tab, move to a new tab, pull,
// swap and every layout. Each sleep must keep its PID. Everything it opened is closed at the end.
func TestMoveOnITerm2(t *testing.T) {
	if os.Getenv("TERMCTL_ITERM_TEST") != "1" {
		t.Skip("set TERMCTL_ITERM_TEST=1")
	}
	tmp := t.TempDir()
	b, err := term.Make("iterm2", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var made []*term.Pane
	t.Cleanup(func() {
		for _, p := range made {
			b.Close(p)
		}
	})
	spawn := func(at *term.Pane, where, name string) *term.Pane {
		t.Helper()
		var p *term.Pane
		var err error
		if at == nil {
			p, err = b.Spawn(where, "sleep 900", tmp, name)
		} else {
			p, err = b.(term.Anchored).SpawnAt(at, where, "sleep 900", tmp, name)
		}
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, p)
		return p
	}
	a1 := spawn(nil, "tab", "termctl-move-a1") // scratch tab A
	a2 := spawn(a1, "split", "termctl-move-a2")
	b1 := spawn(a1, "tab", "termctl-move-b1") // scratch tab B

	now := func(p *term.Pane) *term.Pane {
		t.Helper()
		panes, err := b.ListPanes()
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range panes {
			if q.ID == p.ID {
				return q
			}
		}
		t.Fatalf("%s is gone", p.ID)
		return nil
	}
	tabPanes := func(tab string) []*term.Pane {
		panes, _ := b.ListPanes()
		return term.TabPanes(panes, tab)
	}
	sleepPID := func(p *term.Pane) string {
		t.Helper()
		tty := now(p).TTY
		for i := 0; i < 50; i++ {
			out, _ := exec.Command("ps", "-t", tty, "-o", "pid=,comm=").Output()
			for _, line := range strings.Split(string(out), "\n") {
				if f := strings.Fields(line); len(f) == 2 && f[1] == "sleep" {
					return f[0]
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("no sleep on %s (%s)", p.ID, tty)
		return ""
	}
	pids := map[string]string{}
	for _, p := range []*term.Pane{a1, a2, b1} {
		pids[p.ID] = sleepPID(p)
	}
	samePIDs := func(step string) {
		t.Helper()
		for _, p := range []*term.Pane{a1, a2, b1} {
			if got := sleepPID(p); got != pids[p.ID] {
				t.Fatalf("%s: %s runs sleep %s, want %s", step, p.ID, got, pids[p.ID])
			}
		}
	}
	run := func(bin string, args ...string) string {
		t.Helper()
		cmd := exec.Command(filepath.Join(binDir, bin), append([]string{"--backend", "iterm2"}, args...)...)
		// The caller is scratch session a1, so self and pull stay inside the scratch tabs.
		cmd.Env = append(os.Environ(), "TERMCTL_STATE_DIR="+filepath.Join(tmp, "state"), "ITERM_SESSION_ID=w0t0p0:"+a1.Native)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %s %v", bin, args, out, err)
		}
		if args[0] != "ls" { // ls lists every session, not only the scratch ones
			t.Logf("%s %v: %s", bin, args, strings.TrimSpace(string(out)))
		}
		return string(out)
	}
	tabA, tabB := now(a1).Tab, now(b1).Tab

	run("termctl", "move", a2.ID, "--to", "tab-of:"+b1.ID)
	if now(a2).Tab != tabB {
		t.Fatalf("a2 is in tab %s, want %s", now(a2).Tab, tabB)
	}
	samePIDs("move --to tab-of")

	run("termctl", "move", a2.ID, "--to", "new")
	if tab := now(a2).Tab; tab == tabA || tab == tabB || len(tabPanes(tab)) != 1 {
		t.Fatalf("a2 is in tab %s with %d panes, want a new tab of its own", tab, len(tabPanes(tab)))
	}
	samePIDs("move --to new")

	run("harness-ops", "pull", a2.ID)
	if now(a2).Tab != tabA {
		t.Fatalf("pull left a2 in tab %s, want %s", now(a2).Tab, tabA)
	}
	samePIDs("pull")

	run("termctl", "swap", a2.ID, b1.ID)
	if now(a2).Tab != tabB || now(b1).Tab != tabA {
		t.Fatalf("after the swap a2 is in %s and b1 in %s", now(a2).Tab, now(b1).Tab)
	}
	samePIDs("swap")

	a3 := spawn(a1, "split", "termctl-move-a3") // tab A: a1, b1, a3
	pids[a3.ID] = sleepPID(a3)
	shape := func() (ws, hs []int) {
		for _, p := range tabPanes(tabA) {
			ws, hs = append(ws, p.Width), append(hs, p.Height)
		}
		return
	}
	near := func(xs []int) bool {
		for _, x := range xs {
			if x < xs[0]-2 || x > xs[0]+2 {
				return false
			}
		}
		return true
	}
	run("termctl", "arrange", "tab-of:"+a1.ID, "--layout", "columns")
	if ws, hs := shape(); !near(ws) || !near(hs) || len(ws) != 3 {
		t.Fatalf("columns: widths %v heights %v", ws, hs)
	}
	colW, _ := shape()
	run("termctl", "arrange", "tab-of:"+a1.ID, "--layout", "rows")
	if ws, hs := shape(); !near(ws) || !near(hs) || ws[0] < 2*colW[0] {
		t.Fatalf("rows: widths %v heights %v", ws, hs)
	}
	run("termctl", "arrange", "tab-of:"+a1.ID, "--layout", "tiled")
	run("termctl", "arrange", "tab-of:"+a1.ID, "--layout", "main-left", "--main", b1.ID)
	main := now(b1)
	for _, p := range tabPanes(tabA) {
		if p.ID != main.ID && (p.Width > main.Width+2 || p.Width < main.Width-2) {
			t.Fatalf("main-left: main is %dx%d, %s is %dx%d; want half the width each", main.Width, main.Height, p.ID, p.Width, p.Height)
		}
		if p.ID != main.ID && p.Height*2 > main.Height+2 {
			t.Fatalf("main-left: %s is %d tall, main %d; want the others stacked", p.ID, p.Height, main.Height)
		}
	}
	run("termctl", "arrange", "tab-of:"+a1.ID, "--layout", "main-top")
	run("termctl", "arrange", "tab-of:"+a1.ID, "--layout", "even")
	samePIDs("arrange")
	if got := sleepPID(a3); got != pids[a3.ID] {
		t.Fatalf("arrange: a3 runs sleep %s, want %s", got, pids[a3.ID])
	}
	out := run("termctl", "ls")
	if !strings.Contains(out, fmt.Sprintf("%-6s", tabA)) {
		t.Fatalf("ls does not show tab %s", tabA)
	}
}
