package term

import (
	"fmt"
	"testing"
)

func TestPlaceIn(t *testing.T) {
	me := func(tab string) *Pane { return &Pane{ID: "x:me", Folder: "~/app", Tab: tab, IsSelf: true} }
	pane := func(id, tab string) *Pane { return &Pane{ID: "x:" + id, Folder: "~/other", Tab: tab} }
	sized := func(p *Pane, w, h int) *Pane { p.Width, p.Height = w, h; return p }
	fill := func(tab string, n int, prefix string) []*Pane {
		var out []*Pane
		for i := 0; i < n; i++ {
			out = append(out, pane(fmt.Sprintf("%s%d", prefix, i+1), tab))
		}
		return out
	}
	owns := func(ids ...string) func(*Pane) bool {
		return func(p *Pane) bool {
			for _, id := range ids {
				if p.ID == "x:"+id {
					return true
				}
			}
			return false
		}
	}
	cat := func(lists ...[]*Pane) []*Pane {
		var out []*Pane
		for _, l := range lists {
			out = append(out, l...)
		}
		return out
	}

	cases := []struct {
		name   string
		panes  []*Pane
		owned  func(*Pane) bool
		where  string
		anchor string // ID of the pane split, or beside whose tab the new tab opens
	}{
		{"alone in the tab", []*Pane{me("t1")}, nil, "split", "x:me"},
		{"no sizes: split the newest pane", []*Pane{me("t1"), pane("w1", "t1")}, nil, "split", "x:w1"},
		{"split the largest pane", []*Pane{sized(me("t1"), 140, 70), sized(pane("w1", "t1"), 140, 35), sized(pane("w2", "t1"), 140, 35)}, nil, "split", "x:me"},
		{"equal sizes: the newest", []*Pane{sized(me("t1"), 100, 40), sized(pane("w1", "t1"), 100, 40)}, nil, "split", "x:w1"},
		{"any folder joins the caller's tab", []*Pane{me("t1"), pane("w1", "t2")}, nil, "split", "x:me"},
		{"eleven panes still have room", cat([]*Pane{me("t1")}, fill("t1", 10, "w")), nil, "split", "x:w10"},
		{"tab full at twelve: new tab beside the caller", cat([]*Pane{me("t1")}, fill("t1", 11, "w")), nil, "tab", "x:me"},
		{"panes in other tabs do not count", cat([]*Pane{me("t1")}, fill("t2", 11, "o")), nil, "split", "x:me"},
		{"no caller pane", []*Pane{pane("a", "t1")}, nil, "tab", ""},
		{"backend without tabs splits the caller", cat([]*Pane{me("")}, fill("", 13, "w")), nil, "split", "x:me"},
		{"caller tab full: continue in an owned tab with room",
			cat([]*Pane{me("t1")}, fill("t1", 11, "w"), []*Pane{pane("o1", "t2")}), owns("o1"), "split", "x:o1"},
		{"a tab of someone else's agent is left alone",
			cat([]*Pane{me("t1")}, fill("t1", 11, "w"), []*Pane{pane("o1", "t2")}), owns(), "tab", "x:me"},
		{"every tab full: new tab beside the caller",
			cat([]*Pane{me("t1")}, fill("t1", 11, "w"), fill("t2", 12, "o")), owns("o1"), "tab", "x:me"},
	}
	for _, c := range cases {
		where, anchor := PlaceIn(c.panes, c.owned)
		got := ""
		if anchor != nil {
			got = anchor.ID
		}
		if where != c.where || got != c.anchor {
			t.Errorf("%s: PlaceIn = %s at %q, want %s at %q", c.name, where, got, c.where, c.anchor)
		}
	}
}

func TestSideBySide(t *testing.T) {
	cases := []struct {
		cols, rows int
		want       bool
	}{
		{280, 75, true},  // a full screen: side by side
		{140, 75, false}, // half of it: below
		{140, 37, true},  // a quarter: side by side again
		{100, 50, true},  // exactly 2:1
		{99, 50, false},
		{0, 0, true}, // unknown size: side by side, as before
	}
	for _, c := range cases {
		if got := SideBySide(c.cols, c.rows); got != c.want {
			t.Errorf("SideBySide(%d, %d) = %v, want %v", c.cols, c.rows, got, c.want)
		}
	}
}

type failingLister struct{ Backend }

func (failingLister) Name() string                { return "iterm2" }
func (failingLister) ListPanes() ([]*Pane, error) { return nil, BackendErr("a session closed") }

// Placement is cosmetic: a pane list that fails must not fail the spawn.
func TestPlaceFallsBackToTabWhenListingFails(t *testing.T) {
	where, err := Place(failingLister{}, "auto")
	if err != nil || where != "tab" {
		t.Fatalf("Place = %s, %v; want tab, nil", where, err)
	}
}

func TestPlaceOnHcIsTab(t *testing.T) {
	if where, err := Place(hcNamed{}, "auto"); err != nil || where != "tab" {
		t.Fatalf("Place = %s, %v", where, err)
	}
}

type hcNamed struct{ Backend }

func (hcNamed) Name() string { return "hc" }

func TestPlacePassesExplicitWhere(t *testing.T) {
	for _, w := range []string{"tab", "split", "window"} {
		if got, err := Place(nil, w); err != nil || got != w {
			t.Errorf("Place(%s) = %s, %v", w, got, err)
		}
	}
}

func TestCheckWhereAcceptsAuto(t *testing.T) {
	if err := CheckWhere("auto"); err != nil {
		t.Fatal(err)
	}
}
