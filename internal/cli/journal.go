package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/samhvw8/termctl/internal/harness"
	"github.com/samhvw8/termctl/internal/term"
)

// The journal commands: log prints the timeline, resume prints what a new session needs to go on
// with a task or a project, note files a text (a job brief, a plan) in the project folder.

func (a *App) journal() *harness.Journal {
	return &harness.Journal{Root: harness.DefaultRoot(), Clock: a.Clock}
}

func (a *App) now() time.Time {
	if a.Clock != nil {
		return time.Unix(int64(a.Clock.Now()), 0)
	}
	return time.Now()
}

var sinceRe = regexp.MustCompile(`^(\d+)([smhd])$`)

// parseSince reads 30m, 2h, 3d, a Unix time, or a local date (2006-01-02) or date and time (2006-01-02T15:04).
func parseSince(s string, now time.Time) (int64, error) {
	if s == "" {
		return 0, nil
	}
	if m := sinceRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		unit := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
		return now.Add(-time.Duration(n) * unit).Unix(), nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, term.UsageErr("--since: %q is not 30m, 2h, 3d, a Unix time or a date (2006-01-02)", s)
}

// myProject is the caller's project, the one its writes go to: its pane's folder, else the working directory.
func (a *App) myProject(o *Options) string {
	var sender *term.Pane
	if b, err := a.OpenBackend(o, ""); err == nil {
		sender, _ = a.OpenSender(o, b)
	}
	return harness.ProjectOf(sender)
}

// projectArg resolves a project argument; "." is the caller's project, which a shell that changed
// folder would get wrong.
func (a *App) projectArg(p string, o *Options) (string, error) {
	if p == "" {
		return "", nil
	}
	if p == "." {
		return a.myProject(o), nil
	}
	abs, err := filepath.Abs(harness.ExpandHome(p))
	if err != nil {
		return "", term.UsageErr("--project: %v", err)
	}
	return abs, nil
}

func (a *App) readJournal(f harness.Filter) ([]harness.Entry, error) {
	entries, bad, err := a.journal().Read(f)
	if bad > 0 {
		fmt.Fprintf(a.Err, "harness-ops: warning: %d journal lines could not be read and are not shown\n", bad)
	}
	return entries, err
}

func short(id string) string {
	if i := strings.Index(id, ":"); i >= 0 && !strings.HasPrefix(id, "iterm2:") {
		return id // other backends need their prefix
	}
	return term.Cut(strings.TrimPrefix(id, "iterm2:"), 8)
}

func who(id, name string) string {
	if id == "" {
		return "-"
	}
	if name != "" && name != id {
		return short(id) + "(" + term.Cut(name, 20) + ")"
	}
	return short(id)
}

// line is one timeline row: local time, event, who to whom, task, text, file.
func line(e harness.Entry, withProject bool) string {
	var b strings.Builder
	b.WriteString(time.Unix(e.T, 0).Format("2006-01-02 15:04:05"))
	if withProject {
		fmt.Fprintf(&b, "  %-24s", term.Cut(harness.ProjectSlug(e.Project), 24))
	}
	fmt.Fprintf(&b, "  %-12s %s", e.Event, who(e.From, e.FromName))
	if e.To != "" {
		b.WriteString(" → " + who(e.To, e.ToName))
	}
	if e.Task != "" {
		b.WriteString("  [" + e.Task + "]")
	} else if e.Scope != "" {
		b.WriteString("  [scope " + e.Scope + "]")
	}
	if e.Status != "" {
		b.WriteString("  " + e.Status)
	}
	if e.Text != "" {
		b.WriteString("  " + term.Cut(harness.OneLine(e.Text), 120))
	}
	if e.File != "" {
		b.WriteString("  file: " + term.Tilde(e.File))
	}
	if e.Error != "" {
		b.WriteString("  FAILED: " + term.Cut(harness.OneLine(e.Error), 100))
	}
	return b.String()
}

func (a *App) resolveScopeArg(target, project string, o *Options) (string, error) {
	if target == "" {
		return "", nil
	}
	d, err := harness.NewRegistry("").Read()
	if err != nil {
		return "", err
	}
	callerProj := project
	if callerProj == "" {
		callerProj = a.myProject(o)
	}
	s, ferr := d.FindScope(target, callerProj)
	if ferr == nil {
		return s.ID, nil
	}
	if errors.Is(ferr, harness.ErrSeveralScopes) {
		return "", ferr
	}
	if scopeIDRe.MatchString(target) {
		return target, nil
	}
	return "", ferr
}

