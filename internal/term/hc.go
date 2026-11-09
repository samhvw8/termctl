package term

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HcBackend drives Herdr terminals through the `hc` CLI (herdr-core).
//
// Three properties of hc shape this file; each was measured against hc 0.1.19.
//
//   - hc has no paste-without-Enter: `hc send` always submits, `hc keys` presses only named
//     keys. So SendText refuses and the backend implements Submitter instead.
//   - Any hc command auto-starts a core, and that core keeps the environment of whoever
//     started it. So every path, spawn included, checks the core's socket first and never
//     calls hc when nothing is listening.
//   - `hc send` always names a caller, and a caller with a slash wraps the text in
//     <hc from="..."> tags. Messages go through the API (`hc connect`), where the caller is
//     optional: agent messages name theirs and show the tag, anything else stays bare.
type HcBackend struct {
	SocketPath string // when set, the core to talk to (HC_SOCKET); otherwise the environment's
	run        func(args ...string) (stdout string, code int, stderr string, err error)
	probe      func() bool // tests: stands in for the socket check
	rpc        func(req []byte) ([]byte, error)
}

const (
	hcLabelKey     = "termctl_label"
	hcSpaceLabel   = "herdr.space"
	hcExitNotFound = 3
	hcExitBlocked  = 9
	hcExitFinished = 11
	hcDialTimeout  = 500 * time.Millisecond
	hcSendTimeout  = 60 * time.Second // a send waits until nobody has typed for ~3 s
)

var hcKeys = map[string]string{"escape": "esc"}

// NewHc builds the real backend. socketPath may be empty, for the environment's core.
func NewHc(socketPath string) *HcBackend {
	b := &HcBackend{SocketPath: socketPath}
	b.run = func(args ...string) (string, int, string, error) {
		cmd := exec.Command("hc", args...)
		cmd.Env = hcEnv(os.Environ(), socketPath)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return string(out), exit.ExitCode(), stderr.String(), nil
		}
		return string(out), 0, stderr.String(), err
	}
	b.rpc = func(req []byte) ([]byte, error) { return hcConnect(hcEnv(os.Environ(), socketPath), req) }
	return b
}

// hcConnect makes one API call over `hc connect` and returns its one-line reply. Stdin stays
// open until the reply arrives, because closing it ends the connection.
func hcConnect(env []string, req []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), hcSendTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "hc", "connect")
	cmd.Env = env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	_, writeErr := stdin.Write(append(append([]byte{}, req...), '\n'))
	line, readErr := bufio.NewReader(stdout).ReadBytes('\n')
	stdin.Close()
	cmd.Wait()
	if readErr != nil {
		return nil, fmt.Errorf("no reply (write: %v, read: %v): %s", writeErr, readErr, strings.TrimSpace(stderr.String()))
	}
	return line, nil
}

// hcEnv is the subprocess environment. HC_TERMINAL_ID is removed: with it, polling counts as that
// terminal looking at its children. An explicit socket wins over any session in the environment.
func hcEnv(environ []string, socketPath string) []string {
	var env []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if name == "HC_TERMINAL_ID" || (socketPath != "" && (name == "HC_SOCKET" || name == "HC_SESSION")) {
			continue
		}
		env = append(env, kv)
	}
	if socketPath != "" {
		env = append(env, "HC_SOCKET="+socketPath)
	}
	return env
}

func (b *HcBackend) Name() string { return "hc" }

// socket is the core's socket path: explicit, HC_SOCKET, the terminal's own core, or the session's.
func (b *HcBackend) socket() string {
	if b.SocketPath != "" {
		return b.SocketPath
	}
	return hcDefaultSocket(os.Getenv)
}

func hcDefaultSocket(getenv func(string) string) string {
	if v := getenv("HC_SOCKET"); v != "" {
		return v
	}
	if v := getenv("HC_TERMINAL_SOCKET"); v != "" {
		return v
	}
	dir := getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "run")
	}
	session := getenv("HC_SESSION")
	if session == "" {
		session = "default"
	}
	return filepath.Join(dir, "herdr-core", session+".sock")
}

