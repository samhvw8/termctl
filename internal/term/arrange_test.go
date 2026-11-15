package term_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/term"
	"github.com/samhvw8/termctl/internal/termtest"
)

// Two tabs: A holds me (the caller) and a1, B holds b1 (small) and b2 (large), C holds c1 alone.
func moveRig() (*termtest.Fake, map[string]*term.Pane) {
	mk := func(n int, label, tab string, w, h int) *term.Pane {
		p := termtest.MakePane(n, label, label, "/w/"+label, "")
		p.Tab, p.Width, p.Height = tab, w, h
		return p
	}
	ps := map[string]*term.Pane{
		"me": mk(1, "me", "A", 80, 40), "a1": mk(2, "a1", "A", 80, 40),
		"b1": mk(3, "b1", "B", 40, 20), "b2": mk(4, "b2", "B", 120, 40), "c1": mk(5, "c1", "C", 160, 40),
	}
	ps["me"].IsSelf = true
	f := termtest.New([]*term.Pane{ps["me"], ps["a1"], ps["b1"], ps["b2"], ps["c1"]}, ps["me"])
	return f, ps
}

func code(err error) int {
	var e *term.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return -1
}

func changes(f *termtest.Fake) []termtest.Event {
	var out []termtest.Event
	for _, e := range f.Events {
		if e.Kind == "move" || e.Kind == "break" || e.Kind == "swap" || e.Kind == "arrange" {
			out = append(out, e)
		}
	}
	return out
}

func TestMoveToTabOfSplitsTheLargestPaneThere(t *testing.T) {
	f, ps := moveRig()
	panes, _ := f.ListPanes()
	m, err := term.Move(f, panes, ps["a1"], "tab-of:b1")
	if err != nil {
		t.Fatal(err)
	}
	if ev := changes(f); len(ev) != 1 || ev[0].Pane != "fake:2" || ev[0].Text != "fake:4" {
		t.Fatalf("want a1 split beside b2, the largest pane in B: %+v", ev)
	}
	if m.Pane.ID != "fake:2" || m.Pane.Tab != "B" || m.Anchor.ID != "fake:4" || m.New || m.Pane.Label != "a1" {
		t.Fatalf("%+v", m)
	}
}

func TestMoveToTabIDAndToSelf(t *testing.T) {
	f, ps := moveRig()
	panes, _ := f.ListPanes()
	if _, err := term.Move(f, panes, ps["c1"], "B"); err != nil || ps["c1"].Tab != "B" {
		t.Fatal(err, ps["c1"].Tab)
	}
	panes, _ = f.ListPanes()
	if _, err := term.Move(f, panes, ps["b1"], "self"); err != nil || ps["b1"].Tab != "A" {
		t.Fatal(err, ps["b1"].Tab)
	}
}

func TestMoveToNewBreaksThePaneOut(t *testing.T) {
	f, ps := moveRig()
	panes, _ := f.ListPanes()
	m, err := term.Move(f, panes, ps["b1"], "new")
	if err != nil || !m.New || m.Pane.Tab != "tab-3" {
		t.Fatal(err, m)
	}
	if ev := changes(f); len(ev) != 1 || ev[0].Kind != "break" || ev[0].Pane != "fake:3" {
		t.Fatalf("%+v", ev)
	}
}

func TestMoveRefusalsChangeNothing(t *testing.T) {
	cases := []struct {
		pane, to, want string
	}{
		{"a1", "self", "already in tab A"},
		{"a1", "tab-of:me", "already in tab A"},
		{"c1", "new", "already alone"},
		{"a1", "Z", "no tab 'Z'"},
		{"a1", "tab-of:nothing", "no pane matches"},
		{"a1", "tab-of:fake", "matches 5 panes"},
	}
	for _, c := range cases {
		f, ps := moveRig()
		panes, _ := f.ListPanes()
		_, err := term.Move(f, panes, ps[c.pane], c.to)
		if code(err) != term.CodeUsage || !strings.Contains(err.Error(), c.want) || len(changes(f)) != 0 {
			t.Errorf("move %s --to %s: %v (want exit 2, %q, nothing changed; changes %+v)", c.pane, c.to, err, c.want, changes(f))
		}
	}
	f, ps := moveRig()
	f.Me, ps["me"].IsSelf = nil, false
	panes, _ := f.ListPanes()
	if _, err := term.Move(f, panes, ps["b1"], "self"); code(err) != term.CodeUsage || !strings.Contains(err.Error(), "cannot tell") {
		t.Fatal(err)
	}
}

// A backend without the interfaces is refused with its name, before anything changes.
func TestUnsupportedBackendIsRefusedByName(t *testing.T) {
	for _, name := range []string{"hc", "ghostty"} {
		f, ps := moveRig()
		f.BackendName = name
		b := termtest.Basic{Backend: f}
		panes, _ := f.ListPanes()
		_, e1 := term.Move(b, panes, ps["a1"], "tab-of:b1")
		_, e2 := term.Move(b, panes, ps["a1"], "new")
		_, e3 := term.Swap(b, ps["a1"], ps["b1"])
		_, e4 := term.Arrange(b, panes, "", "even", "")
		for _, err := range []error{e1, e2, e3, e4} {
			if code(err) != term.CodeBackend || !strings.Contains(err.Error(), "not supported on "+name) || !strings.Contains(err.Error(), "nothing was changed") {
				t.Errorf("%s: %v", name, err)
			}
		}
		if len(changes(f)) != 0 {
			t.Errorf("%s: %+v", name, changes(f))
		}
	}
}

