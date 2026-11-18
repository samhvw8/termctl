package e2e

// The real-harness dialog matrix behind "mise run test:dialogs" (opt-in: TERMCTL_DIALOGS_TEST=1; it
// costs model turns). Each cell is one backend and one harness: a lead pane runs the fake harness, and
// its task's worker is the real harness (Claude Code on haiku, or Antigravity CLI), told to run touch.
// The outside judge is the file system: "task answer 1" must make the touch run, "task deny" on a
// second dialog must leave its file absent. The notice must reach the lead's screen; the time from the
// dialog first visible on the worker to the notice visible on the lead is measured.
//
// Every pane, server and core it creates is closed: iTerm2 scratch tabs, a private tmux socket
// (termctl-test-dialogs), a private Rex server and a private hc core (HC_SESSION termctl-test-*).
// TERMCTL_DIALOGS_ONLY (comma-separated cells or backends, e.g. "rex,tmux-agy") narrows a rerun.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samhvw8/termctl/internal/harness"
	"github.com/samhvw8/termctl/internal/term"
)

const dialogsSocket = "termctl-test-dialogs"

func dialogsOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv("TERMCTL_DIALOGS_TEST") != "1" {
		t.Skip("set TERMCTL_DIALOGS_TEST=1 (runs real claude and agy turns)")
	}
}

func wanted(names ...string) bool {
	only := os.Getenv("TERMCTL_DIALOGS_ONLY")
	if only == "" {
		return true
	}
	for _, w := range strings.Split(only, ",") {
		for _, n := range names {
			if strings.TrimSpace(w) == n {
				return true
			}
		}
	}
	return false
}

// dialogsRoot is where the cells' folders go: TERMCTL_DIALOGS_DIR, or a test temp folder.
func dialogsRoot(t *testing.T) string {
	if d := os.Getenv("TERMCTL_DIALOGS_DIR"); d != "" {
		root := filepath.Join(d, time.Now().Format("150405"))
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		return root
	}
	return t.TempDir()
}

// cleanEnv is the test's environment without anything that names a terminal or a state folder.
func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "ITERM") || strings.HasPrefix(name, "TMUX") || strings.HasPrefix(name, "REX_") ||
			strings.HasPrefix(name, "HC_") || name == "TERM_SESSION_ID" || name == "TERMCTL_STATE_DIR" || name == "CLAUDE_CODE_CHILD_SESSION" ||
			name == "CLAUDECODE" || name == "CLAUDE_CODE_ENTRYPOINT" {
			if name != "TMUX_TMPDIR" { // where private tmux sockets go, not a terminal: keeps them off /tmp
				continue
			}
		}
		env = append(env, kv)
	}
	return env
}

// leadRig is a lead pane running the fake harness (whole lines echoed), and harness-ops as that lead.
type leadRig struct {
	t       *testing.T
	name    string
	b       term.Backend // in the test process, to read screens
	lead    *term.Pane
	args    []string
	env     []string
	state   string
	leadLog string
}

func (r *leadRig) ops(args ...string) (int, string, string) {
	cmd := exec.Command(filepath.Join(binDir, "harness-ops"), append(append([]string{}, r.args...), args...)...)
	cmd.Env = r.env
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
		stderr.WriteString(err.Error())
	}
	return code, stdout.String(), stderr.String()
}

func leadCmd(log string) string { return "env FAKE_ECHO=full " + ompCmd(log) } // a calibrated lead: dialog notices are typed to it

func findPane(b term.Backend, id string) *term.Pane {
	panes, err := b.ListPanes()
	if err != nil {
		return nil
	}
	for _, p := range panes {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func setupTmuxLead(t *testing.T, root string) *leadRig {
	exec.Command("tmux", "-L", dialogsSocket, "kill-server").Run()
	state := filepath.Join(root, "state-tmux")
	log := filepath.Join(root, "lead-tmux.log")
	env := append(cleanEnv(), "TERMCTL_STATE_DIR="+state, "PATH="+binDir+":"+os.Getenv("PATH"))
	cmd := exec.Command("tmux", "-L", dialogsSocket, "new-session", "-d", "-s", "lead", "-x", "220", "-y", "60", "-c", root, leadCmd(log))
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux: %s", out)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", dialogsSocket, "kill-server").Run() })
	q := func(f string) string {
		out, _ := exec.Command("tmux", "-L", dialogsSocket, "display-message", "-p", "-t", "lead", f).Output()
		return strings.TrimSpace(string(out))
	}
	paneID, sock := q("#{pane_id}"), q("#{socket_path}")
	b, _ := term.Make("tmux", dialogsSocket, "")
	r := &leadRig{t: t, name: "tmux", b: b, args: []string{"--backend", "tmux", "-L", dialogsSocket}, state: state, leadLog: log,
		env: append(env, fmt.Sprintf("TMUX=%s,1,0", sock), "TMUX_PANE="+paneID)}
	r.lead = findPane(b, "tmux:"+paneID)
	return r
}

