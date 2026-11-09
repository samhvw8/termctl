package harness

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/term"
	"github.com/samhvw8/termctl/internal/termtest"
)

const tag = "[agent-msg from me-label (python3) id=fake:9 folder=~/w/me]"

type opsRig struct {
	ops     *Ops
	b       *termtest.Fake
	t, o, m *term.Pane
	clock   *termtest.Clock
}

func newOps(t *testing.T) *opsRig {
	target := termtest.MakePane(1, "", "target", "~/w/t", "")
	other := termtest.MakePane(2, "", "other", "~/w/o", "")
	me := termtest.MakePane(9, "me-label", "", "~/w/me", "")
	b := termtest.New([]*term.Pane{target, other, me}, me)
	clock := &termtest.Clock{}
	return &opsRig{NewOps(b, me, clock, t.TempDir()), b, target, other, me, clock}
}

func wantCode(t *testing.T, err error, code int) {
	t.Helper()
	var e *term.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("want exit %d, got %v", code, err)
	}
}

func typed(r *opsRig) []string { return r.b.Typed(r.t.ID) }

func TestMsgSendsTaggedLineThenEnter(t *testing.T) {
	r := newOps(t)
	if _, err := r.ops.Msg(r.t, "hello there", MsgOpts{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(typed(r), []string{"text:" + tag + " hello there", "key:enter"}) {
		t.Fatal(typed(r))
	}
}

func TestMsgRefusesDraft(t *testing.T) {
	r := newOps(t)
	r.b.State[r.t.ID].Input = "half typed"
	_, err := r.ops.Msg(r.t, "hello", MsgOpts{})
	wantCode(t, err, 4)
	if !strings.Contains(err.Error(), "half typed") || len(typed(r)) != 0 {
		t.Fatal(err, typed(r))
	}
}

func TestMsgForceIgnoresDraft(t *testing.T) {
	r := newOps(t)
	r.b.State[r.t.ID].Input = "half typed"
	if _, err := r.ops.Msg(r.t, "hello", MsgOpts{Force: true}); err != nil || len(typed(r)) != 2 {
		t.Fatal(err, typed(r))
	}
}

func TestLongAndMultilineGoToFile(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", 801), "line one\nline two"} {
		r := newOps(t)
		if _, err := r.ops.Msg(r.t, body, MsgOpts{}); err != nil {
			t.Fatal(err)
		}
		line := strings.TrimPrefix(typed(r)[0], "text:")
		if !strings.HasPrefix(line, tag+" Long message; read the file ") || strings.Contains(line, "\n") {
			t.Fatal(line)
		}
		data, err := os.ReadFile(strings.SplitN(line, "read the file ", 2)[1])
		if err != nil || !strings.HasPrefix(string(data), tag+"\n\n") || !strings.Contains(string(data), body) {
			t.Fatal(err, string(data))
		}
	}
}

func TestMessageAtLimitStaysInline(t *testing.T) {
	r := newOps(t)
	r.ops.Msg(r.t, strings.Repeat("y", 800), MsgOpts{})
	if strings.Contains(typed(r)[0], "Long message") {
		t.Fatal()
	}
}

func TestMessageIsSanitised(t *testing.T) {
	r := newOps(t)
	r.ops.Msg(r.t, "a\rb\x1bc", MsgOpts{})
	if typed(r)[0] != "text:"+tag+" a bc" {
		t.Fatalf("%q", typed(r)[0])
	}
}

func TestEmptyMessageRefused(t *testing.T) {
	r := newOps(t)
	_, err := r.ops.Msg(r.t, "   ", MsgOpts{})
	wantCode(t, err, 2)
}

func TestClearFirstOrdering(t *testing.T) {
	r := newOps(t)
	if _, err := r.ops.Msg(r.t, "after clear", MsgOpts{ClearFirst: true, Timeout: 120}); err != nil {
		t.Fatal(err)
	}
	want := []string{"text:/clear", "key:enter", "text:" + tag + " after clear", "key:enter"}
	if !reflect.DeepEqual(typed(r), want) {
		t.Fatal(typed(r))
	}
	// The message was typed only after the harness stopped being busy, and the wait polled the screen.
	enter, msg := -1, -1
	for i, e := range r.b.Events {
		if e.Pane == r.t.ID && e.Kind == "key" && enter < 0 {
			enter = i
		}
		if e.Pane == r.t.ID && e.Kind == "text" && strings.Contains(e.Text, "after clear") {
			msg = i
			if e.Busy != 0 {
				t.Fatal("typed while busy")
			}
		}
	}
	polled := false
	for _, e := range r.b.Events[enter:msg] {
		polled = polled || (e.Kind == "capture" && e.Pane == r.t.ID)
	}
	if !polled {
		t.Fatal("no poll between /clear and the message")
	}
}

func TestClearFirstRefusesDraftBeforeSending(t *testing.T) {
	r := newOps(t)
	r.b.State[r.t.ID].Input = "draft"
	_, err := r.ops.Msg(r.t, "x", MsgOpts{ClearFirst: true})
	wantCode(t, err, 4)
	if len(typed(r)) != 0 {
		t.Fatal(typed(r))
	}
}

func TestClearFirstForceWipesDraft(t *testing.T) {
	r := newOps(t)
	r.b.State[r.t.ID].Input = "draft"
	if _, err := r.ops.Msg(r.t, "x", MsgOpts{ClearFirst: true, Force: true, Timeout: 60}); err != nil {
		t.Fatal(err)
	}
	if typed(r)[0] != "key:ctrl-u" || typed(r)[1] != "text:/clear" {
		t.Fatal(typed(r))
	}
}

func TestClearFirstRefusesWhenSlashStaysInInput(t *testing.T) {
	// Enter was ignored: /clear is still typed. Appending would recreate the original bug.
	r := newOps(t)
	r.b.KeepClearText = true
	_, err := r.ops.Msg(r.t, "x", MsgOpts{ClearFirst: true, Timeout: 60})
	wantCode(t, err, 4)
	if !strings.Contains(err.Error(), "/clear") || !reflect.DeepEqual(r.b.Texts(r.t.ID), []string{"/clear"}) {
		t.Fatal(err, r.b.Texts(r.t.ID))
	}
}

func TestSlash(t *testing.T) {
	r := newOps(t)
	if err := r.ops.Slash(r.t, "/compact", false, false, 0); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(typed(r), []string{"text:/compact", "key:enter"}) {
		t.Fatal(typed(r))
	}
	wantCode(t, r.ops.Slash(r.t, "clear", false, false, 0), 2)
	wantCode(t, r.ops.Slash(r.t, "/foo\nbar", false, false, 0), 2)
}

func TestSlashDraftRefusal(t *testing.T) {
	r := newOps(t)
	r.b.State[r.t.ID].Input = "draft"
	wantCode(t, r.ops.Slash(r.t, "/compact", false, false, 0), 4)
	if len(typed(r)) != 0 {
		t.Fatal()
	}
}

func TestClearWaitsForIdle(t *testing.T) {
	r := newOps(t)
	if err := r.ops.Clear(r.t, false, 60); err != nil {
		t.Fatal(err)
	}
	if r.b.State[r.t.ID].Busy != 0 {
		t.Fatal()
	}
}

func TestWaitIdleTimesOut(t *testing.T) {
	r := newOps(t)
	r.b.State[r.t.ID].Busy = 1 << 30
	wantCode(t, r.ops.WaitIdle(r.t, 5, nil), 5)
}

func TestMsgNeedsSender(t *testing.T) {
	r := newOps(t)
	ops := NewOps(r.b, nil, r.clock, t.TempDir())
	_, err := ops.Msg(r.t, "hi", MsgOpts{})
	wantCode(t, err, 2)
}

func TestSelfAndAmbiguousTargets(t *testing.T) {
	r := newOps(t)
	_, err := r.ops.Pick("me-label")
	wantCode(t, err, 2)
	if !strings.Contains(err.Error(), "own pane") {
		t.Fatal(err)
	}
	if p, err := r.ops.Pick("target"); err != nil || p.ID != r.t.ID {
		t.Fatal(p, err)
	}
	r.o.Title = "target"
	_, err = r.ops.Pick("target")
	wantCode(t, err, 2)
}

func TestSpawnWaitsReadyThenSubmits(t *testing.T) {
	r := newOps(t)
	p, err := r.ops.Spawn("generic", "tab", "/tmp/x", SpawnOpts{Prompt: "do the thing", Label: "worker", Command: "fake-cmd --flag", Timeout: 60})
	if err != nil {
		t.Fatal(err)
	}
	if r.b.Events[0].Kind != "spawn" || r.b.Events[0].Text != "fake-cmd --flag|/tmp/x" {
		t.Fatal(r.b.Events[0])
	}
	if !r.b.Has("label", p.ID) || !reflect.DeepEqual(r.b.Typed(p.ID), []string{"text:do the thing", "key:enter"}) {
		t.Fatal(r.b.Typed(p.ID))
	}
}

func TestSpawnMultilinePromptGoesToFile(t *testing.T) {
	r := newOps(t)
	p, _ := r.ops.Spawn("generic", "tab", "/tmp/x", SpawnOpts{Prompt: "a\nb", Command: "c", Timeout: 60})
	line := r.b.Typed(p.ID)[0]
	if !strings.Contains(strings.ToLower(line), "read the file") || strings.Contains(line, "\n") {
		t.Fatal(line)
	}
}

func TestSpawnUsage(t *testing.T) {
	r := newOps(t)
	_, err := r.ops.Spawn("nonsense", "tab", "/tmp", SpawnOpts{})
	wantCode(t, err, 2)
	f := filepath.Join(t.TempDir(), "f")
	_, err = r.ops.Spawn("generic", "tab", "/tmp", SpawnOpts{Prompt: "a", PromptFile: f, Command: "c"})
	wantCode(t, err, 2)
}

func TestInterrupt(t *testing.T) {
	claude := termtest.MakePane(1, "", "c", "", "claude")
	open := termtest.MakePane(2, "", "o", "", "opencode")
	b := termtest.New([]*term.Pane{claude, open}, nil)
	ops := NewOps(b, nil, &termtest.Clock{}, t.TempDir())
	keys, ok, err := ops.Interrupt(claude, "")
	if err != nil || !ok || !reflect.DeepEqual(keys, []string{"escape"}) || b.Typed(claude.ID)[0] != "key:escape" {
		t.Fatal(keys, ok, err)
	}
	keys, ok, _ = ops.Interrupt(claude, "ctrl-c")
	if !ok || keys[0] != "ctrl-c" {
		t.Fatal(keys, ok)
	}
	if _, ok, _ := ops.Interrupt(open, ""); ok {
		t.Fatal("opencode is not verified")
	}
}

func TestSubmitPressesEnterAgainWhenTheTextStays(t *testing.T) {
	r := newOps(t)
	r.b.DropEnters = 1
	if _, err := r.ops.Msg(r.t, "long text", MsgOpts{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"text:" + tag + " long text", "key:enter", "key:enter"}
	if !reflect.DeepEqual(typed(r), want) {
		t.Fatal(typed(r))
	}
	if r.b.State[r.t.ID].Input != "" {
		t.Fatal("still in the input line")
	}
}

func TestSubmitFailsLoudlyWhenTheTextNeverGoes(t *testing.T) {
	r := newOps(t)
	r.b.DropEnters = 100
	_, err := r.ops.Msg(r.t, "stuck", MsgOpts{})
	wantCode(t, err, 4)
	if !strings.Contains(err.Error(), "not submitted") {
		t.Fatal(err)
	}
}
