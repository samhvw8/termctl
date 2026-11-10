package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// hcRig is a private herdr-core: its own HC_SESSION and XDG paths, so it can never see or
// disturb the user's core. The runtime directory is short because a Unix socket path is limited.
type hcRig struct {
	t          *testing.T
	tmp        string
	env        []string
	socket     string
	owner      string // address of the terminal that plays the calling agent
	ownerLog   string
	ownerPane  string
	serverEnvs []string
}

func hcEnvBase(tmp, runtime, session string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "ITERM") || strings.HasPrefix(name, "TMUX") || strings.HasPrefix(name, "REX_") ||
			strings.HasPrefix(name, "HC_") || name == "TERM_SESSION_ID" || strings.HasPrefix(name, "XDG_") || name == "CLAUDE_CODE_CHILD_SESSION" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HC_SESSION="+session, "XDG_RUNTIME_DIR="+filepath.Join(runtime, "run"), "XDG_STATE_HOME="+filepath.Join(runtime, "state"),
		"XDG_CONFIG_HOME="+filepath.Join(runtime, "config"), "TERMCTL_STATE_DIR="+filepath.Join(tmp, "state"), "PATH="+binDir+":"+os.Getenv("PATH"))
}

func newHcRig(t *testing.T) *hcRig {
	t.Helper()
	if os.Getenv("TERMCTL_HC_TEST") != "1" {
		t.Skip("set TERMCTL_HC_TEST=1")
	}
	if _, err := exec.LookPath("hc"); err != nil {
		t.Skip("hc not installed")
	}
	tmp := t.TempDir()
	runtime, err := os.MkdirTemp("/tmp", "hc")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"run", "state", "config"} {
		if err := os.MkdirAll(filepath.Join(runtime, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	session := fmt.Sprintf("termctl-test-e2e-%d", os.Getpid())
	r := &hcRig{t: t, tmp: tmp, env: hcEnvBase(tmp, runtime, session), socket: filepath.Join(runtime, "run", "herdr-core", session+".sock")}
	t.Cleanup(func() {
		cmd := exec.Command("hc", "server", "stop", "--json")
		cmd.Env = r.env
		out, err := cmd.CombinedOutput()
		t.Logf("hc server stop: %s err=%v", strings.TrimSpace(string(out)), err)
		if _, err := os.Stat(r.socket); err == nil {
			t.Errorf("the private core's socket is still there: %s", r.socket)
		}
		os.RemoveAll(runtime)
	})
	return r
}

func (r *hcRig) hc(args ...string) (string, error) {
	r.t.Helper()
	cmd := exec.Command("hc", args...)
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *hcRig) mustHc(args ...string) string {
	r.t.Helper()
	out, err := r.hc(args...)
	if err != nil {
		r.t.Fatalf("hc %v: %s", args, out)
	}
	return out
}

// startCore starts the private core with a parent agent's environment, the way a real auto-start would inherit it.
func (r *hcRig) startCore() {
	r.t.Helper()
	cmd := exec.Command("hc", "server", "start", "--json")
	cmd.Env = append(append([]string{}, r.env...), "CLAUDE_CODE_CHILD_SESSION=1", "ITERM_SESSION_ID=w0:not-this-one", "TMUX=/tmp/not-this-one,1,0")
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("hc server start: %s", out)
	}
}

func (r *hcRig) ops(sender string, args ...string) (int, string, string) {
	r.t.Helper()
	cmd := exec.Command(filepath.Join(binDir, "harness-ops"), args...)
	cmd.Env = r.env
	if sender != "" {
		cmd.Env = append(append([]string{}, cmd.Env...), "HC_TERMINAL_ID="+sender, "HC_TERMINAL_SOCKET="+r.socket)
	}
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

func (r *hcRig) termctl(args ...string) (int, string, string) {
	r.t.Helper()
	cmd := exec.Command(filepath.Join(binDir, "termctl"), args...)
	cmd.Env = r.env
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

type hcRow struct {
	ID, Harness, Label, Status string
	Self                       bool
}

func (r *hcRig) ls(sender string) []hcRow {
	r.t.Helper()
	code, out, stderr := r.ops(sender, "ls", "--json")
	if code != 0 {
		r.t.Fatal(code, stderr)
	}
	var rows []hcRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		r.t.Fatal(err, out)
	}
	return rows
}

func (r *hcRig) row(sender, id string) *hcRow {
	r.t.Helper()
	for _, x := range r.ls(sender) {
		if x.ID == id {
			return &x
		}
	}
	return nil
}

func (r *hcRig) screen(addr string) string {
	r.t.Helper()
	return r.mustHc("read", addr, "--lines", "0")
}

func (r *hcRig) waitScreen(addr, want string) {
	r.t.Helper()
	waitFor(r.t, "screen shows "+want, func() bool { return strings.Contains(r.screen(addr), want) })
}

// startFake runs the fake harness in a terminal named name. Pass parent to make it that terminal's child.
func (r *hcRig) startFake(name, log, parent string) string {
	r.t.Helper()
	args := []string{"run", "--bg", "--json", "--name", name, "--no-notify", "--outlive-parent", "--cwd", r.tmp}
	if parent == "" {
		args = append(args, "--no-parent")
	}
	args = append(args, "--", "/bin/sh", "-c", "FAKE_OSC7501=1 FAKE_TRAP_INT=1 "+fakeCmd(log))
	cmd := exec.Command("hc", args...)
	cmd.Env = r.env
	if parent != "" {
		cmd.Env = append(append([]string{}, r.env...), "HC_TERMINAL_ID="+parent)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("hc run %s: %s", name, out)
	}
	var d struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(out, &d); err != nil || d.Address == "" {
		r.t.Fatalf("hc run %s: %v %s", name, err, out)
	}
	r.waitScreen(d.Address, "fake harness ready")
	return d.Address
}

// A read-only call must not create a core: it would inherit the caller's agent environment.
func TestHcReadOnlyCallsStartNoCore(t *testing.T) {
	r := newHcRig(t)
	code, out, stderr := r.ops("", "--backend", "hc", "ls", "--json")
	if code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatal(code, out, stderr)
	}
	if _, err := os.Stat(r.socket); err == nil {
		t.Fatal("harness-ops ls started a core")
	}
	if code, _, stderr := r.ops("", "--backend", "hc", "which", "hc:t1"); code == 0 {
		t.Fatal("which on an empty core must fail: ", stderr)
	}
	if _, err := os.Stat(r.socket); err == nil {
		t.Fatal("harness-ops which started a core")
	}
}

func TestHcFullSession(t *testing.T) {
	r := newHcRig(t)
	r.startCore()
	r.ownerLog = filepath.Join(r.tmp, "owner.log")
	r.owner = r.startFake("owner", r.ownerLog, "")
	r.ownerPane = "hc:" + r.owner
	waitFor(t, "owner reported", func() bool {
		s := r.row(r.owner, r.ownerPane)
		return s != nil && s.Status == "idle" && s.Self
	})

	// Self is refused.
	if code, _, stderr := r.ops(r.owner, "msg", r.ownerPane, "hi"); code != 2 || !strings.Contains(stderr, "own pane") {
		t.Fatal(code, stderr)
	}

	// Spawn a worker through harness-ops; the core inherited a parent agent's environment.
	workerLog := filepath.Join(r.tmp, "worker.log")
	envFile := filepath.Join(r.tmp, "worker.env")
	command := "env | sort > " + envFile + "; FAKE_OSC7501=1 FAKE_TRAP_INT=1 FAKE_CLEAR_DELAY=2 " + fakeCmd(workerLog)
	code, out, stderr := r.ops(r.owner, "spawn", "generic", "--where", "split", "--cwd", r.tmp, "--label", "w1", "--command", command, "--timeout", "60")
	if code != 0 {
		t.Fatal(code, out, stderr)
	}
	pane := strings.TrimSpace(out)
	if !strings.HasPrefix(pane, "hc:") {
		t.Fatal(pane)
	}
	addr := strings.TrimPrefix(pane, "hc:")
	t.Logf("worker %s", pane)

	// ls shows it, with hc's own status word and the foreground program.
	waitFor(t, "worker row", func() bool {
		s := r.row(r.owner, pane)
		return s != nil && s.Status == "idle" && s.Label == "w1"
	})
	if s := r.row(r.owner, pane); s.Harness != "fakeharness" {
		t.Fatalf("%+v", s)
	}
	t.Logf("ls row: %+v", r.row(r.owner, pane))

	// The agent markers a core inherited must not reach a spawned program.
	envData, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"CLAUDE_CODE_CHILD_SESSION=", "ITERM_SESSION_ID=", "TMUX="} {
		if strings.Contains(string(envData), leak) {
			t.Fatalf("spawned program inherited %s", leak)
		}
	}
	if !strings.Contains(string(envData), "HC_TERMINAL_ID="+addr) {
		t.Fatal("no HC_TERMINAL_ID in the worker: ", string(envData))
	}

	// read through harness-ops.
	if code, out, stderr := r.ops(r.owner, "read", pane); code != 0 || !strings.Contains(out, "fake harness ready") {
		t.Fatal(code, out, stderr)
	}

	// msg: delivered inside hc's tag, with the message line whole.
	if code, out, stderr := r.ops(r.owner, "msg", pane, "hello", "world"); code != 0 {
		t.Fatal(code, out, stderr)
	}
	waitFor(t, "message", func() bool { return len(readLog(workerLog)) > 0 })
	line := readLog(workerLog)[0]
	t.Logf("received: %s", line)
	if !strings.Contains(line, ">[agent-ops from owner (fakeharness) id="+r.ownerPane+" ") || !strings.HasSuffix(line, "] hello world</hc>") {
		t.Fatal(line)
	}

	// termctl send-text cannot be honoured on hc: it must fail loudly, not submit.
	before := len(readLog(workerLog))
	if code, _, stderr := r.termctl("--backend", "hc", "send-text", pane, "typed"); code != 1 || !strings.Contains(stderr, "always submits") {
		t.Fatal(code, stderr)
	}
	if len(readLog(workerLog)) != before {
		t.Fatal("send-text reached the program")
	}

	// wait-idle while a slow /clear runs: hc reports working, so wait-idle must not return early.
	if code, _, stderr := r.ops(r.owner, "slash", pane, "/clear"); code != 0 {
		t.Fatal(code, stderr)
	}
	r.waitScreen(addr, "clearing...")
	code, out, stderr = r.ops(r.owner, "wait-idle", "--timeout", "30", pane)
	if code != 0 || !strings.HasPrefix(out, "idle:") {
		t.Fatal(code, out, stderr)
	}
	if !strings.Contains(r.screen(addr), "<<CLEARED>>") {
		t.Fatal("wait-idle returned while the clear ran: ", r.screen(addr))
	}

	// interrupt: generic keys are ctrl-c; the fake logs SIGINT and survives.
	code, out, stderr = r.ops(r.owner, "interrupt", pane)
	if code != 0 {
		t.Fatal(code, out, stderr)
	}
	waitFor(t, "SIGINT", func() bool { return strings.Contains(strings.Join(readLog(workerLog), "\n"), "<<SIGINT>>") })
	t.Logf("interrupt: %s", strings.TrimSpace(out))
	r.mustHc("keys", addr, "ctrl+u")

	// A blocked program: wait-idle times out, close and msg are refused loudly.
	if code, _, stderr := r.ops(r.owner, "slash", pane, "/block"); code != 0 {
		t.Fatal(code, stderr)
	}
	waitFor(t, "blocked", func() bool { s := r.row(r.owner, pane); return s != nil && s.Status == "blocked" })
	if code, _, stderr := r.ops(r.owner, "msg", pane, "not", "now"); code != 6 || !strings.Contains(stderr, "waiting for a person") {
		t.Fatal(code, stderr)
	}
	if code, _, _ := r.ops(r.owner, "wait-idle", "--timeout", "1", pane); code != 5 {
		t.Fatalf("wait-idle on a blocked program: exit %d", code)
	}
	if code, _, stderr := r.ops(r.owner, "close", pane); code != 6 || !strings.Contains(stderr, "busy") {
		t.Fatal(code, stderr)
	}
	before = len(readLog(workerLog))
	r.mustHc("send", "--force", addr, "/done")
	waitFor(t, "unblocked", func() bool { return len(readLog(workerLog)) > before })
	waitFor(t, "done word", func() bool { s := r.row(r.owner, pane); return s != nil && s.Status == "done" })

	// close: kill then rm, nothing left behind, even in --all.
	if code, out, stderr := r.ops(r.owner, "close", pane); code != 0 {
		t.Fatal(code, out, stderr)
	}
	var all struct {
		Terminals []struct{ Address string } `json:"terminals"`
	}
	if err := json.Unmarshal([]byte(r.mustHc("ls", "--all", "--json")), &all); err != nil {
		t.Fatal(err)
	}
	for _, x := range all.Terminals {
		if x.Address == addr {
			t.Fatal("close left a finished terminal: ", x)
		}
	}
}

