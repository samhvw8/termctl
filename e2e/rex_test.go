package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rexRig is a private Rex server: its own socket, state and config under a short /tmp folder, never
// the user's. Every rex call names that socket with -S and passes --autostart=false, and
// REX_RUNTIME_DIR points into the folder, so a call that forgot -S still cannot reach or start
// the user's server.
type rexRig struct {
	t                 *testing.T
	bin, dir, addr    string
	env               []string
	session           string
	caller, other     string // blocks: the one termctl runs in, and one in a second window
	callerWin, other2 string // windows of caller and other
}

const rexAppCLI = "Library/Application Support/com.superlogical.rex/RexServer/Versions/Current/Rex Server.app/Contents/MacOS/rex"

func rexCLI() string {
	if d := os.Getenv("REX_BIN_DIR"); d != "" {
		if _, err := os.Stat(filepath.Join(d, "rex")); err == nil {
			return filepath.Join(d, "rex")
		}
	}
	if p, err := exec.LookPath("rex"); err == nil {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		if _, err := os.Stat(filepath.Join(home, rexAppCLI)); err == nil {
			return filepath.Join(home, rexAppCLI)
		}
	}
	return ""
}

// shortTmp is where private servers keep their sockets: a Unix socket path is limited to about 104
// bytes. TERMCTL_TEST_SHORT_DIR overrides /tmp (say, a short symlink to a scratch folder).
func shortTmp() string {
	if d := os.Getenv("TERMCTL_TEST_SHORT_DIR"); d != "" {
		return d
	}
	return "/tmp"
}

func newRexRig(t *testing.T) *rexRig {
	t.Helper()
	if os.Getenv("TERMCTL_REX_TEST") != "1" {
		t.Skip("set TERMCTL_REX_TEST=1")
	}
	bin := rexCLI()
	if bin == "" {
		t.Skip("the Rex CLI is not installed")
	}
	r := startRexServer(t, bin)
	r.addBlocks()
	return r
}

// startRexServer starts a private Rex server and stops it when the test ends.
func startRexServer(t *testing.T, bin string) *rexRig {
	t.Helper()
	dir, err := os.MkdirTemp(shortTmp(), "rxt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "init.lua"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := &rexRig{t: t, bin: bin, dir: dir, addr: "unix://" + filepath.Join(dir, "s")}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "REX_") || strings.HasPrefix(name, "ITERM") || strings.HasPrefix(name, "TMUX") ||
			strings.HasPrefix(name, "HC_") || name == "TERM_SESSION_ID" || name == "CLAUDE_CODE_CHILD_SESSION" || name == "SHELL" {
			continue
		}
		r.env = append(r.env, kv)
	}
	r.env = append(r.env, "REX_RUNTIME_DIR="+filepath.Join(dir, "run"), "SHELL=/bin/sh")
	server := exec.Command(bin, "server", "run", "--listen", r.addr, "--state-dir", filepath.Join(dir, "state"),
		"--restore=false", "--config", filepath.Join(dir, "init.lua"))
	server.Env = r.env
	logf, err := os.Create(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	server.Stdout, server.Stderr = logf, logf
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Wait() }()
	t.Cleanup(func() {
		out, err := r.rexErr("server", "stop")
		t.Logf("rex server stop: %s err=%v", strings.TrimSpace(out), err)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			server.Process.Kill()
			<-done
			t.Errorf("the private Rex server did not stop; killed it")
		}
		logf.Close()
		if _, err := os.Stat(filepath.Join(dir, "s")); err == nil {
			t.Errorf("the private server's socket is still there")
		}
		os.RemoveAll(dir)
	})
	waitFor(t, "the private Rex server", func() bool { _, err := r.rexErr("whoami"); return err == nil })
	return r
}

