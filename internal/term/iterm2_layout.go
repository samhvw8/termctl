package term

import (
	api "github.com/samhvw8/termctl/internal/iterm2api"
	"google.golang.org/protobuf/proto"
)

// evenTree returns a copy of a tab's split tree with even grid sizes: panes side by side share the
// width in proportion to how many columns each holds, and panes stacked share the height in
// proportion to how many rows each holds. So [a | b/c] gives a and the b/c column half the width
// each, and b and c half the height each. The totals stay as they are, so the window keeps its size.
// It returns nil when a pane has no grid size (a buried session), since nothing can be shared then.
func evenTree(root *api.SplitTreeNode) *api.SplitTreeNode {
	w, h, ok := treeSize(root)
	if !ok {
		return nil
	}
	out := proto.Clone(root).(*api.SplitTreeNode)
	share(out, w, h)
	return out
}

// treeSize is the node's size in cells: side-by-side children add their widths, stacked children
// add their heights.
func treeSize(n *api.SplitTreeNode) (w, h int, ok bool) {
	for i, l := range n.GetLinks() {
		var cw, ch int
		if s := l.GetSession(); s != nil {
			g := s.GetGridSize()
			if g == nil || g.GetWidth() <= 0 || g.GetHeight() <= 0 {
				return 0, 0, false
			}
			cw, ch = int(g.GetWidth()), int(g.GetHeight())
		} else if l.GetNode() != nil {
			if cw, ch, ok = treeSize(l.GetNode()); !ok {
				return 0, 0, false
			}
		}
		switch {
		case n.GetVertical():
			w += cw
			if i == 0 || ch > h {
				h = ch
			}
		default:
			h += ch
			if i == 0 || cw > w {
				w = cw
			}
		}
	}
	return w, h, true
}

// span is how many panes a link holds across (cols) or down (rows).
func span(l *api.SplitTreeNode_SplitTreeLink, cols bool) int {
	n := l.GetNode()
	if n == nil {
		return 1
	}
	total := 0
	for _, c := range n.GetLinks() {
		s := span(c, cols)
		if n.GetVertical() == cols {
			total += s // children laid out along this axis add up
		} else if s > total {
			total = s
		}
	}
	return total
}

// share sets the grid sizes under n so that it fills w x h cells.
func share(n *api.SplitTreeNode, w, h int) {
	links := n.GetLinks()
	cols := n.GetVertical()
	total := h
	if cols {
		total = w
	}
	sum := 0
	for _, l := range links {
		sum += span(l, cols)
	}
	cum := 0
	for _, l := range links {
		start := total * cum / sum
		cum += span(l, cols)
		size := total*cum/sum - start // cumulative rounding: the sizes add up to total exactly
		cw, ch := w, size
		if cols {
			cw, ch = size, h
		}
		if s := l.GetSession(); s != nil {
			s.GridSize = &api.Size{Width: proto.Int32(int32(cw)), Height: proto.Int32(int32(ch))}
		} else if l.GetNode() != nil {
			share(l.GetNode(), cw, ch)
		}
	}
}
