package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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

const (
	logMax  = 1 << 20   // a watcher log over this is cut when the next watcher starts
	logKeep = 256 << 10 // to its last lines that fit this
)

// trimLog cuts a log over logMax to its last whole lines within logKeep. A watcher that still has the old
// file open writes on into it, out of sight; only one watcher per owner runs.
func trimLog(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() <= logMax {
		return err
	}
	tail := make([]byte, logKeep)
	n, err := f.ReadAt(tail, st.Size()-logKeep)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	tail = tail[:n]
	if i := bytes.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	tmp := path + ".cut"
	if err := os.WriteFile(tmp, tail, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
	trimErr := trimLog(log)
	f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if trimErr != nil { // the watcher still starts; the log says why it is long
		fmt.Fprintf(f, "%s could not cut this log: %v\n", time.Now().Format("15:04:05"), trimErr)
	}
	cmd := exec.Command(exe, append(args, "watch")...)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
