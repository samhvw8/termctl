package term

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// Moving and arranging Rex blocks. Rex moves a live block anywhere in its session
// (session.move_block), detaches one from its window and places it again, swaps two, and sets the
// ratio of a split (session.resize_pane), so every capability is here; a block keeps its process.
// Rex has no command that moves a block into a new window or lays out a window, so those are
// built from these calls. All of them work only inside one session: Rex refuses a move between
// sessions ("blocks are in different sessions").

// rexCall is one server API call of a batch.
type rexCall struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

// rexBatchLua runs the calls of rex.args in order, in one session, and stops at the first error.
const rexBatchLua = `
local a = rex.args
rex.call("session.attach", {session_id = a.session_id})
for _, c in ipairs(a.calls) do
  local p = c.params or {}
  p.session_id = a.session_id
  local _, err = rex.call(c.method, p)
  if err ~= nil then error(c.method .. ": " .. tostring(err)) end
end
return true
`

// batch runs API calls in one rex process, so a layout change costs one call, not one per block.
func (b *RexBackend) batch(session string, calls []rexCall) error {
	if len(calls) == 0 {
		return nil
	}
	args, err := json.Marshal(map[string]any{"session_id": session, "calls": calls})
	if err != nil {
		return BackendErr("rex: %v", err)
	}
	_, err = b.call("do", "-e", rexBatchLua, "--json", "--args", string(args))
	return err
}

// rexTree is a window's layout: a block, or a split of kids along dir ("horizontal" puts them side
// by side, "vertical" one above the other). Rex's own splits have two kids; a target layout may
// have more.
type rexTree struct {
	Block string
	Dir   string
	Ratio float64 // Rex's: the share of the first kid
	Kids  []*rexTree
}

type rexViewNode struct {
	BlockID string        `json:"block_id"`
	Split   *rexViewSplit `json:"split"`
}

type rexViewSplit struct {
	Direction string       `json:"direction"`
	Ratio     float64      `json:"ratio"`
	Before    *rexViewNode `json:"before"`
	After     *rexViewNode `json:"after"`
}

func (n *rexViewNode) tree() *rexTree {
	if n == nil {
		return nil
	}
	if n.Split == nil {
		return &rexTree{Block: rexID(n.BlockID)}
	}
	return &rexTree{Dir: n.Split.Direction, Ratio: n.Split.Ratio, Kids: []*rexTree{n.Split.Before.tree(), n.Split.After.tree()}}
}

type rexView struct {
	ActiveWindowID string `json:"active_window_id"`
	Windows        []struct {
		WindowID       string `json:"window_id"`
		FocusedBlockID string `json:"focused_block_id"`
		Layers         []struct {
			Kind   string       `json:"kind"`
			Layout *rexViewNode `json:"layout"`
		} `json:"layers"`
	} `json:"windows"`
}

// rexWindow is one window of a view: its tiled layout and where it sits in the window order.
type rexWindow struct {
	session, id, next, focused string // next: the window after it, "" for the last
	active                     bool
	tree                       *rexTree
}

func (b *RexBackend) view(session string) (*rexView, error) {
	args, _ := json.Marshal(map[string]string{"session_id": session})
	out, err := b.call("api", "call", "session.view", string(args))
	if err != nil {
		return nil, err
	}
	var v rexView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return nil, BackendErr("rex session.view: bad JSON: %v", err)
	}
	return &v, nil
}

// window returns the window that holds block, or the window with ID tab when block is "".
func (b *RexBackend) window(block, tab string) (*rexWindow, error) {
	if block == "" {
		panes, err := b.ListPanes()
		if err != nil {
			return nil, err
		}
		for _, p := range panes {
			if p.Tab == tab {
				block = p.Native
				break
			}
		}
		if block == "" {
			return nil, TargetErr("rex has no window %s", tab)
		}
	}
	session, err := b.session(block)
	if err != nil {
		return nil, err
	}
	v, err := b.view(session)
	if err != nil {
		return nil, err
	}
	for i, w := range v.Windows {
		for _, l := range w.Layers {
			if l.Kind != "tiled" || !l.Layout.tree().has(block) {
				continue
			}
			win := &rexWindow{session: session, id: rexID(w.WindowID), focused: rexID(w.FocusedBlockID),
				active: w.WindowID == v.ActiveWindowID, tree: l.Layout.tree()}
			if i+1 < len(v.Windows) {
				win.next = rexID(v.Windows[i+1].WindowID)
			}
			return win, nil
		}
	}
	return nil, TargetErr("rex block %s is in no window's layout (detached or floating)", block)
}

