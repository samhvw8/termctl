package term

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// The JSON in testdata/hc is captured verbatim from hc 0.1.19 on a private core.
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "hc", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type hcReply struct {
	out, stderr string
	code        int
}

type hcStub struct {
	mu       sync.Mutex // ListPanes calls get concurrently
	calls    [][]string
	route    func(args []string) hcReply
	reqs     [][]byte
	rpcReply string // what `hc connect` answers; empty is success
}

func (s *hcStub) rpc(req []byte) ([]byte, error) {
	s.reqs = append(s.reqs, req)
	if s.rpcReply == "" {
		return []byte(`{"id":1,"jsonrpc":"2.0","result":{"started":true}}`), nil
	}
	return []byte(s.rpcReply), nil
}

func (s *hcStub) run(args ...string) (string, int, string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, args)
	s.mu.Unlock()
	var r hcReply
	if s.route != nil {
		r = s.route(args)
	}
	return r.out, r.code, r.stderr, nil
}

func hcBackend(s *hcStub, running bool) *HcBackend {
	return &HcBackend{run: s.run, rpc: s.rpc, probe: func() bool { return running }}
}

func hcPane() *Pane { return &Pane{ID: "hc:t1", Backend: "hc", Native: "t1", Rows: 120} }

func routed(t *testing.T) *hcStub {
	return &hcStub{route: func(args []string) hcReply {
		switch {
		case args[0] == "ls":
			return hcReply{out: fixture(t, "ls.json")}
		case args[0] == "get" && args[1] == "t1":
			return hcReply{out: fixture(t, "get_idle.json")}
		case args[0] == "get" && args[1] == "t2":
			return hcReply{out: fixture(t, "get_plain.json")}
		}
		return hcReply{}
	}}
}

func TestHcListPanesFromCapturedJSON(t *testing.T) {
	t.Setenv("HC_TERMINAL_ID", "")
	t.Setenv("HC_TERMINAL_SOCKET", "")
	s := routed(t)
	panes, err := hcBackend(s, true).ListPanes()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.calls[0], []string{"ls", "--json"}) || len(s.calls) != 3 {
		t.Fatal(s.calls)
	}
	if len(panes) != 2 {
		t.Fatal(panes)
	}
	a := panes[0]
	if a.ID != "hc:t1" || a.Native != "t1" || a.Backend != "hc" || a.Label != "worker7" || a.Process != "fakeharness" || a.Rows != 120 {
		t.Fatalf("%+v", a)
	}
	if a.Title != "fix1" { // get has an empty title: the name stands in
		t.Fatalf("title %q", a.Title)
	}
	if a.Folder != "/private/tmp" { // foreground cwd, not the launch cwd
		t.Fatalf("folder %q", a.Folder)
	}
	if panes[1].Process != "sleep" || panes[1].Folder != "/tmp" { // no foreground: falls back to ls.agent and the launch cwd
		t.Fatalf("%+v", panes[1])
	}
}

func TestHcListPanesEmptyWhenNoCoreRuns(t *testing.T) {
	s := &hcStub{}
	panes, err := hcBackend(s, false).ListPanes()
	if err != nil || panes != nil || len(s.calls) != 0 {
		t.Fatal(panes, err, s.calls)
	}
}

// Any hc command auto-starts a core that keeps the caller's environment. Read-only paths must not call hc at all.
func TestHcReadOnlyCallsNeverStartACore(t *testing.T) {
	s := &hcStub{}
	b := hcBackend(s, false)
	if ref := b.ServerRef(); len(ref) != 0 {
		t.Fatal(ref)
	}
	if st := b.NativeStatus(hcPane()); st != "" {
		t.Fatal(st)
	}
	if _, err := b.Capture(hcPane(), 40); err == nil {
		t.Fatal("capture with no core should fail")
	}
	if err := b.SetLabel(hcPane(), "x"); err == nil {
		t.Fatal("label with no core should fail")
	}
	if err := b.SendKey(hcPane(), "enter"); err == nil {
		t.Fatal("key with no core should fail")
	}
	if len(s.calls) != 0 {
		t.Fatal("hc was run with no core: ", s.calls)
	}
}

