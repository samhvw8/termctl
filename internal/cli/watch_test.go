package cli

import (
	"os"
	"path/filepath"
	"testing"
)

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
