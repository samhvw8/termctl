package term

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// RexBackend drives Rex (superlogical.com/rex) through its CLI. Checked against Rex 0.1.0-preview.19
// on a private server (`mise run test:rex`). Rex terms: a session holds windows (Rex.app shows them
// as tabs), a window holds blocks (panes). Pane.Native is the block's UUID, Pane.Tab its window's.
//
//   - The CLI is found through REX_BIN_DIR (Rex sets it in its own terminals), then PATH, then the
//     app bundle, because the Rex app does not put it on PATH.
//   - Every call names its server (-S) and passes --autostart=false. Rex sets no server variable in
//     its terminals, and a bare rex looks at $XDG_RUNTIME_DIR/rex/server.sock first, which is not the
//     app's server: it would start a second, empty server there. See server.
//   - ls, view and layout changes run as one Lua script each (rex do), not one CLI call per block:
//     each CLI call costs about 180 ms.
type RexBackend struct {
	run    func(args ...string) (stdout string, code int, stderr string, err error) // the CLI, without -S
	getenv func(string) string
	isSock func(path string) bool
	addr   *string           // the resolved -S value; "" when no server listens
	blocks map[string]string // block UUID -> session ID, from the last ListPanes
}

const (
	rexExitTarget   = 3 // a block, window or session target that does not exist
	rexExitNoServer = 4
	rexTerminal     = "com.superlogical.terminal"
	// rexAppCLI is where the Rex app keeps its CLI, below the home directory.
	rexAppCLI = "Library/Application Support/com.superlogical.rex/RexServer/Versions/Current/Rex Server.app/Contents/MacOS/rex"
)

func NewRex() *RexBackend {
	b := &RexBackend{getenv: os.Getenv, isSock: isSocket}
	b.run = func(args ...string) (string, int, string, error) {
		bin := rexBin(b.getenv)
		if bin == "" {
			return "", 0, "", errors.New("rex not found")
		}
		cmd, finish := cliCommand(bin, args...)
		// Every call names its blocks, windows and sessions by ID. With REX_SESSION set, the CLI can
		// look IDs up in that session only, and miss a block of another (a new session's, for one).
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "REX_SESSION=") && !strings.HasPrefix(kv, "REX_BLOCK=") && !strings.HasPrefix(kv, "REX_CLIENT=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		err = finish(err)
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return string(out), exit.ExitCode(), stderr.String(), nil
		}
		return string(out), 0, stderr.String(), err
	}
	return b
}

func isSocket(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode()&os.ModeSocket != 0
}

// rexBin finds the CLI: REX_BIN_DIR, which Rex sets in its terminals, then PATH, then the app bundle.
func rexBin(getenv func(string) string) string {
	if d := getenv("REX_BIN_DIR"); d != "" {
		if p := filepath.Join(d, "rex"); isFile(p) {
			return p
		}
	}
	if p, err := exec.LookPath("rex"); err == nil {
		return p
	}
	if home := getenv("HOME"); home != "" {
		if p := filepath.Join(home, rexAppCLI); isFile(p) {
			return p
		}
	}
	return ""
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func (b *RexBackend) Name() string { return "rex" }

func (b *RexBackend) Available() bool { return rexBin(b.env) != "" }

func (b *RexBackend) env(k string) string {
	if b.getenv == nil {
		return os.Getenv(k)
	}
	return b.getenv(k)
}

// rexSockets lists the sockets a local Rex server listens on, in the order the CLI tries them:
// $REX_RUNTIME_DIR/server.sock, $XDG_RUNTIME_DIR/rex/server.sock, then the app's
// ~/Library/Application Support/rex/server.sock.
func rexSockets(getenv func(string) string) []string {
	var out []string
	if d := getenv("REX_RUNTIME_DIR"); d != "" {
		out = append(out, filepath.Join(d, "server.sock"))
	}
	if d := getenv("XDG_RUNTIME_DIR"); d != "" {
		out = append(out, filepath.Join(d, "rex", "server.sock"))
	}
	if home := getenv("HOME"); home != "" {
		out = append(out, filepath.Join(home, "Library", "Application Support", "rex", "server.sock"))
	}
	return out
}

// server picks the Rex server to talk to: REX_SERVER when set; otherwise, of the sockets that
// exist, the one whose server holds this terminal's session (REX_SESSION), or the first that
// answers. Rex sets no server variable in its terminals, and the app's server is not where a bare
// rex looks first when XDG_RUNTIME_DIR is set, hence the search. "" means no server listens.
func (b *RexBackend) server() string {
	if b.addr != nil {
		return *b.addr
	}
	addr := b.findServer()
	b.addr = &addr
	return addr
}

func (b *RexBackend) findServer() string {
	if s := b.env("REX_SERVER"); s != "" {
		return s
	}
	isSock := b.isSock
	if isSock == nil {
		isSock = isSocket
	}
	var live []string
	seen := map[string]bool{}
	for _, s := range rexSockets(b.env) {
		if !seen[s] && isSock(s) {
			live = append(live, "unix://"+s)
		}
		seen[s] = true
	}
	if len(live) < 2 {
		return strings.Join(live, "")
	}
	want := b.env("REX_SESSION")
	first := ""
	for _, addr := range live {
		out, code, _, err := b.run("-S", addr, "--autostart=false", "ls", "-q", "--short=false")
		if err != nil || code != 0 {
			continue
		}
		if want == "" {
			return addr
		}
		if first == "" {
			first = addr
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) == want {
				return addr
			}
		}
	}
	return first
}

