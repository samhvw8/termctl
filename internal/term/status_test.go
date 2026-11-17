package term

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func strp(s string) *string { return &s }

// iTerm2 shows an OSC 7501 "blocked" as "waiting" with the program's msg as the detail.
func TestITermStatusMapsWaitingToBlocked(t *testing.T) {
	cases := []struct {
		in   itermStatus
		want ProgramStatus
		ok   bool
	}{
		{itermStatus{Status: strp("waiting"), Detail: strp("Run touch x?")}, ProgramStatus{State: "blocked", Msg: "Run touch x?"}, true},
		{itermStatus{Status: strp("Waiting")}, ProgramStatus{State: "blocked"}, true},
		{itermStatus{Status: strp("done"), Detail: strp("finished")}, ProgramStatus{State: "done", Msg: "finished"}, true},
		{itermStatus{Status: strp("working")}, ProgramStatus{State: "working"}, true},
		{itermStatus{}, ProgramStatus{}, false},
		{itermStatus{Status: strp("")}, ProgramStatus{}, false},
	}
	for _, c := range cases {
		got, ok := itermProgramStatus(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("%+v: got %+v %v, want %+v %v", c.in, got, ok, c.want, c.ok)
		}
		if ok && got.Blocked() != (c.want.State == "blocked") {
			t.Errorf("%+v: Blocked() %v", c.in, got.Blocked())
		}
	}
}

func TestRexPicksTheMostUrgentRecord(t *testing.T) {
	got, ok := rexProgramStatus([]rexStatusRecord{{State: "working"}, {State: "blocked", Kind: "permission", Msg: "Run?"}, {State: "done"}})
	if !ok || got != (ProgramStatus{State: "blocked", Kind: "permission", Msg: "Run?"}) {
		t.Fatal(got, ok)
	}
	if _, ok := rexProgramStatus(nil); ok {
		t.Fatal("no record is no status")
	}
	if _, ok := rexProgramStatus([]rexStatusRecord{{State: "clear"}}); ok {
		t.Fatal("a cleared record is no status")
	}
}

// A block keeps a record per id. A "blocked" under an id the program stopped updating outranked what it
// reports now, so a stale or spoofed blocked stuck (agy review A11). The newest record wins; urgency only
// breaks a tie.
func TestRexPrefersTheNewestRecord(t *testing.T) {
	got, ok := rexProgramStatus([]rexStatusRecord{
		{State: "blocked", Kind: "permission", Msg: "Run old?", UpdatedAt: "2026-11-17T18:15:23.966679+07:00"},
		{State: "working", UpdatedAt: "2026-11-17T18:16:01.5+07:00"},
		{State: "done"}, // no time: the oldest
	})
	if !ok || got != (ProgramStatus{State: "working"}) {
		t.Fatal(got, ok)
	}
	got, _ = rexProgramStatus([]rexStatusRecord{{State: "working", UpdatedAt: "2026-11-17T11:16:01Z"}, {State: "blocked", Msg: "Run?", UpdatedAt: "2026-11-17T18:16:01+07:00"}})
	if got.State != "blocked" {
		t.Fatal("the same time in another zone is a tie, broken by urgency:", got)
	}
}

// One rex do call reads every block, against JSON captured from Rex 0.1.0-preview.19 on a private server.
func TestRexProgramStatusesIsOneCall(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "rex", "program_status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	b := &RexBackend{
		getenv: func(k string) string {
			if k == "REX_SERVER" {
				return "unix:///x/s"
			}
			return ""
		},
		run: func(args ...string) (string, int, string, error) {
			calls = append(calls, args)
			return string(data), 0, "", nil
		},
		blocks: map[string]string{"01a1e938-008e-7678-9c4f-8b50f4fb8faa": "session:a", "01a1e938-0000-7678-9c4f-8b50f4fb8fab": "session:b"},
	}
	panes := []*Pane{{ID: "rex:01a1e938-008e-7678-9c4f-8b50f4fb8faa", Native: "01a1e938-008e-7678-9c4f-8b50f4fb8faa"},
		{ID: "rex:01a1e938-0000-7678-9c4f-8b50f4fb8fab", Native: "01a1e938-0000-7678-9c4f-8b50f4fb8fab"}}
	st, err := b.ProgramStatuses(panes)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0][3] != "do" {
		t.Fatalf("want one rex do call, got %q", calls)
	}
	script := calls[0][5]
	for _, want := range []string{"block:01a1e938-008e-7678-9c4f-8b50f4fb8faa", "block:01a1e938-0000-7678-9c4f-8b50f4fb8fab", "session.attach", "program_status"} {
		if !strings.Contains(script, want) {
			t.Fatalf("the script lacks %q:\n%s", want, script)
		}
	}
	want := ProgramStatus{State: "blocked", Kind: "permission", Msg: "Run touch fake-file?"}
	if len(st) != 1 || st[panes[0].ID] != want {
		t.Fatalf("%+v", st)
	}
}

// hc ls gives the word for every terminal; only a blocked one costs an hc get, for its reason.
func TestHcProgramStatusesFromCapturedJSON(t *testing.T) {
	s := &hcStub{route: func(args []string) hcReply {
		switch {
		case args[0] == "ls":
			return hcReply{out: fixture(t, "ls_blocked.json")}
		case args[0] == "get" && args[1] == "t1":
			return hcReply{out: fixture(t, "get_permission.json")}
		}
		return hcReply{code: 2, stderr: "unexpected"}
	}}
	panes := []*Pane{{ID: "hc:t1", Native: "t1"}, {ID: "hc:t2", Native: "t2"}}
	st, err := hcBackend(s, true).ProgramStatuses(panes)
	if err != nil {
		t.Fatal(err)
	}
	if want := (ProgramStatus{State: "blocked", Kind: "permission", Msg: "Run touch fake-file?"}); st["hc:t1"] != want {
		t.Fatalf("%+v", st)
	}
	if _, ok := st["hc:t2"]; ok {
		t.Fatal("t2 never reported a status")
	}
	gets := 0
	for _, c := range s.calls {
		if c[0] == "get" {
			gets++
		}
	}
	if gets != 1 {
		t.Fatalf("want one hc get, got %q", s.calls)
	}
	if st, err := hcBackend(&hcStub{}, false).ProgramStatuses(panes); err != nil || len(st) != 0 {
		t.Fatal("no core: no status, and nothing started", st, err)
	}
}
