package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/samhvw8/termctl/internal/harness"
	"github.com/samhvw8/termctl/internal/term"
)

func (a *App) attachCmd(rest []string, o *Options) error {
	p, err := parseArgs(rest, []string{"--notify", "--force"}, []string{"--name", "--about", "--stall", "--sub-depth", "--tree-max"}, -1)
	if err != nil {
		return err
	}
	if err := applyOptions(p, o); err != nil {
		return err
	}
	if len(p.pos) != 1 {
		return term.UsageErr("usage: attach <pane> [--name SLUG] [--about TEXT] [--stall DUR] [--sub-depth K] [--tree-max M] [--notify] [--force]")
	}
	var stall float64
	_, hasStall := p.vals["--stall"]
	if hasStall {
		stall, err = harness.ParseStallDuration(p.vals["--stall"])
		if err != nil {
			return err
		}
	}
	subDepth, err := p.int("--sub-depth", 0)
	if err != nil {
		return err
	}
	treeMax, err := p.int("--tree-max", 0)
	if err != nil {
		return err
	}
	b, err := a.OpenBackend(o, p.pos[0])
	if err != nil {
		return err
	}
	tasks, err := a.openTasks(o, b)
	if err != nil {
		return err
	}
	_, hasAbout := p.vals["--about"]
	_, hasSubDepth := p.vals["--sub-depth"]
	_, hasTreeMax := p.vals["--tree-max"]
	sv, updated, err := tasks.Attach(harness.AttachOpts{
		Target:      p.pos[0],
		Name:        p.vals["--name"],
		About:       p.vals["--about"],
		HasAbout:    hasAbout,
		Stall:       stall,
		HasStall:    hasStall,
		SubDepth:    subDepth,
		HasSubDepth: hasSubDepth,
		TreeMax:     treeMax,
		HasTreeMax:  hasTreeMax,
		Notify:      p.bools["--notify"],
		Force:       p.bools["--force"],
	})
	if err != nil {
		return err
	}
	if updated {
		fmt.Fprintf(a.Out, "updated %s (%s, lease %s)\n", sv.Name, sv.ID, sv.Lease)
	} else {
		fmt.Fprintf(a.Out, "%s (%s, lease %s)\n", sv.Name, sv.ID, sv.Lease)
	}
	a.ensureWatcher(o, tasks)
	return nil
}

func (a *App) detachCmd(rest []string, o *Options) error {
	p, err := parseArgs(rest, []string{"--keep", "--notify"}, nil, -1)
	if err != nil {
		return err
	}
	if err := applyOptions(p, o); err != nil {
		return err
	}
	if len(p.pos) != 1 {
		return term.UsageErr("usage: detach <scope|pane|lease> [--keep] [--notify]")
	}
	b, err := a.OpenBackend(o, p.pos[0])
	if err != nil {
		return err
	}
	tasks, err := a.openTasks(o, b)
	if err != nil {
		return err
	}
	sv, err := tasks.Detach(harness.DetachOpts{
		Target: p.pos[0],
		Keep:   p.bools["--keep"],
		Notify: p.bools["--notify"],
	})
	if err != nil {
		return err
	}
	if p.bools["--keep"] {
		fmt.Fprintf(a.Out, "detached %s (%s, kept)\n", sv.Name, sv.ID)
	} else {
		fmt.Fprintf(a.Out, "detached %s (%s)\n", sv.Name, sv.ID)
	}
	if tasks.Sender != nil && tasks.Watching(tasks.Sender.ID) != 0 {
		tasks.WakeWatcher(tasks.Sender.ID)
	}
	return nil
}