func (b *RexBackend) call(args ...string) (string, error) {
	addr := b.server()
	if addr == "" {
		return "", BackendErr("no Rex server is running (looked in %s); open Rex, or set REX_SERVER", strings.Join(rexSockets(b.env), ", "))
	}
	out, code, stderr, err := b.run(append([]string{"-S", addr, "--autostart=false"}, args...)...)
	if errors.Is(err, errNoAnswer) {
		return "", BackendErr("rex %s: %v", args[0], err)
	}
	if err != nil {
		return "", BackendErr("rex CLI not found: install Rex, or set REX_BIN_DIR to the folder that holds it")
	}
	msg := strings.TrimPrefix(strings.TrimSpace(stderr), "Error: ")
	switch code {
	case 0:
		return out, nil
	case rexExitTarget:
		return "", TargetErr("rex: %s", msg)
	case rexExitNoServer:
		return "", BackendErr("rex: %s", msg)
	}
	return "", BackendErr("rex %s: exit %d: %s", args[0], code, msg)
}

// rexRow is one block as rexListLua reports it.
type rexRow struct {
	Session, Window, Block, Label, Title, Name, Path, Argv0, Cwd string
	Cols, Rows                                                   int
}

// rexListLua lists every block of every session, in window order and then layout order, with what
// ls shows; detached blocks come last, with no window. A session must be attached before its view
// and its blocks' methods answer.
const rexListLua = `
local function term(sid, bid, m)
  return rex.call("block.method", {session_id = sid, block_id = bid, method = m}) or {}
end
local out = {}
for _, s in ipairs((rex.call("session.list", {}) or {}).sessions or {}) do
  local sid = s.session_id
  rex.call("session.attach", {session_id = sid})
  local v = rex.call("session.view", {session_id = sid}) or {}
  local seen = {}
  local function add(b, win)
    if seen[b.block_id] then return end
    seen[b.block_id] = true
    local row = {session = sid, window = win or "", block = b.block_id, label = b.label or ""}
    if b.creator_name == "com.superlogical.terminal" then
      local fg = term(sid, b.block_id, "process").foreground or {}
      local sz = term(sid, b.block_id, "size")
      row.title = term(sid, b.block_id, "title").title or ""
      row.name, row.path, row.argv0, row.cwd = fg.name or "", fg.path or "", fg.argv0 or "", fg.cwd or ""
      row.cols, row.rows = sz.columns or 0, sz.rows or 0
    end
    table.insert(out, row)
  end
  for _, w in ipairs(v.windows or {}) do
    for _, layer in ipairs(w.layers or {}) do
      for _, b in ipairs(layer.blocks or {}) do add(b, w.window_id) end
    end
  end
  for _, b in ipairs((rex.call("session.list_blocks", {session_id = sid}) or {}).blocks or {}) do add(b, nil) end
end
return out
`

