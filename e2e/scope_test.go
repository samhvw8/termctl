package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestScopeLifecycleAndCrashRebind(t *testing.T) {
	r := newRig(t)
	lead, leadLog := r.lead()

	// Start an existing pane running agy (not created by spawn/task new).
	workerLog := filepath.Join(r.tmp, "scope-worker.log")
	worker := tmux(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "-n", "worker", "--", filepath.Join(binDir, "agy", "agy")+" "+workerLog)
	tmux(t, "set-option", "-p", "-t", worker, "@termctl_label", "worker")
	waitFor(t, "worker reads as agy", func() bool {
		_, out, _ := r.ops(lead, "which", "tmux:"+worker)
		return strings.Contains(out, " agy ")
	})

	// 1. Silent attach: 0 turns typed to worker; watcher starts.
	code, out, stderr := r.ops(lead, "attach", "tmux:"+worker, "--name", "auth-refactor", "--about", "refactor auth", "--sub-depth", "1")
	if code != 0 || !strings.Contains(out, "auth-refactor (s") {
		t.Fatalf("attach: code=%d out=%q stderr=%q", code, out, stderr)
	}
	sid := regexp.MustCompile(`auth-refactor \((s[0-9a-f]{6})`).FindStringSubmatch(out)[1]
	if len(readLog(workerLog)) != 0 {
		t.Fatalf("attach without --notify must be silent, got %v", readLog(workerLog))
	}

	// 2. First send piggybacks attach suffix once; second send omits it.
	if code, out, stderr := r.ops(lead, "send", "--force", "tmux:"+worker, "first task prompt"); code != 0 {
		t.Fatalf("first send: %d %q %q", code, out, stderr)
	}
	waitFor(t, "first send delivered", func() bool { return len(readLog(workerLog)) >= 1 })
	firstLine := readLog(workerLog)[0]
	if !strings.Contains(firstLine, "[attached: scope auth-refactor, lead tmux:"+lead+"; report with harness-ops report]") {
		t.Fatalf("missing piggyback suffix in first send: %q", firstLine)
	}
	if code, out, stderr := r.ops(lead, "send", "--force", "tmux:"+worker, "second prompt"); code != 0 {
		t.Fatalf("second send: %d %q %q", code, out, stderr)
	}
	waitFor(t, "second send delivered", func() bool { return len(readLog(workerLog)) >= 2 })
	if strings.Contains(readLog(workerLog)[1], "[attached: scope auth-refactor") {
		t.Fatalf("piggyback suffix repeated in second send: %q", readLog(workerLog)[1])
	}

	// 3. Scope reports progress with --notify and reaches lead.
	if code, out, stderr := r.ops(worker, "report", "halfway", "done", "--notify"); code != 0 || !strings.Contains(out, "reported on scope auth-refactor") {
		t.Fatalf("report: code=%d out=%q stderr=%q", code, out, stderr)
	}
	waitFor(t, "progress notice on lead", func() bool {
		return strings.Contains(strings.Join(readLog(leadLog), "\n"), "PROGRESS scope auth-refactor ("+sid+"): halfway done")
	})

	// 4. Dialog triggers watcher notice to lead, answered by scope name.
	tmux(t, "send-keys", "-t", worker, "/dialog", "Enter")
	var dlgNote string
	waitFor(t, "scope dialog notice on lead", func() bool {
		for _, ln := range readLog(leadLog) {
			if strings.Contains(ln, "scope auth-refactor ("+sid+") DIALOG ") {
				dlgNote = ln
				return true
			}
		}
		return false
	})
	fp := dialogFP.FindStringSubmatch(dlgNote)[1]
	if code, out, stderr := r.ops(lead, "answer", "auth-refactor", "3", "--expect", fp); code != 0 || !strings.Contains(out, "No, cancel") {
		t.Fatalf("answer by scope name: code=%d out=%q stderr=%q", code, out, stderr)
	}
	waitFor(t, "dialog answer logged", func() bool {
		return strings.Contains(strings.Join(readLog(workerLog), "\n"), "<<ANSWER 3>>")
	})

	// 5. Worker pane dies -> Reconcile marks lost + queues "exited" notice -> rebind onto new pane keeps scope identity.
	tmux(t, "kill-pane", "-t", worker)
	waitFor(t, "scope ls shows lost and exited notice delivered", func() bool {
		_, lsOut, _ := r.ops(lead, "scope", "ls")
		return strings.Contains(lsOut, "lost") && strings.Contains(strings.Join(readLog(leadLog), "\n"), "lost; rebind with harness-ops attach <new-pane> --name auth-refactor")
	})

	worker2Log := filepath.Join(r.tmp, "scope-worker2.log")
	worker2 := tmux(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "-n", "worker2", "--", filepath.Join(binDir, "agy", "agy")+" "+worker2Log)
	waitFor(t, "worker2 reads as agy", func() bool {
		_, out, _ := r.ops(lead, "which", "tmux:"+worker2)
		return strings.Contains(out, " agy ")
	})
	code, out, stderr = r.ops(lead, "attach", "tmux:"+worker2, "--name", "auth-refactor")
	if code != 0 || !strings.Contains(out, "auth-refactor ("+sid+",") {
		t.Fatalf("rebind attach: code=%d out=%q stderr=%q", code, out, stderr)
	}

	// 6. resume <scope> and scope ls --tree reflect timeline and live rebound state.
	code, resOut, stderr := r.ops(lead, "resume", "auth-refactor")
	if code != 0 {
		t.Fatalf("resume scope: %d %s", code, stderr)
	}
	for _, want := range []string{"# Scope auth-refactor (" + sid + ")", "halfway done", "scope.attach", "scope.report", "answer", "scope.lost"} {
		if !strings.Contains(resOut, want) {
			t.Fatalf("resume auth-refactor missing %q:\n%s", want, resOut)
		}
	}
	if code, treeOut, stderr := r.ops(lead, "scope", "ls", "--tree"); code != 0 || !strings.Contains(treeOut, "auth-refactor") || !strings.Contains(treeOut, "live") {
		t.Fatalf("scope ls --tree: code=%d out=%q stderr=%q", code, treeOut, stderr)
	}

	// 7. detach wakes watcher via SIGUSR1 and stops it promptly.
	if code, out, stderr := r.ops(lead, "detach", "auth-refactor"); code != 0 || !strings.Contains(out, "detached auth-refactor ("+sid+")") {
		t.Fatalf("detach: code=%d out=%q stderr=%q", code, out, stderr)
	}
	logs, _ := filepath.Glob(filepath.Join(r.tmp, "state", "watch", "*.log"))
	if len(logs) != 1 {
		t.Fatalf("expected 1 watch log, got %v", logs)
	}
	waitFor(t, "watcher stops immediately on detach", func() bool {
		data, _ := os.ReadFile(logs[0])
		return strings.Contains(string(data), "no running task and no held notice left")
	})
}
