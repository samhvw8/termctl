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
	"testing"

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
