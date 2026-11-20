package term

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testSocket = "termctl-test-go"

func tmuxCmd(args ...string) (string, error) {
	out, err := exec.Command("tmux", append([]string{"-L", testSocket}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func tmuxRig(t *testing.T) *TmuxBackend {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tmuxCmd("kill-server")
	if out, err := tmuxCmd("new-session", "-d", "-s", "base", "-x", "160", "-y", "40"); err != nil {
		t.Fatal(out)
	}
	t.Cleanup(func() { tmuxCmd("kill-server") })
	return &TmuxBackend{Socket: testSocket}
}

func waitLines(t *testing.T, b *TmuxBackend, p *Pane, ok func([]string) bool) {
	t.Helper()
	var lines []string
	for i := 0; i < 100; i++ {
		lines, _ = b.Capture(p, 20)
		if ok(lines) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("screen never matched: %q", lines)
}

func countEq(xs []string, s string) int {
	n := 0
	for _, x := range xs {
		if x == s {
			n++
		}
	}
	return n
}

func TestTmuxSpawnSendCaptureClose(t *testing.T) {
	b := tmuxRig(t)
	p, err := b.Spawn("tab", "cat", t.TempDir(), "cat-pane")
	if err != nil || !strings.HasPrefix(p.ID, "tmux:%") {
		t.Fatal(p, err)
	}
	text := "hello-literal $HOME"
	b.SendText(p, text)
	b.SendKey(p, "enter")
	waitLines(t, b, p, func(l []string) bool { return countEq(l, text) >= 2 })
	if err := b.Close(p); err != nil {
		t.Fatal(err)
	}
	panes, _ := b.ListPanes()
	for _, x := range panes {
		if x.ID == p.ID {
			t.Fatal("pane still there")
		}
	}
}

func TestTmuxListLabelTitleFolder(t *testing.T) {
	b := tmuxRig(t)
	dir := t.TempDir()
	p, _ := b.Spawn("tab", "sleep 30", dir, "sleeper")
	if err := b.SetLabel(p, "worker-1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	panes, _ := b.ListPanes()
	for _, x := range panes {
		if x.ID == p.ID {
			if x.Label != "worker-1" || x.Title != "sleeper" || !strings.HasSuffix(x.Folder, dir[strings.LastIndex(dir, "/"):]) || x.Process != "sleep" {
				t.Fatalf("%+v", x)
			}
			if err := b.SetTitle(x, "WORKER · t1 · renamed"); err != nil {
				t.Fatal(err)
			}
			after, _ := b.ListPanes()
			for _, y := range after {
				if y.ID == p.ID {
					if y.Title != "WORKER · t1 · renamed" {
						t.Fatalf("after SetTitle: got %q, want %q", y.Title, "WORKER · t1 · renamed")
					}
					return
				}
			}
			t.Fatal("missing after SetTitle")
		}
	}
	t.Fatal("not listed")
}

func TestTmuxWindowAndSplit(t *testing.T) {
	b := tmuxRig(t)
	panes, _ := b.ListPanes()
	b.SelfOverride = panes[0]
	s, err := b.Spawn("split", "sleep 30", "", "")
	if err != nil {
		t.Fatal(err)
	}
	w, err := b.Spawn("window", "sleep 30", "", "neww")
	if err != nil {
		t.Fatal(err)
	}
	panes, _ = b.ListPanes()
	ids := map[string]bool{}
	for _, x := range panes {
		ids[x.ID] = true
	}
	if !ids[s.ID] || !ids[w.ID] {
		t.Fatal(ids)
	}
}

func TestTmuxTextIsLiteral(t *testing.T) {
	b := tmuxRig(t)
	// tmux treats a final ';' as a command separator; the backend must escape it.
	for _, text := range []string{"Enter C-c ;", `path\;`} {
		p, _ := b.Spawn("tab", "cat", "", "")
		if err := b.SendText(p, text); err != nil {
			t.Fatal(err)
		}
		waitLines(t, b, p, func(l []string) bool { return strings.Contains(strings.Join(l, "\n"), text) })
	}
}

// The combos reach the program as the bytes a terminal sends: cat -v prints them in caret notation.
func TestTmuxComboKeysReachTheProgram(t *testing.T) {
	b := tmuxRig(t)
	p, err := b.Spawn("tab", "cat -v", "", "")
	if err != nil {
		t.Fatal(err)
	}
	keys, err := CheckKeys(b, []string{"alt+m", "shift-tab", "F5", "home", "enter"})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if err := b.SendKey(p, k); err != nil {
			t.Fatal(k, err)
		}
	}
	waitLines(t, b, p, func(l []string) bool {
		for _, ln := range l {
			if ln == "^[m^[[Z^[[15~^[[1~" || ln == "^[m^[[Z^[[15~^[[H" {
				return true
			}
		}
		return false
	})
}

func TestTmuxKeys(t *testing.T) {
	b := tmuxRig(t)
	p, _ := b.Spawn("tab", "cat", "", "")
	if err := b.SendKey(p, "escape"); err != nil {
		t.Fatal(err)
	}
	if err := b.SendKey(p, "nonsense-key"); err == nil {
		t.Fatal("unknown key accepted")
	}
	b.SendKey(p, "ctrl-c") // ends cat, so the pane goes away
	for i := 0; i < 100; i++ {
		panes, _ := b.ListPanes()
		gone := true
		for _, x := range panes {
			gone = gone && x.ID != p.ID
		}
		if gone {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("ctrl-c did not end cat")
}

func TestTmuxNoServer(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	b := &TmuxBackend{Socket: "termctl-test-go-none"}
	exec.Command("tmux", "-L", b.Socket, "kill-server").Run()
	if b.Available() {
		t.Fatal("no server, but available")
	}
	panes, err := b.ListPanes()
	if err != nil || len(panes) != 0 {
		t.Fatal(panes, err)
	}
	if _, err := b.Capture(&Pane{Native: "%0", ID: "tmux:%0"}, 5); err == nil {
		t.Fatal("capture on a missing server must fail")
	}
}

func TestTmuxServerRefReopensTheSameServer(t *testing.T) {
	b := tmuxRig(t)
	p, _ := b.Spawn("tab", "sleep 30", "", "reach")
	ref := b.ServerRef()
	if ref["socket_path"] == "" || ref["pid"] == "" {
		t.Fatal(ref)
	}
	again := &TmuxBackend{SocketPath: ref["socket_path"]}
	panes, _ := again.ListPanes()
	found := false
	for _, x := range panes {
		found = found || x.ID == p.ID
	}
	if !found || again.ServerRef()["pid"] != ref["pid"] {
		t.Fatal(panes, again.ServerRef())
	}
}

// tmuxInfo reads a pane's window, process and geometry from tmux itself.
func tmuxInfo(t *testing.T, id string) (window, pid string, left, top, width, height int) {
	t.Helper()
	out, err := tmuxCmd("display-message", "-p", "-t", id, "#{window_id} #{pane_pid} #{pane_left} #{pane_top} #{pane_width} #{pane_height}")
	if err != nil {
		t.Fatal(out)
	}
	f := strings.Fields(out)
	n := func(s string) int { v, _ := strconv.Atoi(s); return v }
	return f[0], f[1], n(f[2]), n(f[3]), n(f[4]), n(f[5])
}

func listed(t *testing.T, b *TmuxBackend, id string) (*Pane, []*Pane) {
	t.Helper()
	panes, err := b.ListPanes()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range panes {
		if p.ID == id {
			return p, panes
		}
	}
	t.Fatalf("%s is gone", id)
	return nil, nil
}

// Every move keeps the pane's %id, its process and its label; only its window changes.
func TestTmuxMoveBreakSwapArrangeKeepTheProcess(t *testing.T) {
	b := tmuxRig(t)
	x, _ := b.Spawn("tab", "sleep 300", "", "x")
	y, _ := b.Spawn("tab", "sleep 300", "", "y")
	if err := b.SetLabel(x, "mover"); err != nil {
		t.Fatal(err)
	}
	winX, pid, _, _, _, _ := tmuxInfo(t, x.Native)
	winY, pidY, _, _, _, _ := tmuxInfo(t, y.Native)
	same := func(step string) {
		t.Helper()
		if _, p, _, _, _, _ := tmuxInfo(t, x.Native); p != pid {
			t.Fatalf("%s: pid %s, want %s", step, p, pid)
		}
		if _, p, _, _, _, _ := tmuxInfo(t, y.Native); p != pidY {
			t.Fatalf("%s: y's pid %s, want %s", step, p, pidY)
		}
		if p, _ := listed(t, b, x.ID); p.Label != "mover" || p.Process != "sleep" {
			t.Fatalf("%s: %+v", step, p)
		}
	}

	p, panes := listed(t, b, x.ID)
	if _, err := Move(b, panes, p, "tab-of:"+y.ID); err != nil {
		t.Fatal(err)
	}
	if w, _, _, _, _, _ := tmuxInfo(t, x.Native); w != winY {
		t.Fatalf("x is in %s, want %s", w, winY)
	}
	if out, _ := tmuxCmd("list-windows", "-a", "-F", "#{window_id}"); strings.Contains(out, winX) {
		t.Fatal("the emptied window stayed open")
	}
	same("move")

	p, panes = listed(t, b, x.ID)
	m, err := Move(b, panes, p, "new")
	if err != nil || m.Pane.Tab == winY {
		t.Fatal(err, m)
	}
	idx := func(w string) string {
		out, _ := tmuxCmd("display-message", "-p", "-t", w, "#{window_index}")
		return out
	}
	if a, b := idx(winY), idx(m.Pane.Tab); b != fmt.Sprint(atoi(a)+1) {
		t.Fatalf("new window at index %s, want right after %s", b, a)
	}
	same("break")
	winNew := m.Pane.Tab

	p, _ = listed(t, b, x.ID)
	q, _ := listed(t, b, y.ID)
	if _, err := Swap(b, p, q); err != nil {
		t.Fatal(err)
	}
	if w, _, _, _, _, _ := tmuxInfo(t, x.Native); w != winY {
		t.Fatalf("after the swap x is in %s, want %s", w, winY)
	}
	if w, _, _, _, _, _ := tmuxInfo(t, y.Native); w != winNew {
		t.Fatalf("after the swap y is in %s, want %s", w, winNew)
	}
	same("swap")

	// four panes in x's window, then each layout
	p, _ = listed(t, b, x.ID)
	for i := 0; i < 3; i++ {
		if _, err := b.SpawnAt(p, "split", "sleep 300", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	geo := func() [][4]int {
		out, _ := tmuxCmd("list-panes", "-t", winY, "-F", "#{pane_left} #{pane_top} #{pane_width} #{pane_height}")
		var g [][4]int
		for _, line := range strings.Split(out, "\n") {
			var r [4]int
			fmt.Sscan(line, &r[0], &r[1], &r[2], &r[3])
			g = append(g, r)
		}
		return g
	}
	_, panes = listed(t, b, x.ID)
	for _, layout := range []string{"columns", "rows", "tiled", "even"} {
		if _, err := Arrange(b, panes, "tab-of:"+x.ID, layout, ""); err != nil {
			t.Fatal(layout, err)
		}
		for _, g := range geo() {
			if (layout == "columns" && g[1] != 0) || (layout == "rows" && g[0] != 0) || ((layout == "tiled" || layout == "even") && g[2] < 70) {
				t.Fatalf("%s: %v", layout, geo())
			}
		}
	}
	if _, err := Arrange(b, panes, winY, "main-left", x.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, left, top, w, _ := tmuxInfo(t, x.Native); left != 0 || top != 0 || w < 75 || w > 85 {
		t.Fatalf("main-left: x at %d,%d width %d, want the left half of 160", left, top, w)
	}
	same("arrange")
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }
