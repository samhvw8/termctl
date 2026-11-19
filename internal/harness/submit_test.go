package harness

import (
	"regexp"
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

// A starting Claude Code took the typed prompt late and lost its Enter, while the input line still
// looked empty right after (2026-11-19, task t93a651). One empty look is not proof the line went.
func TestSubmitLooksAgainWhenTheTextShowsLate(t *testing.T) {
	r := newOps(t)
	r.b.LateInput = 1
	if err := r.ops.SubmitFirst(r.t, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if in := r.b.State[r.t.ID].Input; in != "" {
		t.Fatalf("the text stayed on the input line: %q", in)
	}
	if got := typed(r); len(got) != 3 || got[1] != "key:enter" || got[2] != "key:enter" {
		t.Fatalf("expected a second Enter once the text showed: %v", got)
	}
}

func TestSpawnSubmitsWhenTheFirstEnterIsLost(t *testing.T) {
	r := newOps(t)
	r.b.LateInput = 1
	p, err := r.ops.Spawn("generic", "tab", "/tmp/x", SpawnOpts{Prompt: "do the thing", Command: "c", Timeout: 60})
	if err != nil {
		t.Fatal(err)
	}
	if in := r.b.State[p.ID].Input; in != "" {
		t.Fatalf("the prompt stayed on the input line: %q", in)
	}
}

// Text that never leaves the input line is an error, never success. It is not exit 4 (a draft refused,
// nothing sent): the text was typed, and a busy harness can still deliver it (2026-11-19, three copies).
func TestSubmitFailsWhenTheTextStays(t *testing.T) {
	r := newOps(t)
	r.b.DropEnters = 10
	err := r.ops.Submit(r.t, "hello", nil)
	wantCode(t, err, term.CodeTimeout)
	if !strings.Contains(err.Error(), "stayed on the input line") || !strings.Contains(err.Error(), "do not send it again") {
		t.Fatal(err)
	}
}

// A send to a busy pane whose text stays on the input line is no draft refusal.
func TestMsgToABusyPaneWhoseTextStaysIsNotARefusal(t *testing.T) {
	r := newOps(t)
	r.b.Natives[r.t.ID] = "working"
	r.b.DropEnters = 10
	_, err := r.ops.Msg(r.t, "hello", MsgOpts{Timeout: 60})
	wantCode(t, err, term.CodeTimeout)
	if len(r.b.Texts(r.t.ID)) != 1 {
		t.Fatal(r.b.Texts(r.t.ID))
	}
}

// A harness that went busy has taken the line: SubmitFirst needs no second look then.
func TestSubmitFirstTrustsABusyHarnessAtOnce(t *testing.T) {
	r := newOps(t)
	busy := *Profiles["generic"]
	busy.BusyPatterns = []*regexp.Regexp{regexp.MustCompile("(?m)^> $")} // the empty line itself reads as busy
	if err := r.ops.SubmitFirst(r.t, "hello", &busy); err != nil {
		t.Fatal(err)
	}
	if got := r.b.Captures(r.t.ID); got != 1 {
		t.Fatalf("expected one look, got %d", got)
	}
}

// A starting harness whose input line cannot be read at the first look gets a second look.
func TestSubmitFirstLooksAgainWhenTheLineIsUnreadable(t *testing.T) {
	r := newOps(t)
	prof := *Profiles["generic"]
	looks := 0
	prof.ReadInput = func(lines []string) *string {
		looks++
		if looks == 1 {
			return nil // a startup redraw hides the box
		}
		return genericInput(lines)
	}
	r.b.LateInput = 1
	if err := r.ops.SubmitFirst(r.t, "hello", &prof); err != nil {
		t.Fatal(err)
	}
	if in := r.b.State[r.t.ID].Input; in != "" {
		t.Fatalf("the text stayed on the input line: %q", in)
	}
}

// The same lost Enter, read through Claude Code's own input box (claudeInput).
func TestSpawnClaudeSubmitsWhenTheFirstEnterIsLost(t *testing.T) {
	r := newOps(t)
	r.b.LateInput = 1
	p, err := r.ops.Spawn("claude", "tab", "/tmp/x", SpawnOpts{Prompt: "do the thing", Timeout: 60})
	if err != nil {
		t.Fatal(err)
	}
	if in := r.b.State[p.ID].Input; in != "" {
		t.Fatalf("the prompt stayed on the input line: %q", in)
	}
}

// clockFake notes the time of the first typed text.
type clockFake struct {
	*termtest.Fake
	clock   *termtest.Clock
	typedAt float64
}

func (c *clockFake) SendText(p *term.Pane, text string) error {
	if c.typedAt == 0 {
		c.typedAt = c.clock.T
	}
	return c.Fake.SendText(p, text)
}

// Late startup banners redraw the screen: the first prompt waits for 3 s of stable screen, not 1.5 s.
func TestStartupWaitsForAStableScreenOf3s(t *testing.T) {
	r := newOps(t)
	c := &clockFake{Fake: r.b, clock: r.clock}
	r.ops.Backend = c
	if _, err := r.ops.Spawn("generic", "tab", "/tmp/x", SpawnOpts{Prompt: "go", Command: "c", Timeout: 60}); err != nil {
		t.Fatal(err)
	}
	if c.typedAt < 3.0 {
		t.Fatalf("typed at %.2f s, before %g s of stable screen", c.typedAt, startStable)
	}
}
