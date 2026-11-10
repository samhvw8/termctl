package term

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	api "github.com/samhvw8/termctl/internal/iterm2api"
	"google.golang.org/protobuf/proto"
)

const (
	itermApp      = "/Applications/iTerm.app"
	nativeIT2     = itermApp + "/Contents/Resources/utilities/it2"
	labelVar      = "user.agent"
	apiTimeout    = 30 * time.Second // a stalled API call must fail, not hang every caller
	statusTimeout = 5 * time.Second
)

var itermKeys = map[string]string{
	"enter": "\r", "escape": "\x1b", "tab": "\t", "backspace": "\x7f",
	"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
}

// iterm2Command starts cmd in a login, interactive shell. A session that ends within seconds makes
// iTerm2 raise a modal dialog, and that dialog stalls the whole API, so a failed command stays open briefly.
func iterm2Command(shell, cmd string) string {
	script := cmd + `; s=$?; [ $s -eq 0 ] || { echo "termctl: command exited with status $s"; sleep 8; }`
	return ShellQuote(shell) + " -lic " + ShellQuote(script)
}

func iterm2KeyBytes(key string) (string, error) {
	k, err := NormalizeKey(key)
	if err != nil {
		return "", err
	}
	if b, ok := itermKeys[k]; ok {
		return b, nil
	}
	return string(rune(k[len(k)-1] - 'a' + 1)), nil
}

// ITerm2Backend keeps one websocket for the process lifetime.
type ITerm2Backend struct {
	mu     sync.Mutex
	client *api.Client
}

var (
	itermOnce sync.Once
	itermInst *ITerm2Backend
)

func theITerm2() *ITerm2Backend {
	itermOnce.Do(func() { itermInst = &ITerm2Backend{} })
	return itermInst
}

func (b *ITerm2Backend) Name() string { return "iterm2" }

func selfNative() string {
	s := os.Getenv("ITERM_SESSION_ID")
	if i := strings.Index(s, ":"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func (b *ITerm2Backend) Available() bool {
	_, err := os.Stat(itermApp)
	return err == nil && (selfNative() != "" || os.Getenv("ITERM2_COOKIE") != "")
}

func (b *ITerm2Backend) ServerRef() map[string]string { return map[string]string{} }

// call runs one API request. Connecting happens on first use.
func (b *ITerm2Backend) call(req *api.ClientOriginatedMessage) (*api.ServerOriginatedMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	if b.client == nil {
		c, err := api.Connect(ctx, filepath.Base(os.Args[0]))
		if err != nil {
			return nil, BackendErr("cannot reach the iTerm2 API: %v. Is the Python API enabled (Settings > General > Magic > Enable Python API)?", err)
		}
		b.client = c
	}
	resp, err := b.client.Call(ctx, req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, BackendErr("iTerm2 API call timed out after %v", apiTimeout)
		}
		return nil, BackendErr("iTerm2 API error: %v", err)
	}
	return resp, nil
}

type itermSession struct {
	id, window, tab, selectedTab string
	tabIndex                     int // the tab's position in its window
}

func walkSplit(n *api.SplitTreeNode, fn func(string)) {
	for _, l := range n.GetLinks() {
		if l.GetSession() != nil {
			fn(l.GetSession().GetUniqueIdentifier())
		} else if l.GetNode() != nil {
			walkSplit(l.GetNode(), fn)
		}
	}
}

func (b *ITerm2Backend) sessions() ([]itermSession, error) {
	resp, err := b.call(&api.ClientOriginatedMessage{ListSessionsRequest: &api.ListSessionsRequest{}})
	if err != nil {
		return nil, err
	}
	var out []itermSession
	for _, w := range resp.GetListSessionsResponse().GetWindows() {
		for ti, t := range w.GetTabs() {
			walkSplit(t.GetRoot(), func(id string) {
				out = append(out, itermSession{id, w.GetWindowId(), t.GetTabId(), w.GetSelectedTabId(), ti})
			})
		}
	}
	return out, nil
}

func jsonString(raw string) string {
	var v *string
	if json.Unmarshal([]byte(raw), &v) != nil || v == nil {
		return ""
	}
	return *v
}

func (b *ITerm2Backend) variables(session string, names ...string) ([]string, error) {
	resp, err := b.call(&api.ClientOriginatedMessage{VariableRequest: &api.VariableRequest{SessionId: proto.String(session), Get: names}})
	if err != nil {
		return nil, err
	}
	r := resp.GetVariableResponse()
	if r.GetStatus() != api.VariableResponse_OK {
		return nil, BackendErr("iTerm2 variable: %s", r.GetStatus())
	}
	values := r.GetValues()
	if len(values) != len(names) {
		return nil, BackendErr("iTerm2 variable: %d values for %d names", len(values), len(names))
	}
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = jsonString(v)
	}
	return out, nil
}