func setupITermLead(t *testing.T, root string) *leadRig {
	b, _ := term.Make("iterm2", "", "")
	if !b.Available() {
		t.Skip("iTerm2 is not available")
	}
	state := filepath.Join(root, "state-iterm2")
	log := filepath.Join(root, "lead-iterm2.log")
	lead, err := b.Spawn("tab", leadCmd(log), root, "termctl-dialogs-lead")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close(lead) })
	env := append(cleanEnv(), "TERMCTL_STATE_DIR="+state, "PATH="+binDir+":"+os.Getenv("PATH"), "ITERM_SESSION_ID=w0t0p0:"+lead.Native)
	r := &leadRig{t: t, name: "iterm2", b: b, args: []string{"--backend", "iterm2"}, state: state, leadLog: log, env: env}
	waitFor(t, "lead listed", func() bool { r.lead = findPane(b, lead.ID); return r.lead != nil })
	return r
}

func setupRexLead(t *testing.T, root string) *leadRig {
	bin := rexCLI()
	if bin == "" {
		t.Skip("the Rex CLI is not installed")
	}
	x := startRexServer(t, bin)
	state := filepath.Join(root, "state-rex")
	log := filepath.Join(root, "lead-rex.log")
	var created struct {
		SessionID      string `json:"session_id"`
		InitialWindows []struct {
			BlockIDs []string `json:"block_ids"`
		} `json:"initial_windows"`
	}
	out := x.rex("new", "--json", "dialogs", "-c", root, "--shell=none", "--", "/bin/sh", "-c", leadCmd(log))
	if err := json.Unmarshal([]byte(out), &created); err != nil || len(created.InitialWindows) == 0 {
		t.Fatalf("rex new: %v %s", err, out)
	}
	block := uuid(created.InitialWindows[0].BlockIDs[0])
	t.Setenv("REX_SERVER", x.addr)
	t.Setenv("REX_RUNTIME_DIR", filepath.Join(x.dir, "run"))
	t.Setenv("REX_SESSION", "")
	t.Setenv("REX_BLOCK", "")
	b := term.NewRex()
	env := append(append([]string{}, x.env...), "REX_SERVER="+x.addr, "REX_SESSION="+created.SessionID, "REX_BLOCK=block:"+block,
		"TERMCTL_STATE_DIR="+state, "PATH="+binDir+":"+os.Getenv("PATH"))
	r := &leadRig{t: t, name: "rex", b: b, args: []string{"--backend", "rex"}, state: state, leadLog: log, env: env}
	waitFor(t, "lead listed", func() bool { r.lead = findPane(b, "rex:"+block); return r.lead != nil })
	return r
}

func setupHcLead(t *testing.T, root string) *leadRig {
	if _, err := exec.LookPath("hc"); err != nil {
		t.Skip("hc not installed")
	}
	x := startHcRig(t)
	for _, kv := range x.env {
		name, val, _ := strings.Cut(kv, "=")
		if name == "HC_SESSION" || strings.HasPrefix(name, "XDG_") {
			t.Setenv(name, val) // the in-process backend talks to the private core only
		}
	}
	t.Setenv("HC_TERMINAL_ID", "")
	t.Setenv("HC_TERMINAL_SOCKET", "")
	x.mustHc("server", "start", "--json")
	log := filepath.Join(root, "lead-hc.log")
	cmd := exec.Command("hc", "run", "--bg", "--json", "--name", "lead", "--no-notify", "--no-parent", "--outlive-parent", "--cwd", root,
		"--", "/bin/sh", "-c", leadCmd(log))
	cmd.Env = x.env
	out, err := cmd.CombinedOutput()
	var d struct {
		Address string `json:"address"`
	}
	if err != nil || json.Unmarshal(out, &d) != nil || d.Address == "" {
		t.Fatalf("hc run lead: %v %s", err, out)
	}
	state := filepath.Join(x.tmp, "state")
	b := term.NewHc(x.socket)
	env := append(append([]string{}, x.env...), "HC_TERMINAL_ID="+d.Address, "HC_TERMINAL_SOCKET="+x.socket)
	r := &leadRig{t: t, name: "hc", b: b, args: []string{"--backend", "hc"}, state: state, leadLog: log, env: env}
	waitFor(t, "lead listed", func() bool { r.lead = findPane(b, "hc:"+d.Address); return r.lead != nil })
	return r
}

// noticeWatch reads the lead's screen every 150 ms and records when each dialog fingerprint first shows
// on it: inline ("DIALOG <fp>:"), or in the inbox file a long notice points to ("read the file <path>").
type noticeWatch struct {
	mu    sync.Mutex
	seen  map[string]time.Time
	files map[string]bool
	stop  chan struct{}
	done  chan struct{}
}

var (
	noticeFP   = regexp.MustCompile(`DIALOG([0-9a-f]{8}):`)
	noticeFile = regexp.MustCompile(`readthefile(/[A-Za-z0-9/._\-]+?\.md)`)
	spaceRe    = regexp.MustCompile(`\s+`)
)

func strip(s string) string { return spaceRe.ReplaceAllString(s, "") }

