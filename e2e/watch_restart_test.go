package e2e

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	out.Close()
}

var watchingPID = regexp.MustCompile(`watching the tasks of \S+ \(pid (\d+)\)`)

// The watcher runs from its own executable. When that file is replaced by a new build (as
// "mise run install" does), the watcher execs the new one in place: same pid, same arguments, and
// it goes on serving the owner, so an upgraded binary never serves old parsers and fingerprints.
func TestWatcherRestartsIntoANewBuild(t *testing.T) {
	r := newRig(t)
	lead, leadLog := r.lead()
	bin := filepath.Join(r.tmp, "bin", "harness-ops")
	os.MkdirAll(filepath.Dir(bin), 0o755)
	copyFile(t, filepath.Join(binDir, "harness-ops"), bin)
	workerLog := filepath.Join(r.tmp, "agy.log")
	code, out, stderr := r.opsBin(bin, lead, "task", "new", "generic", "--title", "restart", "--brief", "x", "--cwd", r.tmp,
		"--command", filepath.Join(binDir, "agy", "agy")+" "+workerLog, "--timeout", "30")
	if code != 0 {
		t.Fatal(out, stderr)
	}
	id := strings.TrimSpace(out)
	logs, _ := filepath.Glob(filepath.Join(r.tmp, "state", "watch", "*.log"))
	if len(logs) != 1 {
		t.Fatal(logs)
	}
	watchLog := func() string { data, _ := os.ReadFile(logs[0]); return string(data) }
	waitFor(t, "watcher up", func() bool { return watchingPID.MatchString(watchLog()) })
	pid := watchingPID.FindStringSubmatch(watchLog())[1]

	// A new build lands by rename, as go build and installers do: a new file under the same name.
	copyFile(t, filepath.Join(binDir, "harness-ops"), bin+".new")
	if err := os.Rename(bin+".new", bin); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the watcher restarts", func() bool { return len(watchingPID.FindAllString(watchLog(), -1)) == 2 })
	log := watchLog()
	t.Log(log)
	ms := watchingPID.FindAllStringSubmatch(log, -1)
	if !strings.Contains(log, "the executable changed; restarting") || ms[1][1] != pid {
		t.Fatalf("want an exec in place (pid %s):\n%s", pid, log)
	}

	// The restarted watcher still serves the owner.
	waitFor(t, "brief", func() bool { return len(readLog(workerLog)) > 0 })
	_, shown, _ := r.ops(lead, "task", "show", id, "--json")
	worker := strings.SplitN(regexp.MustCompile(`"pane": "([^"]+)"`).FindStringSubmatch(shown)[1], ":", 2)[1]
	waitFor(t, "worker reads as agy", func() bool {
		_, out, _ := r.ops(lead, "which", "tmux:"+worker)
		return strings.Contains(out, " agy ")
	})
	tmux(t, "send-keys", "-t", worker, "/dialog", "Enter")
	waitFor(t, "dialog notice after the restart", func() bool {
		return strings.Contains(strings.Join(readLog(leadLog), "\n"), "DIALOG")
	})
	if code, _, stderr := r.ops(lead, "task", "close", "--force", id); code != 0 {
		t.Fatal(stderr)
	}
	waitFor(t, "watcher stops", func() bool { return strings.Contains(watchLog(), "no running task and no held notice left") })
}
