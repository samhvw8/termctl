package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var dialogFP = regexp.MustCompile(`DIALOG ([0-9a-f]{8}):`)

// A worker shows a permission dialog: the watcher that task new started tells the owner, and the
// owner answers it through harness-ops. The fake runs as "agy" and draws agy's dialog.
func TestDialogWatchAndAnswer(t *testing.T) {
	r := newRig(t)
	lead, leadLog := r.lead()
	workerLog := filepath.Join(r.tmp, "agy.log")
	code, out, stderr := r.ops(lead, "task", "new", "generic", "--title", "dialog", "--brief", "x", "--cwd", r.tmp,
		"--command", filepath.Join(binDir, "agy", "agy")+" "+workerLog, "--timeout", "30")
	if code != 0 {
		t.Fatal(out, stderr)
	}
	id := strings.TrimSpace(out)
	_, shown, _ := r.ops(lead, "task", "show", id, "--json")
	worker := strings.SplitN(regexp.MustCompile(`"pane": "([^"]+)"`).FindStringSubmatch(shown)[1], ":", 2)[1]
	waitFor(t, "brief", func() bool { return len(readLog(workerLog)) > 0 })
	waitFor(t, "worker reads as agy", func() bool {
		_, out, _ := r.ops(lead, "which", "tmux:"+worker)
		return strings.Contains(out, " agy ")
	})

	tmux(t, "send-keys", "-t", worker, "/dialog", "Enter")
	var note string
	waitFor(t, "dialog notice to the owner", func() bool {
		for _, l := range readLog(leadLog) {
			if strings.Contains(l, "DIALOG") {
				note = l
				return true
			}
		}
		return false
	})
	t.Log(note)
	m := dialogFP.FindStringSubmatch(note)
	if m == nil || !strings.HasPrefix(note, "[agent-ops from "+id+" ") || !strings.Contains(note, "Run this command?") ||
		!strings.Contains(note, "touch fake-file") || !strings.Contains(note, "[persist]") || !strings.Contains(note, "harness-ops answer "+id) {
		t.Fatal(note)
	}
	fp := m[1]
	if code, out, _ := r.ops(lead, "watch"); code != 0 || !strings.Contains(out, "already runs") {
		t.Fatal("a second watcher for the same owner must step aside: ", code, out)
	}
	if code, _, stderr := r.ops(lead, "answer", id, "2", "--expect", fp); code != 0 && code != 6 {
		t.Fatal(code, stderr)
	} else if code == 0 {
		t.Fatal("an always-allow choice went through without --persist")
	}
	if code, _, _ := r.ops(lead, "answer", id, "1", "--expect", "00000000"); code != 7 {
		t.Fatal(code)
	}
	if code, _, _ := r.ops(r.sender, "answer", id, "1", "--expect", fp); code != 2 {
		t.Fatal("only the owner answers: ", code)
	}
	code, out, stderr = r.ops(lead, "answer", id, "3", "--expect", fp)
	if code != 0 || !strings.Contains(out, "No, cancel") {
		t.Fatal(code, out, stderr)
	}
	waitFor(t, "answer logged", func() bool { return strings.Contains(strings.Join(readLog(workerLog), "\n"), "<<ANSWER 3>>") })
	if code, _, _ := r.ops(lead, "answer", id, "1", "--expect", fp); code != 7 {
		t.Fatal("no dialog left: ", code)
	}

	// task wait returns a dialog too; deny presses Esc, never Enter. A draft on the lead holds the watcher's
	// notice: a dialog whose notice reached the lead is not returned by task wait again.
	tmux(t, "send-keys", "-t", lead, "-l", "draft")
	waitFor(t, "draft on the lead", func() bool { return strings.Contains(tmux(t, "capture-pane", "-p", "-t", lead), "╰─ draft") })
	tmux(t, "send-keys", "-t", worker, "/dialog", "Enter")
	code, out, stderr = r.ops(lead, "task", "wait", id, "--timeout", "20")
	if code != 0 || !strings.Contains(out, "DIALOG task "+id) || !strings.Contains(out, "[persist]") {
		t.Fatal(code, out, stderr)
	}
	fp = regexp.MustCompile(`fingerprint: +([0-9a-f]{8})`).FindStringSubmatch(out)[1]
	if code, out, stderr := r.ops(lead, "deny", id, "--expect", fp); code != 0 || !strings.Contains(out, "pressed Esc") {
		t.Fatal(code, out, stderr)
	}
	waitFor(t, "deny logged", func() bool { return strings.Contains(strings.Join(readLog(workerLog), "\n"), "<<CANCELLED>>") })

	// With no open task left, the watcher stops.
	if code, _, stderr := r.ops(lead, "task", "close", "--force", id); code != 0 {
		t.Fatal(stderr)
	}
	logs, _ := filepath.Glob(filepath.Join(r.tmp, "state", "watch", "*.log"))
	if len(logs) != 1 {
		t.Fatal(logs)
	}
	waitFor(t, "watcher stops", func() bool {
		data, _ := os.ReadFile(logs[0])
		return strings.Contains(string(data), "no running task and no held notice left")
	})
}

