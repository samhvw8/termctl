package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/harness"
	"github.com/samhvw8/termctl/internal/term"
	"github.com/samhvw8/termctl/internal/termtest"
)

// layoutEnv puts target and dup A in tab T1, dup B in T2, and the caller alone in T9.
func layoutEnv(t *testing.T) *env {
	e := newEnv(t)
	for _, p := range e.b.Panes {
		p.Tab = map[string]string{"fake:1": "T1", "fake:2": "T1", "fake:3": "T2", "fake:9": "T9"}[p.ID]
	}
	return e
}

func (e *env) termctl(args ...string) (int, string, string) {
	e.out.Reset()
	e.err.Reset()
	code := e.app.Termctl(args)
	return code, e.out.String(), e.err.String()
}

func lastChange(f *termtest.Fake) termtest.Event {
	for i := len(f.Events) - 1; i >= 0; i-- {
		switch f.Events[i].Kind {
		case "move", "break", "swap", "arrange":
			return f.Events[i]
		}
	}
	return termtest.Event{}
}

func TestHarnessOpsPullMoveSwapArrange(t *testing.T) {
	e := layoutEnv(t)
	if code, out, stderr := e.run("pull", "target"); code != 0 || out != "moved fake:1 (python3) to tab T9, beside fake:9\n" {
		t.Fatal(code, out, stderr)
	}
	if ev := lastChange(e.b); ev.Kind != "move" || ev.Pane != "fake:1" || ev.Text != "fake:9" {
		t.Fatalf("%+v", ev)
	}
	if code, out, stderr := e.run("move", "target", "--to", "new"); code != 0 || out != "moved fake:1 (python3) to a new tab tab-1\n" {
		t.Fatal(code, out, stderr)
	}
	if code, out, stderr := e.run("move", "--to", "tab-of:fake:3", "target"); code != 0 || !strings.Contains(out, "to tab T2, beside fake:3") {
		t.Fatal(code, out, stderr)
	}
	if code, out, stderr := e.run("swap", "target", "fake:2"); code != 0 || out != "swapped fake:1 (python3) (now in tab T1) and fake:2 (python3) (now in tab T2)\n" {
		t.Fatal(code, out, stderr)
	}
	if code, out, stderr := e.run("arrange", "T2", "--layout", "main-top", "--main", "fake:2"); code != 0 || out != "arranged tab T2 (2 panes) as main-top, main fake:2\n" {
		t.Fatal(code, out, stderr)
	}
	if code, out, _ := e.run("arrange", "--layout", "even"); code != 0 || !strings.Contains(out, "nothing to arrange") {
		t.Fatal(code, out)
	}
}

// A moved task pane keeps its task: the registry holds pane IDs, and the ID does not change.
func TestHarnessOpsMoveNamesTheTaskAndKeepsItsRecord(t *testing.T) {
	e := layoutEnv(t)
	reg := harness.NewRegistry("")
	reg.Update(func(d *harness.Data) error {
		d.Jobs["t-abc"] = &harness.Job{ID: "t-abc", Lease: "l-abc", Status: "running", Round: 1}
		d.Leases["l-abc"] = &harness.Lease{ID: "l-abc", Pane: "fake:1", Job: "t-abc", State: "active", Harness: "generic", Owner: "fake:9"}
		d.Created["fake:1"] = &harness.CreatedPane{Harness: "generic"}
		return nil
	})
	code, out, stderr := e.run("move", "target", "--to", "T2")
	if code != 0 || out != "moved fake:1 (python3, task t-abc) to tab T2, beside fake:3\n" {
		t.Fatal(code, out, stderr)
	}
	d, _ := reg.Read()
	if d.TaskView("t-abc").Pane != "fake:1" || d.Created["fake:1"] == nil {
		t.Fatalf("%+v", d.TaskView("t-abc"))
	}
}

func TestLayoutRefusals(t *testing.T) {
	cases := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"move", "me", "--to", "new"}, 2, "own pane"},
		{[]string{"pull", "dup"}, 2, "matches 2 panes"},
		{[]string{"swap", "target", "me"}, 2, "own pane"},
		{[]string{"move", "target"}, 2, "usage: move"},
		{[]string{"pull", "target", "--to", "T2"}, 2, "pull takes no --to"},
		{[]string{"swap", "target"}, 2, "usage: swap"},
		{[]string{"arrange"}, 2, "usage: arrange"},
		{[]string{"arrange", "--layout", "spiral"}, 2, "--layout must be"},
		{[]string{"move", "target", "--to", "T1"}, 2, "already in tab T1"},
		{[]string{"move", "target", "--to", "nowhere"}, 2, "no tab 'nowhere'"},
	}
	for _, c := range cases {
		for _, cmd := range []string{"harness-ops", "termctl"} {
			e := layoutEnv(t)
			var code int
			var stderr string
			if cmd == "termctl" {
				code, _, stderr = e.termctl(c.args...)
			} else {
				code, _, stderr = e.run(c.args...)
			}
			if code != c.code || !strings.Contains(stderr, c.want) || lastChange(e.b).Kind != "" {
				t.Errorf("%s %v: exit %d %q, want exit %d with %q and nothing changed", cmd, c.args, code, stderr, c.code, c.want)
			}
		}
	}
}

func TestLayoutUnsupportedBackendExits1(t *testing.T) {
	e := layoutEnv(t)
	e.b.BackendName = "hc"
	e.app.OpenBackend = func(*Options, string) (term.Backend, error) { return termtest.Basic{Backend: e.b}, nil }
	for _, args := range [][]string{{"pull", "target"}, {"move", "target", "--to", "new"}, {"swap", "target", "fake:3"}, {"arrange", "T1", "--layout", "rows"}} {
		if code, _, stderr := e.termctl(args...); code != 1 || !strings.Contains(stderr, "not supported on hc") {
			t.Errorf("termctl %v: %d %q", args, code, stderr)
		}
		if code, _, stderr := e.run(args...); code != 1 || !strings.Contains(stderr, "not supported on hc") {
			t.Errorf("harness-ops %v: %d %q", args, code, stderr)
		}
	}
	if lastChange(e.b).Kind != "" {
		t.Fatal(e.b.Events)
	}
}

func TestTermctlMoveAndLsShowTheTab(t *testing.T) {
	e := layoutEnv(t)
	if code, out, stderr := e.termctl("move", "target", "--to", "tab-of:fake:3"); code != 0 || out != "moved fake:1 to tab T2, beside fake:3\n" {
		t.Fatal(code, out, stderr)
	}
	_, out, _ := e.termctl("ls")
	if !strings.Contains(strings.SplitN(out, "\n", 2)[0], " TAB ") || !strings.Contains(out, "fake:1    T2 ") {
		t.Fatal(out)
	}
	_, out, _ = e.termctl("ls", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || rows[0]["tab"] != "T2" {
		t.Fatal(err, out)
	}
	_, out, _ = e.run("ls", "--json")
	rows = nil
	if err := json.Unmarshal([]byte(out), &rows); err != nil || rows[0]["tab"] != "T2" {
		t.Fatal(err, out)
	}
	_, out, _ = e.run("ls")
	if !strings.Contains(strings.SplitN(out, "\n", 2)[0], " TAB ") {
		t.Fatal(out)
	}
}