func (t *rexTree) has(block string) bool {
	for _, id := range t.blocks() {
		if id == block {
			return true
		}
	}
	return false
}

// blocks lists the tree's blocks in layout order: left to right, top to bottom.
func (t *rexTree) blocks() []string {
	if t == nil {
		return nil
	}
	if t.Block != "" {
		return []string{t.Block}
	}
	var out []string
	for _, k := range t.Kids {
		out = append(out, k.blocks()...)
	}
	return out
}

// span is how many blocks t holds along dir: kids along dir add up, kids across it take the most.
func (t *rexTree) span(dir string) int {
	if t.Block != "" {
		return 1
	}
	n := 0
	for _, k := range t.Kids {
		s := k.span(dir)
		if t.Dir == dir {
			n += s
		} else if s > n {
			n = s
		}
	}
	return n
}

// evenCalls sets every split of a two-kid tree (a view) so that each block gets an even share of
// its row or column, counted in blocks across, as iTerm2's even layout does: in [a | b/c] a and the
// b/c column get half the width each. A split already even is left alone.
func (t *rexTree) evenCalls() []rexCall {
	if t == nil || t.Block != "" || len(t.Kids) != 2 {
		return nil
	}
	before, after := t.Kids[0].span(t.Dir), t.Kids[1].span(t.Dir)
	ratio := float64(before) / float64(before+after)
	var calls []rexCall
	if math.Abs(ratio-t.Ratio) > 0.001 {
		calls = append(calls, rexCall{"session.resize_pane", map[string]any{
			"direction": t.Dir, "ratio": ratio,
			"before_block_id": "block:" + t.Kids[0].blocks()[0], "after_block_id": "block:" + t.Kids[1].blocks()[0],
		}})
	}
	return append(append(calls, t.Kids[0].evenCalls()...), t.Kids[1].evenCalls()...)
}

// placeCalls builds the target tree t out of blocks that are all detached but t's first block,
// which fills t's space. Each step moves the first block of the rest beside the first block of
// the first kid, with the ratio that gives every block an even share, so no even pass is needed.
func (t *rexTree) placeCalls() []rexCall {
	if t.Block != "" || len(t.Kids) == 0 {
		return nil
	}
	if len(t.Kids) == 1 {
		return t.Kids[0].placeCalls()
	}
	head, rest := t.Kids[0], &rexTree{Dir: t.Dir, Kids: t.Kids[1:]}
	if len(t.Kids) == 2 {
		rest = t.Kids[1]
	}
	share := float64(head.span(t.Dir)) / float64(t.span(t.Dir))
	calls := []rexCall{{"session.move_block", map[string]any{
		"block_id": "block:" + rest.blocks()[0], "anchor_block_id": "block:" + head.blocks()[0],
		"direction": t.Dir, "side": "after", "ratio": share,
	}}}
	return append(append(calls, head.placeCalls()...), rest.placeCalls()...)
}

// rexLayoutTree is the target tree of a layout over ids, ids[0] being main. It reuses iTerm2's
// layoutTree, where "vertical" means side by side.
func rexLayoutTree(layout string, ids []string) *rexTree {
	var conv func(n map[string]any) *rexTree
	conv = func(n map[string]any) *rexTree {
		if id, ok := n["session_id"].(string); ok {
			return &rexTree{Block: id}
		}
		t := &rexTree{Dir: "vertical"}
		if v, _ := n["vertical"].(bool); v {
			t.Dir = "horizontal"
		}
		for _, k := range n["children"].([]any) {
			t.Kids = append(t.Kids, conv(k.(map[string]any)))
		}
		return t
	}
	return conv(layoutTree(layout, ids))
}