func (a *App) reportCmd(rest, tail []string, dashed bool, o *Options) error {
	notify := false
	var words []string
	seenText := false
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		name, val, inline := strings.Cut(arg, "=")
		switch {
		case arg == "--notify":
			notify = true
		case !seenText && (name == "--backend" || name == "-L"):
			if !inline {
				i++
				if i >= len(rest) {
					return term.UsageErr("%s needs a value", name)
				}
				val = rest[i]
			}
			if name == "--backend" {
				o.Backend = val
			} else {
				o.Socket = val
			}
		case !seenText && strings.HasPrefix(arg, "-") && arg != "-":
			return term.UsageErr("unrecognized argument: %s", arg)
		default:
			seenText = true
			words = append(words, arg)
		}
	}
	if o.Backend != "" && !term.IsBackend(o.Backend) {
		return term.UsageErr("unknown backend '%s'. Known: %s", o.Backend, strings.Join(term.BackendNames, ", "))
	}
	allWords, _ := attachLiteral(words, tail, dashed)
	text := strings.Join(allWords, " ")
	b, err := a.OpenBackend(o, "")
	if err != nil {
		return err
	}
	tasks, err := a.openTasks(o, b)
	if err != nil {
		return err
	}
	res, err := tasks.Report(text, notify)
	if err != nil {
		return err
	}
	what := "pane " + res.Pane
	if res.Scope != nil {
		what = fmt.Sprintf("scope %s (%s)", res.Scope.Name, res.Scope.ID)
	}
	suffix := ""
	switch {
	case res.OwnerGone:
		suffix = " (lead pane is gone; report recorded)"
	case res.Notified:
		suffix = " (lead notice queued)"
	}
	fmt.Fprintf(a.Out, "reported on %s%s\n", what, suffix)
	return nil
}

