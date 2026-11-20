package harness

import (
	"strings"
	"testing"
)

// A busy worker may move: its program keeps running. Its task and pane record stay, by pane ID.
func TestMoveTaskPaneKeepsTaskAndRecord(t *testing.T) {
	r := newRig(t)
	x := r.mustNew(t, nil, NewOpts{})
	p := r.pane(x)
	p.Tab, r.owner.Tab, r.other.Tab = "W", "O", "O"
	r.b.State[p.ID].Busy = 5
	m, err := r.tasks.MovePane(p.ID, "self")
	if err != nil || m.Pane.Tab != "O" || m.Pane.ID != x.Pane {
		t.Fatal(err, m)
	}
	if got := r.get(t, x.ID); got.Pane != p.ID || got.Status != "running" || r.data().Created[p.ID] == nil {
		t.Fatalf("%+v", got)
	}
	if r.tasks.TaskOn(p.ID) != x.ID || r.tasks.TaskOn(r.other.ID) != "" {
		t.Fatal(r.tasks.TaskOn(p.ID))
	}
}

// The caller's own pane is refused even when the backend does not mark it (the sender is known by ID).
func TestMoveRefusesTheSendersPane(t *testing.T) {
	r := newRig(t)
	r.owner.IsSelf, r.owner.Tab, r.other.Tab = false, "O", "X"
	for _, err := range []error{
		func() error { _, err := r.tasks.MovePane(r.owner.ID, "new"); return err }(),
		func() error { _, err := r.tasks.SwapPanes(r.other.ID, r.owner.ID); return err }(),
	} {
		if err == nil || !strings.Contains(err.Error(), "own pane") {
			t.Fatal(err)
		}
	}
	if r.b.Has("move", r.owner.ID) || r.b.Has("break", r.owner.ID) || r.b.Has("swap", r.other.ID) {
		t.Fatal(r.b.Events)
	}
}
