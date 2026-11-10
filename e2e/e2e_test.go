// Package e2e drives the real binaries against real terminals, with the fake harness.
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

	"github.com/samhvw8/termctl/internal/term"
)

const socket = "termctl-test-e2e"

var binDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "termctl-e2e")
	if err != nil {
		panic(err)
	}
	binDir = dir
	for _, pkg := range []string{"harness-ops", "termctl"} {
		if out, err := exec.Command("go", "build", "-o", filepath.Join(dir, pkg), "../cmd/"+pkg).CombinedOutput(); err != nil {
			panic(string(out))
		}
	}
	if out, err := exec.Command("go", "build", "-o", filepath.Join(dir, "fakeharness"), "../internal/termtest/fakeharness").CombinedOutput(); err != nil {
		panic(string(out))
	}
	// A child process that forgets its own setting must still miss the live registry.
	state, err := os.MkdirTemp("", "termctl-e2e-state")
	if err != nil {
		panic(err)
	}
	os.Setenv("TERMCTL_STATE_DIR", state)
	code := m.Run()
	exec.Command("tmux", "-L", socket, "kill-server").Run()
	os.RemoveAll(state)
	os.RemoveAll(dir)
	os.Exit(code)
}

func tmux(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if fn() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func readLog(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

type rig struct {
	t                                *testing.T
	tmp, socketPath, harness, sender string
	log                              string
}

func fakeCmd(log string) string { return filepath.Join(binDir, "fakeharness") + " " + log }

func promptShown(t *testing.T, pane string) bool {
	for _, f := range strings.Fields(tmux(t, "capture-pane", "-p", "-t", pane)) {
		if f == ">" {
			return true
		}
	}
	return false
}

func newRig(t *testing.T) *rig {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	exec.Command("tmux", "-L", socket, "kill-server").Run()
	tmux(t, "new-session", "-d", "-s", "base", "-x", "200", "-y", "50")
	t.Cleanup(func() { exec.Command("tmux", "-L", socket, "kill-server").Run() })
	tmp := t.TempDir()
	log := filepath.Join(tmp, "harness.log")
	h := tmux(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "-n", "fakeh", "--", fakeCmd(log))
	s := tmux(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "-n", "sender", "--", "sleep 300")
	tmux(t, "set-option", "-p", "-t", h, "@termctl_label", "fakeh")
	tmux(t, "set-option", "-p", "-t", s, "@termctl_label", "sender")
	waitFor(t, "fake prompt", func() bool { return promptShown(t, h) })
	return &rig{t: t, tmp: tmp, socketPath: tmux(t, "display-message", "-p", "#{socket_path}"), harness: h, sender: s, log: log}
}

func (r *rig) ops(pane string, args ...string) (int, string, string) {
	r.t.Helper()
	cmd := exec.Command(filepath.Join(binDir, "harness-ops"), append([]string{"--backend", "tmux", "-L", socket}, args...)...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ITERM_SESSION_ID=") && !strings.HasPrefix(kv, "REX_") && !strings.HasPrefix(kv, "TMUX") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, fmt.Sprintf("TMUX=%s,1,0", r.socketPath), "TMUX_PANE="+pane, "TERMCTL_STATE_DIR="+filepath.Join(r.tmp, "state"),
		"PATH="+binDir+":"+os.Getenv("PATH"))
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

// The message failure case: /clear then a message must be two separate lines.
func TestClearFirstProducesTwoSeparateLines(t *testing.T) {
	r := newRig(t)
	code, out, stderr := r.ops(r.sender, "msg", "--clear-first", "fakeh", "please", "start", "over")
	if code != 0 {
		t.Fatal(code, out, stderr)
	}
	waitFor(t, "two log lines", func() bool { return len(readLog(r.log)) >= 2 })
	lines := readLog(r.log)
	t.Log(lines)
	if len(lines) != 2 || lines[0] != "/clear" || !strings.HasPrefix(lines[1], "[agent-ops from sender (") ||
		!strings.HasSuffix(lines[1], "] please start over") || !strings.Contains(lines[1], "id=tmux:"+r.sender) {
		t.Fatalf("%q", lines)
	}
}

func TestDraftRefusedExit4(t *testing.T) {
	r := newRig(t)
	tmux(t, "send-keys", "-l", "-t", r.harness, "typing a draft")
	waitFor(t, "draft", func() bool { return strings.Contains(tmux(t, "capture-pane", "-p", "-t", r.harness), "typing a draft") })
	code, _, stderr := r.ops(r.sender, "msg", "fakeh", "hello")
	if code != 4 || !strings.Contains(stderr, "draft") || len(readLog(r.log)) != 0 {
		t.Fatal(code, stderr)
	}
}

func TestSlashWaitReturnsAfterClear(t *testing.T) {
	r := newRig(t)
	code, _, stderr := r.ops(r.sender, "slash", "--wait", "fakeh", "/clear")
	if code != 0 || len(readLog(r.log)) != 1 || !strings.Contains(tmux(t, "capture-pane", "-p", "-t", r.harness), "<<CLEARED>>") {
		t.Fatal(code, stderr, readLog(r.log))
	}
}

func TestSelfRefused(t *testing.T) {
	r := newRig(t)
	code, _, stderr := r.ops(r.sender, "msg", "sender", "hi")
	if code != 2 || !strings.Contains(stderr, "own pane") {
		t.Fatal(code, stderr)
	}
}

func TestSpawnSubmitsFirstPromptAndCloses(t *testing.T) {
	r := newRig(t)
	log2 := filepath.Join(r.tmp, "spawned.log")
	code, out, stderr := r.ops(r.sender, "spawn", "generic", "--where", "tab", "--cwd", r.tmp, "--command", fakeCmd(log2), "--label", "worker-2", "--prompt", "first prompt")
	if code != 0 {
		t.Fatal(out, stderr)
	}
	waitFor(t, "first prompt", func() bool { return len(readLog(log2)) > 0 })
	if readLog(log2)[0] != "first prompt" {
		t.Fatal(readLog(log2))
	}
	if code, out, _ := r.ops(r.sender, "which", "worker-2"); code != 0 || !strings.Contains(out, "worker-2") {
		t.Fatal(code, out)
	}
	if code, _, stderr := r.ops(r.sender, "close", "worker-2"); code != 0 {
		t.Fatal(stderr)
	}
}

func newTask(t *testing.T, r *rig, brief string) (string, map[string]any, string) {
	t.Helper()
	log := filepath.Join(r.tmp, "worker.log")
	code, out, stderr := r.ops(r.harness, "task", "new", "generic", "--title", "report", "--brief", brief, "--cwd", r.tmp, "--command", fakeCmd(log), "--timeout", "30")
	if code != 0 {
		t.Fatal(out, stderr)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	id := lines[len(lines)-1]
	code, out, stderr = r.ops(r.harness, "task", "show", id, "--json")
	if code != 0 {
		t.Fatal(stderr)
	}
	var task map[string]any
	if err := json.Unmarshal([]byte(out), &task); err != nil {
		t.Fatal(err)
	}
	return id, task, log
}

func workerPane(task map[string]any) string { return strings.SplitN(task["pane"].(string), ":", 2)[1] }

func TestTaskNewDoneNotifyClose(t *testing.T) {
	r := newRig(t)
	id, task, workerLog := newTask(t, r, "write the report")
	waitFor(t, "brief", func() bool { return len(readLog(workerLog)) > 0 })
	line := readLog(workerLog)[0]
	t.Log(line)
	if !strings.Contains(line, "[task "+id+" from fakeh] write the report") || !strings.Contains(line, "task done "+id) {
		t.Fatal(line)
	}
	worker := workerPane(task)
	if code, _, stderr := r.ops(r.harness, "task", "close", id); code != 6 {
		t.Fatal(code, stderr)
	}
	if code, out, stderr := r.ops(worker, "task", "done", id, "--status", "ok", "--summary", "report written"); code != 0 || !strings.Contains(out, "ready") {
		t.Fatal(code, out, stderr)
	}
	waitFor(t, "owner notified", func() bool { return len(readLog(r.log)) > 0 })
	note := readLog(r.log)[0]
	t.Log(note)
	if !strings.HasPrefix(note, "[agent-ops from ") || !strings.Contains(note, "RE: task "+id) || !strings.Contains(note, "report written") {
		t.Fatal(note)
	}
	if !paneAlive(t, worker) {
		t.Fatal("task done closed the worker's pane")
	}
	if _, out, _ := r.ops(r.harness, "task", "ls"); !strings.Contains(out, id) || !strings.Contains(out, "ready") || !strings.Contains(out, "report written") {
		t.Fatal(out)
	}
	if code, _, _ := r.ops(r.harness, "task", "close", id); code != 6 {
		t.Fatal("close before accept must be refused: ", code)
	}
	if code, _, stderr := r.ops(worker, "task", "accept", id); code != 2 {
		t.Fatal("the worker must not accept: ", code, stderr)
	}

	// Round 2 in the same session.
	if code, _, stderr := r.ops(r.harness, "task", "send", id, "please", "fix", "the", "typo"); code != 0 {
		t.Fatal(stderr)
	}
	waitFor(t, "round 2 message", func() bool { return len(readLog(workerLog)) >= 2 })
	if _, out, _ := r.ops(r.harness, "task", "show", id, "--json"); !strings.Contains(out, `"round": 2`) || !strings.Contains(out, `"status": "running"`) {
		t.Fatal(out)
	}
	if code, out, stderr := r.ops(worker, "task", "done", id, "--status", "ok", "--summary", "typo fixed"); code != 0 {
		t.Fatal(out, stderr)
	}
	waitFor(t, "second note", func() bool { return len(readLog(r.log)) > 1 })
	if !strings.Contains(readLog(r.log)[1], "round 2") {
		t.Fatal(readLog(r.log))
	}

	if code, _, stderr := r.ops(r.harness, "task", "accept", id, "--close"); code != 0 {
		t.Fatal(stderr)
	}
	if paneAlive(t, worker) {
		t.Fatal("accept --close left the worker pane")
	}
	if _, out, _ := r.ops(r.harness, "task", "ls"); strings.Contains(out, id) {
		t.Fatal(out)
	}
}

func paneAlive(t *testing.T, pane string) bool {
	t.Helper()
	for _, p := range strings.Fields(tmux(t, "list-panes", "-a", "-F", "#{pane_id}")) {
		if p == pane {
			return true
		}
	}
	return false
}

// A tab must survive the harness: the shell stays, the error stays on screen, the owner is told.
func TestHarnessExitLeavesPaneAndTellsOwner(t *testing.T) {
	r := newRig(t)
	id, task, _ := newTask(t, r, "x")
	worker := workerPane(task)
	tmux(t, "send-keys", "-t", worker, "C-d") // EOF ends the fake harness
	waitFor(t, "status exited", func() bool {
		_, out, _ := r.ops(r.harness, "task", "show", id, "--json")
		return strings.Contains(out, `"status": "exited"`)
	})
	if !paneAlive(t, worker) {
		t.Fatal("the pane died with the harness")
	}
	waitFor(t, "exit message on screen", func() bool {
		return strings.Contains(tmux(t, "capture-pane", "-p", "-t", worker), "exited with status")
	})
	waitFor(t, "owner told", func() bool { return len(readLog(r.log)) > 0 })
	note := readLog(r.log)[0]
	t.Log(note)
	if !strings.Contains(note, id) || !strings.Contains(note, "exited") {
		t.Fatal(note)
	}
	if code, _, _ := r.ops(r.harness, "task", "send", id, "hello?"); code != 2 {
		t.Fatal(code)
	}
	if code, _, _ := r.ops(r.harness, "task", "close", id); code != 6 {
		t.Fatal(code)
	}
	if code, _, stderr := r.ops(r.harness, "task", "close", "--force", id); code != 0 || paneAlive(t, worker) {
		t.Fatal(code, stderr)
	}
}

func TestPlainSpawnSurvivesItsHarness(t *testing.T) {
	r := newRig(t)
	log2 := filepath.Join(r.tmp, "s.log")
	code, out, stderr := r.ops(r.sender, "spawn", "generic", "--where", "tab", "--cwd", r.tmp, "--command", fakeCmd(log2), "--label", "keeper")
	if code != 0 {
		t.Fatal(out, stderr)
	}
	pane := strings.SplitN(strings.TrimSpace(out), ":", 2)[1]
	tmux(t, "send-keys", "-t", pane, "C-d")
	waitFor(t, "exit message", func() bool { return strings.Contains(tmux(t, "capture-pane", "-p", "-t", pane), "exited with status") })
	if !paneAlive(t, pane) {
		t.Fatal("pane died")
	}
}

func TestTaskWaitAnyAndLost(t *testing.T) {
	r := newRig(t)
	id, task, _ := newTask(t, r, "x")
	if code, _, _ := r.ops(r.harness, "task", "wait", "--any", "--timeout", "1"); code != 5 {
		t.Fatal(code)
	}
	tmux(t, "kill-pane", "-t", workerPane(task))
	if code, out, stderr := r.ops(r.harness, "task", "wait", id, "--timeout", "20"); code != 0 || !strings.Contains(out, "lost") {
		t.Fatal(code, out, stderr)
	}
}

func TestNotificationWaitsForOwnerDraft(t *testing.T) {
	r := newRig(t)
	id, task, _ := newTask(t, r, "x")
	tmux(t, "send-keys", "-l", "-t", r.harness, "owner draft")
	waitFor(t, "draft", func() bool { return strings.Contains(tmux(t, "capture-pane", "-p", "-t", r.harness), "owner draft") })
	code, out, stderr := r.ops(workerPane(task), "task", "done", id, "--status", "ok", "--summary", "held back")
	if code != 0 || !strings.Contains(out, "pending") || len(readLog(r.log)) != 0 {
		t.Fatal(code, out, stderr)
	}
	tmux(t, "send-keys", "-t", r.harness, "C-u")
	waitFor(t, "draft gone", func() bool { return !strings.Contains(tmux(t, "capture-pane", "-p", "-t", r.harness), "owner draft") })
	if code, _, _ := r.ops(r.harness, "task", "ls"); code != 0 { // any harness-ops call delivers
		t.Fatal(code)
	}
	waitFor(t, "late delivery", func() bool { return len(readLog(r.log)) > 0 })
	if !strings.Contains(readLog(r.log)[0], "held back") {
		t.Fatal(readLog(r.log))
	}
}

func TestCloseCommandSafety(t *testing.T) {
	r := newRig(t)
	if code, _, _ := r.ops(r.sender, "close", "sender", "--force"); code != 2 {
		t.Fatal(code)
	}
	extra := tmux(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "-n", "extra", "--", "sleep 60")
	tmux(t, "set-option", "-p", "-t", extra, "@termctl_label", "extra")
	if code, _, _ := r.ops(r.sender, "close", "extra"); code != 6 {
		t.Fatal(code)
	}
	if code, _, stderr := r.ops(r.sender, "close", "--force", "extra"); code != 0 {
		t.Fatal(stderr)
	}
}

// TestClearFirstOnITerm2 is opt-in (TERMCTL_ITERM_TEST=1): one scratch tab, closed at the end. It sends only to that tab.
func TestClearFirstOnITerm2(t *testing.T) {
	if os.Getenv("TERMCTL_ITERM_TEST") != "1" {
		t.Skip("set TERMCTL_ITERM_TEST=1")
	}
	tmp := t.TempDir()
	log := filepath.Join(tmp, "harness.log")
	b, err := term.Make("iterm2", "", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := b.Spawn("tab", fakeCmd(log), tmp, "termctl-iterm-test")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(p)
	label := "termctl-iterm-test-" + fmt.Sprint(os.Getpid())
	if err := b.SetLabel(p, label); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "fake prompt", func() bool {
		lines, err := b.Capture(p, 40)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range lines {
			if strings.TrimSpace(l) == ">" {
				return true
			}
		}
		return false
	})
	cmd := exec.Command(filepath.Join(binDir, "harness-ops"), "--backend", "iterm2", "msg", "--clear-first", label, "start", "over")
	cmd.Env = append(os.Environ(), "TERMCTL_STATE_DIR="+filepath.Join(tmp, "state"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(string(out), err)
	}
	waitFor(t, "two log lines", func() bool { return len(readLog(log)) >= 2 })
	lines := readLog(log)
	t.Log(lines)
	if len(lines) != 2 || lines[0] != "/clear" || !strings.HasPrefix(lines[1], "[agent-ops from ") || !strings.HasSuffix(lines[1], "] start over") {
		t.Fatalf("%q", lines)
	}
}

// TestHarnessExitOnITerm2 is opt-in: two scratch tabs, closed at the end. It sends only to those tabs.
func TestHarnessExitOnITerm2(t *testing.T) {
	if os.Getenv("TERMCTL_ITERM_TEST") != "1" {
		t.Skip("set TERMCTL_ITERM_TEST=1")
	}
	tmp := t.TempDir()
	ownerLog, workerLog := filepath.Join(tmp, "owner.log"), filepath.Join(tmp, "worker.log")
	b, err := term.Make("iterm2", "", "")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := b.Spawn("tab", fakeCmd(ownerLog), tmp, "termctl-owner-test")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(owner)
	time.Sleep(3 * time.Second)
	run := func(args ...string) (int, string, string) {
		cmd := exec.Command(filepath.Join(binDir, "harness-ops"), append([]string{"--backend", "iterm2"}, args...)...)
		cmd.Env = append(os.Environ(), "TERMCTL_STATE_DIR="+filepath.Join(tmp, "state"), "ITERM_SESSION_ID=w0:"+owner.Native)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return code, stdout.String(), stderr.String()
	}
	code, out, stderr := run("task", "new", "generic", "--title", "x", "--brief", "b", "--cwd", tmp, "--command", fakeCmd(workerLog), "--timeout", "30")
	if code != 0 {
		t.Fatal(out, stderr)
	}
	id := strings.TrimSpace(out)
	_, shown, _ := run("task", "show", id, "--json")
	var task map[string]any
	json.Unmarshal([]byte(shown), &task)
	native := strings.SplitN(task["pane"].(string), ":", 2)[1]
	worker := &term.Pane{ID: task["pane"].(string), Backend: "iterm2", Native: native}
	defer b.Close(worker)
	waitFor(t, "brief", func() bool { return len(readLog(workerLog)) > 0 })
	if err := b.SendKey(worker, "ctrl-d"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "status exited", func() bool {
		_, shown, _ := run("task", "show", id, "--json")
		return strings.Contains(shown, `"status": "exited"`)
	})
	waitFor(t, "exit message on screen", func() bool {
		lines, err := b.Capture(worker, 40)
		if err != nil {
			t.Fatal("the pane died with the harness: ", err)
		}
		return strings.Contains(strings.Join(lines, "\n"), "exited with status")
	})
	waitFor(t, "owner told", func() bool { return len(readLog(ownerLog)) > 0 })
	t.Log(readLog(ownerLog)[0])
	if !strings.Contains(readLog(ownerLog)[0], "exited") {
		t.Fatal(readLog(ownerLog))
	}
}