// rexID drops the type prefix of a Rex ID: block:<uuid> and window:<uuid> become <uuid>, which
// every rex command accepts.
func rexID(id string) string {
	if _, uuid, ok := strings.Cut(id, ":"); ok {
		return uuid
	}
	return id
}

// rexTabs names each window as Rex's own short IDs do: the last 6 hex digits of its UUID (rex
// window ls shows them, and every rex command accepts them). The full UUIDs are time-ordered, so
// their starts look alike. Should two windows share 6 digits, all names grow until they differ.
func rexTabs(rows []rexRow) map[string]string {
	hex := map[string]string{} // window ID -> the hex digits of its UUID
	for _, r := range rows {
		if r.Window != "" {
			hex[r.Window] = strings.ReplaceAll(rexID(r.Window), "-", "")
		}
	}
	for n := 6; ; n++ {
		out, seen, unique := map[string]string{}, map[string]bool{}, true
		for id, h := range hex {
			s := h[max(0, len(h)-n):]
			unique = unique && !seen[s]
			seen[s], out[id] = true, s
		}
		if unique || n >= 32 {
			return out
		}
	}
}

// rexProcess names the program on a block: the foreground name, or a known agent that an
// interpreter runs, by its path or argv[0].
func rexProcess(r rexRow) string {
	for _, n := range MainProcesses {
		for _, s := range []string{r.Name, filepath.Base(r.Path), filepath.Base(strings.TrimPrefix(r.Argv0, "-"))} {
			if s == n {
				return n
			}
		}
	}
	return r.Name
}

func (b *RexBackend) ListPanes() ([]*Pane, error) {
	if b.server() == "" {
		return nil, nil // nothing to list; never start a server to find that out
	}
	out, err := b.call("do", "-e", rexListLua, "--json")
	if err != nil {
		return nil, err
	}
	var rows []rexRow
	if t := strings.TrimSpace(out); t != "" && t != "{}" && t != "null" { // Lua gives {} for an empty list
		if err := json.Unmarshal([]byte(t), &rows); err != nil {
			return nil, BackendErr("rex ls: bad JSON: %v", err)
		}
	}
	me := rexID(b.env("REX_BLOCK"))
	tabs := rexTabs(rows)
	b.blocks = map[string]string{}
	panes := make([]*Pane, 0, len(rows))
	for _, r := range rows {
		native := rexID(r.Block)
		b.blocks[native] = r.Session
		panes = append(panes, &Pane{
			ID: "rex:" + native, Backend: "rex", Native: native, Title: CleanTitle(r.Title), Label: r.Label,
			Process: rexProcess(r), Folder: Tilde(r.Cwd), IsSelf: me != "" && native == me,
			Rows: r.Rows, Width: r.Cols, Height: r.Rows, Tab: tabs[r.Window],
		})
	}
	return panes, nil
}

func (b *RexBackend) SelfPane() (*Pane, error) {
	if b.env("REX_BLOCK") == "" {
		return nil, nil
	}
	panes, err := b.ListPanes()
	if err != nil {
		return nil, err
	}
	for _, p := range panes {
		if p.IsSelf {
			return p, nil
		}
	}
	return nil, nil
}

// session returns the session that holds a block.
func (b *RexBackend) session(native string) (string, error) {
	if s, ok := b.blocks[native]; ok {
		return s, nil
	}
	if _, err := b.ListPanes(); err != nil {
		return "", err
	}
	if s, ok := b.blocks[native]; ok {
		return s, nil
	}
	return "", TargetErr("rex has no block %s", native)
}

