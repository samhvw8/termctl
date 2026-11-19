package term

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Fixtures are real output of Rex 0.1.0-preview.19 on a private server (2026-11-15), cut down.

// rex new --json
const rexNewJSON = `{
  "session_id": "session:01a1dff3-551f-757e-a580-bb957e47767c",
  "initial_windows": [
    {
      "window_id": "window:01a1dff3-5547-7bea-8b1c-ec52b8781d44",
      "block_ids": [
        "block:01a1dff3-5520-7693-99f6-151097823792"
      ],
      "revision": 1
    }
  ]
}`

// rex split --json
const rexSplitJSON = `{
  "anchor_id": "block:01a1dff3-5520-7693-99f6-151097823792",
  "block_ids": [
    "block:01a1dff5-36b8-79b4-8ce9-d0820a3c7a4d"
  ],
  "split_ids": [
    "split:01a1dff5-36d8-74c6-a044-5e5c6c4ae4fe"
  ],
  "revision": 2
}`

// rex window new --json (rex run --json gives the same)
const rexWindowJSON = `{
  "window_id": "window:01a1dff5-3835-7d3a-837d-4879206efc01",
  "block_ids": [
    "block:01a1dff5-3811-71d8-a809-ab2ddc334c04"
  ],
  "revision": 6
}`

// rex do -e rexListLua --json: one window with four blocks, the second running a shell.
const rexListJSON = `[
  {"argv0": "sleep", "block": "block:01a1dffb-aa5b-7683-baec-ef5333391d48", "cols": 80, "cwd": "/w", "label": "shell", "name": "sleep", "path": "/bin/sleep", "rows": 24,
   "session": "session:01a1dffb-aa5b-757a-9a9a-b07939b00b00", "title": "", "window": "window:01a1dffb-aa6d-71ad-85ff-5d258b80d378"},
  {"argv0": "-zsh", "block": "block:01a1dffb-ac4d-7e67-ad83-c988dd28f8b4", "cols": 120, "cwd": "/w/y", "label": "y", "name": "zsh", "path": "/bin/zsh", "rows": 40,
   "session": "session:01a1dffb-aa5b-757a-9a9a-b07939b00b00", "title": "zsh", "window": "window:01a1dffb-aa6d-71ad-85ff-5d258b80d378"},
  {"argv0": "/bin/sleep", "block": "block:01a1dffb-ad8b-758d-9848-d7dc0f4dbaa8", "cols": 80, "cwd": "/w", "label": "z", "name": "sleep", "path": "/bin/sleep", "rows": 24,
   "session": "session:01a1dffb-aa5b-757a-9a9a-b07939b00b00", "title": "", "window": "window:01a1dffb-aa6d-71ad-85ff-5d258b80d378"},
  {"argv0": "/bin/sleep", "block": "block:01a1dffb-ab32-7061-b551-cbaeeae611de", "cols": 80, "cwd": "/w", "label": "x", "name": "sleep", "path": "/bin/sleep", "rows": 24,
   "session": "session:01a1dffb-aa5b-757a-9a9a-b07939b00b00", "title": "", "window": "window:01a1dffb-aa6d-71ad-85ff-5d258b80d378"}
]`