func (b *ITerm2Backend) ListPanes() ([]*Pane, error) {
	sess, err := b.sessions()
	if err != nil {
		return nil, err
	}
	type row struct{ id, tab, tty, title, label string }
	rows := make([]row, 0, len(sess))
	for _, s := range sess {
		v, err := b.variables(s.id, "tty", "autoName", labelVar)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row{s.id, s.tab, strings.TrimPrefix(v[0], "/dev/"), CleanTitle(v[1]), v[2]})
	}
	procs, err := processesByTTY()
	if err != nil {
		return nil, err
	}
	picks := map[string]proc{}
	var pids []int
	for _, r := range rows {
		p := mainProcess(procs[r.tty])
		picks[r.id] = p
		if p.pid != 0 {
			pids = append(pids, p.pid)
		}
	}
	cwds := cwdOf(pids)
	me := selfNative()
	panes := make([]*Pane, 0, len(rows))
	for _, r := range rows {
		p := picks[r.id]
		name := p.name
		if name == "" {
			name = "-"
		}
		panes = append(panes, &Pane{
			ID: "iterm2:" + r.id, Backend: "iterm2", Native: r.id, TTY: r.tty, Title: r.title, Label: r.label,
			Process: name, Folder: Tilde(cwds[p.pid]), IsSelf: r.id == me, Tab: r.tab,
		})
	}
	return panes, nil
}

