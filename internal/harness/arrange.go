package harness

import "github.com/samhvw8/termctl/internal/term"

// Moving and arranging panes for harness-ops. The mechanism is Layer 1 (term.Move, term.Swap,
// term.Arrange); this adds the caller's guards. A busy harness may be moved: its program keeps
// running. A moved pane keeps its ID, which term checks after every change, so its label, its task
// and its registry record stay valid: the registry stores pane IDs, never tabs.

// pickToMove resolves a pane the caller may move: exactly one match, and never the caller's own.
func (t *Tasks) pickToMove(panes []*term.Pane, target string) (*term.Pane, error) {
	p, err := term.Resolve(panes, target)
	if err != nil {
		return nil, err
	}
	if p.IsSelf || t.isSelf(p.ID) {
		return nil, term.TargetErr("refusing to move your own pane")
	}
	return p, nil
}

// selfTab turns "self" into the sender's tab, when the sender is a pane of this backend.
func (t *Tasks) selfTab(tab string) string {
	if (tab == "self" || tab == "") && t.Sender != nil && t.Sender.Backend == t.Backend.Name() {
		return "tab-of:" + t.Sender.ID
	}
	return tab
}

// MovePane moves the pane target resolves to into the tab named by to: a tab target or "new".
func (t *Tasks) MovePane(target, to string) (*term.Moved, error) {
	to = t.selfTab(to)
	panes, err := t.Backend.ListPanes()
	if err != nil {
		return nil, err
	}
	p, err := t.pickToMove(panes, target)
	if err != nil {
		return nil, err
	}
	return term.Move(t.Backend, panes, p, to)
}

// SwapPanes trades the places of two panes.
func (t *Tasks) SwapPanes(a, b string) ([]*term.Pane, error) {
	panes, err := t.Backend.ListPanes()
	if err != nil {
		return nil, err
	}
	x, err := t.pickToMove(panes, a)
	if err != nil {
		return nil, err
	}
	y, err := t.pickToMove(panes, b)
	if err != nil {
		return nil, err
	}
	return term.Swap(t.Backend, x, y)
}

// ArrangeTab lays out a tab again. It moves no pane out of its tab, so the caller's own tab is fine.
func (t *Tasks) ArrangeTab(tab, layout, main string) (*term.Arranged, error) {
	panes, err := t.Backend.ListPanes()
	if err != nil {
		return nil, err
	}
	return term.Arrange(t.Backend, panes, t.selfTab(tab), layout, main)
}

// TaskOn names the open task a pane holds, or "".
func (t *Tasks) TaskOn(paneID string) string {
	d, err := t.Reg.Read()
	if err != nil {
		return ""
	}
	for _, x := range d.Tasks {
		if x.Pane == paneID && !settled(x.Status) && !x.Closed {
			return x.ID
		}
	}
	return ""
}