// A worker must keep running when the agent that spawned it ends.
func TestHcWorkerOutlivesItsSpawner(t *testing.T) {
	r := newHcRig(t)
	r.startCore()
	owner := r.startFake("owner", filepath.Join(r.tmp, "owner.log"), "")
	code, out, stderr := r.ops(owner, "spawn", "generic", "--cwd", r.tmp, "--command", fakeCmd(filepath.Join(r.tmp, "w.log")), "--timeout", "60")
	if code != 0 {
		t.Fatal(code, out, stderr)
	}
	addr := strings.TrimPrefix(strings.TrimSpace(out), "hc:")
	r.mustHc("kill", owner, "--json")
	var d struct {
		State  string            `json:"state"`
		Parent *string           `json:"parent"`
		Labels map[string]string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(r.mustHc("get", addr, "--json")), &d); err != nil {
		t.Fatal(err)
	}
	t.Logf("after the spawner ended: state=%s parent=%v labels=%v", d.State, d.Parent, d.Labels)
	if d.State != "live" || d.Parent != nil {
		t.Fatalf("%+v", d)
	}
	if d.Labels["herdr.space"] == "" { // without it the Herdr app hides the agent
		t.Fatalf("no herdr.space label: %+v", d)
	}
}

// A message to a Herdr agent shows hc's tag, with the message line inside it.
func TestHcMsgShowsHerdrTag(t *testing.T) {
	r := newHcRig(t)
	r.startCore()
	ownerLog, kidLog := filepath.Join(r.tmp, "owner.log"), filepath.Join(r.tmp, "kid.log")
	owner := r.startFake("owner", ownerLog, "")
	waitFor(t, "owner reports", func() bool {
		return strings.Contains(r.mustHc("get", owner, "--json"), `"reported":true`)
	})
	kid := r.startFake("kid", kidLog, owner)
	waitFor(t, "kid reports", func() bool {
		return strings.Contains(r.mustHc("get", kid, "--json"), `"reported":true`)
	})

	code, out, stderr := r.ops(owner, "msg", "hc:"+kid, "are", "you", "there")
	if code != 0 {
		t.Fatal(code, out, stderr)
	}
	waitFor(t, "kid message", func() bool { return len(readLog(kidLog)) >= 1 })
	line := readLog(kidLog)[0]
	t.Logf("received: %s", line)
	if !strings.HasPrefix(line, `<hc from="hc/`+owner+`">[agent-ops from `) || !strings.HasSuffix(line, "are you there</hc>") {
		t.Fatal(line)
	}
}