func watchNotices(r *leadRig) *noticeWatch {
	w := &noticeWatch{seen: map[string]time.Time{}, files: map[string]bool{}, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for {
			select {
			case <-w.stop:
				return
			case <-time.After(150 * time.Millisecond):
			}
			lines, err := r.b.Capture(r.lead, 1000)
			if err != nil {
				continue
			}
			now := time.Now()
			screen := strip(strings.Join(lines, ""))
			w.mu.Lock()
			for _, m := range noticeFP.FindAllStringSubmatch(screen, -1) {
				if _, ok := w.seen[m[1]]; !ok {
					w.seen[m[1]] = now
				}
			}
			for _, m := range noticeFile.FindAllStringSubmatch(screen, -1) {
				data, err := os.ReadFile(m[1])
				if err != nil {
					continue
				}
				for _, f := range noticeFP.FindAllStringSubmatch(strip(string(data)), -1) {
					if _, ok := w.seen[f[1]]; !ok {
						w.seen[f[1]] = now
					}
				}
			}
			w.mu.Unlock()
		}
	}()
	return w
}

func (w *noticeWatch) close() { close(w.stop); <-w.done }

func (w *noticeWatch) wait(fp string, timeout time.Duration) (time.Time, bool) {
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		w.mu.Lock()
		at, ok := w.seen[fp]
		w.mu.Unlock()
		if ok {
			return at, true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return time.Time{}, false
}

// harnessCommand starts the real harness so that it asks before running a shell command, with no
// change to the user's settings files: Claude Code in its default (manual) permission mode without the
// user's settings (whose defaultMode is auto) or MCP servers; agy in accept-edits mode (its auto mode runs
// touch unasked), trusting the cell folder and without its terminal sandbox for this session only. The state folder and PATH are set
// explicitly: an iTerm2 tab gets iTerm2's environment, not the test's, and the live registry must stay untouched.
func harnessCommand(h, dir, state string) (string, error) {
	env := "env TERMCTL_STATE_DIR=" + term.ShellQuote(state) + " PATH=" + term.ShellQuote(binDir) + ":\"$PATH\" "
	switch h {
	case "claude":
		bin, err := exec.LookPath("claude")
		if err != nil {
			return "", err
		}
		return env + term.ShellQuote(bin) + " --model haiku --setting-sources project --permission-mode default --strict-mcp-config", nil
	case "agy":
		bin, err := exec.LookPath("agy")
		if err != nil {
			return "", err
		}
		// agy's terminal sandbox (the user's enableTerminalSandbox) refuses the approved touch in these
		// folders with "Operation not permitted", whatever the answer; it is off for this session only.
		settings, _ := json.Marshal(map[string]any{"trustedWorkspaces": []string{dir}, "enableTerminalSandbox": false})
		// Hydrogen medium, the roster's agy model: on 2026-11-17 the default (Gemini 4 Carbon medium) finished a
		// one-command task prompt in 3 of 6 tries within 240 s (15-160 s, one stuck at Generating), Hydrogen in
		// 3 of 3 (4-27 s). TERMCTL_DIALOGS_AGY_MODEL overrides it.
		model := os.Getenv("TERMCTL_DIALOGS_AGY_MODEL")
		if model == "" {
			model = "gemini-4-hydrogen-medium"
		}
		return env + term.ShellQuote(bin) + " --model " + term.ShellQuote(model) + " --mode accept-edits --settings " + term.ShellQuote(string(settings)), nil
	}
	return "", fmt.Errorf("unknown harness %s", h)
}

// turnWait is how long a harness gets for one turn. agy 1.5.10 on Gemini 4 Carbon often thought for
// minutes (and sometimes read the agent-ops skill) before it asked; on Hydrogen it takes seconds.
const turnWait = 300 * time.Second

type cellResult struct {
	Cell        string
	Pass        bool
	YesMs, NoMs int64
	ExtraDenies int
	Note        string
	Latencies   []int64 // from the look that saw the dialog
	Upper       []int64 // from the look before it, which did not: the most the latency can be
}

// flow runs one cell: a task whose worker is the real harness, a dialog answered with 1 (the file must
// appear) and a second dialog denied (its file must stay absent for 10 s).
func flow(r *leadRig, notices *noticeWatch, h, cell, root string, logf func(string, ...any)) (res cellResult) {
	res.Cell = cell
	dir := filepath.Join(root, cell)
	os.MkdirAll(dir, 0o755)
	yes, no := filepath.Join(dir, cell+"-yes"), filepath.Join(dir, cell+"-no")
	fail := func(format string, a ...any) cellResult {
		res.Note = fmt.Sprintf(format, a...)
		logf("%s: FAIL: %s", cell, res.Note)
		return res
	}
	cmd, err := harnessCommand(h, dir, r.state)
	if err != nil {
		return fail("%v", err)
	}
	brief := fmt.Sprintf("Run this exact shell command and nothing else: touch %s . Do not run any other command.", yes)
	code, out, stderr := r.ops("task", "new", h, "--title", cell, "--brief", brief, "--cwd", dir, "--command", cmd, "--where", "tab", "--timeout", "180")
	if code != 0 {
		return fail("task new: exit %d: %s %s", code, out, stderr)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	id := lines[len(lines)-1]
	defer func() {
		if code, _, stderr := r.ops("task", "close", "--force", id); code != 0 {
			logf("%s: task close: %s", cell, stderr)
		}
	}()
	_, shown, _ := r.ops("task", "show", id, "--json")
	var task struct {
		Pane string `json:"pane"`
	}
	json.Unmarshal([]byte(shown), &task)
	wp := findPane(r.b, task.Pane)
	if wp == nil {
		return fail("worker pane %q not found", task.Pane)
	}
	prof := harness.Profiles[h]
	read := func() *harness.Dialog {
		lines, err := r.b.Capture(wp, 200)
		if err != nil {
			return nil
		}
		return prof.ReadDialog(lines)
	}
	deny := func(d *harness.Dialog, why string) {
		res.ExtraDenies++
		code, out, stderr := r.ops("task", "deny", id, "--expect", d.Fingerprint)
		logf("%s: denied an extra dialog (%s) %q: exit %d %s%s", cell, why, harness.DialogLine(id, d), code, strings.TrimSpace(out), strings.TrimSpace(stderr))
	}
	// waitDialog waits for the dialog whose request names want; other dialogs are denied. It returns when
	// the dialog was seen, and when the look before began: the dialog came up between the two.
	waitDialog := func(want string, timeout time.Duration) (*harness.Dialog, time.Time, time.Time) {
		end := time.Now().Add(timeout)
		lastLook := time.Now()
		for time.Now().Before(end) {
			before := time.Now()
			if d := read(); d != nil {
				if strings.Contains(strip(d.Body+d.Question), want) {
					return d, time.Now(), lastLook
				}
				deny(d, "not the touch asked for")
			}
			lastLook = before
			time.Sleep(100 * time.Millisecond)
		}
		return nil, time.Time{}, time.Time{}
	}
	d, t0, t0Early := waitDialog(cell+"-yes", turnWait)
	if d == nil {
		lines, _ := r.b.Capture(wp, 60)
		return fail("no touch dialog on the worker in %v; screen:\n%s", turnWait, strings.Join(lines, "\n"))
	}
	logf("%s: dialog %s on the worker: %s", cell, d.Fingerprint, harness.DialogLine(id, d))
	t1, ok := notices.wait(d.Fingerprint, 60*time.Second)
	if !ok {
		return fail("the notice of dialog %s never showed on the lead's screen", d.Fingerprint)
	}
	res.YesMs = t1.Sub(t0).Milliseconds()
	res.Latencies = append(res.Latencies, res.YesMs)
	res.Upper = append(res.Upper, t1.Sub(t0Early).Milliseconds())
	logf("%s: notice on the lead after %d ms (at most %d ms: the dialog came up after the look before, %d ms earlier)", cell, res.YesMs, t1.Sub(t0Early).Milliseconds(), t0.Sub(t0Early).Milliseconds())
	if code, out, stderr := r.ops("task", "answer", id, "1", "--expect", d.Fingerprint); code != 0 {
		return fail("task answer: exit %d: %s %s", code, out, stderr)
	}
	ok = false
	for end := time.Now().Add(90 * time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if _, err := os.Stat(yes); err == nil {
			ok = true
			break
		}
	}
	if !ok {
		lines, _ := r.b.Capture(wp, 60)
		return fail("task answer 1 went through, but %s does not exist after 90 s; screen:\n%s", yes, strings.Join(lines, "\n"))
	}
	// Wait until the worker is back at its input line, denying anything else it asks.
	ready, calm := false, 0
	for end := time.Now().Add(turnWait); time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		lines, err := r.b.Capture(wp, 200)
		if err != nil {
			continue
		}
		if d := prof.ReadDialog(lines); d != nil {
			deny(d, "after the answer")
			calm = 0
			continue
		}
		if prof.ReadInput(lines) != nil && !prof.IsBusy(lines) {
			if calm++; calm >= 3 {
				ready = true
				break
			}
		} else {
			calm = 0
		}
	}
	if !ready {
		lines, _ := r.b.Capture(wp, 60)
		return fail("the worker never came back to its input line; screen:\n%s", strings.Join(lines, "\n"))
	}
	// Look for the second dialog while task send runs: it can take seconds, and the dialog can come first.
	type seen struct {
		d          *harness.Dialog
		at, before time.Time
	}
	second := make(chan seen, 1)
	go func() {
		d, at, before := waitDialog(cell+"-no", turnWait)
		second <- seen{d, at, before}
	}()
	if code, out, stderr := r.ops("task", "send", id, "Now run this exact shell command and nothing else: touch "+no); code != 0 {
		<-second
		return fail("task send: exit %d: %s %s", code, out, stderr)
	}
	s2 := <-second
	d2, t2, t2Early := s2.d, s2.at, s2.before
	if d2 == nil {
		lines, _ := r.b.Capture(wp, 60)
		return fail("no second touch dialog in %v; screen:\n%s", turnWait, strings.Join(lines, "\n"))
	}
	t3, ok := notices.wait(d2.Fingerprint, 60*time.Second)
	if !ok {
		return fail("the notice of dialog %s never showed on the lead's screen", d2.Fingerprint)
	}
	res.NoMs = t3.Sub(t2).Milliseconds()
	res.Latencies = append(res.Latencies, res.NoMs)
	res.Upper = append(res.Upper, t3.Sub(t2Early).Milliseconds())
	logf("%s: second notice on the lead after %d ms (at most %d ms)", cell, res.NoMs, t3.Sub(t2Early).Milliseconds())
	if code, out, stderr := r.ops("task", "deny", id, "--expect", d2.Fingerprint); code != 0 {
		return fail("task deny: exit %d: %s %s", code, out, stderr)
	}
	// The harness may try again some other way; the lead denies each try.
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		if d := read(); d != nil {
			deny(d, "a retry after the deny")
		}
	}
	if _, err := os.Stat(no); err == nil {
		return fail("%s exists after task deny", no)
	}
	res.Pass = true
	logf("%s: PASS", cell)
	return res
}

