package cli

import (
	"os"
	"path/filepath"
	"strings"
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

// install puts a build at path, executable.
func install(t *testing.T, path, build string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(build), 0o755); err != nil {
		t.Fatal(err)
	}
}

func relink(t *testing.T, link, target string) {
	t.Helper()
	os.Remove(link)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// mise up installs a new version beside the old one and re-points the PATH entry; the file the watcher runs
// never changes. Each round the watcher also looks at the harness-ops that PATH resolves to, and execs it
// once it differs from the running one and has stayed the same for a round (round-6 review R5).
func TestRestartFollowsTheHarnessOpsOnPath(t *testing.T) {
	dir := t.TempDir()
	v1, v2, bin := filepath.Join(dir, "v1", "harness-ops"), filepath.Join(dir, "v2", "harness-ops"), filepath.Join(dir, "bin")
	install(t, v1, "build 1")
	install(t, v2, "build 2, longer")
	os.MkdirAll(bin, 0o755)
	link := filepath.Join(bin, "harness-ops")
	relink(t, link, v1)
	t.Setenv("PATH", bin)
	t.Setenv(execedEnv, "")
	check := restartCheck(v1)
	if got := check(); got != "" {
		t.Fatalf("PATH runs the same file: %q", got)
	}
	relink(t, link, v2)
	if got := check(); got != "" {
		t.Fatalf("restarted on the first look at a new PATH entry: %q", got)
	}
	want, _ := filepath.EvalSymlinks(v2)
	if got := check(); got != want {
		t.Fatalf("want a restart into %s, got %q", want, got)
	}

	// After the exec, the new process does not exec the same file again, even when it runs something else
	// (a wrapper that execs another file).
	t.Setenv(execedEnv, pathKey(want))
	if got := restartCheck(v1); got() != "" || got() != "" {
		t.Fatal("exec'd into the same PATH build twice")
	}

	// The exec names the build it goes into, once, replacing an older name.
	t.Setenv(execedEnv, "old")
	var gotEnv []string
	oldExec := Exec
	Exec = func(_ string, _ []string, env []string) error { gotEnv = env; return nil }
	defer func() { Exec = oldExec }()
	(&App{}).reexec(v2)
	var named []string
	for _, kv := range gotEnv {
		if strings.HasPrefix(kv, execedEnv+"=") {
			named = append(named, kv)
		}
	}
	if len(named) != 1 || named[0] != execedEnv+"="+pathKey(v2) {
		t.Fatalf("exec env: %q", named)
	}

	// A mise shim is a link to mise itself: not a harness-ops build, never exec'd.
	t.Setenv(execedEnv, "")
	install(t, filepath.Join(dir, "mise"), "mise")
	relink(t, link, filepath.Join(dir, "mise"))
	if c := restartCheck(v1); c() != "" || c() != "" {
		t.Fatal("exec'd into a mise shim")
	}
	// No harness-ops on PATH: nothing to follow.
	t.Setenv("PATH", filepath.Join(dir, "empty"))
	if c := restartCheck(v1); c() != "" || c() != "" {
		t.Fatal("restarted with no harness-ops on PATH")
	}
	// The running file's own new build still restarts it, into itself.
	install(t, v1+".new", "build 3, longer still")
	os.Rename(v1+".new", v1)
	c := restartCheck(v2)
	install(t, v2+".new", "build 4, the longest of them")
	os.Rename(v2+".new", v2)
	if c() != "" || c() != v2 {
		t.Fatal("a new build of the running file must restart it")
	}
}
