package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/samhvw8/termctl/internal/harness"
	"github.com/samhvw8/termctl/internal/term"
)

func (a *App) watch(rest []string, o *Options) error {
	p, err := parseArgs(rest, nil, []string{"--interval"}, -1)
	if err != nil {
		return err
	}
	if err := applyOptions(p, o); err != nil {
		return err
	}
	if len(p.pos) != 0 {
		return term.UsageErr("usage: watch [--interval S]")
	}
	interval, err := p.float("--interval", harness.WatchInterval)
	if err != nil {
		return err
	}
	b, err := a.OpenBackend(o, "")
	if err != nil {
		return err
	}
	tasks, err := a.openTasks(o, b)
	if err != nil {
		return err
	}
	say := func(s string) { fmt.Fprintf(a.Out, "%s %s\n", time.Now().Format("15:04:05"), s) }
	exe, _ := os.Executable()
	check, target := restartCheck(exe), ""
	if p := pathBinary(); p != "" && !startedFromPath(exe) {
		say(fmt.Sprintf("started from %s, not from the harness-ops on PATH (%s): restarts only into a new build of %s", exe, p, exe))
	}
	err = tasks.Watch(harness.WatchOpts{Interval: interval, Log: say, Restart: func() bool { target = check(); return target != "" }})
	if errors.Is(err, harness.ErrRestart) {
		// Same arguments and environment: an upgraded binary must not keep serving old fingerprints and parsers.
		say("restarting into " + target)
		err = a.reexec(target)
		say("restart failed, going on with the old executable: " + err.Error())
		err = tasks.Watch(harness.WatchOpts{Interval: interval, Log: say})
	}
	if errors.Is(err, harness.ErrWatching) {
		fmt.Fprintf(a.Out, "a watcher already runs for %s (pid %d)\n", tasks.Sender.Address(), tasks.Watching(tasks.Sender.ID))
		return nil
	}
	return err
}

// Exec replaces the process (syscall.Exec); a test can stub it.
var Exec = syscall.Exec

func (a *App) reexec(exe string) error {
	if exe == "" {
		return errors.New("the executable's path is unknown")
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, execedEnv+"=") { // the first of two entries wins: drop the old one
			env = append(env, kv)
		}
	}
	if key := pathKey(exe); key != "" {
		env = append(env, execedEnv+"="+key)
	}
	return Exec(exe, os.Args, env)
}

// execedEnv names the build a watcher last exec'd into, as pathKey gives it, so the new process never execs
// the same build again: a wrapper named harness-ops that runs another file would otherwise loop.
const execedEnv = "TERMCTL_WATCH_EXECED"

// pathBinary is the harness-ops that PATH runs, with symlinks resolved (mise up re-points its "latest"
// link), or "" when PATH has none, or it resolves to a file of another name (a mise shim is a link to mise).
func pathBinary() string {
	p, err := exec.LookPath("harness-ops")
	if err != nil {
		return ""
	}
	if p, err = filepath.Abs(p); err != nil {
		return ""
	}
	if p, err = filepath.EvalSymlinks(p); err != nil || filepath.Base(p) != "harness-ops" {
		return ""
	}
	return p
}

// pathKey names one build of a file at one path: its resolved path and binaryStamp; "" when it is missing.
func pathKey(path string) string {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	if stamp := binaryStamp(real); stamp != "" {
		return real + "|" + stamp
	}
	return ""
}

// startedFromPath: the watcher's own executable, symlinks resolved, is the harness-ops that PATH resolves to
// now (pathBinary). Asked once, at start.
func startedFromPath(exe string) bool {
	p := pathBinary()
	if p == "" || exe == "" {
		return false
	}
	real, err := filepath.Abs(exe)
	if err != nil {
		return false
	}
	if real, err = filepath.EvalSymlinks(real); err != nil {
		return false
	}
	return real == p
}

// restartCheck is asked at each round start which executable the watcher should exec into, and says "" for
// none: its own executable when that file is a new build (changedBinary), else, for a watcher started from
// the harness-ops that PATH resolved to (startedFromPath), the harness-ops that PATH resolves to now when
// that is not the build it runs (another path, inode, size or modification time) and has stayed the same
// since the previous look. mise up installs a new version beside the old one and re-points PATH to it,
// leaving the running file unchanged (round-6 review R5). A watcher started from another file (a go build
// output, a worktree binary, a test binary) never follows PATH: until 2026-11-18 it moved to the installed
// build within two rounds. Only stat calls, no process.
func restartCheck(exe string) func() string {
	self := changedBinary(exe)
	running := pathKey(exe)
	follow := startedFromPath(exe)
	last := ""
	return func() string {
		if self() {
			return exe
		}
		if !follow {
			return ""
		}
		p := pathBinary()
		key := ""
		if p != "" {
			key = pathKey(p)
		}
		stable := key == last
		last = key
		if key == "" || !stable || key == running || key == os.Getenv(execedEnv) {
			return ""
		}
		return p
	}
}

// binaryStamp names one build of a file: its inode, size and modification time.
func binaryStamp(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	ino := uint64(0)
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		ino = uint64(sys.Ino)
	}
	return fmt.Sprintf("%d/%d/%d", ino, st.Size(), st.ModTime().UnixNano())
}

// changedBinary reports, each time it is asked, whether the executable at path is a different build from
// the one that started, and has stayed the same since the previous look: a binary being written is
// never run half-copied. A missing file (between remove and write) is no change yet.
func changedBinary(path string) func() bool {
	start := binaryStamp(path)
	last := start
	return func() bool {
		if start == "" {
			return false
		}
		now := binaryStamp(path)
		stable := now == last
		last = now
		return now != "" && now != start && stable
	}
}

// startWatcher runs "harness-ops watch" in a session of its own, so it outlives the caller and the
// caller's terminal; its output goes to log. It finds the owner pane from the inherited environment.
func startWatcher(o *Options, log string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	var args []string
	if o.Backend != "" {
		args = append(args, "--backend", o.Backend)
	}
	if o.Socket != "" {
		args = append(args, "-L", o.Socket)
	}
	// The watcher cuts its own log once it holds the owner's lock (harness.trimLog), never here: a
	// watcher that is stopping can take the lock again and must keep writing into this same file.
	f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.Command(exe, append(args, "watch")...)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