func TestSwap(t *testing.T) {
	f, ps := moveRig()
	got, err := term.Swap(f, ps["a1"], ps["c1"])
	if err != nil || got[0].Tab != "C" || got[1].Tab != "A" {
		t.Fatal(err, got)
	}
	if _, err := term.Swap(f, ps["a1"], ps["a1"]); code(err) != term.CodeUsage {
		t.Fatal(err)
	}
}

func TestArrange(t *testing.T) {
	f, _ := moveRig()
	panes, _ := f.ListPanes()
	r, err := term.Arrange(f, panes, "", "main-left", "")
	if err != nil || r.Tab != "A" || r.Panes != 2 || r.Main == nil || r.Main.ID != "fake:1" {
		t.Fatal(err, r)
	}
	if ev := changes(f); len(ev) != 1 || ev[0].Pane != "A" || ev[0].Text != "main-left|fake:1" {
		t.Fatalf("the caller is main by default: %+v", ev)
	}
	if _, err := term.Arrange(f, panes, "tab-of:b1", "main-top", "b1"); err != nil || f.Events[len(f.Events)-1].Text != "main-top|fake:3" {
		t.Fatal(err, f.Events[len(f.Events)-1])
	}
	if _, err := term.Arrange(f, panes, "B", "columns", ""); err != nil || f.Events[len(f.Events)-1].Text != "columns|" {
		t.Fatal(err)
	}
	before := len(changes(f))
	if r, err := term.Arrange(f, panes, "C", "tiled", ""); err != nil || r.Panes != 1 || len(changes(f)) != before {
		t.Fatal("a tab with one pane is left alone", err, r)
	}
	for _, c := range []struct{ tab, layout, main, want string }{
		{"A", "spiral", "", "--layout must be"},
		{"A", "columns", "a1", "--main needs"},
		{"A", "main-left", "b1", "not in tab A"},
		{"Z", "even", "", "no tab"},
	} {
		if _, err := term.Arrange(f, panes, c.tab, c.layout, c.main); code(err) != term.CodeUsage || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	if len(changes(f)) != before {
		t.Fatal("a refused arrange changed something")
	}
}

// dropLabel is a backend whose move loses the pane's label, or the pane itself.
type dropLabel struct {
	*termtest.Fake
	lose bool
}

func (d dropLabel) MovePane(p, anchor *term.Pane) error {
	d.Fake.MovePane(p, anchor)
	for i, q := range d.Fake.Panes {
		if q.ID == p.ID {
			if d.lose {
				d.Fake.Panes = append(d.Fake.Panes[:i], d.Fake.Panes[i+1:]...)
			} else {
				q.Label = ""
			}
			break
		}
	}
	return nil
}

func TestMoveKeepsLabelAndID(t *testing.T) {
	f, ps := moveRig()
	panes, _ := f.ListPanes()
	m, err := term.Move(dropLabel{f, false}, panes, ps["a1"], "B")
	if err != nil || m.Pane.Label != "a1" || !f.Has("label", "fake:2") {
		t.Fatal("a label the move dropped is put back", err, m)
	}
	f, ps = moveRig()
	panes, _ = f.ListPanes()
	if _, err := term.Move(dropLabel{f, true}, panes, ps["a1"], "B"); code(err) != term.CodeBackend || !strings.Contains(err.Error(), "no longer listed") {
		t.Fatal(err)
	}
}

// What each backend can do; docs and README list the same.
var (
	_ term.Mover    = (*term.TmuxBackend)(nil)
	_ term.Breaker  = (*term.TmuxBackend)(nil)
	_ term.Swapper  = (*term.TmuxBackend)(nil)
	_ term.Arranger = (*term.TmuxBackend)(nil)
	_ term.Mover    = (*term.ITerm2Backend)(nil)
	_ term.Breaker  = (*term.ITerm2Backend)(nil)
	_ term.Swapper  = (*term.ITerm2Backend)(nil)
	_ term.Arranger = (*term.ITerm2Backend)(nil)
	_ term.Mover    = (*term.RexBackend)(nil)
	_ term.Breaker  = (*term.RexBackend)(nil)
	_ term.Swapper  = (*term.RexBackend)(nil)
	_ term.Arranger = (*term.RexBackend)(nil)
)

func TestHcAndGhosttyCannotMove(t *testing.T) {
	for _, name := range []string{"hc", "ghostty"} {
		b, err := term.Make(name, "", "")
		if err != nil {
			t.Fatal(err)
		}
		_, m := b.(term.Mover)
		_, br := b.(term.Breaker)
		_, s := b.(term.Swapper)
		_, a := b.(term.Arranger)
		if m || br || s || a {
			t.Fatalf("%s claims to move or arrange panes", name)
		}
	}
}