func (a *App) scopeCmd(rest []string, o *Options) error {
	if len(rest) == 0 {
		return term.UsageErr("usage: scope ls|show")
	}
	cmd, rest := rest[0], rest[1:]
	switch cmd {
	case "ls":
		p, err := parseArgs(rest, []string{"--all", "--tree", "--json"}, nil, -1)
		if err != nil {
			return err
		}
		if err := applyOptions(p, o); err != nil {
			return err
		}
		if len(p.pos) != 0 {
			return term.UsageErr("usage: scope ls [--all] [--tree] [--json]")
		}
		b, err := a.OpenBackend(o, "")
		if err != nil {
			return err
		}
		tasks, err := a.openTasks(o, b)
		if err != nil {
			return err
		}
		if p.bools["--tree"] {
			nodes, err := tasks.ScopeTree(p.bools["--all"])
			if err != nil {
				return err
			}
			if p.bools["--json"] {
				if nodes == nil {
					nodes = []*harness.TreeNode{}
				}
				data, _ := json.MarshalIndent(nodes, "", " ")
				fmt.Fprintln(a.Out, string(data))
				return nil
			}
			a.printScopeTree(nodes)
			return nil
		}
		scopes, err := tasks.ScopeLs(p.bools["--all"])
		if err != nil {
			return err
		}
		if p.bools["--json"] {
			if scopes == nil {
				scopes = []*harness.ScopeView{}
			}
			data, _ := json.MarshalIndent(scopes, "", " ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		a.printScopes(scopes)
		return nil
	case "show":
		p, err := parseArgs(rest, []string{"--json"}, nil, -1)
		if err != nil {
			return err
		}
		if err := applyOptions(p, o); err != nil {
			return err
		}
		if len(p.pos) != 1 {
			return term.UsageErr("usage: scope show <name|sID> [--json]")
		}
		b, err := a.OpenBackend(o, "")
		if err != nil {
			return err
		}
		tasks, err := a.openTasks(o, b)
		if err != nil {
			return err
		}
		sv, err := tasks.ScopeShow(p.pos[0])
		if err != nil {
			return err
		}
		if p.bools["--json"] {
			data, _ := json.MarshalIndent(sv, "", " ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		a.printScopeShow(sv)
		return nil
	default:
		return term.UsageErr("unknown scope command '%s' (use ls or show)", cmd)
	}
}

func scopeStateLabel(sv *harness.ScopeView) string {
	if sv.State == "live" && sv.Stall != nil {
		return "live stalled"
	}
	return sv.State
}

func scopeReportCell(sv *harness.ScopeView) string {
	rep := ""
	if sv.Progress != nil {
		rep = term.Cut(strings.ReplaceAll(sv.Progress.Text, "\n", " "), 60)
	}
	if sv.State == "live" && sv.Dialog != nil {
		if rep != "" {
			rep += " "
		}
		rep += "[dialog " + sv.Dialog.Fingerprint + "]"
	}
	return rep
}

func (a *App) printScopes(scopes []*harness.ScopeView) {
	fmt.Fprintf(a.Out, "%-20s %-9s %-13s %-10s %-9s REPORT\n", "NAME", "ID", "STATE", "PANE", "HARNESS")
	for _, sv := range scopes {
		fmt.Fprintf(a.Out, "%-20s %-9s %-13s %-10s %-9s %s\n",
			term.Cut(sv.Name, 20), sv.ID, scopeStateLabel(sv), orDash(sv.Pane), orDash(sv.Harness), scopeReportCell(sv))
	}
}

func (a *App) printScopeTree(nodes []*harness.TreeNode) {
	fmt.Fprintf(a.Out, "%-20s %-9s %-13s %-10s %-9s REPORT\n", "NAME", "ID", "STATE", "PANE", "HARNESS")
	var walk func(ns []*harness.TreeNode, indent string)
	walk = func(ns []*harness.TreeNode, indent string) {
		for _, n := range ns {
			switch n.Kind {
			case "scope":
				sv := n.Scope
				fmt.Fprintf(a.Out, "%-20s %-9s %-13s %-10s %-9s %s\n",
					term.Cut(indent+sv.Name, 20), sv.ID, scopeStateLabel(sv), orDash(sv.Pane), orDash(sv.Harness), scopeReportCell(sv))
			case "task":
				t := n.Task
				status := t.Status
				if t.Status == "running" && t.Stall != nil {
					status += " stalled"
				}
				rep := t.Summary
				if t.Status == "running" && t.Progress != nil {
					rep = t.Progress.Text
				}
				extra := ""
				if t.Closed {
					extra += " [closed]"
				}
				if t.Dialog != nil {
					extra += " [dialog " + t.Dialog.Fingerprint + "]"
				}
				if m := harness.AckMarks(t); m != "" {
					extra += " " + m
				}
				fmt.Fprintf(a.Out, "%-20s %-9s %-13s %-10s %-9s %s%s\n",
					term.Cut(indent+t.Title, 20), t.ID, status, orDash(t.Pane), orDash(t.Harness),
					term.Cut(strings.ReplaceAll(rep, "\n", " "), 60), extra)
			}
			walk(n.Children, indent+"  ")
		}
	}
	walk(nodes, "")
}

func scopeRole(sv *harness.ScopeView) string {
	if sv.Lease == "" {
		return ""
	}
	if sv.SubDepth < 1 {
		return fmt.Sprintf("leaf; tree limit %d", sv.TreeMax)
	}
	return fmt.Sprintf("lead, may start tasks with --sub-depth up to %d; tree limit %d", sv.SubDepth-1, sv.TreeMax)
}

func (a *App) printScopeShow(sv *harness.ScopeView) {
	stallAfter := ""
	if sv.StallAfter > 0 {
		stallAfter = fmt.Sprintf("%gs", sv.StallAfter)
	}
	progress := ""
	if sv.Progress != nil {
		progress = fmt.Sprintf("%s (%s)", sv.Progress.Text, a.age(sv.Progress.At))
	}
	stalled := ""
	if sv.State == "live" && sv.Stall != nil {
		stalled = fmt.Sprintf("idle without report, marked %s", a.age(sv.Stall.At))
	}
	dialog := ""
	if sv.State == "live" && sv.Dialog != nil {
		dialog = fmt.Sprintf("%s (%s)", sv.Dialog.Fingerprint, sv.Dialog.Question)
	}
	for _, kv := range [][2]string{
		{"id", sv.ID},
		{"name", sv.Name},
		{"state", scopeStateLabel(sv)},
		{"about", sv.About},
		{"project", sv.Project},
		{"lease", sv.Lease},
		{"pane", sv.Pane},
		{"harness", sv.Harness},
		{"owner", sv.Owner},
		{"cwd", sv.Cwd},
		{"role", scopeRole(sv)},
		{"stall_after", stallAfter},
		{"progress", progress},
		{"stalled", stalled},
		{"dialog", dialog},
		{"created", when(sv.Created)},
	} {
		if kv[1] == "" && kv[0] != "id" && kv[0] != "name" && kv[0] != "state" && kv[0] != "project" && kv[0] != "created" {
			continue
		}
		fmt.Fprintf(a.Out, "%-12s %s\n", kv[0]+":", kv[1])
	}
}