func (b *ITerm2Backend) SelfPane() (*Pane, error) {
	if selfNative() == "" {
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

func jsonValue(v string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return strings.TrimSpace(buf.String())
}

func (b *ITerm2Backend) Spawn(where, cmd, cwd, title string) (*Pane, error) {
	if err := CheckWhere(where); err != nil {
		return nil, err
	}
	where, anchor := PlaceFor(b, where, cwd, nil)
	return b.SpawnAt(anchor, where, cmd, cwd, title)
}

// SpawnAt splits anchor, or opens a tab right after anchor's tab. A nil anchor means the caller's pane.
func (b *ITerm2Backend) SpawnAt(at *Pane, where, cmd, cwd, title string) (*Pane, error) {
	if err := CheckWhere(where); err != nil {
		return nil, err
	}
	if where == "auto" {
		where, at = PlaceFor(b, where, cwd, nil)
	}
	var props []*api.ProfileProperty
	set := func(k, v string) {
		props = append(props, &api.ProfileProperty{Key: proto.String(k), JsonValue: proto.String(jsonValue(v))})
	}
	if cmd != "" {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/zsh"
		}
		set("Custom Command", "Yes")
		set("Command", iterm2Command(shell, cmd))
	}
	if cwd != "" {
		set("Custom Directory", "Yes")
		set("Working Directory", cwd)
	}
	want := selfNative()
	if at != nil {
		want = at.Native
	}
	var anchor *itermSession
	if want != "" {
		sess, err := b.sessions()
		if err != nil {
			return nil, err
		}
		for i := range sess {
			if sess[i].id == want {
				anchor = &sess[i]
			}
		}
	}
	var sid string
	switch {
	case where == "window":
		r, err := b.createTab(nil, nil, props)
		if err != nil {
			return nil, err
		}
		sid = r
	case anchor == nil:
		return nil, UsageErr("--where %s needs a pane to anchor on: run inside an iTerm2 pane", where)
	case where == "split":
		resp, err := b.call(&api.ClientOriginatedMessage{SplitPaneRequest: &api.SplitPaneRequest{
			Session: proto.String(anchor.id), SplitDirection: api.SplitPaneRequest_VERTICAL.Enum(), Before: proto.Bool(false), CustomProfileProperties: props,
		}})
		if err != nil {
			return nil, err
		}
		r := resp.GetSplitPaneResponse()
		if r.GetStatus() != api.SplitPaneResponse_OK || len(r.GetSessionId()) == 0 {
			return nil, BackendErr("iTerm2 split: %s", r.GetStatus())
		}
		sid = r.GetSessionId()[0]
	default:
		next := uint32(anchor.tabIndex + 1) // beside the anchor's tab, not at the end of the window
		r, err := b.createTab(&anchor.window, &next, props)
		if err != nil {
			return nil, err
		}
		sid = r
	}
	if title != "" {
		if err := b.setName(sid, title); err != nil {
			return nil, err
		}
	}
	return &Pane{ID: "iterm2:" + sid, Backend: "iterm2", Native: sid, Title: title}, nil
}

func (b *ITerm2Backend) createTab(window *string, index *uint32, props []*api.ProfileProperty) (string, error) {
	// select_tab false keeps the user's focus where it was. A new window is always ordered front.
	req := &api.CreateTabRequest{WindowId: window, TabIndex: index, SelectTab: proto.Bool(false), CustomProfileProperties: props}
	resp, err := b.call(&api.ClientOriginatedMessage{CreateTabRequest: req})
	if err != nil {
		return "", err
	}
	r := resp.GetCreateTabResponse()
	if r.GetStatus() != api.CreateTabResponse_OK {
		return "", BackendErr("iTerm2 create tab: %s", r.GetStatus())
	}
	return r.GetSessionId(), nil
}

func (b *ITerm2Backend) setName(session, name string) error {
	invocation := fmt.Sprintf("iterm2.set_name(name: %s)", jsonValue(name))
	resp, err := b.call(&api.ClientOriginatedMessage{InvokeFunctionRequest: &api.InvokeFunctionRequest{
		Invocation: proto.String(invocation), Method: &api.InvokeFunctionRequest_Method{Receiver: proto.String(session)}, Timeout: proto.Float64(-1),
	}})
	if err != nil {
		return err
	}
	if e := resp.GetInvokeFunctionResponse().GetError(); e != nil {
		return BackendErr("iTerm2 set_name: %s: %s", e.GetStatus(), e.GetErrorReason())
	}
	return nil
}

func (b *ITerm2Backend) sendRaw(p *Pane, data string) error {
	resp, err := b.call(&api.ClientOriginatedMessage{SendTextRequest: &api.SendTextRequest{
		Session: proto.String(p.Native), Text: proto.String(data), SuppressBroadcast: proto.Bool(false),
	}})
	if err != nil {
		return err
	}
	if s := resp.GetSendTextResponse().GetStatus(); s != api.SendTextResponse_OK {
		return BackendErr("iTerm2 send to %s: %s", p.Native, s)
	}
	return nil
}

func (b *ITerm2Backend) SendText(p *Pane, text string) error {
	if err := CheckText(text); err != nil {
		return err
	}
	return b.sendRaw(p, text)
}

func (b *ITerm2Backend) SendKey(p *Pane, key string) error {
	data, err := iterm2KeyBytes(key)
	if err != nil {
		return err
	}
	return b.sendRaw(p, data)
}

func (b *ITerm2Backend) Capture(p *Pane, lines int) ([]string, error) {
	resp, err := b.call(&api.ClientOriginatedMessage{GetBufferRequest: &api.GetBufferRequest{
		Session: proto.String(p.Native), LineRange: &api.LineRange{ScreenContentsOnly: proto.Bool(true)}, IncludeStyles: proto.Bool(false),
	}})
	if err != nil {
		return nil, err
	}
	r := resp.GetGetBufferResponse()
	if r.GetStatus() != api.GetBufferResponse_OK {
		return nil, BackendErr("iTerm2 read of %s: %s", p.Native, r.GetStatus())
	}
	out := make([]string, len(r.GetContents()))
	for i, c := range r.GetContents() {
		out[i] = c.GetText()
	}
	return TrimScreen(out, lines), nil
}

func (b *ITerm2Backend) Close(p *Pane) error {
	resp, err := b.call(&api.ClientOriginatedMessage{CloseRequest: &api.CloseRequest{
		Sessions: &api.CloseRequest_CloseSessions{SessionIds: []string{p.Native}}, Force: proto.Bool(true),
	}})
	if err != nil {
		return err
	}
	for _, s := range resp.GetCloseResponse().GetStatuses() {
		if s != api.CloseResponse_OK {
			return BackendErr("iTerm2 close of %s: %s", p.Native, s)
		}
	}
	return nil
}

func (b *ITerm2Backend) SetLabel(p *Pane, label string) error {
	resp, err := b.call(&api.ClientOriginatedMessage{VariableRequest: &api.VariableRequest{
		SessionId: proto.String(p.Native), Set: []*api.VariableRequest_Set{{Name: proto.String(labelVar), Value: proto.String(jsonValue(label))}},
	}})
	if err != nil {
		return err
	}
	if s := resp.GetVariableResponse().GetStatus(); s != api.VariableResponse_OK {
		return BackendErr("iTerm2 set label on %s: %s", p.Native, s)
	}
	return nil
}

// NativeStatus reads iTerm2's own it2 status (Claude Code sets it). "" when unavailable.
func (b *ITerm2Backend) NativeStatus(p *Pane) string {
	if _, err := os.Stat(nativeIT2); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, nativeIT2, "session", "get-status", "-s", p.Native)
	// The native CLI authenticates by itself; a borrowed cookie would break it.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ITERM2_COOKIE=") && !strings.HasPrefix(kv, "ITERM2_KEY=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	var d struct {
		Status *string `json:"status"`
		Detail *string `json:"detail"`
	}
	if json.Unmarshal(out, &d) != nil || d.Status == nil || *d.Status == "" {
		return ""
	}
	if d.Detail != nil && *d.Detail != "" {
		return *d.Status + ": " + *d.Detail
	}
	return *d.Status
}