var scopeIDRe = regexp.MustCompile(`^s[0-9a-f]{6}$`)

func looksLikeScopeID(s string) bool { return scopeIDRe.MatchString(s) }

func (a *App) logCmd(rest []string, o *Options) error {
	p, err := parseArgs(rest, []string{"--json"}, []string{"--project", "--task", "--scope", "--pane", "--since", "-n"}, -1)
	if err != nil {
		return err
	}
	if err := applyOptions(p, o); err != nil {
		return err
	}
	if len(p.pos) != 0 {
		return term.UsageErr("usage: log [--project DIR] [--task ID] [--scope NAME|ID] [--pane ID] [--since 2h|3d|DATE] [-n N] [--json]")
	}
	project, err := a.projectArg(p.vals["--project"], o)
	if err != nil {
		return err
	}
	scopeID, err := a.resolveScopeArg(p.vals["--scope"], project, o)
	if err != nil {
		return err
	}
	since, err := parseSince(p.vals["--since"], a.now())
	if err != nil {
		return err
	}
	n, err := p.int("-n", 50)
	if err != nil {
		return err
	}
	entries, err := a.readJournal(harness.Filter{Project: project, Task: p.vals["--task"], Scope: scopeID, Pane: p.vals["--pane"], Since: since})
	if err != nil {
		return err
	}
	if n > 0 && len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	if p.bools["--json"] {
		if entries == nil {
			entries = []harness.Entry{}
		}
		enc := json.NewEncoder(a.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}
	if len(entries) == 0 {
		fmt.Fprintln(a.Out, "no journal entries match")
		return nil
	}
	for _, e := range entries {
		fmt.Fprintln(a.Out, line(e, project == "" && p.vals["--task"] == "" && scopeID == ""))
	}
	return nil
}

// body is a journal file without its header line.
func body(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(cannot read %s: %v)", path, err)
	}
	s := string(data)
	if first, rest, ok := strings.Cut(s, "\n"); ok && strings.Count(first, " · ") >= 2 {
		s = rest
	}
	return strings.TrimSpace(s)
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

// timeline prints entries with every text in full: a file's text under its entry.
func (a *App) timeline(w io.Writer, entries []harness.Entry, skip string) {
	for _, e := range entries {
		head := line(harness.Entry{T: e.T, Event: e.Event, From: e.From, FromName: e.FromName, To: e.To, ToName: e.ToName,
			Task: e.Task, Scope: e.Scope, Status: e.Status, Error: e.Error}, false)
		fmt.Fprintln(w, head)
		switch {
		case e.File != "" && e.File == skip:
			fmt.Fprintf(w, "    (the brief, above: %s)\n", term.Tilde(e.File))
		case e.File != "":
			fmt.Fprintf(w, "    file: %s\n%s\n", term.Tilde(e.File), indent(body(e.File)))
		case e.Text != "":
			fmt.Fprintln(w, indent(e.Text))
		}
	}
}

func (a *App) resumeCmd(rest []string, o *Options) error {
	p, err := parseArgs(rest, []string{"--tree"}, []string{"-n"}, -1)
	if err != nil {
		return err
	}
	if err := applyOptions(p, o); err != nil {
		return err
	}
	if len(p.pos) != 1 {
		return term.UsageErr("usage: resume (TASK-ID | SCOPE | DIR) [--tree]   DIR: a project folder, . for your own")
	}
	n, err := p.int("-n", 30)
	if err != nil {
		return err
	}
	reg := harness.NewRegistry("")
	d, err := reg.Read()
	if err != nil {
		return err
	}
	tasks := d.TaskViews()
	if x := tasks[p.pos[0]]; x != nil {
		if p.bools["--tree"] {
			fmt.Fprintf(a.Out, "# Tree of task %s: %s\n\n", x.ID, x.Title)
			a.rollup(tasks, []*harness.Task{x}, true)
			return nil
		}
		return a.resumeTask(x)
	}
	if arg := p.pos[0]; arg != "." && !strings.HasPrefix(arg, "/") && !strings.HasPrefix(arg, "./") && !strings.HasPrefix(arg, "../") && !strings.HasPrefix(arg, "~") {
		s, serr := d.FindScope(arg, a.myProject(o))
		if serr == nil {
			if p.bools["--tree"] {
				return a.resumeScopeTree(d, s, tasks)
			}
			return a.resumeScope(d, s, tasks)
		}
		if errors.Is(serr, harness.ErrSeveralScopes) {
			return serr
		}
	}
	if looksLikeTaskID(p.pos[0]) {
		if _, err := os.Stat(p.pos[0]); err != nil {
			return term.TargetErr("no task %s and no folder %s; see task ls --all", p.pos[0], p.pos[0])
		}
	}
	if looksLikeScopeID(p.pos[0]) {
		if _, err := os.Stat(p.pos[0]); err != nil {
			return term.TargetErr("no scope %s and no folder %s; see scope ls --all", p.pos[0], p.pos[0])
		}
	}
	project, err := a.projectArg(p.pos[0], o)
	if err != nil {
		return err
	}
	if p.bools["--tree"] {
		return a.resumeProjectTree(project, d, tasks)
	}
	return a.resumeProject(project, d, tasks, n)
}

func scopeLatestReport(d *harness.Data, scopeID string) *harness.ProgressNote {
	var best *harness.ProgressNote
	for _, l := range d.Leases {
		if l.Scope == scopeID && l.Progress != nil {
			if best == nil || l.Progress.At > best.At {
				best = l.Progress
			}
		}
	}
	return best
}

func scopeLeaseIDs(d *harness.Data, scopeID string) map[string]bool {
	ids := map[string]bool{}
	for _, l := range d.Leases {
		if l.Scope == scopeID {
			ids[l.ID] = true
		}
	}
	return ids
}

func scopeChildLeases(d *harness.Data, scopeID string) []*harness.Lease {
	lids := scopeLeaseIDs(d, scopeID)
	var out []*harness.Lease
	for _, l := range d.Leases {
		if lids[l.Parent] {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Started != out[j].Started {
			return out[i].Started < out[j].Started
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func scopeChildTasks(d *harness.Data, scopeID string, tasks map[string]*harness.Task, withClosed bool) []*harness.Task {
	var out []*harness.Task
	for _, l := range scopeChildLeases(d, scopeID) {
		if l.Job != "" {
			if x := tasks[l.Job]; x != nil && (withClosed || (!x.Closed && x.Status != "done")) {
				out = append(out, x)
			}
		}
	}
	return out
}

func (a *App) printScopeSubtree(d *harness.Data, tasks map[string]*harness.Task, sv *harness.ScopeView, withClosed bool, indent string, seenScopes, seenTasks map[string]bool) {
	if sv == nil || seenScopes[sv.ID] {
		return
	}
	seenScopes[sv.ID] = true
	fmt.Fprintln(a.Out, a.scopeLine(d, sv, indent))
	for _, l := range scopeChildLeases(d, sv.ID) {
		if l.Job != "" {
			if x := tasks[l.Job]; x != nil && (withClosed || (!x.Closed && x.Status != "done")) && !seenTasks[x.ID] {
				a.rollupSeen(tasks, []*harness.Task{x}, withClosed, indent+"  ", seenTasks)
			}
		} else if l.Scope != "" && !seenScopes[l.Scope] {
			sub := d.ScopeView(l.Scope)
			if sub != nil && (!sub.Dropped || withClosed) {
				if sub.Lease == l.ID || (sub.Lease == "" && (l.State == "exited" || l.State == "lost")) {
					a.printScopeSubtree(d, tasks, sub, withClosed, indent+"  ", seenScopes, seenTasks)
				}
			}
		}
	}
}

func (a *App) scopeLine(d *harness.Data, sv *harness.ScopeView, indent string) string {
	state := scopeStateLabel(sv)
	line := fmt.Sprintf("%s%s (%s)  %-8s %-8s", indent, sv.Name, sv.ID, state, orDash(sv.Harness))
	if sv.Pane != "" {
		line += "  pane " + sv.Pane
	}
	if rep := scopeLatestReport(d, sv.ID); rep != nil {
		line += "  — report: " + term.Cut(harness.OneLine(rep.Text), 100)
	}
	if sv.State == "live" && sv.Dialog != nil {
		line += fmt.Sprintf("  [dialog %s: %s]", sv.Dialog.Fingerprint, term.Cut(sv.Dialog.Question, 60))
	}
	return line
}

func (a *App) resumeScopeTree(d *harness.Data, s *harness.Scope, tasks map[string]*harness.Task) error {
	sv := d.ScopeView(s.ID)
	fmt.Fprintf(a.Out, "# Tree of scope %s (%s)\n\n", sv.Name, sv.ID)
	a.printScopeSubtree(d, tasks, sv, true, "", map[string]bool{}, map[string]bool{})
	return nil
}

func (a *App) resumeScope(d *harness.Data, s *harness.Scope, tasks map[string]*harness.Task) error {
	w := a.Out
	sv := d.ScopeView(s.ID)
	fmt.Fprintf(w, "# Scope %s (%s)\n\n", sv.Name, sv.ID)
	fmt.Fprintf(w, "state %s · harness %s · pane %s · owner %s\n", scopeStateLabel(sv), orDash(sv.Harness), orDash(sv.Pane), orDash(sv.Owner))
	fmt.Fprintf(w, "project %s · created %s\n", orDash(sv.Project), localTime(sv.Created))
	if sv.About != "" {
		fmt.Fprintf(w, "about %s\n", sv.About)
	}
	if rep := scopeLatestReport(d, s.ID); rep != nil {
		fmt.Fprintf(w, "\n## Latest report\n\n%s (%s)\n", rep.Text, localTime(rep.At))
	}
	children := scopeChildTasks(d, s.ID, tasks, false)
	fmt.Fprintf(w, "\n## Open child tasks (%d)\n\n", len(children))
	for _, x := range children {
		fmt.Fprintf(w, "%s  %-8s %-8s %s  pane %s  (resume %s)\n", x.ID, x.Status, x.Harness, x.Title, orDash(x.Pane), x.ID)
	}
	entries, err := a.readJournal(harness.Filter{Scope: s.ID})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "\n## Timeline (%d entries)\n\n", len(entries))
	if len(entries) == 0 {
		fmt.Fprintln(w, "(no journal entries)")
	}
	a.timeline(w, entries, "")
	return nil
}

func (a *App) rollup(tasks map[string]*harness.Task, roots []*harness.Task, withClosed bool) {
	a.rollupSeen(tasks, roots, withClosed, "", map[string]bool{})
}

func (a *App) rollupSeen(tasks map[string]*harness.Task, roots []*harness.Task, withClosed bool, baseIndent string, seen map[string]bool) {
	kids := map[string][]*harness.Task{}
	for _, x := range tasks {
		if x.Parent != "" && (withClosed || !x.Closed) {
			kids[x.Parent] = append(kids[x.Parent], x)
		}
	}
	var walk func(xs []*harness.Task, indent string)
	walk = func(xs []*harness.Task, indent string) {
		sort.Slice(xs, func(i, j int) bool { return xs[i].Started < xs[j].Started })
		for _, x := range xs {
			if seen[x.ID] {
				continue
			}
			seen[x.ID] = true
			status := x.Status
			if x.Closed {
				status += " (closed)"
			}
			what := x.Summary
			if x.Status == "running" && x.Progress != nil {
				what = "progress: " + x.Progress.Text
			}
			line := fmt.Sprintf("%s%s  %-8s %-8s %s", indent, x.ID, status, x.Harness, term.Cut(x.Title, 40))
			if what != "" {
				line += "  — " + term.Cut(harness.OneLine(what), 100)
			}
			if x.Dialog != nil && x.Status == "running" && !x.Closed {
				line += fmt.Sprintf("  [dialog %s: %s]", x.Dialog.Fingerprint, term.Cut(x.Dialog.Question, 60))
			}
			if m := harness.AckMarks(x); m != "" {
				line += "  " + m
			}
			fmt.Fprintln(a.Out, line)
			walk(kids[x.ID], indent+"  ")
		}
	}
	walk(roots, baseIndent)
}

func projectScopes(d *harness.Data, project string) []*harness.ScopeView {
	var out []*harness.ScopeView
	for _, s := range d.Scopes {
		if !s.Dropped && filepath.Clean(s.Project) == filepath.Clean(project) {
			out = append(out, d.ScopeView(s.ID))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created < out[j].Created })
	return out
}

func hasParentScopeIn(d *harness.Data, scopeID string, inProj map[string]bool) bool {
	l := d.Leases[d.Scopes[scopeID].Lease]
	if l == nil {
		return false
	}
	seen := map[string]bool{l.ID: true}
	for p := d.Leases[l.Parent]; p != nil && !seen[p.ID]; p = d.Leases[p.Parent] {
		seen[p.ID] = true
		if p.Scope != "" && inProj[p.Scope] {
			return true
		}
	}
	return false
}

// resumeProjectTree is resume DIR --tree: the project's scopes and non-closed tasks, each with a compact
// rollup of the tasks under it, whatever their project.
func (a *App) resumeProjectTree(project string, d *harness.Data, tasks map[string]*harness.Task) error {
	fmt.Fprintf(a.Out, "# Tree of project %s (%s)\n\n", project, harness.ProjectSlug(project))
	scopes := projectScopes(d, project)
	inProj := map[string]bool{}
	for _, sv := range scopes {
		inProj[sv.ID] = true
	}
	printedScopes := map[string]bool{}
	printedTasks := map[string]bool{}
	for _, sv := range scopes {
		if hasParentScopeIn(d, sv.ID, inProj) {
			continue
		}
		a.printScopeSubtree(d, tasks, sv, false, "", printedScopes, printedTasks)
	}
	for _, sv := range scopes {
		if !printedScopes[sv.ID] {
			a.printScopeSubtree(d, tasks, sv, false, "", printedScopes, printedTasks)
		}
	}
	var open []*harness.Task
	for _, x := range tasks {
		if printedTasks[x.ID] {
			continue
		}
		if filepath.Clean(x.Project) == filepath.Clean(project) && !x.Closed && x.Status != "done" {
			if p := tasks[x.Parent]; p != nil && filepath.Clean(p.Project) == filepath.Clean(project) && !p.Closed && p.Status != "done" {
				continue // listed under its parent
			}
			open = append(open, x)
		}
	}
	if len(scopes) == 0 && len(open) == 0 {
		fmt.Fprintln(a.Out, "(no tasks open)")
		return nil
	}
	if len(open) > 0 {
		a.rollupSeen(tasks, open, false, "", printedTasks)
	}
	return nil
}

var taskIDRe = regexp.MustCompile(`^t[0-9a-f]{6}$`)

func looksLikeTaskID(s string) bool { return taskIDRe.MatchString(s) }

func localTime(unix float64) string {
	if unix == 0 {
		return "-"
	}
	return time.Unix(int64(unix), 0).Format("2006-01-02 15:04:05")
}

func (a *App) resumeTask(x *harness.Task) error {
	w := a.Out
	fmt.Fprintf(w, "# Task %s: %s\n\n", x.ID, x.Title)
	fmt.Fprintf(w, "status %s · harness %s · round %d · worker pane %s · owner %s\n", x.Status, x.Harness, x.Round, orDash(x.Pane), orDash(x.Owner))
	fmt.Fprintf(w, "project %s · worker folder %s\n", orDash(x.Project), orDash(x.Cwd))
	fmt.Fprintf(w, "started %s · ended %s\n", localTime(x.Started), localTime(x.Ended))
	if x.ReviewOf != "" {
		fmt.Fprintf(w, "reviews task %s (blind: %t)\n", x.ReviewOf, x.Blind)
	}
	if x.Summary != "" {
		fmt.Fprintf(w, "\n## Last report\n\n%s\n", x.Summary)
	}
	if x.Brief != "" {
		fmt.Fprintf(w, "\n## Brief (%s)\n\n%s\n", term.Tilde(x.Brief), body(x.Brief))
	}
	entries, err := a.readJournal(harness.Filter{Task: x.ID})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "\n## Timeline (%d entries)\n\n", len(entries))
	if len(entries) == 0 {
		fmt.Fprintln(w, "(no journal entries: the task is older than the journal)")
	}
	a.timeline(w, entries, x.Brief)
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (a *App) resumeProject(project string, d *harness.Data, tasks map[string]*harness.Task, n int) error {
	w := a.Out
	fmt.Fprintf(w, "# Project %s (%s)\n", project, harness.ProjectSlug(project))
	scopes := projectScopes(d, project)
	if len(scopes) > 0 {
		fmt.Fprintf(w, "\n## Scopes (%d)\n\n", len(scopes))
		for _, sv := range scopes {
			fmt.Fprintf(w, "%s  (resume %s)\n", a.scopeLine(d, sv, ""), sv.Name)
			for _, c := range scopeChildTasks(d, sv.ID, tasks, false) {
				fmt.Fprintf(w, "  %s  %-8s %-8s %s  pane %s  (resume %s)\n", c.ID, c.Status, c.Harness, c.Title, orDash(c.Pane), c.ID)
			}
		}
	}
	var open []*harness.Task
	for _, x := range tasks {
		if filepath.Clean(x.Project) == filepath.Clean(project) && !x.Closed && x.Status != "done" {
			open = append(open, x)
		}
	}
	sort.Slice(open, func(i, j int) bool { return open[i].Started < open[j].Started })
	fmt.Fprintf(w, "\n## Tasks not closed (%d)\n\n", len(open))
	// Under each, the open tasks started from its worker (Parent), whatever their project, a level deeper each.
	kids := map[string][]*harness.Task{}
	for _, x := range tasks {
		if x.Parent != "" && !x.Closed && x.Status != "done" {
			kids[x.Parent] = append(kids[x.Parent], x)
		}
	}
	seen := map[string]bool{}
	var list func(xs []*harness.Task, indent string)
	list = func(xs []*harness.Task, indent string) {
		sort.Slice(xs, func(i, j int) bool { return xs[i].Started < xs[j].Started })
		for _, x := range xs {
			if seen[x.ID] {
				continue
			}
			seen[x.ID] = true
			fmt.Fprintf(w, "%s%s  %-8s %-8s %s  pane %s  (resume %s)\n", indent, x.ID, x.Status, x.Harness, x.Title, orDash(x.Pane), x.ID)
			list(kids[x.ID], indent+"  ")
		}
	}
	list(open, "")
	entries, err := a.readJournal(harness.Filter{Project: project})
	if err != nil {
		return err
	}
	var notes []harness.Entry
	for _, e := range entries {
		if e.Event == "note" {
			notes = append(notes, e)
		}
	}
	fmt.Fprintf(w, "\n## Notes (%d)\n\n", len(notes))
	for _, e := range notes {
		fmt.Fprintf(w, "%s  %s  %s\n", time.Unix(e.T, 0).Format("2006-01-02 15:04"), term.Tilde(e.File), term.Cut(e.Text, 80))
	}
	if n > 0 && len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	fmt.Fprintf(w, "\n## Last %d entries\n\n", len(entries))
	for _, e := range entries {
		fmt.Fprintln(w, line(e, false))
	}
	return nil
}

func (a *App) noteCmd(rest, tail []string, dashed bool, o *Options) error {
	p, err := parseArgs(rest, nil, []string{"--file"}, 1)
	if err != nil {
		return err
	}
	if err := applyOptions(p, o); err != nil {
		return err
	}
	if len(p.pos) != 1 {
		return term.UsageErr("usage: note [--file F] WHAT [TEXT... | --file F | text on stdin]")
	}
	what := p.pos[0]
	// --file may also follow WHAT; only as the first word after it, so a text that mentions --file stays text.
	if rest := p.rest; len(rest) > 0 && (rest[0] == "--file" || strings.HasPrefix(rest[0], "--file=")) {
		q, err := parseArgs(rest, nil, []string{"--file"}, 0)
		if err != nil {
			return err
		}
		if p.vals["--file"] != "" {
			return term.UsageErr("note: --file given twice")
		}
		p.vals["--file"], p.rest = q.vals["--file"], q.rest
	}
	words, _ := attachLiteral(p.rest, tail, dashed)
	text := strings.Join(words, " ")
	switch {
	case p.vals["--file"] != "" && text != "":
		return term.UsageErr("note: give the text or --file, not both")
	case p.vals["--file"] != "":
		data, err := os.ReadFile(harness.ExpandHome(p.vals["--file"]))
		if err != nil {
			return term.UsageErr("--file: %v", err)
		}
		text = string(data)
	case text == "":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return term.UsageErr("note: reading stdin: %v", err)
		}
		text = string(data)
	}
	if strings.TrimSpace(text) == "" {
		return term.UsageErr("note: empty text")
	}
	var sender *term.Pane
	if b, err := a.OpenBackend(o, ""); err == nil {
		sender, _ = a.OpenSender(o, b)
	}
	project := harness.ProjectOf(sender) // the same rule as myProject
	j := a.journal()
	path, err := j.WriteFile(project, "note", what, text)
	if err != nil {
		return err
	}
	e := harness.Entry{Event: "note", Project: project, File: path, Text: term.Cut(harness.OneLine(text), 200)}
	if sender != nil {
		e.From, e.FromName = sender.Address(), sender.Label
	}
	if err := j.Append(e); err != nil {
		return err
	}
	fmt.Fprintln(a.Out, path)
	return nil
}
