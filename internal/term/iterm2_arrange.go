package term

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"

	api "github.com/samhvw8/termctl/internal/iterm2api"
	"google.golang.org/protobuf/proto"
)

// Moving and arranging iTerm2 sessions goes through iTerm2's built-in functions, the ones its Python
// API calls (App.async_move_session, Session.async_move_to_new_tab, App.async_apply_layout):
//
//   - iterm2.move_session (API 1.11) moves a live session to split another session, in any tab;
//   - iterm2.move_session_to_new_tab (API 1.13) moves one out into a new tab;
//   - iterm2.apply_layout (API 1.14) replaces the split tree of tabs with one that names the same
//     sessions, which swaps and re-lays out panes.
//
// All three move the session object itself, so its process, scrollback and variables (the label)
// stay. iTerm2 3.7.6 speaks API 1.21. SetTabLayoutRequest cannot do any of this: it changes sizes
// only and refuses a tree of another shape.

const itermMoveHint = "moving sessions needs iTerm2 3.7 or later"

// invokeApp calls a built-in function in the app context and returns its JSON result.
func (b *ITerm2Backend) invokeApp(name, invocation string) (string, error) {
	resp, err := b.call(&api.ClientOriginatedMessage{InvokeFunctionRequest: &api.InvokeFunctionRequest{
		Invocation: proto.String(invocation), App: &api.InvokeFunctionRequest_App{}, Timeout: proto.Float64(-1),
	}})
	if err != nil {
		return "", err
	}
	r := resp.GetInvokeFunctionResponse()
	if e := r.GetError(); e != nil {
		return "", BackendErr("iTerm2 %s: %s: %s (%s)", name, e.GetStatus(), e.GetErrorReason(), itermMoveHint)
	}
	return r.GetSuccess().GetJsonResult(), nil
}

type itermTab struct {
	id, window string
	index      int // position in its window
	root       *api.SplitTreeNode
}

func (b *ITerm2Backend) tabs() ([]itermTab, error) {
	resp, err := b.call(&api.ClientOriginatedMessage{ListSessionsRequest: &api.ListSessionsRequest{}})
	if err != nil {
		return nil, err
	}
	var out []itermTab
	for _, w := range resp.GetListSessionsResponse().GetWindows() {
		for i, t := range w.GetTabs() {
			out = append(out, itermTab{t.GetTabId(), w.GetWindowId(), i, t.GetRoot()})
		}
	}
	return out, nil
}

// sessionIDs lists the sessions of a split tree in iTerm2's order: left to right, top to bottom.
func sessionIDs(root *api.SplitTreeNode) []string {
	var ids []string
	walkSplit(root, func(s *api.SessionSummary) { ids = append(ids, s.GetUniqueIdentifier()) })
	return ids
}

func tabOf(tabs []itermTab, session string) *itermTab {
	for i := range tabs {
		for _, id := range sessionIDs(tabs[i].root) {
			if id == session {
				return &tabs[i]
			}
		}
	}
	return nil
}

func tabByID(tabs []itermTab, tab string) *itermTab {
	for i := range tabs {
		if tabs[i].id == tab {
			return &tabs[i]
		}
	}
	return nil
}

// MovePane splits anchor with p, then evens out anchor's tab. iTerm2 reports success even when it
// did nothing (a locked layout or session, no room to split), so the result is checked.
func (b *ITerm2Backend) MovePane(p, anchor *Pane) error {
	inv := fmt.Sprintf("iterm2.move_session(session: %s, destination: %s, vertical: %t, before: false)",
		jsonValue(p.Native), jsonValue(anchor.Native), SideBySide(anchor.Width, anchor.Height))
	if _, err := b.invokeApp("move_session", inv); err != nil {
		return err
	}
	tabs, err := b.tabs()
	if err != nil {
		return err
	}
	now, dest := tabOf(tabs, p.Native), tabOf(tabs, anchor.Native)
	if now == nil {
		return BackendErr("iTerm2 lists no session %s after the move; check ls", p.Native)
	}
	if dest == nil || now.id != dest.id {
		return BackendErr("iTerm2 did not move %s: its window's layout or the session may be locked, or %s has no room to split", p.Native, anchor.Native)
	}
	if err := b.evenTab(dest.id); err != nil { // cosmetic: the pane moved, so warn and go on
		fmt.Fprintf(os.Stderr, "warning: even out the tab: %v\n", err)
	}
	return nil
}

