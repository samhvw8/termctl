package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/harness"
	"github.com/samhvw8/termctl/internal/term"
)

// newTask starts a generic task as the current sender and returns its ID.
func (e *env) newTask(t *testing.T, title, cwd string, extra ...string) string {
	t.Helper()
	code, out, stderr := e.run(append([]string{"task", "new", "generic", "--title", title, "--brief", "do it", "--cwd", cwd, "--command", "fake-cmd", "--timeout", "60"}, extra...)...)
	if code != 0 {
		t.Fatal(out, stderr)
	}
	return strings.Fields(out)[0]
}

// as makes the commands run from the pane of task id's worker.
func (e *env) as(t *testing.T, id string) {
	t.Helper()
	d, err := harness.NewRegistry("").Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range e.b.Panes {
		if p.ID == d.Tasks[id].Pane {
			p.Folder = "/w/sub"
			e.app.OpenSender = func(*Options, term.Backend) (*term.Pane, error) { return p, nil }
			return
		}
	}
	t.Fatalf("no pane for task %s", id)
}

// task ls --tree indents sub-tasks two spaces per level, --json --tree nests them in "children", and
// resume . lists the open sub-tasks of the project's open tasks, whatever their project.
func TestTaskTreeAndResumeShowSubTasks(t *testing.T) {
	e := newEnv(t)
	me := e.app.OpenSender
	top := e.newTask(t, "top", "/w", "--sub-depth", "1")
	e.as(t, top)
	sub := e.newTask(t, "sub", "/w")
	e.app.OpenSender = me
	code, out, stderr := e.run("task", "ls", "--tree")
	if code != 0 {
		t.Fatal(stderr)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], top+" ") || !strings.HasPrefix(lines[2], "  "+sub+" ") {
		t.Fatalf("ls --tree:\n%s", out)
	}
	if code, out, _ := e.run("task", "ls"); code != 0 || strings.Contains(out, sub) {
		t.Fatalf("plain ls lists another owner's task:\n%s", out)
	}
	code, out, _ = e.run("task", "ls", "--tree", "--json")
	var nodes []map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &nodes) != nil || len(nodes) != 1 {
		t.Fatalf("ls --tree --json:\n%s", out)
	}
	kids, _ := nodes[0]["children"].([]any)
	if nodes[0]["id"] != top || len(kids) != 1 || kids[0].(map[string]any)["id"] != sub {
		t.Fatalf("ls --tree --json:\n%s", out)
	}
	code, out, _ = e.run("resume", ".")
	if code != 0 || !strings.Contains(out, "\n"+top+"  ") || !strings.Contains(out, "\n  "+sub+"  ") {
		t.Fatalf("resume . does not nest the sub-task (project /w/sub):\n%s", out)
	}
}

// task progress: the worker's line shows in ls's SUMMARY and in show with its age; a stall mark shows as
// "stalled" beside a running task's status; refusals exit 2.
func TestTaskProgressCommandAndStallInLs(t *testing.T) {
	e := newEnv(t)
	me := e.app.OpenSender
	id := e.newTask(t, "port", "/w")
	e.as(t, id)
	code, out, stderr := e.run("task", "progress", id, "--summary", "half done", "--notify")
	if code != 0 || !strings.Contains(out, "progress noted") {
		t.Fatal(code, out, stderr)
	}
	e.app.OpenSender = me
	e.app.Clock.(interface{ Sleep(float64) }).Sleep(125)
	if code, out, _ := e.run("task", "ls"); code != 0 || !strings.Contains(out, "half done") || strings.Contains(out, "stalled") {
		t.Fatalf("ls:\n%s", out)
	}
	if code, out, _ := e.run("task", "show", id); code != 0 || !strings.Contains(out, "progress: half done (2m ago)") {
		t.Fatalf("show:\n%s", out)
	}
	if err := harness.NewRegistry("").Update(func(d *harness.Data) error {
		d.Tasks[id].Stall = &harness.StallMark{At: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := e.run("task", "ls"); code != 0 || !strings.Contains(out, "running stalled") {
		t.Fatalf("ls with a stall mark:\n%s", out)
	}
	if code, _, _ := e.run("task", "progress", "t000000", "--summary", "x"); code != 2 {
		t.Fatal(code)
	}
	if code, _, _ := e.run("task", "progress", id); code != 2 {
		t.Fatal("no --summary:", code)
	}
}
