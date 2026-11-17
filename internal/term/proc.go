package term

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// CallTimeout bounds one backend call that reads or types: a run of a terminal's CLI (tmux, hc,
	// Rex) or an iTerm2 API call. Such a call answers in milliseconds (a Rex CLI call about 180 ms, a
	// rex do script that walks every block well under a second), so 30 s fits a machine under heavy
	// load and still ends a call that hangs, so a watcher round or task wait goes on.
	CallTimeout = 30 * time.Second
	// SendTimeout bounds hc send, which waits until nobody has typed in the pane for about 3 s.
	SendTimeout = 60 * time.Second
	// cliWaitDelay is how long a killed CLI's output pipes may stay open, held by a child it forked,
	// before they are closed: the deadline must end the call even then.
	cliWaitDelay = time.Second
)

// cliTimeout is CallTimeout for a CLI run; tests shorten it.
var cliTimeout = CallTimeout

// errNoAnswer is a CLI run that hit its deadline.
var errNoAnswer = errors.New("no answer")

// cliCommand is exec.Command under cliTimeout. A CLI that has not answered by then is killed, and finish
// turns the error of its run into errNoAnswer, so a hung terminal fails the call instead of hanging its
// caller. Call finish once the run is over.
func cliCommand(name string, args ...string) (cmd *exec.Cmd, finish func(error) error) {
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	cmd = exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = cliWaitDelay
	return cmd, func(err error) error {
		defer cancel()
		if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%w after %v", errNoAnswer, cliTimeout)
		}
		return err
	}
}

// MainProcesses are programs that own a pane: a pane running one of these is that program,
// even when it has child shells. Layer 2 profiles must cover every name here.
var MainProcesses = []string{"claude", "omp", "opencode", "codex", "gemini", "agy", "aider", "pi"}

type proc struct {
	pid  int
	name string
}

func processesByTTY() (map[string][]proc, error) {
	out, err := exec.Command("ps", "-axo", "pid=,tty=,comm=").Output()
	if err != nil {
		return nil, BackendErr("ps: %v", err)
	}
	by := map[string][]proc{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[1] == "??" {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		// comm is everything after the tty, and a path may hold spaces.
		idx := strings.Index(line, f[1]) + len(f[1])
		comm := strings.TrimSpace(line[idx:])
		by[f[1]] = append(by[f[1]], proc{pid, filepath.Base(comm)})
	}
	return by, nil
}

func mainProcess(procs []proc) proc {
	for i := len(procs) - 1; i >= 0; i-- {
		for _, n := range MainProcesses {
			if procs[i].name == n {
				return procs[i]
			}
		}
	}
	if len(procs) > 0 {
		return procs[len(procs)-1]
	}
	return proc{}
}

func cwdOf(pids []int) map[int]string {
	res := map[int]string{}
	if len(pids) == 0 {
		return res
	}
	list := make([]string, len(pids))
	for i, p := range pids {
		list[i] = strconv.Itoa(p)
	}
	out, _ := exec.Command("lsof", "-a", "-d", "cwd", "-Fpn", "-p", strings.Join(list, ",")).Output()
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "p") {
			pid, _ = strconv.Atoi(line[1:])
		} else if strings.HasPrefix(line, "n") && pid != 0 {
			res[pid] = line[1:]
		}
	}
	return res
}
