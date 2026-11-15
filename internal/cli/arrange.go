package cli

import (
	"fmt"
	"strings"

	"github.com/samhvw8/termctl/internal/harness"
	"github.com/samhvw8/termctl/internal/term"
)

// move, pull, swap and arrange, shared by termctl and harness-ops.

type layoutArgs struct {
	cmd              string
	targets          []string // the panes to move or swap; arrange's optional tab target
	to, layout, main string
}

func parseLayoutCmd(cmd string, rest []string, o *Options) (*layoutArgs, error) {
	p, err := parseArgs(rest, nil, []string{"--to", "--layout", "--main"}, -1)
	if err != nil {
		return nil, err
	}
	if err := applyOptions(p, o); err != nil {
		return nil, err
	}
	l := &layoutArgs{cmd: cmd, targets: p.pos, to: p.vals["--to"], layout: p.vals["--layout"], main: p.vals["--main"]}
	allowed := map[string][]string{"move": {"--to"}, "pull": nil, "swap": nil, "arrange": {"--layout", "--main"}}[cmd]
	for _, f := range []string{"--to", "--layout", "--main"} {
		if _, set := p.vals[f]; set && !has(allowed, f) {
			return nil, term.UsageErr("%s takes no %s", cmd, f)
		}
	}
	switch cmd {
	case "move":
		if len(p.pos) != 1 || l.to == "" {
			return nil, term.UsageErr("usage: move <target> --to <tab>  (tab: new, %s)", term.TabTargets)
		}
	case "pull":
		if len(p.pos) != 1 {
			return nil, term.UsageErr("usage: pull <target>  (moves it into your tab)")
		}
		l.to = "self"
	case "swap":
		if len(p.pos) != 2 {
			return nil, term.UsageErr("usage: swap <target> <target>")
		}
	case "arrange":
		if len(p.pos) > 1 || l.layout == "" {
			return nil, term.UsageErr("usage: arrange [<tab>] --layout %s [--main <pane>]  (tab: %s; default self)", strings.Join(term.Layouts, "|"), term.TabTargets)
		}
		if err := term.CheckLayout(l.layout); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// backendTarget is the word that selects the backend: a pane target, or the pane in tab-of:.
func (l *layoutArgs) backendTarget() string {
	if len(l.targets) == 0 {
		return strings.TrimPrefix(l.to, "tab-of:")
	}
	return strings.TrimPrefix(l.targets[0], "tab-of:")
}

func (l *layoutArgs) tab() string {
	if len(l.targets) == 0 {
		return ""
	}
	return l.targets[0]
}

// describe prints what changed. about adds harness details to a pane's ID.
func (a *App) describeMove(m *term.Moved, about func(*term.Pane) string) {
	switch {
	case m.New:
		fmt.Fprintf(a.Out, "moved %s to a new tab %s\n", about(m.Pane), m.Pane.Tab)
	case m.Pane.Tab == "":
		fmt.Fprintf(a.Out, "moved %s beside %s\n", about(m.Pane), m.Anchor.Short())
	default:
		fmt.Fprintf(a.Out, "moved %s to tab %s, beside %s\n", about(m.Pane), m.Pane.Tab, m.Anchor.Short())
	}
}

func (a *App) describeSwap(ps []*term.Pane, about func(*term.Pane) string) {
	where := func(p *term.Pane) string {
		if p.Tab == "" {
			return ""
		}
		return " (now in tab " + p.Tab + ")"
	}
	fmt.Fprintf(a.Out, "swapped %s%s and %s%s\n", about(ps[0]), where(ps[0]), about(ps[1]), where(ps[1]))
}

func (a *App) describeArrange(r *term.Arranged, layout string) {
	if r.Panes < 2 {
		fmt.Fprintf(a.Out, "tab %s holds %d pane; nothing to arrange\n", r.Tab, r.Panes)
		return
	}
	main := ""
	if r.Main != nil {
		main = ", main " + r.Main.Short()
	}
	fmt.Fprintf(a.Out, "arranged tab %s (%d panes) as %s%s\n", r.Tab, r.Panes, layout, main)
}

// termctlLayout runs move, pull, swap and arrange for termctl: no registry, the caller's pane refused.
func (a *App) termctlLayout(cmd string, rest []string, o *Options) error {
	l, err := parseLayoutCmd(cmd, rest, o)
	if err != nil {
		return err
	}
	b, err := a.OpenBackend(o, l.backendTarget())
	if err != nil {
		return err
	}
	panes, err := b.ListPanes()
	if err != nil {
		return err
	}
	pick := func(target string) (*term.Pane, error) {
		p, err := term.Resolve(panes, target)
		if err != nil {
			return nil, err
		}
		if p.IsSelf {
			return nil, term.TargetErr("refusing to move your own pane")
		}
		return p, nil
	}
	short := func(p *term.Pane) string { return p.Short() }
	switch cmd {
	case "move", "pull":
		p, err := pick(l.targets[0])
		if err != nil {
			return err
		}
		m, err := term.Move(b, panes, p, l.to)
		if err != nil {
			return err
		}
		a.describeMove(m, short)
	case "swap":
		x, err := pick(l.targets[0])
		if err != nil {
			return err
		}
		y, err := pick(l.targets[1])
		if err != nil {
			return err
		}
		ps, err := term.Swap(b, x, y)
		if err != nil {
			return err
		}
		a.describeSwap(ps, short)
	case "arrange":
		r, err := term.Arrange(b, panes, l.tab(), l.layout, l.main)
		if err != nil {
			return err
		}
		a.describeArrange(r, l.layout)
	}
	return nil
}

// harnessLayout runs move, pull, swap and arrange for harness-ops: the same mechanism, with the
// caller taken from the sender and each pane named with its harness and task.
func (a *App) harnessLayout(cmd string, rest []string, o *Options) error {
	l, err := parseLayoutCmd(cmd, rest, o)
	if err != nil {
		return err
	}
	b, err := a.OpenBackend(o, l.backendTarget())
	if err != nil {
		return err
	}
	tasks, err := a.openTasks(o, b)
	if err != nil {
		return err
	}
	about := func(p *term.Pane) string {
		s := term.Cut(p.Address(), 8) + " (" + harness.HarnessName(p)
		if id := tasks.TaskOn(p.ID); id != "" {
			s += ", task " + id
		}
		return s + ")"
	}
	switch cmd {
	case "move", "pull":
		m, err := tasks.MovePane(l.targets[0], l.to)
		if err != nil {
			return err
		}
		a.describeMove(m, about)
	case "swap":
		ps, err := tasks.SwapPanes(l.targets[0], l.targets[1])
		if err != nil {
			return err
		}
		a.describeSwap(ps, about)
	case "arrange":
		r, err := tasks.ArrangeTab(l.tab(), l.layout, l.main)
		if err != nil {
			return err
		}
		a.describeArrange(r, l.layout)
	}
	return nil
}