// BreakPane moves p into a new tab right after its tab, in the same window.
func (b *ITerm2Backend) BreakPane(p *Pane) error {
	tabs, err := b.tabs()
	if err != nil {
		return err
	}
	from := tabOf(tabs, p.Native)
	if from == nil {
		return TargetErr("iTerm2 has no session %s in a tab", p.Native)
	}
	inv := fmt.Sprintf("iterm2.move_session_to_new_tab(session: %s, window_id: %s, tab_index: %d)",
		jsonValue(p.Native), jsonValue(from.window), from.index+1)
	if _, err := b.invokeApp("move_session_to_new_tab", inv); err != nil {
		return err
	}
	if tabs, err = b.tabs(); err != nil {
		return err
	}
	now := tabOf(tabs, p.Native)
	if now == nil || now.id == from.id || len(sessionIDs(now.root)) != 1 {
		return BackendErr("iTerm2 did not move %s to a new tab", p.Native)
	}
	return nil
}

// SwapPanes rebuilds the tab or tabs holding x and y with the two exchanged, then gives every
// place back its old size, so only the two sessions trade places.
func (b *ITerm2Backend) SwapPanes(x, y *Pane) error {
	tabs, err := b.tabs()
	if err != nil {
		return err
	}
	tx, ty := tabOf(tabs, x.Native), tabOf(tabs, y.Native)
	if tx == nil || ty == nil {
		return TargetErr("iTerm2 has no session %s or %s in a tab", x.Native, y.Native)
	}
	type planned struct {
		tab  string
		root *api.SplitTreeNode
	}
	plan := []planned{{tx.id, swapSessions(tx.root, x.Native, y.Native)}}
	if ty.id != tx.id {
		plan = append(plan, planned{ty.id, swapSessions(ty.root, x.Native, y.Native)})
	}
	var specTabs []any
	for _, p := range plan {
		specTabs = append(specTabs, map[string]any{"tab_id": p.tab, "root": layoutSpec(p.root)})
	}
	if err := b.applyLayout(map[string]any{"tabs": specTabs}); err != nil {
		return err
	}
	if tabs, err = b.tabs(); err != nil {
		return err
	}
	for _, p := range plan {
		t := tabByID(tabs, p.tab)
		if t == nil || fmt.Sprint(sessionIDs(t.root)) != fmt.Sprint(sessionIDs(p.root)) {
			return BackendErr("iTerm2 did not swap %s and %s", x.Native, y.Native)
		}
		if err := b.setLayout(p.tab, p.root); err != nil { // cosmetic: the swap happened
			fmt.Fprintf(os.Stderr, "warning: keep the pane sizes: %v\n", err)
		}
	}
	return nil
}

// Arrange lays out a tab again. even keeps the split tree and evens the sizes, as after a spawn;
// the other layouts build a new tree from the tab's sessions, main first, then even the sizes.
func (b *ITerm2Backend) Arrange(tab, layout string, main *Pane) error {
	if err := CheckLayout(layout); err != nil {
		return err
	}
	if layout == "even" {
		return b.evenTab(tab)
	}
	tabs, err := b.tabs()
	if err != nil {
		return err
	}
	t := tabByID(tabs, tab)
	if t == nil {
		return TargetErr("iTerm2 has no tab %s", tab)
	}
	ids := sessionIDs(t.root)
	if main != nil {
		ordered := []string{main.Native}
		for _, id := range ids {
			if id != main.Native {
				ordered = append(ordered, id)
			}
		}
		ids = ordered
	}
	if err := b.applyLayout(map[string]any{"tabs": []any{map[string]any{"tab_id": tab, "root": layoutTree(layout, ids)}}}); err != nil {
		return err
	}
	if err := b.evenTab(tab); err != nil {
		return BackendErr("laid out tab %s as %s, but could not even out the sizes: %v", tab, layout, err)
	}
	return nil
}

