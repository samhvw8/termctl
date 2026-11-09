package term

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"testing"
)

type rexStub struct {
	calls   [][]string
	replies []rexReply
}
type rexReply struct {
	out  string
	code int
}

func (s *rexStub) run(args ...string) (string, int, string, error) {
	s.calls = append(s.calls, args)
	if len(s.replies) == 0 {
		return "", 0, "", nil
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r.out, r.code, "boom", nil
}

func rexPane() *Pane { return &Pane{ID: "rex:abcd1234", Backend: "rex", Native: "abcd1234"} }

func TestRexListReadsBlocks(t *testing.T) {
	s := &rexStub{replies: []rexReply{{out: `[{"id":"b1","label":"logs","title":"t","cwd":"/w"}]`}}}
	panes, err := (&RexBackend{run: s.run}).ListPanes()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.calls[0], []string{"block", "ls", "-A", "--json"}) {
		t.Fatal(s.calls[0])
	}
	if panes[0].ID != "rex:b1" || panes[0].Label != "logs" || panes[0].Folder != "/w" {
		t.Fatalf("%+v", panes[0])
	}
}

func TestRexSendAndKeys(t *testing.T) {
	s := &rexStub{}
	b := &RexBackend{run: s.run}
	b.SendText(rexPane(), "hello")
	b.SendKey(rexPane(), "enter")
	b.SendKey(rexPane(), "ctrl-u")
	want := [][]string{{"send", "-b", "abcd1234", "hello"}, {"send-key", "-b", "abcd1234", "enter"}, {"send-key", "-b", "abcd1234", "ctrl+u"}}
	if !reflect.DeepEqual(s.calls, want) {
		t.Fatal(s.calls)
	}
}

func TestRexCapture(t *testing.T) {
	s := &rexStub{replies: []rexReply{{out: "line1\nline2\n"}}}
	got, _ := (&RexBackend{run: s.run}).Capture(rexPane(), 1)
	if !reflect.DeepEqual(got, []string{"line2"}) || !reflect.DeepEqual(s.calls[0], []string{"capture", "-b", "abcd1234", "--trim"}) {
		t.Fatal(got, s.calls)
	}
}

func TestRexSpawnTabKeepsFocus(t *testing.T) {
	os.Setenv("REX_SESSION", "s1")
	defer os.Unsetenv("REX_SESSION")
	reply, _ := json.Marshal(map[string]string{"id": "b2"})
	s := &rexStub{replies: []rexReply{{out: string(reply)}}}
	p, err := (&RexBackend{run: s.run}).Spawn("tab", "omp", "/w", "t")
	if err != nil || p.ID != "rex:b2" {
		t.Fatal(p, err)
	}
	joined := " " + join(s.calls[0]) + " "
	for _, want := range []string{" window new ", " --focus=false ", " --json "} {
		if !contains(joined, want) {
			t.Fatal(joined)
		}
	}
}

func TestRexCloseAndLabel(t *testing.T) {
	s := &rexStub{}
	b := &RexBackend{run: s.run}
	b.Close(rexPane())
	b.SetLabel(rexPane(), "worker")
	want := [][]string{{"block", "close", "abcd1234"}, {"block", "rename", "abcd1234", "worker"}}
	if !reflect.DeepEqual(s.calls, want) {
		t.Fatal(s.calls)
	}
}

func TestRexExitCodes(t *testing.T) {
	s := &rexStub{replies: []rexReply{{code: 3}, {code: 1}}}
	b := &RexBackend{run: s.run}
	var e *Error
	if err := b.SendText(rexPane(), "x"); !errors.As(err, &e) || e.Code != 2 {
		t.Fatal(err)
	}
	if err := b.SendText(rexPane(), "x"); !errors.As(err, &e) || e.Code != 1 {
		t.Fatal(err)
	}
}

func TestRexLive(t *testing.T) {
	if _, err := exec.LookPath("rex"); err != nil {
		t.Skip("rex not installed")
	}
	if _, err := NewRex().ListPanes(); err != nil {
		t.Fatal(err)
	}
}

func join(xs []string) string {
	out := ""
	for _, x := range xs {
		out += x + " "
	}
	return out
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