// even evens out the window that holds block. It is cosmetic, so a failure only warns.
func (b *RexBackend) even(block string) {
	w, err := b.window(block, "")
	if err == nil {
		err = b.batch(w.session, w.tree.evenCalls())
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: even out the window: %v\n", err)
	}
}

// MovePane moves p beside anchor, splitting anchor along its long side, then evens out anchor's
// window. It works across windows of one session, and for a detached block.
func (b *RexBackend) MovePane(p, anchor *Pane) error {
	session, err := b.session(anchor.Native)
	if err != nil {
		return err
	}
	if from, err := b.session(p.Native); err != nil {
		return err
	} else if from != session {
		return BackendErr("rex cannot move a block between sessions: %s and %s are in different sessions", p.Address(), anchor.Address())
	}
	dir := "vertical"
	if SideBySide(anchor.Width, anchor.Height) {
		dir = "horizontal"
	}
	if err := b.batch(session, []rexCall{{"session.move_block", map[string]any{
		"block_id": "block:" + p.Native, "anchor_block_id": "block:" + anchor.Native, "direction": dir, "side": "after",
	}}}); err != nil {
		return err
	}
	b.even(anchor.Native)
	return nil
}

// BreakPane moves p into a new window right after its own. Rex has no call for that: a new window
// starts with a placeholder block (a sleep that ends by itself if this fails half way), p moves in
// beside it, and the placeholder closes.
func (b *RexBackend) BreakPane(p *Pane) error {
	from, err := b.window(p.Native, "")
	if err != nil {
		return err
	}
	out, err := b.call("window", "new", "--focus=false", "--json", "-s", from.session, "--shell=none", "--", "sleep", "60")
	if err != nil {
		return err
	}
	var c rexCreated
	if err := json.Unmarshal([]byte(out), &c); err != nil || c.block() == "" {
		return BackendErr("rex window new: no block in the result: %s", out)
	}
	move := map[string]any{"window_id": "window:" + c.window()}
	if from.next != "" {
		move["before_window_id"] = "window:" + from.next
	}
	return b.batch(from.session, []rexCall{
		{"session.move_block", map[string]any{"block_id": "block:" + p.Native, "anchor_block_id": "block:" + c.block()}},
		{"block.close", map[string]any{"block_id": "block:" + c.block()}},
		{"session.move_window", move},
	})
}

// SwapPanes trades the places of two blocks; the splits and their sizes stay. Focus follows a
// focused block, as Rex does.
func (b *RexBackend) SwapPanes(x, y *Pane) error {
	_, err := b.call("swap", x.Native, y.Native)
	return err
}

// Arrange lays out a window again. even keeps the split tree and evens the sizes; the other
// layouts detach every block but the first (main), which then fills the window, and place the rest
// one by one with even ratios. Detached blocks keep running. Focus goes back to the block that had
// it when the window is the active one.
func (b *RexBackend) Arrange(tab, layout string, main *Pane) error {
	if err := CheckLayout(layout); err != nil {
		return err
	}
	w, err := b.window("", tab)
	if err != nil {
		return err
	}
	if layout == "even" {
		return b.batch(w.session, w.tree.evenCalls())
	}
	ids := w.tree.blocks()
	if main != nil {
		ordered := []string{main.Native}
		for _, id := range ids {
			if id != main.Native {
				ordered = append(ordered, id)
			}
		}
		ids = ordered
	}
	var calls []rexCall
	for _, id := range ids[1:] {
		calls = append(calls, rexCall{"session.detach_block", map[string]any{"block_id": "block:" + id}})
	}
	calls = append(calls, rexLayoutTree(layout, ids).placeCalls()...)
	if w.active && w.focused != "" {
		calls = append(calls, rexCall{"session.focus_block", map[string]any{"block_id": "block:" + w.focused}})
	}
	return b.batch(w.session, calls)
}