// A lead starts a partner with spawn (as pairs and live teams do), not task new: the watcher that the
// spawn's prompt started tells the lead of the partner's dialog, the lead answers and denies it with the
// top-level commands by the pane's ID, another pane is refused, the watcher stops once the partner is idle,
// and a msg to the partner starts it again.
func TestDialogOnASpawnedPane(t *testing.T) {
	r := newRig(t)
	lead, leadLog := r.lead()
	workerLog := filepath.Join(r.tmp, "partner.log")
	code, out, stderr := r.ops(lead, "spawn", "generic", "--where", "tab", "--label", "partner", "--cwd", r.tmp,
		"--command", filepath.Join(binDir, "agy", "agy")+" "+workerLog, "--prompt", "start", "--timeout", "30")
	if code != 0 {
		t.Fatal(out, stderr)
	}
	pane := strings.TrimSpace(out)
	worker := strings.TrimPrefix(pane, "tmux:")
	waitFor(t, "prompt", func() bool { return len(readLog(workerLog)) > 0 })
	notice := func(n int) string {
		var notes []string
		waitFor(t, "dialog notice to the lead", func() bool {
			notes = nil
			for _, l := range readLog(leadLog) {
				if strings.Contains(l, "DIALOG") {
					notes = append(notes, l)
				}
			}
			return len(notes) >= n
		})
		return notes[n-1]
	}
	tmux(t, "send-keys", "-t", worker, "/dialog", "Enter")
	note := notice(1)
	t.Log(note)
	m := dialogFP.FindStringSubmatch(note)
	if m == nil || !strings.HasPrefix(note, "[agent-ops from partner (generic) id="+pane+" ") || !strings.Contains(note, "pane "+pane+" (partner) DIALOG") ||
		!strings.Contains(note, "touch fake-file") || !strings.Contains(note, "answer: harness-ops answer "+pane+" <n> --expect "+m[1]) {
		t.Fatal(note)
	}
	fp := m[1]
	if code, _, _ := r.ops(r.sender, "answer", pane, "1", "--expect", fp); code != 2 {
		t.Fatal("only the pane's owner answers: ", code)
	}
	if code, _, _ := r.ops(lead, "answer", pane, "2", "--expect", fp); code != 6 {
		t.Fatal("an always-allow choice went through without --persist: ", code)
	}
	if code, out, stderr := r.ops(lead, "answer", pane, "3", "--expect", fp); code != 0 || !strings.Contains(out, "No, cancel") {
		t.Fatal(code, out, stderr)
	}
	waitFor(t, "answer logged", func() bool { return strings.Contains(strings.Join(readLog(workerLog), "\n"), "<<ANSWER 3>>") })
	tmux(t, "send-keys", "-t", worker, "/dialog", "Enter")
	fp = dialogFP.FindStringSubmatch(notice(2))[1]
	if code, out, stderr := r.ops(lead, "deny", pane, "--expect", fp); code != 0 || !strings.Contains(out, "pressed Esc") {
		t.Fatal(code, out, stderr)
	}
	waitFor(t, "deny logged", func() bool { return strings.Contains(strings.Join(readLog(workerLog), "\n"), "<<CANCELLED>>") })

	// The partner is idle: the watcher stops, and is not kept on by the open pane. A msg starts it again.
	logs, _ := filepath.Glob(filepath.Join(r.tmp, "state", "watch", "*.log"))
	if len(logs) != 1 {
		t.Fatal(logs)
	}
	watchLog := func() string { data, _ := os.ReadFile(logs[0]); return string(data) }
	for end := time.Now().Add(60 * time.Second); !strings.Contains(watchLog(), "no running task and no held notice left"); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("the watcher did not stop with its partner idle:\n%s", watchLog())
		}
	}
	// --force: the fake draws no agy input line.
	if code, _, stderr := r.ops(lead, "msg", "--force", "partner", "your turn"); code != 0 {
		t.Fatal(stderr)
	}
	waitFor(t, "a watcher again", func() bool { return strings.Count(watchLog(), "watching the tasks of") == 2 })
	tmux(t, "send-keys", "-t", worker, "/dialog", "Enter")
	notice(3)
	if code, out, _ := r.ops(lead, "ls", "--mine"); code != 0 || !strings.Contains(out, "dialog "+dialogFP.FindStringSubmatch(notice(3))[1]) {
		t.Fatal("ls --mine does not show the open dialog: ", out)
	}
	r.ops(lead, "close", "--force", pane)
}
