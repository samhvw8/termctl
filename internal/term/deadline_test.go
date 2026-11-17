package term

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hungCLI puts a stand-in for name first on PATH that never answers. Its shell keeps a child that holds
// stdout open, as a CLI that forked does, so killing the shell alone does not end the read.
func hungCLI(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	old := cliTimeout
	cliTimeout = 200 * time.Millisecond
	t.Cleanup(func() { cliTimeout = old })
	return dir
}

// A CLI that hangs fails its call as a backend error after the deadline, for every CLI backend.
func TestHungCLIFailsAfterItsDeadline(t *testing.T) {
	cases := map[string]func(t *testing.T) error{
		"tmux": func(t *testing.T) error {
			hungCLI(t, "tmux")
			_, err := (&TmuxBackend{Socket: "termctl-test-hung"}).run("list-sessions")
			return err
		},
		"hc": func(t *testing.T) error {
			// The stand-in is first on PATH, so no real hc runs; the private session is a second guard.
			t.Setenv("HC_SESSION", "termctl-test-hung")
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			hungCLI(t, "hc")
			b := NewHc("")
			b.probe = func() bool { return true }
			_, err := b.call("ls", "--json")
			return err
		},
		"rex": func(t *testing.T) error {
			dir := hungCLI(t, "rex")
			b := NewRex()
			b.getenv = func(k string) string {
				if k == "REX_BIN_DIR" {
					return dir
				}
				return ""
			}
			addr := "unix:///nowhere.sock"
			b.addr = &addr
			_, err := b.call("ls")
			return err
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			err := call(t)
			took := time.Since(start)
			te, ok := err.(*Error)
			if !ok || te.Code != CodeBackend || !strings.Contains(te.Msg, "no answer after") {
				t.Fatalf("want a backend error naming the deadline, got %v", err)
			}
			if took > 5*time.Second {
				t.Fatalf("the call took %v: the deadline did not end it", took)
			}
		})
	}
}
