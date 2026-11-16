package term

import (
	"testing"

	api "github.com/samhvw8/termctl/internal/iterm2api"
	"google.golang.org/protobuf/proto"
)

func summary(id string, w, h int32) *api.SessionSummary {
	s := &api.SessionSummary{UniqueIdentifier: proto.String(id)}
	if w > 0 {
		s.GridSize = &api.Size{Width: proto.Int32(w), Height: proto.Int32(h)}
	}
	return s
}

// A tab where "max" is maximized, as iTerm2 3.7.6 reports it: the split tree holds only the
// maximized session, and the tab's other sessions are in minimized_sessions with no grid size.
// Tab 2 is an ordinary split tab.
func maximizedResponse() *api.ListSessionsResponse {
	link := func(s *api.SessionSummary) *api.SplitTreeNode_SplitTreeLink {
		return &api.SplitTreeNode_SplitTreeLink{Session: s}
	}
	return &api.ListSessionsResponse{Windows: []*api.ListSessionsResponse_Window{{
		WindowId: proto.String("w1"), SelectedTabId: proto.String("1"),
		Tabs: []*api.ListSessionsResponse_Tab{
			{TabId: proto.String("1"), Root: &api.SplitTreeNode{Links: []*api.SplitTreeNode_SplitTreeLink{link(summary("max", 200, 50))}},
				MinimizedSessions: []*api.SessionSummary{summary("h1", 0, 0), summary("h2", 0, 0)}},
			{TabId: proto.String("2"), Root: &api.SplitTreeNode{Vertical: proto.Bool(true),
				Links: []*api.SplitTreeNode_SplitTreeLink{link(summary("p1", 100, 50)), link(summary("p2", 99, 50))}}},
		},
	}}}
}

func TestSessionsOfKeepsPanesBehindAMaximizedPane(t *testing.T) {
	got := map[string]itermSession{}
	for _, s := range sessionsOf(maximizedResponse()) {
		got[s.id] = s
	}
	if len(got) != 5 {
		t.Fatalf("listed %d sessions, want 5: %+v", len(got), got)
	}
	for _, id := range []string{"h1", "h2"} {
		s, ok := got[id]
		if !ok || s.tab != "1" || s.window != "w1" || s.tabIndex != 0 || !s.hidden {
			t.Errorf("%s: %+v, want it listed in tab 1 and marked hidden", id, s)
		}
	}
	for _, id := range []string{"max", "p1", "p2"} {
		if got[id].hidden {
			t.Errorf("%s is marked hidden", id)
		}
	}
	if s := got["max"]; s.cols != 200 || s.rows != 50 {
		t.Errorf("max: %dx%d", s.cols, s.rows)
	}
}

func TestMaximizedTabRefusesLayoutChanges(t *testing.T) {
	tabs := tabsOf(maximizedResponse())
	tab := tabOf(tabs, "h2")
	if tab == nil || tab.id != "1" {
		t.Fatalf("tabOf(h2) = %+v, want tab 1", tab)
	}
	err := maximizedErr(tab)
	if err == nil || err.Code != CodeUsage {
		t.Fatalf("maximizedErr(tab 1) = %v, want a refusal", err)
	}
	if err := maximizedErr(tabOf(tabs, "p1")); err != nil {
		t.Fatalf("tab 2 is not maximized: %v", err)
	}
}
