package cli

import (
	"encoding/json"
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
	abs, err := filepath.Abs(expandTilde(p))
	if err != nil {
		return "", term.UsageErr("--project: %v", err)
	}
	return abs, nil
}

func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
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

func (a *App) logCmd(rest []string, o *Options) error {
	p, err := parseArgs(rest, []string{"--json"}, []string{"--project", "--task", "--pane", "--since", "-n"}, -1)
	if err != nil {
		return err
	}
	if err := applyOptions(p, o); err != nil {
		return err
	}
	if len(p.pos) != 0 {
		return term.UsageErr("usage: log [--project DIR] [--task ID] [--pane ID] [--since 2h|3d|DATE] [-n N] [--json]")
	}
	project, err := a.projectArg(p.vals["--project"], o)
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
	entries, err := a.readJournal(harness.Filter{Project: project, Task: p.vals["--task"], Pane: p.vals["--pane"], Since: since})
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
		fmt.Fprintln(a.Out, line(e, project == "" && p.vals["--task"] == ""))
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
			Task: e.Task, Status: e.Status, Error: e.Error}, false)
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
	p, err := parseArgs(rest, nil, []string{"-n"}, -1)
	if err != nil {
		return err
	}
	if err := applyOptions(p, o); err != nil {
		return err
	}
	if len(p.pos) != 1 {
		return term.UsageErr("usage: resume (TASK-ID | DIR)   DIR: a project folder, . for your own")
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
	if x := d.Tasks[p.pos[0]]; x != nil {
		return a.resumeTask(x)
	}
	if looksLikeTaskID(p.pos[0]) {
		if _, err := os.Stat(p.pos[0]); err != nil {
			return term.TargetErr("no task %s and no folder %s; see task ls --all", p.pos[0], p.pos[0])
		}
	}
	project, err := a.projectArg(p.pos[0], o)
	if err != nil {
		return err
	}
	return a.resumeProject(project, d, n)
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

func (a *App) resumeProject(project string, d *harness.Data, n int) error {
	w := a.Out
	fmt.Fprintf(w, "# Project %s (%s)\n", project, harness.ProjectSlug(project))
	var open []*harness.Task
	for _, x := range d.Tasks {
		if filepath.Clean(x.Project) == filepath.Clean(project) && !x.Closed && x.Status != "done" {
			open = append(open, x)
		}
	}
	sort.Slice(open, func(i, j int) bool { return open[i].Started < open[j].Started })
	fmt.Fprintf(w, "\n## Tasks not closed (%d)\n\n", len(open))
	for _, x := range open {
		fmt.Fprintf(w, "%s  %-8s %-8s %s  pane %s  (resume %s)\n", x.ID, x.Status, x.Harness, x.Title, orDash(x.Pane), x.ID)
	}
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
		return term.UsageErr("usage: note WHAT (TEXT... | --file F | text on stdin)")
	}
	what := p.pos[0]
	words, _ := attachLiteral(p.rest, tail, dashed)
	text := strings.Join(words, " ")
	switch {
	case p.vals["--file"] != "" && text != "":
		return term.UsageErr("note: give the text or --file, not both")
	case p.vals["--file"] != "":
		data, err := os.ReadFile(expandTilde(p.vals["--file"]))
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
