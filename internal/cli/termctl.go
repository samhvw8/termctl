package cli

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/samhvw8/termctl/internal/term"
)

// Version is set at build time for releases.
var Version = "dev"

const termctlUsage = `usage: termctl [--backend NAME] [-L SOCKET] COMMAND

Drive terminal panes through backends (iterm2, tmux, rex, hc, ghostty).

  ls [--json] [--all]             list panes
  which TARGET                    resolve a target to one pane
  spawn [--where tab|split|window] [--cwd D] [--title T] [CMD...]
  send-text TARGET TEXT...        type text literally, without Enter (refused on hc)
  send-key TARGET KEY...          press keys: enter, escape, tab, ctrl-c, ctrl-u, ...
  capture TARGET [-n LINES]       print the last lines of a pane's screen
  close TARGET                    close a pane
  name LABEL                      label the current pane
  whoami                          print the current pane

Exit codes: 0 ok, 1 backend error, 2 refused target or usage.
`

var labelRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)

func (a *App) printPanes(panes []*term.Pane) {
	w := idWidth(panes)
	fmt.Fprintf(a.Out, "%-*s %-12s %-14s %-34s FOLDER\n", w, "ID", "PROCESS", "LABEL", "TITLE")
	for _, p := range panes {
		mark := ""
		if p.IsSelf {
			mark = " (you)"
		}
		fmt.Fprintf(a.Out, "%-*s %-12s %-14s %-34s %s\n", w, p.Short(), term.Cut(p.Process, 12), term.Cut(p.Label, 14), term.Cut(term.Cut(p.Title, 28)+mark, 34), p.Folder)
	}
}

type paneJSON struct {
	ID      string `json:"id"`
	Backend string `json:"backend"`
	Native  string `json:"native"`
	TTY     string `json:"tty"`
	Title   string `json:"title"`
	Label   string `json:"label"`
	Process string `json:"process"`
	Folder  string `json:"folder"`
	IsSelf  bool   `json:"is_self"`
	Status  string `json:"status"`
	Address string `json:"address"`
}

func (a *App) pick(b term.Backend, target string, refuseSelf bool) (*term.Pane, error) {
	panes, err := b.ListPanes()
	if err != nil {
		return nil, err
	}
	p, err := term.Resolve(panes, target)
	if err != nil {
		return nil, err
	}
	if refuseSelf && p.IsSelf {
		return nil, term.TargetErr("refusing to act on your own pane")
	}
	return p, nil
}

// Termctl runs the termctl command line and returns the exit code.
func (a *App) Termctl(argv []string) int {
	if err := a.termctl(argv); err != nil {
		return a.fail("termctl", err)
	}
	return 0
}

func (a *App) termctl(argv []string) error {
	o := &Options{}
	head, tail, dashed := splitDoubleDash(argv)
	args, err := globals(head, o)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		fmt.Fprint(a.Err, termctlUsage)
		return term.UsageErr("a command is required")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Fprint(a.Out, termctlUsage)
		return nil
	case "--version":
		fmt.Fprintln(a.Out, Version)
		return nil
	case "ls":
		p, err := parseArgs(rest, []string{"--json", "--all"}, nil, -1)
		if err != nil {
			return err
		}
		if err := applyOptions(p, o); err != nil {
			return err
		}
		panes, err := a.listPanes(o, p.bools["--all"])
		if err != nil {
			return err
		}
		if p.bools["--json"] {
			rows := []paneJSON{}
			for _, x := range panes {
				rows = append(rows, paneJSON{x.ID, x.Backend, x.Native, x.TTY, x.Title, x.Label, x.Process, x.Folder, x.IsSelf, x.Status, x.Address()})
			}
			data, _ := json.MarshalIndent(rows, "", " ")
			fmt.Fprintln(a.Out, string(data))
		} else {
			a.printPanes(panes)
		}
		return nil
	case "spawn":
		p, err := parseArgs(rest, nil, []string{"--where", "--cwd", "--title"}, 0)
		if err != nil {
			return err
		}
		if err := applyOptions(p, o); err != nil {
			return err
		}
		words, _ := attachLiteral(p.rest, tail, dashed)
		where := p.vals["--where"]
		if where == "" {
			where = "tab"
		}
		b, err := a.OpenBackend(o, "")
		if err != nil {
			return err
		}
		pane, err := b.Spawn(where, strings.Join(words, " "), p.vals["--cwd"], p.vals["--title"])
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Out, pane.ID)
		return nil
	case "name", "whoami":
		p, err := parseArgs(rest, nil, nil, -1)
		if err != nil {
			return err
		}
		if err := applyOptions(p, o); err != nil {
			return err
		}
		b, err := a.OpenBackend(o, "")
		if err != nil {
			return err
		}
		if cmd == "name" && (len(p.pos) != 1 || !labelRe.MatchString(p.pos[0])) {
			return term.UsageErr("usage: name <label>  (letters, digits, . _ -; max 40)")
		}
		me, err := b.SelfPane()
		if err != nil {
			return err
		}
		if me == nil {
			return term.UsageErr("cannot tell which pane this runs in")
		}
		if cmd == "whoami" {
			a.printPanes([]*term.Pane{me})
			return nil
		}
		if err := b.SetLabel(me, p.pos[0]); err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "labelled this pane: %s\n", p.pos[0])
		return nil
	case "which", "capture", "close", "send-key", "send-text":
		restAfter := -1
		if cmd == "send-text" {
			restAfter = 1
		}
		p, err := parseArgs(rest, nil, []string{"-n"}, restAfter)
		if err != nil {
			return err
		}
		if err := applyOptions(p, o); err != nil {
			return err
		}
		if len(p.pos) < 1 {
			return term.UsageErr("%s needs a target", cmd)
		}
		target := p.pos[0]
		b, err := a.OpenBackend(o, target)
		if err != nil {
			return err
		}
		pane, err := a.pick(b, target, cmd == "send-text" || cmd == "send-key" || cmd == "close")
		if err != nil {
			return err
		}
		switch cmd {
		case "which":
			a.printPanes([]*term.Pane{pane})
			return nil
		case "capture":
			n, err := p.int("-n", 40)
			if err != nil {
				return err
			}
			lines, err := b.Capture(pane, n)
			if err != nil {
				return err
			}
			fmt.Fprintln(a.Out, strings.Join(lines, "\n"))
			return nil
		case "close":
			return b.Close(pane)
		case "send-text":
			words, _ := attachLiteral(p.rest, tail, dashed)
			return b.SendText(pane, strings.Join(words, " "))
		case "send-key":
			if len(p.pos) < 2 {
				return term.UsageErr("send-key needs at least one key")
			}
			for _, k := range p.pos[1:] {
				if err := b.SendKey(pane, k); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return term.UsageErr("unknown command '%s'", cmd)
}

func (a *App) listPanes(o *Options, all bool) ([]*term.Pane, error) {
	if !all {
		b, err := a.OpenBackend(o, "")
		if err != nil {
			return nil, err
		}
		return b.ListPanes()
	}
	var panes []*term.Pane
	for _, name := range term.BackendNames {
		if name == "ghostty" {
			continue
		}
		b, err := a.OpenBackend(&Options{Backend: name, Socket: o.Socket}, "")
		if err != nil {
			return nil, err
		}
		if !b.Available() {
			continue
		}
		found, err := b.ListPanes()
		if err != nil {
			return nil, err
		}
		panes = append(panes, found...)
	}
	return panes, nil
}