// A core started by a spawn would keep the spawning agent's environment for every later terminal.
func TestHcSpawnRefusesWithoutACore(t *testing.T) {
	s := &hcStub{}
	_, err := hcBackend(s, false).Spawn("tab", "fake", "/tmp", "w")
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatal(err)
	}
	if len(s.calls) != 0 {
		t.Fatal("hc was run with no core: ", s.calls)
	}
}

func TestHcAvailableNeedsRunningCore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hc"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if (&HcBackend{run: (&hcStub{}).run, probe: func() bool { return false }}).Available() {
		t.Fatal("no core: not available")
	}
	if !(&HcBackend{run: (&hcStub{}).run, probe: func() bool { return true }}).Available() {
		t.Fatal("core running and hc on PATH: available")
	}
	t.Setenv("PATH", filepath.Join(dir, "empty"))
	if (&HcBackend{run: (&hcStub{}).run, probe: func() bool { return true }}).Available() {
		t.Fatal("no hc on PATH: not available")
	}
}

func TestHcRunningDialsTheSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "x.sock")
	b := NewHc(sock)
	if b.running() {
		t.Fatal("no socket: not running")
	}
	if b.socket() != sock {
		t.Fatal(b.socket())
	}
	if err := os.WriteFile(sock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if b.running() {
		t.Fatal("a stale file nobody listens on is not a running core")
	}
}

func TestHcSocketPathOrder(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := hcDefaultSocket(env(map[string]string{"HC_SOCKET": "/a.sock", "HC_SESSION": "s", "HC_TERMINAL_SOCKET": "/b.sock"})); got != "/a.sock" {
		t.Fatal(got)
	}
	if got := hcDefaultSocket(env(map[string]string{"HC_TERMINAL_SOCKET": "/b.sock", "HC_SESSION": "s"})); got != "/b.sock" {
		t.Fatal(got)
	}
	if got := hcDefaultSocket(env(map[string]string{"XDG_RUNTIME_DIR": "/run", "HC_SESSION": "sess"})); got != "/run/herdr-core/sess.sock" {
		t.Fatal(got)
	}
	if got := hcDefaultSocket(env(map[string]string{"XDG_RUNTIME_DIR": "/run"})); got != "/run/herdr-core/default.sock" {
		t.Fatal(got)
	}
	got := hcDefaultSocket(env(nil))
	if !strings.HasSuffix(got, "/.local/run/herdr-core/default.sock") {
		t.Fatal(got)
	}
}

func TestHcEnvDropsCallerAndHonoursSocket(t *testing.T) {
	in := []string{"PATH=/x", "HC_TERMINAL_ID=t7", "HC_SESSION=s", "HC_SOCKET=/old", "OTHER=1"}
	if got := hcEnv(in, ""); !reflect.DeepEqual(got, []string{"PATH=/x", "HC_SESSION=s", "HC_SOCKET=/old", "OTHER=1"}) {
		t.Fatal(got)
	}
	if got := hcEnv(in, "/p.sock"); !reflect.DeepEqual(got, []string{"PATH=/x", "OTHER=1", "HC_SOCKET=/p.sock"}) {
		t.Fatal(got)
	}
}

// The real subprocess path, with a stand-in hc that records its environment.
func TestHcSubprocessHidesTheCaller(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.txt")
	script := "#!/bin/sh\nenv > " + envFile + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "hc"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("HC_TERMINAL_ID", "t7")
	b := NewHc("")
	b.probe = func() bool { return true }
	if err := b.SendKey(hcPane(), "enter"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "HC_TERMINAL_ID") {
		t.Fatal("hc saw HC_TERMINAL_ID: polling would count as this terminal looking at its children")
	}
}

func TestHcIsSelfOnlyOnItsOwnCore(t *testing.T) {
	s := routed(t)
	b := hcBackend(s, true)
	b.SocketPath = "/run/core.sock"
	t.Setenv("HC_TERMINAL_ID", "t1")
	t.Setenv("HC_TERMINAL_SOCKET", "/run/other.sock") // same address, different core
	panes, _ := b.ListPanes()
	if panes[0].IsSelf {
		t.Fatal("t1 on another core is a different pane")
	}
	t.Setenv("HC_TERMINAL_SOCKET", "/run/core.sock")
	panes, _ = b.ListPanes()
	if !panes[0].IsSelf || panes[1].IsSelf {
		t.Fatalf("self: %v %v", panes[0].IsSelf, panes[1].IsSelf)
	}
}