// addBlocks makes a session with the caller's block and a block in a second window, both running sleep.
func (r *rexRig) addBlocks() {
	t := r.t
	t.Helper()
	var created struct {
		SessionID      string `json:"session_id"`
		InitialWindows []struct {
			WindowID string   `json:"window_id"`
			BlockIDs []string `json:"block_ids"`
		} `json:"initial_windows"`
	}
	json.Unmarshal([]byte(r.rex("new", "--json", "termctl-test", "--shell=none", "--", "sleep", "900")), &created)
	r.session = created.SessionID
	r.caller, r.callerWin = uuid(created.InitialWindows[0].BlockIDs[0]), uuid(created.InitialWindows[0].WindowID)
	var win struct {
		WindowID string   `json:"window_id"`
		BlockIDs []string `json:"block_ids"`
	}
	json.Unmarshal([]byte(r.rex("window", "new", "-s", r.session, "--focus=false", "--json", "--shell=none", "--", "sleep", "900")), &win)
	r.other, r.other2 = uuid(win.BlockIDs[0]), uuid(win.WindowID)
	r.rex("block", "rename", r.caller, "caller")
	r.rex("block", "rename", r.other, "other")
}

func uuid(id string) string { _, u, _ := strings.Cut(id, ":"); return u }

func (r *rexRig) rexErr(args ...string) (string, error) {
	cmd := exec.Command(r.bin, append([]string{"-S", r.addr, "--autostart=false"}, args...)...)
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *rexRig) rex(args ...string) string {
	r.t.Helper()
	out, err := r.rexErr(args...)
	if err != nil {
		r.t.Fatalf("rex %v: %v: %s", args, err, out)
	}
	return out
}

// run runs termctl or harness-ops as if from the caller's block.
func (r *rexRig) run(prog string, args ...string) (int, string, string) {
	r.t.Helper()
	cmd := exec.Command(filepath.Join(binDir, prog), append([]string{"--backend", "rex"}, args...)...)
	cmd.Env = append(append([]string{}, r.env...), "REX_SERVER="+r.addr, "REX_SESSION="+r.session, "REX_BLOCK=block:"+r.caller,
		"TERMCTL_STATE_DIR="+filepath.Join(r.dir, "termctl-state"), "PATH="+binDir+":"+os.Getenv("PATH"))
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		r.t.Fatal(err)
	}
	return code, stdout.String(), stderr.String()
}

func (r *rexRig) ok(prog string, args ...string) string {
	r.t.Helper()
	code, out, stderr := r.run(prog, args...)
	if code != 0 {
		r.t.Fatalf("%s %v: exit %d: %s%s", prog, args, code, out, stderr)
	}
	return out
}

func (r *rexRig) pid(block string) int {
	r.t.Helper()
	var p struct {
		Child struct {
			Pid int `json:"pid"`
		} `json:"child"`
	}
	json.Unmarshal([]byte(r.rex("block", "call", "-b", block, "process")), &p)
	if p.Child.Pid == 0 {
		r.t.Fatalf("block %s runs nothing", block)
	}
	return p.Child.Pid
}

type rexNode struct {
	BlockID string    `json:"block_id"`
	Rect    []float64 `json:"rect"`
	Split   *struct {
		Direction string   `json:"direction"`
		Before    *rexNode `json:"before"`
		After     *rexNode `json:"after"`
	} `json:"split"`
}

func (n *rexNode) leaves() []*rexNode {
	if n == nil {
		return nil
	}
	if n.Split == nil {
		return []*rexNode{n}
	}
	return append(n.Split.Before.leaves(), n.Split.After.leaves()...)
}

type rexWin struct {
	id     string
	blocks []string
	root   *rexNode
}

// windows lists the session's windows in order, with their blocks in layout order.
func (r *rexRig) windows() []rexWin {
	r.t.Helper()
	var v struct {
		Windows []struct {
			WindowID string `json:"window_id"`
			Layers   []struct {
				Layout *rexNode `json:"layout"`
			} `json:"layers"`
		} `json:"windows"`
	}
	args, _ := json.Marshal(map[string]string{"session_id": r.session})
	if err := json.Unmarshal([]byte(r.rex("api", "call", "session.view", string(args))), &v); err != nil {
		r.t.Fatal(err)
	}
	var out []rexWin
	for _, w := range v.Windows {
		win := rexWin{id: uuid(w.WindowID), root: w.Layers[0].Layout}
		for _, l := range win.root.leaves() {
			win.blocks = append(win.blocks, uuid(l.BlockID))
		}
		out = append(out, win)
	}
	return out
}