// rexCreated is the result of rex new, run, split and window new: new and session.create nest the
// window, the others give it (or the anchor) at the top.
type rexCreated struct {
	SessionID      string   `json:"session_id"`
	WindowID       string   `json:"window_id"`
	BlockIDs       []string `json:"block_ids"`
	InitialWindows []struct {
		WindowID string   `json:"window_id"`
		BlockIDs []string `json:"block_ids"`
	} `json:"initial_windows"`
}

// block is the new block's UUID, or "".
func (c rexCreated) block() string {
	ids := c.BlockIDs
	if len(ids) == 0 && len(c.InitialWindows) > 0 {
		ids = c.InitialWindows[0].BlockIDs
	}
	if len(ids) == 0 {
		return ""
	}
	return rexID(ids[0])
}

func (c rexCreated) window() string {
	if c.WindowID == "" && len(c.InitialWindows) > 0 {
		return rexID(c.InitialWindows[0].WindowID)
	}
	return rexID(c.WindowID)
}

func (b *RexBackend) Spawn(where, cmd, cwd, title string) (*Pane, error) {
	return b.SpawnAt(nil, where, cmd, cwd, title)
}

// SpawnAt splits anchor (beside it or below it, by its long side), or opens a window right after
// anchor's window, in anchor's session. A nil anchor means the caller's block. "window" opens a new
// session. The new block is labelled with title. Focus stays where it was, except that Rex makes a
// new session's window active.
func (b *RexBackend) SpawnAt(at *Pane, where, cmd, cwd, title string) (*Pane, error) {
	if err := CheckWhere(where); err != nil {
		return nil, err
	}
	if where == "auto" {
		where, at = PlaceFor(b, where, nil)
	}
	if at == nil && where != "window" {
		me, err := b.SelfPane()
		if err != nil {
			return nil, err
		}
		at = me
	}
	var argv []string
	switch where {
	case "split":
		if at == nil {
			return nil, UsageErr("split needs a block to split: run inside Rex, or name one")
		}
		dir := "below"
		if SideBySide(at.Width, at.Height) {
			dir = "right"
		}
		argv = []string{"split", "--focus=false", "--json", "-b", at.Native, "--split", dir}
	case "tab":
		if at == nil {
			return nil, UsageErr("a new window needs a session: run inside Rex, or use --where window")
		}
		sid, err := b.session(at.Native)
		if err != nil {
			return nil, err
		}
		argv = []string{"window", "new", "--focus=false", "--json", "-s", sid}
		if title != "" {
			argv = append(argv, title) // the window's label, which Rex.app shows on the tab
		}
	default:
		argv = []string{"new", "--json"}
		if title != "" {
			argv = append(argv, title)
		}
	}
	if cwd != "" {
		argv = append(argv, "-c", cwd)
	}
	if cmd != "" {
		shell := b.env("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		argv = append(argv, "--shell=none", "--", shell, "-lic", cmd) // as iTerm2 does: mise and PATH load
	}
	out, err := b.call(argv...)
	if err != nil {
		return nil, err
	}
	var c rexCreated
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		return nil, BackendErr("rex %s: bad JSON: %v", argv[0], err)
	}
	native := c.block()
	if native == "" {
		return nil, BackendErr("rex %s: the result names no block: %s", argv[0], strings.TrimSpace(out))
	}
	p := &Pane{ID: "rex:" + native, Backend: "rex", Native: native, Title: title, Tab: c.window()}
	if title != "" {
		if err := b.SetLabel(p, title); err != nil {
			return p, err
		}
		p.Label = title
	}
	switch {
	case where == "split":
		p.Tab = at.Tab
		b.even(at.Native) // cosmetic: a failure only warns
	case where == "tab" && at.Tab != "":
		if _, err := b.call("window", "move", p.Tab, "--after", at.Tab); err != nil {
			fmt.Fprintf(os.Stderr, "warning: open the window right after %s: %v\n", at.Tab, err)
		}
	}
	return p, nil
}

func (b *RexBackend) SendText(p *Pane, text string) error {
	if err := CheckText(text); err != nil {
		return err
	}
	if text == "" {
		return nil // rex send refuses empty input
	}
	_, err := b.call("send", "-b", p.Native, "--", text) // -- so a text that starts with - is not a flag
	return err
}