// rex api call session.view, of the window above after arrange --layout main-top: (A / (Y | Z | X)).
const rexViewJSON = `{"revision":54,"session_id":"session:01a1dffb-aa5b-757a-9a9a-b07939b00b00","label":"m","active_window_id":"window:01a1dffb-aa6d-71ad-85ff-5d258b80d378","windows":[{"window_id":"window:01a1dffb-aa6d-71ad-85ff-5d258b80d378","label":"Window 8b80d378","focused_block_id":"block:01a1dffb-ab32-7061-b551-cbaeeae611de","layers":[{"layer_id":"layer:01a1dffb-aa6d-71b5-9acc-e4b18608f72d","kind":"tiled","bounds":[0,0,1,1],"layout":{"rect":[0,0,1,1],"split":{"split_id":"split:01a1dffb-c0d3-74ba-b4f8-3b3b07b612e0","direction":"vertical","ratio":0.5,"before":{"rect":[0,0,1,0.5],"block_id":"block:01a1dffb-aa5b-7683-baec-ef5333391d48"},"after":{"rect":[0,0.5,1,0.5],"split":{"split_id":"split:01a1dffb-c0d3-7659-94ac-77c219e60dc8","direction":"horizontal","ratio":0.5,"before":{"rect":[0,0.5,0.5,0.5],"block_id":"block:01a1dffb-ac4d-7e67-ad83-c988dd28f8b4"},"after":{"rect":[0.5,0.5,0.5,0.5],"split":{"split_id":"split:01a1dffb-c0d3-77cc-b6fa-cdcafb490557","direction":"horizontal","ratio":0.5,"before":{"rect":[0.5,0.5,0.25,0.5],"block_id":"block:01a1dffb-ad8b-758d-9848-d7dc0f4dbaa8"},"after":{"rect":[0.75,0.5,0.25,0.5],"block_id":"block:01a1dffb-ab32-7061-b551-cbaeeae611de"}}}}}}},"blocks":[]}]},{"window_id":"window:01a1dffb-0000-7000-8000-0000009c8450","label":"other","focused_block_id":null,"layers":[]}]}`

const (
	rexA = "01a1dffb-aa5b-7683-baec-ef5333391d48"
	rexY = "01a1dffb-ac4d-7e67-ad83-c988dd28f8b4"
	rexZ = "01a1dffb-ad8b-758d-9848-d7dc0f4dbaa8"
	rexX = "01a1dffb-ab32-7061-b551-cbaeeae611de"
	rexS = "session:01a1dffb-aa5b-757a-9a9a-b07939b00b00"
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
	return r.out, r.code, "Error: boom", nil
}

// cmds are the calls without the -S ADDR --autostart=false that every call starts with.
func (s *rexStub) cmds(t *testing.T) [][]string {
	t.Helper()
	var out [][]string
	for _, c := range s.calls {
		if len(c) < 3 || c[0] != "-S" || c[1] != "unix:///t/s" || c[2] != "--autostart=false" {
			t.Fatalf("call without its server: %q", c)
		}
		out = append(out, c[3:])
	}
	return out
}

