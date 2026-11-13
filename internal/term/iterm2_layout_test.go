package term

import (
	"fmt"
	"strings"
	"testing"

	api "github.com/samhvw8/termctl/internal/iterm2api"
	"google.golang.org/protobuf/proto"
)

func leaf(id string, w, h int) *api.SplitTreeNode_SplitTreeLink {
	return &api.SplitTreeNode_SplitTreeLink{Session: &api.SessionSummary{
		UniqueIdentifier: proto.String(id),
		GridSize:         &api.Size{Width: proto.Int32(int32(w)), Height: proto.Int32(int32(h))},
	}}
}

func node(sideBySide bool, links ...*api.SplitTreeNode_SplitTreeLink) *api.SplitTreeNode {
	return &api.SplitTreeNode{Vertical: proto.Bool(sideBySide), Links: links}
}

func sub(n *api.SplitTreeNode) *api.SplitTreeNode_SplitTreeLink {
	return &api.SplitTreeNode_SplitTreeLink{Node: n}
}

// sizes lists every pane as id=WxH in tree order.
func sizes(n *api.SplitTreeNode) string {
	var out []string
	var walk func(*api.SplitTreeNode)
	walk = func(n *api.SplitTreeNode) {
		for _, l := range n.GetLinks() {
			if s := l.GetSession(); s != nil {
				out = append(out, fmt.Sprintf("%s=%dx%d", s.GetUniqueIdentifier(), s.GetGridSize().GetWidth(), s.GetGridSize().GetHeight()))
			} else {
				walk(l.GetNode())
			}
		}
	}
	walk(n)
	return strings.Join(out, " ")
}

func TestEvenTree(t *testing.T) {
	cases := []struct {
		name string
		root *api.SplitTreeNode
		want string
	}{
		{"two side by side", node(true, leaf("a", 150, 40), leaf("b", 100, 40)), "a=125x40 b=125x40"},
		{"rounding still adds up", node(true, leaf("a", 100, 40), leaf("b", 50, 40), leaf("c", 50, 40)), "a=66x40 b=67x40 c=67x40"},
		{"a pane and a column of two: half the width each",
			node(true, leaf("a", 200, 79), sub(node(false, leaf("b", 50, 20), leaf("c", 50, 59)))),
			"a=125x79 b=125x39 c=125x40"},
		{"columns of two and three rows",
			node(true, sub(node(false, leaf("a", 60, 30), leaf("b", 60, 50))), sub(node(false, leaf("c", 190, 10), leaf("d", 190, 10), leaf("e", 190, 60)))),
			"a=125x40 b=125x40 c=125x26 d=125x27 e=125x27"},
		{"a column of three beside one: shares by columns, not by pane count",
			node(true, leaf("a", 50, 60), sub(node(false, leaf("b", 150, 20), leaf("c", 150, 20), leaf("d", 150, 20)))),
			"a=100x60 b=100x20 c=100x20 d=100x20"},
	}
	for _, c := range cases {
		before := proto.Clone(c.root).(*api.SplitTreeNode)
		got := evenTree(c.root)
		if s := sizes(got); s != c.want {
			t.Errorf("%s: got %s, want %s", c.name, s, c.want)
		}
		w0, h0, _ := treeSize(before)
		w1, h1, _ := treeSize(got)
		if w0 != w1 || h0 != h1 {
			t.Errorf("%s: total %dx%d became %dx%d: the window would resize", c.name, w0, h0, w1, h1)
		}
		if !proto.Equal(c.root, before) {
			t.Errorf("%s: evenTree changed its input", c.name)
		}
		if got.GetVertical() != before.GetVertical() {
			t.Errorf("%s: the divider direction changed", c.name)
		}
	}
}

func TestEvenTreeSkipsABuriedSession(t *testing.T) {
	root := node(true, leaf("a", 100, 40), &api.SplitTreeNode_SplitTreeLink{Session: &api.SessionSummary{UniqueIdentifier: proto.String("b")}})
	if got := evenTree(root); got != nil {
		t.Fatalf("evenTree = %s, want nil for a pane with no grid size", sizes(got))
	}
}