// running is true when a core answers on the socket. It never runs hc, so it cannot start one.
func (b *HcBackend) running() bool {
	if b.probe != nil {
		return b.probe()
	}
	c, err := net.DialTimeout("unix", b.socket(), hcDialTimeout)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func hcErrMessage(stderr string) string {
	var d struct {
		Error struct {
			Message string `json:"message"`
			Hint    string `json:"hint"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(stderr), &d) == nil && d.Error.Message != "" {
		return d.Error.Message
	}
	return strings.TrimSpace(stderr)
}

// call runs hc. Every command needs a live core: asking hc anything else would start one with this process's environment.
func (b *HcBackend) call(args ...string) (string, error) {
	if !b.running() {
		return "", BackendErr("hc %s: herdr-core is not running; open Herdr first", args[0])
	}
	out, code, stderr, err := b.run(args...)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", BackendErr("hc is not installed")
		}
		return "", BackendErr("hc %s: %v", args[0], err)
	}
	msg := hcErrMessage(stderr)
	switch code {
	case 0:
		return out, nil
	case hcExitNotFound, hcExitFinished:
		return "", TargetErr("hc: %s", msg)
	case hcExitBlocked:
		return "", hcBlocked(msg)
	}
	if msg == "" {
		msg = fmt.Sprintf("exit %d", code)
	}
	return "", BackendErr("hc %s: %s", args[0], msg)
}

func (b *HcBackend) Available() bool {
	if _, err := exec.LookPath("hc"); err != nil {
		return false
	}
	return b.running()
}

// ServerRef reopens this core and notices a new one. core_id outlives `hc server restart`, under which
// terminals keep running; it changes only when the core's state is wiped. The pid is informational.
func (b *HcBackend) ServerRef() map[string]string {
	out, err := b.call("server", "status", "--json")
	if err != nil {
		return map[string]string{}
	}
	var d struct {
		Pid  int `json:"pid"`
		Core struct {
			ID string `json:"id"`
		} `json:"core"`
	}
	if json.Unmarshal([]byte(out), &d) != nil || d.Core.ID == "" {
		return map[string]string{}
	}
	return map[string]string{"socket_path": b.socket(), "core_id": d.Core.ID, "pid": strconv.Itoa(d.Pid)}
}

// selfNative trusts HC_TERMINAL_ID only on the core the variable belongs to: on another core the same t7 is a different pane.
func (b *HcBackend) selfNative() string {
	id, sock := os.Getenv("HC_TERMINAL_ID"), os.Getenv("HC_TERMINAL_SOCKET")
	if id == "" || (sock != "" && sock != b.socket()) {
		return ""
	}
	return id
}

type hcListEntry struct {
	Address string `json:"address"`
	Agent   string `json:"agent"`
	Cwd     string `json:"cwd"`
	Name    string `json:"name"`
	Status  string `json:"status"`
}

type hcForeground struct {
	Name string   `json:"name"`
	Argv []string `json:"argv"`
	Cwd  string   `json:"cwd"`
}

type hcTerminal struct {
	Address    string            `json:"address"`
	Name       string            `json:"name"`
	Title      string            `json:"title"`
	Cwd        string            `json:"cwd"`
	Rows       int               `json:"rows"`
	Labels     map[string]string `json:"labels"`
	Foreground *hcForeground     `json:"foreground"`
	Status     *struct {
		State string `json:"state"`
	} `json:"status"`
}

// hcProcess names the program on a terminal. The foreground name is what the OS calls it, and a
// program run by an interpreter shows as node or bun, so argv is checked for a known agent too.
func hcProcess(fg *hcForeground, agent string) string {
	if fg == nil {
		return agent
	}
	if hasMain(fg.Name) {
		return fg.Name
	}
	for i, arg := range fg.Argv {
		if i > 1 {
			break
		}
		if base := filepath.Base(arg); hasMain(base) {
			return base
		}
	}
	return fg.Name
}

func hasMain(name string) bool {
	for _, n := range MainProcesses {
		if n == name {
			return true
		}
	}
	return false
}

func (b *HcBackend) get(native string) (*hcTerminal, error) {
	out, err := b.call("get", native, "--json")
	if err != nil {
		return nil, err
	}
	var t hcTerminal
	if err := json.Unmarshal([]byte(out), &t); err != nil {
		return nil, BackendErr("hc get %s: bad JSON: %v", native, err)
	}
	return &t, nil
}

func (b *HcBackend) pane(e hcListEntry, t *hcTerminal, me string) *Pane {
	title := CleanTitle(t.Title)
	if title == "" {
		title = t.Name
	}
	folder := t.Cwd
	if t.Foreground != nil && t.Foreground.Cwd != "" {
		folder = t.Foreground.Cwd
	}
	return &Pane{
		ID: "hc:" + t.Address, Backend: "hc", Native: t.Address, Title: title, Label: t.Labels[hcLabelKey],
		Process: hcProcess(t.Foreground, e.Agent), Folder: Tilde(folder), Rows: t.Rows, IsSelf: me != "" && t.Address == me,
	}
}

func (b *HcBackend) list() ([]hcListEntry, error) {
	out, err := b.call("ls", "--json")
	if err != nil {
		return nil, err
	}
	var data struct {
		Terminals []hcListEntry `json:"terminals"`
	}
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		return nil, BackendErr("hc ls: bad JSON: %v", err)
	}
	return data.Terminals, nil
}

func hcGone(err error) bool {
	var te *Error
	return errors.As(err, &te) && te.Code == CodeUsage
}

// ListPanes reads `hc ls`, then `hc get` for each terminal: ls has no labels, title or foreground.
// With no core running it returns no panes, and starts nothing.
func (b *HcBackend) ListPanes() ([]*Pane, error) {
	if !b.running() {
		return nil, nil
	}
	entries, err := b.list()
	if err != nil {
		return nil, err
	}
	me := b.selfNative()
	results := make([]*Pane, len(entries))
	errs := make([]error, len(entries))
	var wg sync.WaitGroup
	gate := make(chan struct{}, 8)
	for i, e := range entries {
		wg.Add(1)
		go func(i int, e hcListEntry) {
			defer wg.Done()
			gate <- struct{}{}
			defer func() { <-gate }()
			t, err := b.get(e.Address)
			if err != nil {
				if !hcGone(err) { // otherwise it ended between ls and get
					errs[i] = err
				}
				return
			}
			results[i] = b.pane(e, t, me)
		}(i, e)
	}
	wg.Wait()
	var panes []*Pane
	for i, p := range results {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if p != nil {
			panes = append(panes, p)
		}
	}
	return panes, nil
}

func (b *HcBackend) SelfPane() (*Pane, error) {
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

var hcNameUnsafe = regexp.MustCompile(`[^a-z0-9_-]+`)

// hcName turns a title into a terminal name: 1-32 of a-z 0-9 - _, unique among live terminals.
func hcName(title string) string {
	base := strings.Trim(hcNameUnsafe.ReplaceAllString(strings.ToLower(title), "-"), "-_")
	if len(base) > 24 {
		base = strings.TrimRight(base[:24], "-_")
	}
	if base == "" {
		base = "termctl"
	}
	suffix := make([]byte, 3)
	rand.Read(suffix)
	return fmt.Sprintf("%s-%x", base, suffix)
}

// hcTerminalVars name the terminal a process runs in. A core started from another terminal hands
// them to every program it runs, and a worker that sees one detects the wrong backend.
const hcTerminalVars = "ITERM_SESSION_ID TERM_SESSION_ID TMUX TMUX_PANE REX_SESSION REX_BLOCK"

func hcScript(cmd string) string {
	if cmd == "" {
		cmd = `exec "${SHELL:-/bin/sh}" -l`
	}
	return "unset " + hcTerminalVars + "; " + cmd
}

// hcFolder is cwd in the form two paths can be compared in. Empty means where hc would run.
func hcFolder(cwd string) string {
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		return real
	}
	return cwd
}

// spaceFor picks the Herdr space for a new terminal: the caller's when it has one, else one for
// the folder. The app shows a terminal only when it has a herdr.space label, and an id it does
// not know becomes a new space named after that folder. So this needs no app commands and works
// with the app closed: the terminal appears when the app next shows that core.
func (b *HcBackend) spaceFor(cwd string) (string, error) {
	if me := b.selfNative(); me != "" {
		t, err := b.get(me)
		if err != nil && !hcGone(err) {
			return "", err
		}
		if err == nil && t.Labels[hcSpaceLabel] != "" {
			return t.Labels[hcSpaceLabel], nil
		}
	}
	folder := hcFolder(cwd)
	entries, err := b.list()
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if strings.Contains(e.Address, "/") || hcFolder(e.Cwd) != folder {
			continue // a remote terminal's label names a space on that machine
		}
		t, err := b.get(e.Address)
		if err != nil {
			if hcGone(err) {
				continue
			}
			return "", err
		}
		if id := t.Labels[hcSpaceLabel]; id != "" {
			return id, nil
		}
	}
	sum := sha256.Sum256([]byte(folder))
	return "sp" + hex.EncodeToString(sum[:16]), nil
}

// Spawn starts a worker that outlives whoever started it, as a new tab of a space in the Herdr app.
// where is validated and ignored: the app decides where a tab goes.
func (b *HcBackend) Spawn(where, cmd, cwd, title string) (*Pane, error) {
	if err := CheckWhere(where); err != nil {
		return nil, err
	}
	space, err := b.spaceFor(cwd)
	if err != nil {
		return nil, err
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	argv := []string{"run", "--bg", "--json", "--name", hcName(title), "--no-parent", "--outlive-parent", "--no-notify", "--label", hcSpaceLabel + "=" + space}
	if cwd != "" {
		argv = append(argv, "--cwd", cwd)
	}
	argv = append(argv, "--", shell, "-lic", hcScript(cmd))
	out, err := b.call(argv...)
	if err != nil {
		return nil, err
	}
	var e hcListEntry
	if err := json.Unmarshal([]byte(out), &e); err != nil {
		return nil, BackendErr("hc run: bad JSON: %v", err)
	}
	if e.Address == "" {
		return nil, BackendErr("hc run: result carries no address: %q", out)
	}
	t, err := b.get(e.Address)
	if err != nil {
		return nil, err
	}
	return b.pane(e, t, ""), nil
}

// SendText always fails: hc has no paste-without-Enter, and submitting would break the "never submits" contract.
func (b *HcBackend) SendText(p *Pane, text string) error {
	if err := CheckText(text); err != nil {
		return err
	}
	return BackendErr("hc cannot type text without pressing Enter (`hc send` always submits); use harness-ops msg or slash, or send-key for single keys")
}

func hcBlocked(msg string) *Error {
	return Errf(CodeGuard, "hc: %s: it is waiting for a person (a permission or a question); read its screen", msg)
}

// SubmitText pastes the text and presses Enter. With from set, hc shows it as <hc from="...">.
// A program that is blocked on a person is refused: there is no force here.
func (b *HcBackend) SubmitText(p *Pane, text, from string) error {
	if err := CheckText(text); err != nil {
		return err
	}
	if !b.running() {
		return BackendErr("hc send: herdr-core is not running; open Herdr first")
	}
	params := map[string]any{"terminal": p.Native, "text": text}
	if from != "" {
		params["caller"] = from
	}
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "terminal.send", "params": params})
	if err != nil {
		return BackendErr("hc send: %v", err)
	}
	raw, err := b.rpc(req)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return BackendErr("hc is not installed")
		}
		return BackendErr("hc connect: %v", err)
	}
	var reply struct {
		Error *struct {
			Message string `json:"message"`
			Data    struct {
				Kind string `json:"kind"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return BackendErr("hc connect: bad reply: %q", Cut(string(raw), 200))
	}
	if reply.Error == nil {
		return nil
	}
	switch reply.Error.Data.Kind {
	case "not_found", "finished":
		return TargetErr("hc: %s", reply.Error.Message)
	case "blocked":
		return hcBlocked(reply.Error.Message)
	}
	return BackendErr("hc send: %s", reply.Error.Message)
}

func (b *HcBackend) SendKey(p *Pane, key string) error {
	k, err := NormalizeKey(key)
	if err != nil {
		return err
	}
	if mapped, ok := hcKeys[k]; ok {
		k = mapped
	}
	_, err = b.call("keys", "--json", p.Native, strings.ReplaceAll(k, "-", "+"))
	return err
}

// Capture reads only the visible screen when the request fits it, because history reads can scroll a full-screen app.
func (b *HcBackend) Capture(p *Pane, lines int) ([]string, error) {
	rows := p.Rows
	if rows == 0 {
		t, err := b.get(p.Native)
		if err != nil {
			return nil, err
		}
		rows = t.Rows
	}
	history := 0
	if lines > rows && rows > 0 {
		history = lines - rows
	}
	out, err := b.call("read", p.Native, "--lines", strconv.Itoa(history))
	if err != nil {
		return nil, err
	}
	return TrimScreen(strings.Split(out, "\n"), lines), nil
}

// Close ends the program and removes the terminal, so finished terminals do not pile up. Already gone is success.
func (b *HcBackend) Close(p *Pane) error {
	gone := func(err error) bool {
		var te *Error
		return errors.As(err, &te) && te.Code == CodeUsage
	}
	if _, err := b.call("kill", "--json", p.Native); err != nil && !gone(err) {
		return err
	}
	if _, err := b.call("rm", "--json", p.Native); err != nil && !gone(err) {
		return err
	}
	return nil
}

func (b *HcBackend) SetLabel(p *Pane, label string) error {
	_, err := b.call("label", "--json", p.Native, hcLabelKey+"="+label)
	return err
}

// NativeStatus is hc's status word: idle, working, done, blocked, error. "" when the program never said.
func (b *HcBackend) NativeStatus(p *Pane) string {
	t, err := b.get(p.Native)
	if err != nil || t.Status == nil {
		return ""
	}
	return t.Status.State
}
