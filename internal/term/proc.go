package term

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// MainProcesses are programs that own a pane: a pane running one of these is that program,
// even when it has child shells. Layer 2 profiles must cover every name here.
var MainProcesses = []string{"claude", "omp", "opencode", "codex", "gemini", "aider", "pi"}

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