func (b *RexBackend) SendKey(p *Pane, key string) error {
	k, err := NormalizeKey(key)
	if err != nil {
		return err
	}
	_, err = b.call("send-key", "-b", p.Native, strings.ReplaceAll(k, "-", "+"))
	return err
}

func (b *RexBackend) Capture(p *Pane, lines int) ([]string, error) {
	out, err := b.call("capture", "-b", p.Native, "--trim")
	if err != nil {
		return nil, err
	}
	return TrimScreen(strings.Split(out, "\n"), lines), nil
}

func (b *RexBackend) Close(p *Pane) error {
	_, err := b.call("block", "close", p.Native)
	return err
}

func (b *RexBackend) SetLabel(p *Pane, label string) error {
	_, err := b.call("block", "rename", p.Native, "--", label)
	return err
}

func (b *RexBackend) NativeStatus(*Pane) string { return "" }

// rexStatusRecord is one OSC 7501 record of a block, as its program_status method gives it. A block
// keeps one record per id the program reported under.
type rexStatusRecord struct {
	State string `json:"state"`
	Kind  string `json:"kind"`
	Msg   string `json:"msg"`
}

// statusRank orders states by urgency: the record a person must act on comes first.
var statusRank = map[string]int{"blocked": 5, "error": 4, "working": 3, "done": 2, "idle": 1}

// rexProgramStatus picks a block's most urgent record.
func rexProgramStatus(records []rexStatusRecord) (ProgramStatus, bool) {
	var best *rexStatusRecord
	for i := range records {
		r := &records[i]
		if r.State == "" || r.State == "clear" {
			continue
		}
		if best == nil || statusRank[r.State] > statusRank[best.State] {
			best = r
		}
	}
	if best == nil {
		return ProgramStatus{}, false
	}
	return ProgramStatus{State: strings.ToLower(best.State), Kind: best.Kind, Msg: best.Msg}, true
}

// rexStatusLua reads program_status of the given blocks, keyed by block ID, attaching each session
// once: a session's blocks answer methods only while it is attached.
func rexStatusLua(blocks map[string]string) string {
	var b strings.Builder
	b.WriteString("local want = {\n")
	for block, session := range blocks {
		fmt.Fprintf(&b, "  {s = %s, b = %s},\n", strconv.Quote(session), strconv.Quote("block:"+block))
	}
	b.WriteString(`}
local out, attached = {}, {}
for _, w in ipairs(want) do
  if not attached[w.s] then
    rex.call("session.attach", {session_id = w.s})
    attached[w.s] = true
  end
  out[w.b] = rex.call("block.method", {session_id = w.s, block_id = w.b, method = "program_status"}) or {}
end
return out
`)
	return b.String()
}

// ProgramStatuses reads the OSC 7501 status of every given block in one rex do call: each CLI call
// costs about 180 ms, so one per block would not do.
func (b *RexBackend) ProgramStatuses(panes []*Pane) (map[string]ProgramStatus, error) {
	out := map[string]ProgramStatus{}
	want := map[string]string{}
	for _, p := range panes {
		sid, err := b.session(p.Native)
		if err != nil {
			continue // gone since the list
		}
		want[p.Native] = sid
	}
	if len(want) == 0 {
		return out, nil
	}
	raw, err := b.call("do", "-e", rexStatusLua(want), "--json")
	if err != nil {
		return nil, err
	}
	var got map[string]struct {
		Records []rexStatusRecord `json:"records"`
	}
	if t := strings.TrimSpace(raw); t != "" && t != "{}" && t != "null" && t != "[]" {
		if err := json.Unmarshal([]byte(t), &got); err != nil {
			return nil, BackendErr("rex program_status: bad JSON: %v", err)
		}
	}
	for id, v := range got {
		if st, ok := rexProgramStatus(v.Records); ok {
			out["rex:"+rexID(id)] = st
		}
	}
	return out, nil
}
func (b *RexBackend) ServerRef() map[string]string { return map[string]string{} }
