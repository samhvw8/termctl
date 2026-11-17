package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	err = tasks.Watch(harness.WatchOpts{Interval: interval, Log: say, Restart: changedBinary(exe)})
	if errors.Is(err, harness.ErrRestart) {
		// Same arguments and environment: an upgraded binary must not keep serving old fingerprints and parsers.
		err = a.reexec(exe)
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
	return Exec(exe, os.Args, os.Environ())
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