// layout is the windows and their blocks, in order, as text.
func (r *rexRig) layout() string {
	var b strings.Builder
	for _, w := range r.windows() {
		fmt.Fprintf(&b, "%s:%v ", w.id, w.blocks)
	}
	return b.String()
}

func (r *rexRig) windowOf(block string) (int, rexWin) {
	r.t.Helper()
	for i, w := range r.windows() {
		for _, b := range w.blocks {
			if b == block {
				return i, w
			}
		}
	}
	r.t.Fatalf("block %s is in no window: %+v", block, r.windows())
	return 0, rexWin{}
}

func (r *rexRig) native(out string) string {
	r.t.Helper()
	id := strings.TrimSpace(out)
	if !strings.HasPrefix(id, "rex:") {
		r.t.Fatalf("spawn printed %q", out)
	}
	return strings.TrimPrefix(id, "rex:")
}

// Every Rex method on a real (private) Rex server: ls, spawn in all three places, send, capture,
// move, pull, swap, arrange and close, through the real termctl and harness-ops. Each block that
// moves keeps its process.
func TestRexBackend(t *testing.T) {
	r := newRexRig(t)

	// ls: both blocks, the caller marked, each with its window
	var rows []map[string]any
	json.Unmarshal([]byte(r.ok("termctl", "ls", "--json")), &rows)
	tabs := map[string]string{}
	for _, row := range rows {
		tabs[row["native"].(string)], _ = row["tab"].(string)
		if row["native"] == r.caller && row["is_self"] != true {
			t.Fatalf("the caller is not marked: %v", row)
		}
	}
	if tabs[r.caller] != r.callerWin[len(r.callerWin)-6:] || tabs[r.other] != r.other2[len(r.other2)-6:] {
		t.Fatalf("tabs %v", tabs)
	}
	if out := r.ok("termctl", "whoami"); !strings.Contains(out, r.caller) {
		t.Fatal(out)
	}
	if out := r.ok("harness-ops", "ls"); !strings.Contains(out, r.other) {
		t.Fatal(out)
	}

	// spawn: split (into the caller's window), tab (right after it), window (a new session)
	w1 := r.native(r.ok("termctl", "spawn", "--where", "split", "--title", "w1", "sleep", "901"))
	if _, w := r.windowOf(w1); w.id != r.callerWin {
		t.Fatalf("split went to %s, want the caller's window %s", w.id, r.callerWin)
	}
	w2 := r.native(r.ok("termctl", "spawn", "--where", "tab", "--title", "w2", "cat"))
	if i, _ := r.windowOf(w2); i != 1 {
		t.Fatalf("the new window is at %d, want right after the caller's: %+v", i, r.windows())
	}
	w3 := r.native(r.ok("termctl", "spawn", "--where", "window", "--title", "w3", "sleep", "903"))
	if out := r.ok("termctl", "ls"); !strings.Contains(out, w3) {
		t.Fatal(out)
	}
	r.ok("termctl", "close", "w3")

	// send and capture: cat shows what it reads
	waitFor(t, "cat to start", func() bool {
		var p struct {
			Foreground struct{ Name string } `json:"foreground"`
		}
		json.Unmarshal([]byte(r.rex("block", "call", "-b", w2, "process")), &p)
		return p.Foreground.Name == "cat"
	})
	r.ok("termctl", "send-text", "w2", "-hello rex")
	r.ok("termctl", "send-key", "w2", "enter")
	waitFor(t, "the echo", func() bool {
		_, out, _ := r.run("termctl", "capture", "w2", "-n", "5")
		return strings.Count(out, "-hello rex") == 2
	})

	pids := map[string]int{}
	for _, b := range []string{r.caller, r.other, w1, w2} {
		pids[b] = r.pid(b)
	}
	check := func(step string) {
		t.Helper()
		for b, want := range pids {
			if got := r.pid(b); got != want {
				t.Fatalf("%s: block %s pid %d, want %d", step, b, got, want)
			}
		}
	}

	// move out to a new window, right after its own; pull it back
	out := r.ok("termctl", "move", "w1", "--to", "new")
	if i, w := r.windowOf(w1); i != 1 || len(w.blocks) != 1 || !strings.Contains(out, "new tab "+w.id[len(w.id)-6:]) {
		t.Fatalf("move --to new: %q, window %d %+v", out, i, w)
	}
	check("move --to new")
	r.ok("harness-ops", "pull", "w1")
	if _, w := r.windowOf(w1); w.id != r.callerWin {
		t.Fatalf("pull left w1 in %s", w.id)
	}
	check("pull")

	// move into another window by tab-of, then swap across windows
	r.ok("termctl", "move", "other", "--to", "tab-of:w2")
	_, w2win := r.windowOf(w2)
	if _, w := r.windowOf(r.other); w.id != w2win.id {
		t.Fatalf("other is in %s, want w2's %s", w.id, w2win.id)
	}
	check("move --to tab-of")
	r.ok("termctl", "swap", "other", "w1")
	if _, w := r.windowOf(r.other); w.id != r.callerWin {
		t.Fatalf("after the swap other is in %s", w.id)
	}
	if _, w := r.windowOf(w1); w.id != w2win.id {
		t.Fatalf("after the swap w1 is in %s", w.id)
	}
	check("swap")

	// layouts of the caller's window (caller and other)... plus w2 for three blocks
	r.ok("termctl", "move", "w2", "--to", "self")
	r.ok("termctl", "arrange", "--layout", "main-left")
	_, w := r.windowOf(r.caller)
	if w.root.Split == nil || w.root.Split.Direction != "horizontal" || w.root.Split.Before.BlockID != "block:"+r.caller {
		t.Fatalf("main-left: %+v", w.root)
	}
	r.ok("harness-ops", "arrange", "--layout", "rows")
	_, w = r.windowOf(r.caller)
	for _, l := range w.root.leaves() {
		if l.Rect[0] != 0 || l.Rect[2] != 1 || l.Rect[3] < 0.33 || l.Rect[3] > 0.34 {
			t.Fatalf("rows: %v is not a third of the height across the window", l.Rect)
		}
	}
	r.ok("termctl", "arrange", "--layout", "even")
	check("arrange")

	// a move between sessions is refused, and nothing changes
	other := r.native(r.ok("termctl", "spawn", "--where", "window", "--title", "far", "sleep", "904"))
	before := r.layout()
	if code, _, stderr := r.run("termctl", "move", "far", "--to", "self"); code != 1 || !strings.Contains(stderr, "different sessions") {
		t.Fatalf("move between sessions: exit %d %q", code, stderr)
	}
	if r.layout() != before {
		t.Fatal("a refused move changed the layout")
	}
	r.ok("termctl", "close", "rex:"+other)

	// a harness task: harness-ops starts the fake harness beside the caller, briefs it and messages it
	workerLog := filepath.Join(r.dir, "worker.log")
	out = r.ok("harness-ops", "task", "new", "generic", "--title", "worker", "--brief", "stay alive", "--cwd", r.dir, "--command", fakeCmd(workerLog), "--timeout", "30")
	task := strings.TrimSpace(out[strings.LastIndex(strings.TrimSpace(out), "\n")+1:])
	waitFor(t, "brief", func() bool { return len(readLog(workerLog)) > 0 })
	r.ok("harness-ops", "msg", task, "still", "there")
	waitFor(t, "message", func() bool {
		lines := readLog(workerLog)
		return len(lines) > 1 && strings.HasSuffix(lines[len(lines)-1], "still there")
	})
	r.ok("harness-ops", "move", task, "--to", "new")
	r.ok("harness-ops", "msg", task, "moved")
	waitFor(t, "message after the move", func() bool {
		lines := readLog(workerLog)
		return strings.HasSuffix(lines[len(lines)-1], "moved")
	})
	r.ok("termctl", "close", task)

	// close
	r.ok("termctl", "close", "w2")
	delete(pids, w2)
	waitFor(t, "w2 to close", func() bool { _, out, _ := r.run("termctl", "ls"); return !strings.Contains(out, w2) })
	check("close")
}