func (b *ITerm2Backend) applyLayout(spec map[string]any) error {
	data, err := json.Marshal(spec)
	if err != nil {
		return BackendErr("iTerm2 apply_layout: %v", err)
	}
	// Base64, as the Python API sends it: iTerm2's expression parser does not decode \" in a string.
	inv := fmt.Sprintf("iterm2.apply_layout(spec_json_b64: %s)", jsonValue(base64.StdEncoding.EncodeToString(data)))
	_, err = b.invokeApp("apply_layout", inv)
	return err
}

func (b *ITerm2Backend) setLayout(tab string, root *api.SplitTreeNode) error {
	r, err := b.call(&api.ClientOriginatedMessage{SetTabLayoutRequest: &api.SetTabLayoutRequest{Root: root, TabId: proto.String(tab)}})
	if err != nil {
		return err
	}
	if st := r.GetSetTabLayoutResponse().GetStatus(); st != api.SetTabLayoutResponse_OK {
		return BackendErr("iTerm2 tab layout: %s", st)
	}
	return nil
}

// swapSessions returns a copy of a split tree with sessions x and y exchanged. Sizes stay with
// the places, so a later SetTabLayoutRequest with this tree keeps every pane's size.
func swapSessions(root *api.SplitTreeNode, x, y string) *api.SplitTreeNode {
	out := proto.Clone(root).(*api.SplitTreeNode)
	walkSplit(out, func(s *api.SessionSummary) {
		switch s.GetUniqueIdentifier() {
		case x:
			s.UniqueIdentifier = proto.String(y)
		case y:
			s.UniqueIdentifier = proto.String(x)
		}
	})
	return out
}

// layoutSpec turns a split tree into apply_layout's form. apply_layout refuses a splitter inside one
// of the same orientation and a splitter with one child, so those are flattened.
func layoutSpec(n *api.SplitTreeNode) map[string]any {
	var kids []any
	for _, l := range n.GetLinks() {
		if s := l.GetSession(); s != nil {
			kids = append(kids, specLeaf(s.GetUniqueIdentifier()))
		} else if c := l.GetNode(); c != nil {
			k := layoutSpec(c)
			if v, ok := k["vertical"].(bool); ok && v == n.GetVertical() {
				kids = append(kids, k["children"].([]any)...)
				continue
			}
			kids = append(kids, k)
		}
	}
	return splitter(n.GetVertical(), kids)
}

func specLeaf(id string) map[string]any { return map[string]any{"session_id": id} }

// splitter is a node of apply_layout's tree; vertical puts the children side by side. One child
// stands alone.
func splitter(vertical bool, kids []any) map[string]any {
	if len(kids) == 1 {
		return kids[0].(map[string]any)
	}
	return map[string]any{"vertical": vertical, "children": kids}
}

func leaves(ids []string) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = specLeaf(id)
	}
	return out
}

// layoutTree builds the apply_layout tree of a layout, ids[0] being the main pane. tiled fills
// rows of ceil(sqrt(n)) panes, as tmux's tiled does.
func layoutTree(layout string, ids []string) map[string]any {
	switch layout {
	case "columns":
		return splitter(true, leaves(ids))
	case "rows":
		return splitter(false, leaves(ids))
	case "main-left":
		return splitter(true, []any{specLeaf(ids[0]), splitter(false, leaves(ids[1:]))})
	case "main-top":
		return splitter(false, []any{specLeaf(ids[0]), splitter(true, leaves(ids[1:]))})
	}
	cols := int(math.Ceil(math.Sqrt(float64(len(ids)))))
	var rows []any
	for i := 0; i < len(ids); i += cols {
		rows = append(rows, splitter(true, leaves(ids[i:min(i+cols, len(ids))])))
	}
	return splitter(false, rows)
}
