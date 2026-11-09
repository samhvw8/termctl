package term

import (
	"os/exec"
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
			return
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