// tallLines is the length of the tall cell's heredoc: taller than every cell's worker pane (an iTerm2 tab
// the size of its window, tmux's 60-row windows, Rex's 24 rows, hc's 120 rows), so the dialog's rule is off
// the screen and only a history read sees the whole request.
const tallLines = 160

// tallFlow runs a tall cell: Claude Code asks to run a heredoc taller than the pane. The watcher, task wait
// and task answer must read it whole from the backend's history (term.SafeHistory): the notice and the
// task wait block carry the fingerprint of the whole request, task answer 1 --expect that fingerprint must
// go through, and the file must hold every line. A backend whose history is not safe fails here (the
// request reads truncated and task answer refuses it with exit 6).
func tallFlow(r *leadRig, notices *noticeWatch, cell, root string, logf func(string, ...any)) (res cellResult) {
	res.Cell = cell
	dir := filepath.Join(root, cell)
	os.MkdirAll(dir, 0o755)
	out := filepath.Join(dir, cell+"-yes.txt")
	fail := func(format string, a ...any) cellResult {
		res.Note = fmt.Sprintf(format, a...)
		logf("%s: FAIL: %s", cell, res.Note)
		return res
	}
	var want strings.Builder
	for i := 1; i <= tallLines; i++ {
		fmt.Fprintf(&want, "request line %03d of %d: the quick brown fox jumps over the lazy dog\n", i, tallLines)
	}
	cmdFile := filepath.Join(dir, "cmd.txt")
	if err := os.WriteFile(cmdFile, []byte("cat > "+out+" <<'EOF'\n"+want.String()+"EOF\n"), 0o644); err != nil {
		return fail("%v", err)
	}
	cmd, err := harnessCommand("claude", dir, r.state)
	if err != nil {
		return fail("%v", err)
	}
	brief := fmt.Sprintf("Read the file %s, then run its whole content verbatim as one single Bash shell command (it is a heredoc that writes %s). "+
		"Do not run it as a script file (no bash cmd.txt, no sh), do not split or shorten it, run nothing else.", cmdFile, filepath.Base(out))
	code, stdout, stderr := r.ops("task", "new", "claude", "--title", cell, "--brief", brief, "--cwd", dir, "--command", cmd, "--where", "tab", "--timeout", "180")
	if code != 0 {
		return fail("task new: exit %d: %s %s", code, stdout, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	id := lines[len(lines)-1]
	defer func() {
		if code, _, stderr := r.ops("task", "close", "--force", id); code != 0 {
			logf("%s: task close: %s", cell, stderr)
		}
	}()
	// The dialog the watcher marked on the task is the one the lead is told of (a notice; task wait then does not
	// return it again). Its fingerprint must be that of the whole request, read here from the history too.
	prof := harness.Profiles["claude"]
	last := fmt.Sprintf("request line %03d of %d: the quick brown fox jumps over the lazy dog", tallLines, tallLines)
	var d *harness.Dialog
	var seen time.Time // when a screen first showed the heredoc dialog
	var screenRead *harness.Dialog
	for end := time.Now().Add(turnWait); d == nil && time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		wp := findPane(r.b, taskPane(r, id))
		if wp == nil {
			continue
		}
		rows := wp.Rows
		if rows == 0 {
			rows = 200 // tmux reports no height: harness-ops reads 200 lines there too
		}
		lines, err := r.b.Capture(wp, rows)
		if err != nil {
			continue
		}
		s := prof.ReadDialog(lines)
		if s != nil && seen.IsZero() && strings.Contains(strip(s.Body), strip(last)) {
			seen = time.Now()
		}
		mark := taskMark(r, id)
		if s == nil || mark == "" {
			continue
		}
		if !strings.Contains(strip(s.Body), strip(last)) {
			res.ExtraDenies++
			code, o, e := r.ops("task", "deny", id, "--expect", mark)
			logf("%s: denied an extra dialog %q: exit %d %s%s", cell, harness.DialogLine(id, s), code, strings.TrimSpace(o), strings.TrimSpace(e))
			time.Sleep(2 * time.Second)
			continue
		}
		history, err := r.b.Capture(wp, 2000)
		if err != nil {
			continue
		}
		if whole := prof.ReadScrollbackDialog(history); whole != nil && whole.Fingerprint == mark {
			d, screenRead = whole, s
		}
	}
	if d == nil {
		screen := ""
		if wp := findPane(r.b, taskPane(r, id)); wp != nil {
			lines, _ := r.b.Capture(wp, 60)
			screen = strings.Join(lines, "\n")
		}
		return fail("no heredoc dialog marked with the fingerprint of its history read in %v (mark %q); screen:\n%s", turnWait, taskMark(r, id), screen)
	}
	logf("%s: the screen read %s (truncated %v), the history read %s", cell, screenRead.Fingerprint, screenRead.Truncated, d.Fingerprint)
	first := fmt.Sprintf("request line 001 of %d", tallLines)
	logf("%s: dialog %s (truncated %v, ambiguous %v, %d body lines)", cell, d.Fingerprint, d.Truncated, d.Ambiguous, strings.Count(d.Body, "\n")+1)
	if d.Truncated || d.Ambiguous || !strings.Contains(d.Body, first) || !strings.Contains(d.Body, last) {
		return fail("the request was not read whole from the history: truncated %v, ambiguous %v, body starts %q", d.Truncated, d.Ambiguous, harness.DialogLine(id, d))
	}
	t1, ok := notices.wait(d.Fingerprint, 60*time.Second)
	if !ok {
		return fail("the notice of the whole dialog %s never showed on the lead's screen", d.Fingerprint)
	}
	res.YesMs = t1.Sub(seen).Milliseconds()
	if code, stdout, stderr := r.ops("task", "answer", id, "1", "--expect", d.Fingerprint); code != 0 {
		return fail("task answer: exit %d: %s %s", code, stdout, stderr)
	}
	for end := time.Now().Add(90 * time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if got, err := os.ReadFile(out); err == nil && string(got) == want.String() {
			res.Pass = true
			logf("%s: PASS: answered the whole %d-line request; %s holds every line", cell, tallLines, filepath.Base(out))
			return res
		}
	}
	got, _ := os.ReadFile(out)
	return fail("task answer 1 went through, but %s does not hold the %d lines after 90 s (%d bytes)", out, tallLines, len(got))
}

// taskMark is the fingerprint of the dialog the watcher marked on a task, from task show; "" for none.
func taskMark(r *leadRig, id string) string {
	_, shown, _ := r.ops("task", "show", id, "--json")
	var task struct {
		Dialog *struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"dialog"`
	}
	if json.Unmarshal([]byte(shown), &task) != nil || task.Dialog == nil {
		return ""
	}
	return task.Dialog.Fingerprint
}

// taskPane is the pane of a task, from task show.
func taskPane(r *leadRig, id string) string {
	_, shown, _ := r.ops("task", "show", id, "--json")
	var task struct {
		Pane string `json:"pane"`
	}
	json.Unmarshal([]byte(shown), &task)
	return task.Pane
}

// killWatchers stops a watcher left behind by a failed cell. Watchers stop by themselves once no task is open.
func killWatchers(state string) {
	locks, _ := filepath.Glob(filepath.Join(state, "watch", "*.lock"))
	for _, l := range locks {
		data, _ := os.ReadFile(l)
		var pid int
		fmt.Sscan(strings.TrimSpace(string(data)), &pid)
		if pid > 0 {
			if p, err := os.FindProcess(pid); err == nil {
				p.Signal(os.Interrupt)
			}
		}
	}
}

type setupFn func(*testing.T, string) *leadRig

var dialogBackends = []struct {
	name  string
	setup setupFn
}{{"iterm2", setupITermLead}, {"tmux", setupTmuxLead}, {"rex", setupRexLead}, {"hc", setupHcLead}}

func TestDialogsRealMatrix(t *testing.T) {
	dialogsOptIn(t)
	root := dialogsRoot(t)
	var results []cellResult
	for _, be := range dialogBackends {
		if !wanted(be.name, be.name+"-claude", be.name+"-agy", be.name+"-claude-tall") {
			continue
		}
		t.Run(be.name, func(t *testing.T) {
			r := be.setup(t, root)
			waitFor(t, "lead prompt", func() bool {
				lines, _ := r.b.Capture(r.lead, 50)
				return strings.Contains(strings.Join(lines, "\n"), "fake harness ready")
			})
			notices := watchNotices(r)
			defer notices.close()
			defer killWatchers(r.state)
			for _, h := range []string{"claude", "agy"} {
				cell := be.name + "-" + h
				if !wanted(be.name, cell) {
					continue
				}
				res := flow(r, notices, h, cell, root, t.Logf)
				results = append(results, res)
				if !res.Pass {
					t.Errorf("%s: %s", cell, res.Note)
				}
			}
			if cell := be.name + "-claude-tall"; wanted(be.name, cell) {
				res := tallFlow(r, notices, cell, root, t.Logf)
				results = append(results, res)
				if !res.Pass {
					t.Errorf("%s: %s", cell, res.Note)
				}
			}
		})
	}
	var b strings.Builder
	b.WriteString("\nDIALOG MATRIX\ncell                 result  yes-notice-ms  no-notice-ms  extra-denies\n")
	pass := 0
	for _, r := range results {
		word := "FAIL"
		if r.Pass {
			word, pass = "pass", pass+1
		}
		fmt.Fprintf(&b, "%-20s %-7s %13d %13d %13d\n", r.Cell, word, r.YesMs, r.NoMs, r.ExtraDenies)
	}
	fmt.Fprintf(&b, "%d/%d cells pass\n", pass, len(results))
	fmt.Print(b.String())
}

func p95(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int64{}, xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	k := (95*len(s)+99)/100 - 1 // nearest rank
	return s[k]
}

// psSample counts the watcher's child processes, and those that run it2.
func psSample(watcher int) (children, it2 int) {
	out, err := exec.Command("ps", "-axo", "pid,ppid,command").Output()
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(out), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		var ppid int
		fmt.Sscan(f[1], &ppid)
		if ppid != watcher {
			continue
		}
		children++
		if strings.Contains(strings.Join(f[2:], " "), "it2") {
			it2++
		}
	}
	return
}

func watcherPID(state string) int {
	locks, _ := filepath.Glob(filepath.Join(state, "watch", "*.lock"))
	for _, l := range locks {
		data, _ := os.ReadFile(l)
		var pid int
		fmt.Sscan(strings.TrimSpace(string(data)), &pid)
		if pid > 0 {
			return pid
		}
	}
	return 0
}

// Six real workers under one lead, on iTerm2 and on tmux: the p95 time from a dialog first visible to its
// notice on the lead. On iTerm2, ps is sampled every 0.5 s for it2 processes started by the watcher.
func TestDialogsRealLatency(t *testing.T) {
	dialogsOptIn(t)
	if os.Getenv("TERMCTL_DIALOGS_LATENCY") == "0" {
		t.Skip("TERMCTL_DIALOGS_LATENCY=0")
	}
	root := dialogsRoot(t)
	var report strings.Builder
	report.WriteString("\nNOTICE LATENCY (6 workers)\n")
	for _, be := range dialogBackends[:2] { // iterm2, tmux
		if !wanted(be.name, "latency-"+be.name) {
			continue
		}
		t.Run(be.name, func(t *testing.T) {
			r := be.setup(t, root)
			waitFor(t, "lead prompt", func() bool {
				lines, _ := r.b.Capture(r.lead, 50)
				return strings.Contains(strings.Join(lines, "\n"), "fake harness ready")
			})
			notices := watchNotices(r)
			defer notices.close()
			defer killWatchers(r.state)
			stop := make(chan struct{})
			var samples, maxChildren, it2Seen int
			var sampler sync.WaitGroup
			if be.name == "iterm2" {
				sampler.Add(1)
				go func() {
					defer sampler.Done()
					for {
						select {
						case <-stop:
							return
						case <-time.After(500 * time.Millisecond):
						}
						pid := watcherPID(r.state)
						if pid == 0 {
							continue
						}
						c, n := psSample(pid)
						samples++
						maxChildren = max(maxChildren, c)
						it2Seen += n
					}
				}()
			}
			var mu sync.Mutex
			var all, upper []int64
			var results []cellResult
			var wg sync.WaitGroup
			for i := 0; i < 6; i++ {
				h := []string{"claude", "agy"}[i%2]
				cell := fmt.Sprintf("lat-%s-%d-%s", be.name, i+1, h)
				wg.Add(1)
				go func() {
					defer wg.Done()
					time.Sleep(time.Duration(i) * 2 * time.Second) // spread the spawns a little
					res := flow(r, notices, h, cell, root, t.Logf)
					mu.Lock()
					results = append(results, res)
					all = append(all, res.Latencies...)
					upper = append(upper, res.Upper...)
					mu.Unlock()
				}()
			}
			wg.Wait()
			close(stop)
			sampler.Wait()
			passed := 0
			for _, res := range results {
				if res.Pass {
					passed++
				} else {
					t.Errorf("%s: %s", res.Cell, res.Note)
				}
			}
			sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
			sort.Slice(upper, func(i, j int) bool { return upper[i] < upper[j] })
			line := fmt.Sprintf("%-7s workers %d/6 pass, %d notices, p95 %d ms, max %d ms, all %v", be.name, passed, len(all), p95(all), func() int64 {
				if len(all) == 0 {
					return 0
				}
				return all[len(all)-1]
			}(), all) + fmt.Sprintf("; counted from the look before the dialog was seen (an upper bound): p95 %d ms, all %v", p95(upper), upper)
			if be.name == "iterm2" {
				line += fmt.Sprintf("; ps: %d samples, it2 processes under the watcher: %d, most children at once: %d", samples, it2Seen, maxChildren)
				if it2Seen != 0 {
					t.Errorf("the watcher started it2 %d times", it2Seen)
				}
			}
			mu.Lock()
			report.WriteString(line + "\n")
			mu.Unlock()
		})
	}
	fmt.Print(report.String())
}

// TestDialogsRealHiddenAgy: a real agy asked to run an 84-line heredoc shows 3 of its lines and
// "⋯ (N lines hidden)". The lead hears of the dialog, task answer refuses it (exit 6) whatever the choice,
// the heredoc's file stays absent, and task deny (Esc) works. Run alone: TERMCTL_DIALOGS_ONLY=tmux-agy-hidden.
func TestDialogsRealHiddenAgy(t *testing.T) {
	dialogsOptIn(t)
	const cell = "tmux-agy-hidden"
	if !wanted(cell) {
		t.Skip("not in TERMCTL_DIALOGS_ONLY")
	}
	root := dialogsRoot(t)
	r := setupTmuxLead(t, root)
	waitFor(t, "lead prompt", func() bool {
		lines, _ := r.b.Capture(r.lead, 50)
		return strings.Contains(strings.Join(lines, "\n"), "fake harness ready")
	})
	notices := watchNotices(r)
	defer notices.close()
	defer killWatchers(r.state)
	dir := filepath.Join(root, cell)
	os.MkdirAll(dir, 0o755)
	out := filepath.Join(dir, "out.txt")
	heredoc := "cat > " + out + " <<'EOF'\n"
	for i := 1; i <= 80; i++ {
		heredoc += fmt.Sprintf("request line %02d of 80: the quick brown fox jumps over the lazy dog\n", i)
	}
	os.WriteFile(filepath.Join(dir, "cmd.txt"), []byte(heredoc+"EOF\n"), 0o644)
	cmd, err := harnessCommand("agy", dir, r.state)
	if err != nil {
		t.Fatal(err)
	}
	brief := "Read the file " + filepath.Join(dir, "cmd.txt") + ", then run its whole content verbatim as one single shell command (it is a heredoc that writes out.txt). " +
		"Do not run it as a script file (no bash cmd.txt, no sh), do not split or shorten it, run nothing else."
	code, stdout, stderr := r.ops("task", "new", "agy", "--title", cell, "--brief", brief, "--cwd", dir, "--command", cmd, "--where", "tab", "--timeout", "180")
	if code != 0 {
		t.Fatalf("task new: exit %d: %s %s", code, stdout, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	id := lines[len(lines)-1]
	defer r.ops("task", "close", "--force", id)
	_, shown, _ := r.ops("task", "show", id, "--json")
	var task struct {
		Pane string `json:"pane"`
	}
	json.Unmarshal([]byte(shown), &task)
	wp := findPane(r.b, task.Pane)
	if wp == nil {
		t.Fatalf("worker pane %q not found", task.Pane)
	}
	read := func() *harness.Dialog {
		lines, err := r.b.Capture(wp, 200)
		if err != nil {
			return nil
		}
		return harness.Profiles["agy"].ReadDialog(lines)
	}
	var d *harness.Dialog
	for end := time.Now().Add(turnWait); time.Now().Before(end) && d == nil; time.Sleep(200 * time.Millisecond) {
		if got := read(); got != nil && got.Hidden {
			d = got
		} else if got != nil {
			code, _, stderr := r.ops("task", "deny", id, "--expect", got.Fingerprint)
			t.Logf("denied another dialog %q: exit %d %s", harness.DialogLine(id, got), code, stderr)
		}
	}
	if d == nil {
		lines, _ := r.b.Capture(wp, 60)
		t.Fatalf("no partly hidden dialog in %v; screen:\n%s", turnWait, strings.Join(lines, "\n"))
	}
	t.Logf("dialog: %s", harness.DialogLine(id, d))
	if _, ok := notices.wait(d.Fingerprint, 60*time.Second); !ok {
		t.Fatalf("the notice of dialog %s never showed on the lead's screen", d.Fingerprint)
	}
	for _, args := range [][]string{{"1"}, {"2", "--persist"}} {
		code, stdout, stderr := r.ops(append([]string{"task", "answer", id}, append(args, "--expect", d.Fingerprint)...)...)
		t.Logf("task answer %v: exit %d %s%s", args, code, stdout, stderr)
		if code != 6 {
			t.Fatalf("task answer %v on a partly hidden request: want exit 6, got %d", args, code)
		}
	}
	if now := read(); now == nil || now.Fingerprint != d.Fingerprint {
		t.Fatalf("a refused answer changed the dialog: %+v", now)
	}
	if code, stdout, stderr := r.ops("task", "deny", id, "--expect", d.Fingerprint); code != 0 {
		t.Fatalf("task deny: exit %d %s %s", code, stdout, stderr)
	}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		if got := read(); got != nil {
			r.ops("task", "deny", id, "--expect", got.Fingerprint)
		}
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatalf("%s exists: a partly hidden request ran", out)
	}
}
