package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The watcher log is cut to its last lines when a watcher starts, so it cannot grow without bound (review O10).
func TestWatcherLogIsCutAtStart(t *testing.T) {
	log := filepath.Join(t.TempDir(), "w.log")
	var b strings.Builder
	for i := 0; b.Len() <= logMax; i++ {
		fmt.Fprintf(&b, "12:00:00 line %d\n", i)
	}
	os.WriteFile(log, []byte(b.String()), 0o644)
	trimLog(log)
	data, _ := os.ReadFile(log)
	if len(data) > logKeep || len(data) < logKeep-100 {
		t.Fatalf("kept %d bytes, want just under %d", len(data), logKeep)
	}
	if !strings.HasSuffix(b.String(), string(data)) || !strings.HasPrefix(string(data), "12:00:00 line ") {
		t.Fatalf("not the last whole lines: %q", string(data[:40]))
	}
	small := "12:00:00 watching\n"
	os.WriteFile(log, []byte(small), 0o644)
	trimLog(log)
	if data, _ := os.ReadFile(log); string(data) != small {
		t.Fatal("a small log was changed")
	}
}

// A changed executable restarts the watcher only once it has stopped changing, so a binary still being
// written is never run.
func TestChangedBinaryWaitsForAStableNewBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harness-ops")
	os.WriteFile(path, []byte("build 1"), 0o755)
	changed := changedBinary(path)
	if changed() {
		t.Fatal("same build")
	}
	tmp := path + ".new"
	os.WriteFile(tmp, []byte("build 2, longer"), 0o755)
	os.Rename(tmp, path)
	if changed() {
		t.Fatal("restarted on the first look at a new build; it may still be written")
	}
	if !changed() {
		t.Fatal("a new build that stayed the same must restart")
	}
	os.Remove(path)
	changed = changedBinary(path)
	if changed() {
		t.Fatal("no file: nothing to restart into")
	}
	gone := changedBinary(filepath.Join(t.TempDir(), "x"))
	os.WriteFile(path, []byte("x"), 0o755)
	if gone() || gone() {
		t.Fatal("a watcher whose executable was unknown at start never restarts")
	}
}
