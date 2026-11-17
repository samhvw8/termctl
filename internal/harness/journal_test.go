package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samhvw8/termctl/internal/termtest"
)

func TestProjectSlugIsOmpStyleWithoutTheLeadingDash(t *testing.T) {
	home, _ := os.UserHomeDir()
	for in, want := range map[string]string{
		filepath.Join(home, "workspace/tools/termctl"): "workspace-tools-termctl",
		filepath.Join(home, ".dotfiles"):               ".dotfiles",
		home:                                           "home",
		home + "/":                                     "home",
		"/private/tmp":                                 "private-tmp",
		"/":                                            "root",
		filepath.Join(home, "a b/c:d"):                 "a-b-c-d",
	} {
		if got := ProjectSlug(in); got != want {
			t.Errorf("ProjectSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHeaderHasPathUnixTimeLocalTimeAndZone(t *testing.T) {
	t.Setenv("TZ", "Asia/Ho_Chi_Minh")
	loc, err := time.LoadLocation("Asia/Ho_Chi_Minh")
	if err != nil {
		t.Skip("no zoneinfo:", err)
	}
	got := Header("/Users/x/w/p", time.Date(2026, 11, 16, 23, 9, 2, 0, loc))
	if want := "/Users/x/w/p · 1794845342 · 2026-11-16 23:09:02 +07 (Asia/Ho_Chi_Minh)"; got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestWriteFileNeverOverwrites(t *testing.T) {
	j := &Journal{Root: t.TempDir(), Clock: &termtest.Clock{T: 1794845342}}
	a, err := j.WriteFile("/w/p", "note", "plan", "first")
	if err != nil {
		t.Fatal(err)
	}
	b, err := j.WriteFile("/w/p", "note", "plan", "second")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(a) != "1794845342-note-plan.md" || filepath.Base(b) != "1794845342-note-plan-2.md" {
		t.Fatalf("%s, %s", a, b)
	}
	if data, _ := os.ReadFile(a); !strings.HasSuffix(string(data), "\n\nfirst\n") || !strings.HasPrefix(string(data), "/w/p · 1794845342 · ") {
		t.Fatalf("%q", data)
	}
	if filepath.Base(filepath.Dir(a)) != "w-p" {
		t.Fatal(a)
	}
}

// Many processes append at once; every line must stay whole, however long.
func TestConcurrentAppendsStayWhole(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("z", 200_000)
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			j := &Journal{Root: root}
			for i := 0; i < 20; i++ {
				if err := j.Append(Entry{Event: "msg", Project: "/w/p", Task: fmt.Sprintf("t%02d%02d", w, i), Text: big}); err != nil {
					t.Error(err)
				}
			}
		}(w)
	}
	wg.Wait()
	entries, bad, err := (&Journal{Root: root}).Read(Filter{})
	if err != nil || bad != 0 || len(entries) != 320 {
		t.Fatalf("%d entries, %d bad, %v", len(entries), bad, err)
	}
}

func TestReadFiltersAndCountsBadLines(t *testing.T) {
	root := t.TempDir()
	clock := &termtest.Clock{T: 1000}
	j := &Journal{Root: root, Clock: clock}
	j.Append(Entry{Event: "msg", Project: "/w/a", From: "iterm2:AAAA-1", To: "iterm2:BBBB-2", Task: "t000001"})
	clock.T = 2000
	j.Append(Entry{Event: "note", Project: "/w/b"})
	f, _ := os.OpenFile(filepath.Join(root, journalFile), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("{not json\n")
	f.Close()
	cases := []struct {
		f    Filter
		want int
	}{{Filter{}, 2}, {Filter{Project: "/w/a"}, 1}, {Filter{Task: "t000001"}, 1}, {Filter{Pane: "BBBB"}, 1}, {Filter{Since: 1500}, 1}}
	for _, c := range cases {
		got, bad, err := j.Read(c.f)
		if err != nil || bad != 1 || len(got) != c.want {
			t.Errorf("%+v: %d entries, %d bad, %v", c.f, len(got), bad, err)
		}
	}
	got, _, _ := j.Read(Filter{})
	if got[0].Time == "" || got[0].Zone == "" || got[0].T != 1000 {
		t.Fatalf("%+v", got[0])
	}
}

func TestDialogAnswerIsJournaled(t *testing.T) {
	r := newRig(t)
	x, _ := dialogTask(t, r)
	if _, _, err := r.tasks.Answer(x.ID, AnswerOpts{Deny: true}); err != nil {
		t.Fatal(err)
	}
	entries, _, _ := (&Journal{Root: r.reg.Root}).Read(Filter{Task: x.ID})
	last := entries[len(entries)-1]
	if last.Event != "deny" || !strings.HasSuffix(last.Text, " → Esc") || last.Status == "" || last.Project != ProjectOf(r.owner) {
		t.Fatalf("%+v", last)
	}
}

// A task's whole life is in the journal, filed under the owner's project: resume needs nothing else.
func TestTaskLifeIsJournaled(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{Title: "write docs"})
	if err := r.tasks.Send(x.ID, "also the README", false); err != nil {
		t.Fatal(err)
	}
	r.done(t, x, "ok", "docs written")
	if _, err := r.tasks.Accept(x.ID, true); err != nil {
		t.Fatal(err)
	}
	x = r.get(t, x.ID)
	project := ProjectOf(r.owner)
	if x.Project != project {
		t.Fatalf("task project %q, want the owner's folder %q", x.Project, project)
	}
	if !strings.HasPrefix(x.Brief, filepath.Join(r.reg.Root, projectsDir, ProjectSlug(project))+"/") {
		t.Fatalf("brief %s is not in the project folder", x.Brief)
	}
	if data, _ := os.ReadFile(x.Brief); !strings.HasPrefix(string(data), project+" · 1000 · ") || !strings.Contains(string(data), footer(x.ID)) {
		t.Fatalf("brief: %q", data)
	}
	entries, bad, err := (&Journal{Root: r.reg.Root}).Read(Filter{Task: x.ID})
	if err != nil || bad != 0 {
		t.Fatal(bad, err)
	}
	var events []string
	for _, e := range entries {
		events = append(events, e.Event)
		if e.Project != project {
			t.Errorf("%s is filed under %q, want %q", e.Event, e.Project, project)
		}
	}
	want := []string{"spawn", "task.new", "msg", "task.done", "msg", "task.accept", "task.close"}
	if strings.Join(events, " ") != strings.Join(want, " ") {
		t.Fatalf("events %v, want %v", events, want)
	}
	if entries[1].File != x.Brief || entries[2].Text == "" || entries[3].Text != "docs written" || entries[4].To != r.owner.Address() ||
		entries[3].To != r.owner.ID || entries[4].From != x.Pane {
		t.Fatalf("%+v", entries)
	}
}
