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

// termctl runs the termctl binary as if from pane, like rig.ops runs harness-ops.
func (r *rig) termctl(pane string, args ...string) (int, string, string) {
	r.t.Helper()
	cmd := exec.Command(filepath.Join(binDir, "termctl"), append([]string{"--backend", "tmux", "-L", socket}, args...)...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ITERM_SESSION_ID=") && !strings.HasPrefix(kv, "REX_") && (!strings.HasPrefix(kv, "TMUX") || strings.HasPrefix(kv, "TMUX_TMPDIR=")) {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, fmt.Sprintf("TMUX=%s,1,0", r.socketPath), "TMUX_PANE="+pane, "TERMCTL_STATE_DIR="+filepath.Join(r.tmp, "state"))
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

// paneState is what must survive a move: the process, and that it still runs.
func paneState(t *testing.T, pane string) (window, pid string) {
	t.Helper()
	f := strings.Fields(tmux(t, "display-message", "-p", "-t", pane, "#{window_id} #{pane_pid} #{pane_dead}"))
	if f[2] != "0" {
		t.Fatalf("%s is dead", pane)
	}
	return f[0], f[1]
}

// Every command on real tmux: a task's worker and a plain pane move between windows, swap and get
// laid out, and each keeps its PID, label and task; the worker still answers messages afterwards.
func TestMovePullSwapArrangeKeepThePaneAlive(t *testing.T) {
	r := newRig(t)
	caller := tmux(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "-n", "caller", "--", "sleep 300")
	tmux(t, "set-option", "-p", "-t", caller, "@termctl_label", "caller")
	callerWin, _ := paneState(t, caller)
	fakehWin, _ := paneState(t, r.harness)

	workerLog := filepath.Join(r.tmp, "worker.log")
	code, out, stderr := r.ops(caller, "task", "new", "generic", "--title", "mover", "--brief", "stay alive", "--cwd", r.tmp, "--command", fakeCmd(workerLog), "--timeout", "30")
	if code != 0 {
		t.Fatal(out, stderr)
	}
	id := strings.TrimSpace(out[strings.LastIndex(strings.TrimSpace(out), "\n")+1:])
	waitFor(t, "brief", func() bool { return len(readLog(workerLog)) > 0 })
	_, shown, _ := r.ops(caller, "task", "show", id, "--json")
	var task map[string]any
	json.Unmarshal([]byte(shown), &task)
	worker := workerPane(task)
	if w, _ := paneState(t, worker); w != callerWin {
		t.Fatalf("the worker started in %s, want the caller's window %s", w, callerWin)
	}
	_, workerPID := paneState(t, worker)
	_, senderPID := paneState(t, r.sender)
	check := func(step string) {
		t.Helper()
		if _, pid := paneState(t, worker); pid != workerPID {
			t.Fatalf("%s: worker pid %s, want %s", step, pid, workerPID)
		}
		if _, pid := paneState(t, r.sender); pid != senderPID {
			t.Fatalf("%s: sender pid %s, want %s", step, pid, senderPID)
		}
		if label := tmux(t, "show-options", "-p", "-v", "-t", worker, "@termctl_label"); label != id {
			t.Fatalf("%s: worker label %q, want %q", step, label, id)
		}
		_, shown, _ := r.ops(caller, "task", "show", id, "--json")
		var now map[string]any
		json.Unmarshal([]byte(shown), &now)
		if now["pane"] != task["pane"] || now["status"] != "running" {
			t.Fatalf("%s: task %v", step, now)
		}
	}

	// harness-ops: out to a new window, then back into the caller's
	code, out, stderr = r.ops(caller, "move", id, "--to", "new")
	newWin, _ := paneState(t, worker)
	if code != 0 || newWin == callerWin || !strings.Contains(out, "task "+id) || !strings.Contains(out, "to a new tab "+newWin) {
		t.Fatal(code, out, stderr)
	}
	idx := func(w string) int {
		var n int
		fmt.Sscan(tmux(t, "display-message", "-p", "-t", w, "#{window_index}"), &n)
		return n
	}
	if idx(newWin) != idx(callerWin)+1 {
		t.Fatalf("the new window is at %d, want right after the caller's %d", idx(newWin), idx(callerWin))
	}
	check("move --to new")
	if code, out, stderr = r.ops(caller, "pull", id); code != 0 {
		t.Fatal(code, out, stderr)
	}
	if w, _ := paneState(t, worker); w != callerWin {
		t.Fatalf("pull left the worker in %s", w)
	}
	check("pull")

	// termctl: a plain pane into fakeh's window, then swapped with the worker
	if code, out, stderr = r.termctl(caller, "move", "sender", "--to", "tab-of:fakeh"); code != 0 {
		t.Fatal(code, out, stderr)
	}
	if w, _ := paneState(t, r.sender); w != fakehWin {
		t.Fatalf("sender is in %s, want fakeh's %s", w, fakehWin)
	}
	check("move --to tab-of")
	if code, out, stderr = r.termctl(caller, "swap", "sender", id); code != 0 {
		t.Fatal(code, out, stderr)
	}
	if w, _ := paneState(t, worker); w != fakehWin {
		t.Fatalf("after the swap the worker is in %s, want %s", w, fakehWin)
	}
	if w, _ := paneState(t, r.sender); w != callerWin {
		t.Fatalf("after the swap the sender is in %s, want %s", w, callerWin)
	}
	check("swap")

	// layouts
	geo := func(win string) []string {
		return strings.Split(tmux(t, "list-panes", "-t", win, "-F", "#{pane_id} #{pane_left} #{pane_top} #{pane_width}"), "\n")
	}
	if code, out, stderr = r.termctl(caller, "arrange", "tab-of:fakeh", "--layout", "rows"); code != 0 || !strings.Contains(out, "as rows") {
		t.Fatal(code, out, stderr)
	}
	for _, g := range geo(fakehWin) {
		if strings.Fields(g)[1] != "0" {
			t.Fatalf("rows: %v", geo(fakehWin))
		}
	}
	if code, out, stderr = r.ops(caller, "arrange", "--layout", "main-left"); code != 0 || !strings.Contains(out, "main tmux:"+caller) {
		t.Fatal(code, out, stderr)
	}
	var left, width int
	fmt.Sscan(tmux(t, "display-message", "-p", "-t", caller, "#{pane_left} #{pane_width}"), &left, &width)
	if left != 0 || width < 95 || width > 105 {
		t.Fatalf("main-left: the caller is at %d, %d wide; want the left half of 200: %v", left, width, geo(callerWin))
	}
	check("arrange")

	// ls shows each pane's window
	_, out, _ = r.ops(caller, "ls", "--json")
	var rows []map[string]any
	json.Unmarshal([]byte(out), &rows)
	found := false
	for _, row := range rows {
		if row["pane_id"] == "tmux:"+worker {
			found = row["tab"] == fakehWin
		}
	}
	if !found {
		t.Fatalf("ls --json does not give the worker's window %s: %s", fakehWin, out)
	}

	// the worker's harness still works after all that
	if code, out, stderr = r.ops(caller, "msg", id, "still", "there"); code != 0 {
		t.Fatal(code, out, stderr)
	}
	waitFor(t, "message after moves", func() bool {
		lines := readLog(workerLog)
		return len(lines) > 1 && strings.HasSuffix(lines[len(lines)-1], "still there")
	})

	// refusals change nothing
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"move", "caller", "--to", "new"}, 2, "own pane"},
		{[]string{"move", "fakeh", "--to", "tab-of:fakeh"}, 2, "already in tab"},
		{[]string{"move", "fakeh", "--to", "@999"}, 2, "no tab"},
		{[]string{"arrange", "--layout", "spiral"}, 2, "--layout must be"},
	} {
		before := tmux(t, "list-panes", "-a", "-F", "#{pane_id} #{window_id} #{pane_left} #{pane_top}")
		if code, _, stderr := r.termctl(caller, c.args...); code != c.code || !strings.Contains(stderr, c.want) {
			t.Errorf("termctl %v: exit %d %q", c.args, code, stderr)
		}
		if after := tmux(t, "list-panes", "-a", "-F", "#{pane_id} #{window_id} #{pane_left} #{pane_top}"); after != before {
			t.Errorf("termctl %v changed the layout", c.args)
		}
	}
}
