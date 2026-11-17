package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/samhvw8/termctl/internal/term"
)

func TestReadOfMissingRegistryCreatesNothing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "none")
	d, err := NewRegistry(root).Read()
	if err != nil || len(d.Tasks) != 0 {
		t.Fatal(d, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("root was created")
	}
}

func TestUpdatePersists(t *testing.T) {
	root := t.TempDir()
	NewRegistry(root).Update(func(d *Data) error { d.Tasks["a"] = &Task{ID: "a"}; return nil })
	d, _ := NewRegistry(root).Read()
	if d.Tasks["a"] == nil {
		t.Fatal()
	}
}

func TestFailedUpdateWritesNothing(t *testing.T) {
	r := NewRegistry(t.TempDir())
	want := errors.New("x")
	err := r.Update(func(d *Data) error { d.Tasks["a"] = &Task{}; return want })
	d, _ := r.Read()
	if err != want || len(d.Tasks) != 0 {
		t.Fatal(err, d)
	}
}

// stubSync replaces syncFile for one test.
func stubSync(t *testing.T, fn func(*os.File) error) {
	old := syncFile
	syncFile = fn
	t.Cleanup(func() { syncFile = old })
}

// A write that fails closes its temp file: a long-lived watcher must not run out of descriptors (review O8).
func TestFailedWriteClosesItsTempFile(t *testing.T) {
	r := NewRegistry(t.TempDir())
	var temp *os.File
	stubSync(t, func(f *os.File) error {
		if temp == nil {
			temp = f
		}
		return errors.New("disk full")
	})
	if err := r.Update(func(d *Data) error { d.Tasks["a"] = &Task{ID: "a"}; return nil }); err == nil {
		t.Fatal("a failed sync must fail the update")
	}
	if temp == nil {
		t.Fatal("no file was synced")
	}
	if _, err := temp.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("the temp file is still open after a failed write: %v", err)
	}
	if entries, _ := os.ReadDir(r.Root); len(entries) != 1 || entries[0].Name() != "registry.lock" {
		t.Fatalf("the failed write left files: %v", entries)
	}
}

