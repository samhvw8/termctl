package harness

import (
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/term"
	"github.com/samhvw8/termctl/internal/termtest"
)

// submitFake is a backend without paste-without-Enter, like hc.
type submitFake struct {
	*termtest.Fake
	submitted []string
	stickFor  int // this many submits stay on the input line
	from      []string
}

func (s *submitFake) SubmitText(p *term.Pane, text, from string) error {
	s.submitted = append(s.submitted, text)
	s.from = append(s.from, from)
	if s.stickFor > 0 {
		s.stickFor--
		s.State[p.ID].Input = text
	}
	return nil
}

func TestSubmitUsesSubmitterAndNeverSendText(t *testing.T) {
	r := newOps(t)
	s := &submitFake{Fake: r.b}
	r.ops.Backend = s
	if _, err := r.ops.Msg(r.t, "hello", MsgOpts{Timeout: 60}); err != nil {
		t.Fatal(err)
	}
	if len(s.submitted) != 1 || !strings.HasPrefix(s.submitted[0], tag) || !strings.HasSuffix(s.submitted[0], " hello") {
		t.Fatal(s.submitted)
	}
	if typed(r) != nil {
		t.Fatalf("SendText or Enter was used: %v", typed(r))
	}
	if !strings.Contains(s.from[0], "/") {
		t.Fatalf("an agent message names its sender with a slash, or hc shows no tag: %q", s.from[0])
	}
	if err := r.ops.Submit(r.t, "/clear", nil); err != nil {
		t.Fatal(err)
	}
	if s.from[1] != "" {
		t.Fatalf("a tag would stop a slash command working: %q", s.from[1])
	}
}

func TestSubmitterStillChecksTheInputLineAfterwards(t *testing.T) {
	r := newOps(t)
	s := &submitFake{Fake: r.b, stickFor: 1}
	r.ops.Backend = s
	if err := r.ops.Submit(r.t, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if got := typed(r); len(got) != 1 || got[0] != "key:enter" {
		t.Fatalf("expected one Enter after the text stayed: %v", got)
	}
}

func TestSubmitterRefusesLineBreaks(t *testing.T) {
	r := newOps(t)
	s := &submitFake{Fake: r.b}
	r.ops.Backend = s
	wantCode(t, r.ops.Submit(r.t, "a\nb", nil), term.CodeUsage)
	if len(s.submitted) != 0 {
		t.Fatal(s.submitted)
	}
}

// blocked is hc's word: a person must answer, even though a prompt can still be on screen.
func TestWaitIdleTreatsBlockedAsNotReady(t *testing.T) {
	r := newOps(t)
	r.b.Natives[r.t.ID] = "blocked"
	wantCode(t, r.ops.WaitIdle(r.t, 2, Profiles["generic"]), term.CodeTimeout)
	r.b.Natives[r.t.ID] = "working"
	wantCode(t, r.ops.WaitIdle(r.t, 2, Profiles["generic"]), term.CodeTimeout)
	for _, word := range []string{"idle", "done", "error", ""} {
		r.b.Natives[r.t.ID] = word
		if err := r.ops.WaitIdle(r.t, 60, Profiles["generic"]); err != nil {
			t.Fatalf("native %q: %v", word, err)
		}
	}
}

func TestStatusShowsNativeWord(t *testing.T) {
	r := newOps(t)
	for _, word := range []string{"blocked", "done", "error", "working", "idle"} {
		if got := r.ops.Status(r.t, word); got != word {
			t.Fatal(got)
		}
	}
}

func TestScreenLinesCapsAtPaneHeight(t *testing.T) {
	if got := screenLines(&term.Pane{Rows: 50}); got != 50 {
		t.Fatal(got)
	}
	if got := screenLines(&term.Pane{}); got != captureLines {
		t.Fatal(got)
	}
	if got := screenLines(&term.Pane{Rows: 900}); got != captureLines {
		t.Fatal(got)
	}
}

func spawnText(b *termtest.Fake) string {
	for _, e := range b.Events {
		if e.Kind == "spawn" {
			return e.Text
		}
	}
	return ""
}

func TestSpawnResetsAgentEnvOnlyOnHc(t *testing.T) {
	r := newOps(t)
	r.b.BackendName = "hc"
	if _, err := r.ops.Spawn("generic", "tab", "/w", SpawnOpts{Command: "fake", Timeout: 60}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spawnText(r.b), "unset CLAUDE_CODE_CHILD_SESSION; fake") {
		t.Fatal(spawnText(r.b))
	}
	r = newOps(t)
	if _, err := r.ops.Spawn("generic", "tab", "/w", SpawnOpts{Command: "fake", Timeout: 60}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(spawnText(r.b), "unset") {
		t.Fatal(spawnText(r.b))
	}
}

func TestBusyCountsBlocked(t *testing.T) {
	r := newOps(t)
	tasks := &Tasks{Backend: r.b, Clock: r.clock}
	r.b.Natives[r.t.ID] = "blocked"
	if busy, err := tasks.busy(r.b, r.t); err != nil || !busy {
		t.Fatal(busy, err)
	}
	r.b.Natives[r.t.ID] = "idle"
	if busy, _ := tasks.busy(r.b, r.t); busy {
		t.Fatal("idle is not busy")
	}
}

func TestSameServer(t *testing.T) {
	cases := []struct {
		stored, current map[string]string
		want            bool
	}{
		{map[string]string{"core_id": "a", "pid": "1"}, map[string]string{"core_id": "a", "pid": "2"}, true}, // hc server restart keeps terminals
		{map[string]string{"core_id": "a"}, map[string]string{"core_id": "b"}, false},                        // state wiped
		{map[string]string{"pid": "1"}, map[string]string{"pid": "1"}, true},
		{map[string]string{"pid": "1"}, map[string]string{"pid": "2"}, false}, // tmux restart reuses %N
		{map[string]string{"socket_path": "/x"}, map[string]string{}, true},   // no identity stored
		{nil, map[string]string{"pid": "9"}, true},
		{map[string]string{"pid": "1"}, map[string]string{}, false},
	}
	for _, c := range cases {
		if got := sameServer(c.stored, c.current); got != c.want {
			t.Fatalf("%v vs %v: %v", c.stored, c.current, got)
		}
	}
}