func rexEnv(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func newRexStub(s *rexStub, env ...string) *RexBackend {
	addr := "unix:///t/s"
	return &RexBackend{run: s.run, getenv: rexEnv(env...), addr: &addr}
}

func rexPane() *Pane { return &Pane{ID: "rex:abcd1234", Backend: "rex", Native: "abcd1234"} }

func TestRexListReadsBlocks(t *testing.T) {
	s := &rexStub{replies: []rexReply{{out: rexListJSON}}}
	panes, err := newRexStub(s, "REX_BLOCK", "block:"+rexY, "HOME", "/nohome").ListPanes()
	if err != nil {
		t.Fatal(err)
	}
	if c := s.cmds(t)[0]; !reflect.DeepEqual(c, []string{"do", "-e", rexListLua, "--json"}) {
		t.Fatal(c)
	}
	if len(panes) != 4 {
		t.Fatal(len(panes))
	}
	y := panes[1]
	want := Pane{ID: "rex:" + rexY, Backend: "rex", Native: rexY, Title: "zsh", Label: "y", Process: "zsh", Folder: "/w/y",
		IsSelf: true, Rows: 40, Width: 120, Height: 40, Tab: "80d378"}
	if !reflect.DeepEqual(*y, want) {
		t.Fatalf("%+v", *y)
	}
	if panes[0].IsSelf || panes[0].Tab != "80d378" || panes[0].Process != "sleep" {
		t.Fatalf("%+v", panes[0])
	}
}

func TestRexListEmptyAndNoServer(t *testing.T) {
	s := &rexStub{replies: []rexReply{{out: "{}\n"}}} // Lua returns an empty list as {}
	if panes, err := newRexStub(s).ListPanes(); err != nil || len(panes) != 0 {
		t.Fatal(panes, err)
	}
	none := ""
	s = &rexStub{}
	b := &RexBackend{run: s.run, getenv: rexEnv(), addr: &none}
	if panes, err := b.ListPanes(); err != nil || panes != nil {
		t.Fatal(panes, err)
	}
	var e *Error
	if err := b.SendText(rexPane(), "x"); !errors.As(err, &e) || e.Code != 1 || !strings.Contains(e.Msg, "no Rex server") {
		t.Fatal(err)
	}
	if len(s.calls) != 0 {
		t.Fatal("asked rex without a server, which would start one:", s.calls)
	}
}

func TestRexTabsGrowOnAClash(t *testing.T) {
	rows := []rexRow{{Window: "window:01a1dffb-aaaa-7000-8000-00000000d378"}, {Window: "window:01a1dffb-bbbb-7000-8000-0000ff00d378"}, {}}
	tabs := rexTabs(rows)
	if tabs[rows[0].Window] != "000d378" || tabs[rows[1].Window] != "f00d378" || len(tabs) != 2 {
		t.Fatal(tabs)
	}
}

func TestRexServerChoice(t *testing.T) {
	socks := func(paths ...string) func(string) bool {
		return func(p string) bool {
			for _, q := range paths {
				if p == q {
					return true
				}
			}
			return false
		}
	}
	app := "/h/Library/Application Support/rex/server.sock"
	xdg := "/run/rex/server.sock"
	// REX_SERVER wins, unprobed
	b := &RexBackend{run: (&rexStub{}).run, getenv: rexEnv("REX_SERVER", "unix:///x", "HOME", "/h"), isSock: socks(app)}
	if got := b.server(); got != "unix:///x" {
		t.Fatal(got)
	}
	// one socket: used as it is
	s := &rexStub{}
	b = &RexBackend{run: s.run, getenv: rexEnv("HOME", "/h", "XDG_RUNTIME_DIR", "/run"), isSock: socks(app)}
	if got := b.server(); got != "unix://"+app || len(s.calls) != 0 {
		t.Fatal(got, s.calls)
	}
	// a stray server at the XDG path (what a bare rex starts) and the app's: the one holding our session
	s = &rexStub{replies: []rexReply{{out: "session:other\n"}, {out: "session:other\nsession:mine\n"}}}
	b = &RexBackend{run: s.run, getenv: rexEnv("HOME", "/h", "XDG_RUNTIME_DIR", "/run", "REX_SESSION", "session:mine"), isSock: socks(app, xdg)}
	if got := b.server(); got != "unix://"+app {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(s.calls[0], []string{"-S", "unix://" + xdg, "--autostart=false", "ls", "-q", "--short=false"}) {
		t.Fatal(s.calls)
	}
	// no socket: no server, and rex is never run
	s = &rexStub{}
	b = &RexBackend{run: s.run, getenv: rexEnv("HOME", "/h"), isSock: socks()}
	if got := b.server(); got != "" || len(s.calls) != 0 {
		t.Fatal(got, s.calls)
	}
}

func TestRexSocketOrderMatchesTheCLI(t *testing.T) {
	got := rexSockets(rexEnv("REX_RUNTIME_DIR", "/rt", "XDG_RUNTIME_DIR", "/x", "HOME", "/h"))
	want := []string{"/rt/server.sock", "/x/rex/server.sock", "/h/Library/Application Support/rex/server.sock"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

func TestRexBinFromRexBinDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rex"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := rexBin(rexEnv("REX_BIN_DIR", dir)); got != filepath.Join(dir, "rex") {
		t.Fatal(got)
	}
	home := t.TempDir()
	app := filepath.Join(home, rexAppCLI)
	os.MkdirAll(filepath.Dir(app), 0o755)
	os.WriteFile(app, []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", t.TempDir())
	if got := rexBin(rexEnv("HOME", home)); got != app {
		t.Fatal(got)
	}
}

func TestRexSendAndKeys(t *testing.T) {
	s := &rexStub{}
	b := newRexStub(s)
	b.SendText(rexPane(), "-starts with a dash")
	b.SendText(rexPane(), "") // rex send refuses empty input
	b.SendKey(rexPane(), "enter")
	b.SendKey(rexPane(), "ctrl-u")
	b.SendKey(rexPane(), "PageDown")
	b.SendKey(rexPane(), "option+m")
	want := [][]string{{"send", "-b", "abcd1234", "--", "-starts with a dash"}, {"send-key", "-b", "abcd1234", "enter"}, {"send-key", "-b", "abcd1234", "ctrl+u"},
		{"send-key", "-b", "abcd1234", "page_down"}, {"send-key", "-b", "abcd1234", "alt+m"}}
	if got := s.cmds(t); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

func TestRexCapture(t *testing.T) {
	s := &rexStub{replies: []rexReply{{out: "line1\nline2\n"}}}
	got, _ := newRexStub(s).Capture(rexPane(), 1)
	if !reflect.DeepEqual(got, []string{"line2"}) || !reflect.DeepEqual(s.cmds(t)[0], []string{"capture", "-b", "abcd1234", "--trim"}) {
		t.Fatal(got, s.calls)
	}
}

func TestRexCreationResults(t *testing.T) {
	for _, c := range []struct{ out, block, window string }{
		{rexNewJSON, "01a1dff3-5520-7693-99f6-151097823792", "01a1dff3-5547-7bea-8b1c-ec52b8781d44"},
		{rexSplitJSON, "01a1dff5-36b8-79b4-8ce9-d0820a3c7a4d", ""},
		{rexWindowJSON, "01a1dff5-3811-71d8-a809-ab2ddc334c04", "01a1dff5-3835-7d3a-837d-4879206efc01"},
	} {
		var r rexCreated
		if err := json.Unmarshal([]byte(c.out), &r); err != nil || r.block() != c.block || r.window() != c.window {
			t.Fatal(r, err)
		}
	}
}

// split: beside the anchor (a wide one), labelled, then the anchor's window is evened out.
func TestRexSpawnSplit(t *testing.T) {
	s := &rexStub{replies: []rexReply{{out: rexSplitJSON}, {}, {out: rexListJSON}, {out: rexViewJSON}}}
	at := &Pane{Native: rexY, Width: 120, Height: 40, Tab: "80d378"}
	p, err := newRexStub(s, "SHELL", "/bin/zsh").SpawnAt(at, "split", "omp --x 'a b'", "/w", "t")
	if err != nil || p.ID != "rex:01a1dff5-36b8-79b4-8ce9-d0820a3c7a4d" || p.Tab != "80d378" || p.Label != "t" {
		t.Fatal(p, err)
	}
	cmds := s.cmds(t)
	want := []string{"split", "--focus=false", "--json", "-b", rexY, "--split", "right", "-c", "/w", "--shell=none", "--", "/bin/zsh", "-lic", "omp --x 'a b'"}
	if !reflect.DeepEqual(cmds[0], want) || !reflect.DeepEqual(cmds[1], []string{"block", "rename", "01a1dff5-36b8-79b4-8ce9-d0820a3c7a4d", "--", "t"}) {
		t.Fatal(cmds)
	}
	if cmds[3][0] != "api" || cmds[3][2] != "session.view" {
		t.Fatal(cmds[3])
	}
	// the view is already even (A over Y|Z|X needs 1/3 for Y), so only the Y|ZX ratio changes
	if len(cmds) != 5 || cmds[4][0] != "do" || !strings.Contains(cmds[4][5], `"ratio":0.3333333333333333`) {
		t.Fatal(cmds)
	}
}

// tab: a window in the anchor's session, moved right after the anchor's window.
func TestRexSpawnTabKeepsFocus(t *testing.T) {
	s := &rexStub{replies: []rexReply{{out: rexListJSON}, {out: rexWindowJSON}}}
	p, err := newRexStub(s).SpawnAt(&Pane{Native: rexA, Tab: "80d378"}, "tab", "", "", "")
	if err != nil || p.Native != "01a1dff5-3811-71d8-a809-ab2ddc334c04" {
		t.Fatal(p, err)
	}
	cmds := s.cmds(t)
	if !reflect.DeepEqual(cmds[1], []string{"window", "new", "--focus=false", "--json", "-s", rexS}) ||
		!reflect.DeepEqual(cmds[2], []string{"window", "move", "01a1dff5-3835-7d3a-837d-4879206efc01", "--after", "80d378"}) {
		t.Fatal(cmds)
	}
}

func TestRexSpawnWindowIsASession(t *testing.T) {
	s := &rexStub{replies: []rexReply{{out: rexNewJSON}}}
	p, err := newRexStub(s).SpawnAt(nil, "window", "", "", "")
	if err != nil || p.Native != "01a1dff3-5520-7693-99f6-151097823792" || !reflect.DeepEqual(s.cmds(t), [][]string{{"new", "--json"}}) {
		t.Fatal(p, err, s.calls)
	}
}

func TestRexCloseAndLabel(t *testing.T) {
	s := &rexStub{}
	b := newRexStub(s)
	b.Close(rexPane())
	b.SetLabel(rexPane(), "-worker")
	want := [][]string{{"block", "close", "abcd1234"}, {"block", "rename", "abcd1234", "--", "-worker"}}
	if got := s.cmds(t); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

// 3 is a target Rex does not know, 4 no server: rex: block target "x" not found.
func TestRexExitCodes(t *testing.T) {
	s := &rexStub{replies: []rexReply{{code: 3}, {code: 4}, {code: 1}}}
	b := newRexStub(s)
	for _, want := range []int{2, 1, 1} {
		var e *Error
		if err := b.SendText(rexPane(), "x"); !errors.As(err, &e) || e.Code != want || !strings.Contains(e.Msg, "boom") || strings.Contains(e.Msg, "Error:") {
			t.Fatal(err)
		}
	}
}

func rexBatchCalls(t *testing.T, cmd []string) []rexCall {
	t.Helper()
	if len(cmd) != 6 || cmd[0] != "do" || cmd[2] != rexBatchLua || cmd[4] != "--args" {
		t.Fatal(cmd)
	}
	var a struct {
		Session string    `json:"session_id"`
		Calls   []rexCall `json:"calls"`
	}
	if err := json.Unmarshal([]byte(cmd[5]), &a); err != nil || a.Session != rexS {
		t.Fatal(a, err)
	}
	return a.Calls
}

func TestRexEvenCalls(t *testing.T) {
	var v rexView
	if err := json.Unmarshal([]byte(rexViewJSON), &v); err != nil {
		t.Fatal(err)
	}
	tree := v.Windows[0].Layers[0].Layout.tree()
	if got := tree.blocks(); !reflect.DeepEqual(got, []string{rexA, rexY, rexZ, rexX}) {
		t.Fatal(got)
	}
	calls := tree.evenCalls()
	if len(calls) != 1 || calls[0].Method != "session.resize_pane" || calls[0].Params["direction"] != "horizontal" ||
		calls[0].Params["before_block_id"] != "block:"+rexY || calls[0].Params["after_block_id"] != "block:"+rexZ || calls[0].Params["ratio"] != 1.0/3 {
		t.Fatal(calls)
	}
}

// main-left with Y as main: every other block is detached, then each is placed with an even
// ratio, and focus goes back to the block that had it.
func TestRexArrangeMainLeft(t *testing.T) {
	s := &rexStub{replies: []rexReply{{out: rexListJSON}, {out: rexViewJSON}}}
	if err := newRexStub(s).Arrange("80d378", "main-left", &Pane{Native: rexY}); err != nil {
		t.Fatal(err)
	}
	short := func(v any) string { id := v.(string); return id[len(id)-4:] }
	var got []string
	for _, c := range rexBatchCalls(t, s.cmds(t)[2]) {
		p := c.Params
		if c.Method == "session.move_block" {
			got = append(got, fmt.Sprintf("move %s beside %s %s %s %.3f", short(p["block_id"]), short(p["anchor_block_id"]), p["direction"], p["side"], p["ratio"]))
		} else {
			got = append(got, c.Method+" "+short(p["block_id"]))
		}
	}
	a, y, z, x := short(rexA), short(rexY), short(rexZ), short(rexX)
	want := []string{
		"session.detach_block " + a, "session.detach_block " + z, "session.detach_block " + x,
		"move " + a + " beside " + y + " horizontal after 0.500", // Y | rest
		"move " + z + " beside " + a + " vertical after 0.333",   // A over (Z over X)
		"move " + x + " beside " + z + " vertical after 0.500",
		"session.focus_block " + x,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}

// Break: a placeholder window, the block moved in beside it, the placeholder closed, and the new
// window moved right after the old one.
func TestRexBreakPane(t *testing.T) {
	s := &rexStub{replies: []rexReply{{out: rexListJSON}, {out: rexViewJSON}, {out: rexWindowJSON}}}
	if err := newRexStub(s).BreakPane(&Pane{Native: rexZ}); err != nil {
		t.Fatal(err)
	}
	cmds := s.cmds(t)
	if !reflect.DeepEqual(cmds[2], []string{"window", "new", "--focus=false", "--json", "-s", rexS, "--shell=none", "--", "sleep", "60"}) {
		t.Fatal(cmds[2])
	}
	calls := rexBatchCalls(t, cmds[3])
	ph := "block:01a1dff5-3811-71d8-a809-ab2ddc334c04"
	if len(calls) != 3 || calls[0].Params["block_id"] != "block:"+rexZ || calls[0].Params["anchor_block_id"] != ph ||
		calls[1].Method != "block.close" || calls[1].Params["block_id"] != ph ||
		calls[2].Params["window_id"] != "window:01a1dff5-3835-7d3a-837d-4879206efc01" || calls[2].Params["before_window_id"] != "window:01a1dffb-0000-7000-8000-0000009c8450" {
		t.Fatal(calls)
	}
}

func TestRexMoveAndSwap(t *testing.T) {
	s := &rexStub{replies: []rexReply{{out: rexListJSON}, {}, {out: rexViewJSON}}}
	b := newRexStub(s)
	if err := b.MovePane(&Pane{Native: rexX}, &Pane{Native: rexY, Width: 50, Height: 40}); err != nil {
		t.Fatal(err)
	}
	cmds := s.cmds(t)
	calls := rexBatchCalls(t, cmds[1])
	if len(calls) != 1 || calls[0].Params["direction"] != "vertical" || calls[0].Params["anchor_block_id"] != "block:"+rexY {
		t.Fatal(calls)
	}
	if len(cmds) != 4 || cmds[2][2] != "session.view" || rexBatchCalls(t, cmds[3])[0].Method != "session.resize_pane" {
		t.Fatal("the anchor's window is not evened out:", cmds)
	}
	s.calls = nil
	if err := b.SwapPanes(&Pane{Native: rexX}, &Pane{Native: rexY}); err != nil {
		t.Fatal(err)
	}
	if got := s.cmds(t); !reflect.DeepEqual(got, [][]string{{"swap", rexX, rexY}}) {
		t.Fatal(got)
	}
	var backend Backend = b
	_, mover := backend.(Mover)
	_, breaker := backend.(Breaker)
	_, arranger := backend.(Arranger)
	_, anchored := backend.(Anchored)
	if !mover || !breaker || !arranger || !anchored {
		t.Fatal("rex moves, breaks out, arranges and splits a given block")
	}
}

// rex capture prints the block's scrollback and then its screen, read from the server's copy (the program
// sees nothing; checked on a private Rex 0.1.0-preview.19 server, 2026-11-18). So a read of more lines than
// the pane's rows reaches into the scrollback, and the history is safe.
func TestRexCaptureReadsTheScrollback(t *testing.T) {
	var buf []string
	for i := 1; i <= 30; i++ {
		buf = append(buf, fmt.Sprintf("line %02d", i))
	}
	s := &rexStub{replies: []rexReply{{out: strings.Join(buf, "\n") + "\n"}, {out: strings.Join(buf, "\n") + "\n"}}}
	b := newRexStub(s)
	p := &Pane{ID: "rex:abcd1234", Backend: "rex", Native: "abcd1234", Rows: 24}
	screen, err := b.Capture(p, p.Rows)
	if err != nil || len(screen) != 24 || screen[0] != "line 07" || screen[23] != "line 30" {
		t.Fatalf("screen: %q %v", screen, err)
	}
	all, err := b.Capture(p, 2000)
	if err != nil || len(all) != 30 || all[0] != "line 01" {
		t.Fatalf("history: %q %v", all, err)
	}
	for _, c := range s.cmds(t) {
		if !reflect.DeepEqual(c, []string{"capture", "-b", "abcd1234", "--trim"}) {
			t.Fatalf("%q", c)
		}
	}
	if !b.HistoryIsSafe(p) {
		t.Fatal("rex capture reads the server's copy: the history is safe")
	}
}