func TestHcSpawnArgvAndAddress(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	s := &hcStub{route: func(args []string) hcReply {
		if args[0] == "run" {
			return hcReply{out: fixture(t, "run.json")}
		}
		return hcReply{out: fixture(t, "get_idle.json")}
	}}
	p, err := hcBackend(s, true).Spawn("tab", "omp --flag", "/w", "My Worker! #2")
	if err != nil || p.ID != "hc:t1" || p.Native != "t1" {
		t.Fatal(p, err)
	}
	run := hcRunCall(t, s)
	nameAt := -1
	for i, a := range run {
		if a == "--name" {
			nameAt = i + 1
		}
	}
	if nameAt < 0 || !regexp.MustCompile(`^[a-z0-9_-]{1,32}$`).MatchString(run[nameAt]) || !strings.HasPrefix(run[nameAt], "my-worker-2-") {
		t.Fatal(run)
	}
	want := []string{"run", "--bg", "--json", "--name", run[nameAt], "--no-parent", "--outlive-parent", "--no-notify", "--label", "herdr.space=" + hcSpaceOf(t, run), "--cwd", "/w", "--",
		"/bin/zsh", "-lic", "unset " + hcTerminalVars + "; omp --flag"}
	if !reflect.DeepEqual(run, want) {
		t.Fatalf("%q", run)
	}
}

func TestHcSpawnWithoutCommandAndValidatesWhere(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	s := &hcStub{route: func(args []string) hcReply {
		if args[0] == "run" {
			return hcReply{out: fixture(t, "run.json")}
		}
		return hcReply{out: fixture(t, "get_idle.json")}
	}}
	b := hcBackend(s, true)
	if _, err := b.Spawn("nowhere", "x", "", ""); err == nil {
		t.Fatal("bad where must be refused")
	}
	if len(s.calls) != 0 {
		t.Fatal(s.calls)
	}
	if _, err := b.Spawn("split", "", "", ""); err != nil {
		t.Fatal(err)
	}
	run := hcRunCall(t, s)
	script := run[len(run)-1]
	if !strings.Contains(script, `exec "${SHELL:-/bin/sh}" -l`) || !strings.Contains(script, "unset ITERM_SESSION_ID") {
		t.Fatal(script)
	}
	if !strings.HasPrefix(run[4], "termctl-") {
		t.Fatal(run)
	}
}

func TestHcName(t *testing.T) {
	re := regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	for _, in := range []string{"", "///", strings.Repeat("Long Title ", 10), "-weird_", "Ünïcode"} {
		if got := hcName(in); !re.MatchString(got) {
			t.Fatalf("%q: %q", in, got)
		}
	}
	if hcName("same") == hcName("same") {
		t.Fatal("names must differ")
	}
}

func TestHcProcessMapsAgents(t *testing.T) {
	cases := []struct {
		fg    *hcForeground
		agent string
		want  string
	}{
		{&hcForeground{Name: "claude", Argv: []string{"/x/claude"}}, "zsh", "claude"},
		{&hcForeground{Name: "omp", Argv: []string{"/x/omp"}}, "zsh", "omp"},
		{&hcForeground{Name: "bun", Argv: []string{"/x/bun", "/y/omp"}}, "zsh", "omp"},
		{&hcForeground{Name: "node", Argv: []string{"/x/node", "/y/server.js"}}, "zsh", "node"},
		{nil, "sleep", "sleep"},
	}
	for _, c := range cases {
		if got := hcProcess(c.fg, c.agent); got != c.want {
			t.Fatalf("%+v: %q", c, got)
		}
	}
}

func TestHcClaudeFixtureProcess(t *testing.T) {
	s := &hcStub{route: func(args []string) hcReply {
		if args[0] == "ls" {
			return hcReply{out: `{"terminals":[{"address":"t3","agent":"zsh"}]}`}
		}
		return hcReply{out: fixture(t, "get_claude.json")}
	}}
	panes, err := hcBackend(s, true).ListPanes()
	if err != nil || panes[0].Process != "claude" || panes[0].Title != "Claude Code" {
		t.Fatalf("%+v %v", panes[0], err)
	}
}