// The rename is made durable: the directory is synced after it, so a crash cannot bring the old file
// back (review O9).
func TestWriteSyncsTheDirectoryAfterTheRename(t *testing.T) {
	r := NewRegistry(t.TempDir())
	var synced []string
	stubSync(t, func(f *os.File) error {
		if f.Name() == r.Root {
			d, err := r.Read()
			if err != nil || d.Tasks["a"] == nil {
				t.Errorf("the directory was synced before the rename: %v", err)
			}
		}
		synced = append(synced, f.Name())
		err := f.Sync()
		if err != nil {
			t.Errorf("a real sync of %s failed: %v", f.Name(), err)
		}
		return err
	})
	if err := r.Update(func(d *Data) error { d.Tasks["a"] = &Task{ID: "a"}; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(synced) != 2 || synced[1] != r.Root {
		t.Fatalf("want the temp file, then the directory, synced: %q", synced)
	}
}

func TestCorruptRegistryIsAnErrorNotAReset(t *testing.T) {
	r := NewRegistry(t.TempDir())
	r.Update(func(*Data) error { return nil })
	os.WriteFile(r.Path, []byte("{not json"), 0o644)
	_, err := r.Read()
	var e *term.Error
	if !errors.As(err, &e) || !strings.Contains(err.Error(), r.Path) {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(r.Path); string(data) != "{not json" {
		t.Fatal("file was changed")
	}
}

// TestWriterHelper is the child process of TestTwoWritersLoseNothing.
func TestWriterHelper(t *testing.T) {
	root := os.Getenv("REG_WRITER_ROOT")
	if root == "" {
		t.Skip("helper")
	}
	r := NewRegistry(root)
	name, n := os.Getenv("REG_WRITER_NAME"), 60
	for i := 0; i < n; i++ {
		err := r.Update(func(d *Data) error {
			if d.Tasks["c"] == nil {
				d.Tasks["c"] = &Task{ID: "c"}
			}
			d.Tasks["c"].Depth++
			d.Tasks[fmt.Sprintf("%s-%d", name, i)] = &Task{}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTwoWritersLoseNothing(t *testing.T) {
	root := t.TempDir()
	var procs []*exec.Cmd
	for _, name := range []string{"a", "b"} {
		c := exec.Command(os.Args[0], "-test.run=^TestWriterHelper$")
		c.Env = append(os.Environ(), "REG_WRITER_ROOT="+root, "REG_WRITER_NAME="+name)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		procs = append(procs, c)
	}
	for _, c := range procs {
		if err := c.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(NewRegistry(root).Path)
	var d Data
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.Tasks["c"].Depth != 120 || len(d.Tasks) != 121 {
		t.Fatal(strconv.Itoa(d.Tasks["c"].Depth), len(d.Tasks))
	}
}

// --- the real state directory is live data ----------------------------------

func TestTestsCannotReachTheDefaultStateDir(t *testing.T) {
	t.Setenv("TERMCTL_STATE_DIR", "")
	defer func() {
		if recover() == nil {
			t.Fatal("a test binary must not resolve the default state dir")
		}
	}()
	NewRegistry("")
}

func fakeHome(t *testing.T) (home, state string) {
	home = t.TempDir()
	t.Setenv("HOME", home)
	state = filepath.Join(home, ".local", "state", "termctl")
	os.MkdirAll(filepath.Join(state, "briefs"), 0o755)
	os.WriteFile(filepath.Join(state, "registry.json"), []byte("{}"), 0o644)
	return home, state
}

func stillThere(t *testing.T, state string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(state, "registry.json")); err != nil {
		t.Fatal("the live registry was removed")
	}
}

func TestCleanStateRefusesTheDefaultDir(t *testing.T) {
	home, state := fakeHome(t)
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(state, link)
	for _, target := range []string{state, state + "/", link, filepath.Join(home, ".local", "state"), filepath.Join(home, ".local"), home, filepath.Join(state, "briefs"), "/"} {
		wantCode(t, CleanState(target), term.CodeGuard)
		stillThere(t, state)
	}
}

func TestCleanStateNeedsAnExplicitDir(t *testing.T) {
	_, state := fakeHome(t)
	wantCode(t, CleanState(""), term.CodeGuard)
	stillThere(t, state)
}

func TestCleanStateRefusesAForeignDir(t *testing.T) {
	fakeHome(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "thesis.tex"), []byte("x"), 0o644)
	wantCode(t, CleanState(dir), term.CodeGuard)
	if _, err := os.Stat(filepath.Join(dir, "thesis.tex")); err != nil {
		t.Fatal("removed a directory that is not a state dir")
	}
}

func TestCleanStateRemovesATestDir(t *testing.T) {
	fakeHome(t)
	dir := filepath.Join(t.TempDir(), "state")
	r := NewRegistry(dir)
	r.Update(func(d *Data) error { return nil })
	os.MkdirAll(filepath.Join(dir, "briefs"), 0o755)
	if err := CleanState(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("not removed")
	}
	if err := CleanState(dir); err != nil { // already gone is fine
		t.Fatal(err)
	}
}

// A registry lock held for good (a hung process) fails the update loudly after LockTimeout instead of
// hanging every harness-ops call behind it (review F18).
func TestRegistryLockHasADeadline(t *testing.T) {
	reg := NewRegistry(filepath.Join(t.TempDir(), "state"))
	if err := reg.Update(func(*Data) error { return nil }); err != nil {
		t.Fatal(err)
	}
	held, err := os.OpenFile(filepath.Join(reg.Root, "registry.lock"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	old := LockTimeout
	LockTimeout = 200 * time.Millisecond
	defer func() { LockTimeout = old }()
	done := make(chan error, 1)
	go func() { done <- reg.Update(func(*Data) error { return nil }) }()
	select {
	case err := <-done:
		var te *term.Error
		if !errors.As(err, &te) || te.Code != term.CodeBackend || !strings.Contains(err.Error(), "held by another process") {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Update hangs behind a held lock")
	}
	syscall.Flock(int(held.Fd()), syscall.LOCK_UN)
	if err := reg.Update(func(*Data) error { return nil }); err != nil {
		t.Fatal("a freed lock:", err)
	}
}