func TestHcSendTextRefusesLoudly(t *testing.T) {
	s := &hcStub{}
	err := hcBackend(s, true).SendText(hcPane(), "hello")
	if code(err) != CodeBackend || !strings.Contains(err.Error(), "always submits") || len(s.calls) != 0 {
		t.Fatal(err, s.calls)
	}
	if err := hcBackend(s, true).SendText(hcPane(), "a\nb"); code(err) != CodeUsage {
		t.Fatal(err)
	}
}

// A cross-machine send with a caller arrives as <hc from="mac/ppid:N">. The API's caller is optional; the CLI always fills it.
func TestHcSubmitTextSendsNoCaller(t *testing.T) {
	s := &hcStub{}
	b := hcBackend(s, true)
	remote := &Pane{ID: "hc:homebox/t7", Backend: "hc", Native: "homebox/t7"}
	if err := b.SubmitText(remote, "-looks like a flag", ""); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 0 || len(s.reqs) != 1 {
		t.Fatal(s.calls, s.reqs)
	}
	var req struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(s.reqs[0], &req); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"terminal": "homebox/t7", "text": "-looks like a flag"}
	if req.Method != "terminal.send" || !reflect.DeepEqual(req.Params, want) {
		t.Fatalf("%+v", req)
	}
	if err := b.SubmitText(hcPane(), "a\nb", ""); code(err) != CodeUsage {
		t.Fatal(err)
	}
}

// hc shows <hc from="..."> only for a caller with a slash, so an agent message names one.
func TestHcSubmitTextNamesTheSender(t *testing.T) {
	s := &hcStub{}
	if err := hcBackend(s, true).SubmitText(hcPane(), "hi", "iterm2/F1B64CB3"); err != nil {
		t.Fatal(err)
	}
	var req struct {
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(s.reqs[0], &req); err != nil {
		t.Fatal(err)
	}
	if req.Params["caller"] != "iterm2/F1B64CB3" {
		t.Fatalf("%+v", req.Params)
	}
}

func TestHcSubmitTextNeedsACore(t *testing.T) {
	s := &hcStub{}
	if err := hcBackend(s, false).SubmitText(hcPane(), "x", ""); code(err) != CodeBackend || len(s.reqs) != 0 {
		t.Fatal(err, s.reqs)
	}
}

// The shape is captured from `hc connect` 0.1.19.
func TestHcSubmitRefusalsMapToExitCodes(t *testing.T) {
	reply := func(kind, msg string) string {
		return `{"error":{"code":-32000,"data":{"hint":"h","kind":"` + kind + `"},"message":"` + msg + `"},"id":1,"jsonrpc":"2.0"}`
	}
	cases := []struct {
		reply string
		code  int
		text  string
	}{
		{reply("blocked", "t1 is blocked"), CodeGuard, "waiting for a person"},
		{reply("not_found", "no terminal"), CodeUsage, "no terminal"},
		{reply("finished", "t1 finished"), CodeUsage, "finished"},
		{reply("unavailable", "odd"), CodeBackend, "odd"},
		{"not json", CodeBackend, "bad reply"},
	}
	for _, c := range cases {
		err := hcBackend(&hcStub{rpcReply: c.reply}, true).SubmitText(hcPane(), "x", "")
		if code(err) != c.code || !strings.Contains(err.Error(), c.text) {
			t.Fatalf("%+v: %v", c, err)
		}
	}
}

func TestHcRefusalsMapToExitCodes(t *testing.T) {
	cases := []struct {
		reply hcReply
		code  int
		text  string
	}{
		{hcReply{stderr: fixture(t, "err_blocked.json"), code: 9}, CodeGuard, "waiting for a person"},
		{hcReply{stderr: fixture(t, "err_notfound.json"), code: 3}, CodeUsage, "no terminal"},
		{hcReply{stderr: fixture(t, "err_finished.json"), code: 11}, CodeUsage, "finished"},
		{hcReply{stderr: `{"error":{"message":"odd","kind":"usage"}}`, code: 2}, CodeBackend, "odd"},
		{hcReply{code: 1}, CodeBackend, "exit 1"},
	}
	for _, c := range cases {
		r := c.reply
		s := &hcStub{route: func([]string) hcReply { return r }}
		err := hcBackend(s, true).SendKey(hcPane(), "enter")
		if code(err) != c.code || !strings.Contains(err.Error(), c.text) {
			t.Fatalf("%+v: %v", c, err)
		}
	}
}

func TestHcSendKeyMapping(t *testing.T) {
	s := &hcStub{}
	b := hcBackend(s, true)
	b.SendKey(hcPane(), "escape")
	b.SendKey(hcPane(), "ctrl-x")
	b.SendKey(hcPane(), "enter")
	want := [][]string{{"keys", "--json", "t1", "esc"}, {"keys", "--json", "t1", "ctrl+x"}, {"keys", "--json", "t1", "enter"}}
	if !reflect.DeepEqual(s.calls, want) {
		t.Fatal(s.calls)
	}
	if err := b.SendKey(hcPane(), "nonsense"); code(err) != CodeUsage {
		t.Fatal(err)
	}
}

func TestHcCaptureReadsScreenWhenItFits(t *testing.T) {
	s := &hcStub{route: func([]string) hcReply { return hcReply{out: fixture(t, "read_screen.txt")} }}
	b := hcBackend(s, true)
	got, err := b.Capture(hcPane(), 120)
	if err != nil || !reflect.DeepEqual(s.calls[0], []string{"read", "t1", "--lines", "0"}) {
		t.Fatal(s.calls, err)
	}
	if !reflect.DeepEqual(got, []string{"fake harness ready", ">"}) {
		t.Fatalf("%q", got)
	}
	s.calls = nil
	if _, err := b.Capture(hcPane(), 200); err != nil || !reflect.DeepEqual(s.calls[0], []string{"read", "t1", "--lines", "80"}) {
		t.Fatal(s.calls, err)
	}
}

func TestHcCaptureAsksHeightWhenUnknown(t *testing.T) {
	s := &hcStub{route: func(args []string) hcReply {
		if args[0] == "get" {
			return hcReply{out: fixture(t, "get_plain.json")}
		}
		return hcReply{out: "x\n"}
	}}
	p := hcPane()
	p.Rows = 0
	if _, err := hcBackend(s, true).Capture(p, 40); err != nil || len(s.calls) != 2 || s.calls[1][3] != "0" {
		t.Fatal(s.calls, err)
	}
}

func TestHcCloseKillsThenRemoves(t *testing.T) {
	s := &hcStub{}
	if err := hcBackend(s, true).Close(hcPane()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.calls, [][]string{{"kill", "--json", "t1"}, {"rm", "--json", "t1"}}) {
		t.Fatal(s.calls)
	}
	// Finished already, then gone: both are success.
	s = &hcStub{route: func(args []string) hcReply {
		if args[0] == "kill" {
			return hcReply{code: 11, stderr: fixture(t, "err_finished.json")}
		}
		return hcReply{code: 3, stderr: fixture(t, "err_notfound.json")}
	}}
	if err := hcBackend(s, true).Close(hcPane()); err != nil {
		t.Fatal(err)
	}
	s = &hcStub{route: func([]string) hcReply { return hcReply{code: 1, stderr: `{"error":{"message":"odd"}}`} }}
	if err := hcBackend(s, true).Close(hcPane()); code(err) != CodeBackend {
		t.Fatal(err)
	}
}

func TestHcSetLabel(t *testing.T) {
	s := &hcStub{}
	if err := hcBackend(s, true).SetLabel(hcPane(), "worker7"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.calls[0], []string{"label", "--json", "t1", "termctl_label=worker7"}) {
		t.Fatal(s.calls[0])
	}
}

func TestHcNativeStatusFromCapturedJSON(t *testing.T) {
	for name, want := range map[string]string{"get_idle.json": "idle", "get_blocked.json": "blocked", "get_plain.json": ""} {
		s := &hcStub{route: func([]string) hcReply { return hcReply{out: fixture(t, name)} }}
		if got := hcBackend(s, true).NativeStatus(hcPane()); got != want {
			t.Fatalf("%s: %q", name, got)
		}
	}
	s := &hcStub{route: func([]string) hcReply { return hcReply{code: 3, stderr: fixture(t, "err_notfound.json")} }}
	if got := hcBackend(s, true).NativeStatus(hcPane()); got != "" {
		t.Fatal(got)
	}
}

func TestHcServerRefFromCapturedJSON(t *testing.T) {
	s := &hcStub{route: func([]string) hcReply { return hcReply{out: fixture(t, "status.json")} }}
	b := hcBackend(s, true)
	b.SocketPath = "/run/s.sock"
	ref := b.ServerRef()
	if ref["core_id"] != "ad8c2fbb4925a62b1d375910cc42cc33" || ref["pid"] != "51368" || ref["socket_path"] != "/run/s.sock" {
		t.Fatal(ref)
	}
	if !reflect.DeepEqual(s.calls[0], []string{"server", "status", "--json"}) {
		t.Fatal(s.calls[0])
	}
}

func TestHcErrorMessageFallsBack(t *testing.T) {
	if got := hcErrMessage("plain failure\n"); got != "plain failure" {
		t.Fatal(got)
	}
	var e *Error
	if err := BackendErr("x"); !errors.As(err, &e) {
		t.Fatal(err)
	}
}

func TestHcUnparseableJSONFailsLoudly(t *testing.T) {
	s := &hcStub{route: func([]string) hcReply { return hcReply{out: "{"} }}
	if _, err := hcBackend(s, true).ListPanes(); code(err) != CodeBackend {
		t.Fatal(err)
	}
	if _, err := hcBackend(s, true).Spawn("tab", "x", "", ""); code(err) != CodeBackend {
		t.Fatal(err)
	}
}

func TestHcSpawnFailsWhenNoAddress(t *testing.T) {
	s := &hcStub{route: func(args []string) hcReply {
		if args[0] == "run" {
			return hcReply{out: `{"agent":"zsh"}`}
		}
		return hcReply{out: `{"terminals":[]}`}
	}}
	if _, err := hcBackend(s, true).Spawn("tab", "x", "", ""); err == nil || !strings.Contains(err.Error(), "no address") {
		t.Fatal(err)
	}
}

func TestHcMakeWiresSocket(t *testing.T) {
	b, err := Make("hc", "", "/x/y.sock")
	if err != nil {
		t.Fatal(err)
	}
	h, ok := b.(*HcBackend)
	if !ok || h.SocketPath != "/x/y.sock" || b.Name() != "hc" {
		t.Fatal(b)
	}
	if !IsBackend("hc") {
		t.Fatal("hc not listed")
	}
	if _, ok := b.(Submitter); !ok {
		t.Fatal("hc must be a Submitter")
	}
}

// hcRunCall is the `hc run` call a spawn made.
func hcRunCall(t *testing.T, s *hcStub) []string {
	t.Helper()
	for _, c := range s.calls {
		if c[0] == "run" {
			return c
		}
	}
	t.Fatalf("no hc run in %q", s.calls)
	return nil
}

// hcSpaceOf is the herdr.space label on an `hc run` call.
func hcSpaceOf(t *testing.T, run []string) string {
	t.Helper()
	for i, a := range run {
		if a == "--" {
			break
		}
		if a == "--label" && strings.HasPrefix(run[i+1], "herdr.space=") {
			return strings.TrimPrefix(run[i+1], "herdr.space=")
		}
	}
	t.Fatalf("no herdr.space label in %q", run)
	return ""
}

// hcSpaceStub answers a spawn. terminals is the `hc ls` result; labels gives each address's labels.
func hcSpaceStub(t *testing.T, terminals string, labels map[string]string) *hcStub {
	return &hcStub{route: func(args []string) hcReply {
		switch args[0] {
		case "run":
			return hcReply{out: fixture(t, "run.json")}
		case "ls":
			return hcReply{out: `{"terminals":[` + terminals + `]}`}
		case "get":
			if l, ok := labels[args[1]]; ok {
				return hcReply{out: `{"address":"` + args[1] + `","labels":{` + l + `}}`}
			}
			return hcReply{out: fixture(t, "get_idle.json")}
		}
		return hcReply{}
	}}
}

func TestHcSpawnJoinsTheCallersSpace(t *testing.T) {
	t.Setenv("HC_SOCKET", "")
	t.Setenv("HC_TERMINAL_ID", "t9")
	t.Setenv("HC_TERMINAL_SOCKET", "/run/core.sock")
	s := hcSpaceStub(t, "", map[string]string{"t9": `"herdr.space":"spCaller","herdr.tab":"tbCaller"`})
	if _, err := hcBackend(s, true).Spawn("tab", "x", "/w", "w"); err != nil {
		t.Fatal(err)
	}
	run := hcRunCall(t, s)
	if got := hcSpaceOf(t, run); got != "spCaller" {
		t.Fatalf("space %q", got)
	}
	if strings.Contains(strings.Join(run, " "), "herdr.tab=") {
		t.Fatalf("a herdr.tab label would stack it onto the caller's tab: %q", run)
	}
}

func TestHcSpawnFromOutsideGetsOneSpacePerFolder(t *testing.T) {
	t.Setenv("HC_TERMINAL_ID", "")
	spawn := func(cwd string) string {
		s := hcSpaceStub(t, "", nil)
		if _, err := hcBackend(s, true).Spawn("tab", "x", cwd, "w"); err != nil {
			t.Fatal(err)
		}
		return hcSpaceOf(t, hcRunCall(t, s))
	}
	first := spawn("/w/one")
	if !regexp.MustCompile(`^sp[0-9a-f]{32}$`).MatchString(first) {
		t.Fatalf("space id %q", first)
	}
	if spawn("/w/one") != first {
		t.Fatal("the same folder must land in the same space")
	}
	if spawn("/w/two") == first {
		t.Fatal("another folder must get another space")
	}
}

func TestHcSpawnReusesTheFoldersSpace(t *testing.T) {
	t.Setenv("HC_TERMINAL_ID", "")
	s := hcSpaceStub(t,
		`{"address":"homebox/t1","cwd":"/w"},{"address":"t4","cwd":"/other"},{"address":"t5","cwd":"/w"},{"address":"t6","cwd":"/w"}`,
		map[string]string{"t4": `"herdr.space":"spOther"`, "t5": `"termctl_label":"x"`, "t6": `"herdr.space":"spExisting"`})
	if _, err := hcBackend(s, true).Spawn("tab", "x", "/w", "w"); err != nil {
		t.Fatal(err)
	}
	if got := hcSpaceOf(t, hcRunCall(t, s)); got != "spExisting" {
		t.Fatalf("space %q", got)
	}
	for _, c := range s.calls {
		if c[0] == "get" && strings.Contains(c[1], "homebox/") {
			t.Fatalf("a remote terminal's labels name that machine's spaces: %q", c)
		}
	}
}

func TestHcSpawnCallerWithoutASpaceFallsBackToTheFolder(t *testing.T) {
	t.Setenv("HC_SOCKET", "")
	t.Setenv("HC_TERMINAL_ID", "t9")
	t.Setenv("HC_TERMINAL_SOCKET", "/run/core.sock")
	s := hcSpaceStub(t, "", map[string]string{"t9": `"termctl_label":"x"`})
	if _, err := hcBackend(s, true).Spawn("tab", "x", "/w/one", "w"); err != nil {
		t.Fatal(err)
	}
	if got := hcSpaceOf(t, hcRunCall(t, s)); !strings.HasPrefix(got, "sp") || len(got) != 34 {
		t.Fatalf("space %q", got)
	}
}

// A shell in a Herdr terminal can export XDG_RUNTIME_DIR, and hc then looks for the wrong socket. The
// terminal's own socket is the right one, unless the person named another.
func TestHcEnvUsesTheTerminalsSocket(t *testing.T) {
	has := func(env []string, want string) bool {
		for _, kv := range env {
			if kv == want {
				return true
			}
		}
		return false
	}
	in := []string{"PATH=/x", "HC_TERMINAL_ID=t7", "HC_TERMINAL_SOCKET=/term.sock", "XDG_RUNTIME_DIR=/run"}
	if got := hcEnv(in, ""); !has(got, "HC_SOCKET=/term.sock") {
		t.Fatal(got)
	}
	named := append(append([]string{}, in...), "HC_SOCKET=/named.sock")
	if got := hcEnv(named, ""); has(got, "HC_SOCKET=/term.sock") || !has(got, "HC_SOCKET=/named.sock") {
		t.Fatal(got)
	}
	if got := hcEnv(in, "/p.sock"); has(got, "HC_SOCKET=/term.sock") || !has(got, "HC_SOCKET=/p.sock") {
		t.Fatal(got)
	}
}
