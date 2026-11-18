package term

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const (
	tmuxSep      = "|@@|"
	tmuxLabelOpt = "@termctl_label"
)

var tmuxFields = []string{"pane_id", "pane_tty", "pane_title", tmuxLabelOpt, "pane_current_path", "pane_current_command", "window_id", "pane_width", "pane_height"}

var tmuxKeys = map[string]string{
	"enter": "Enter", "escape": "Escape", "tab": "Tab", "backspace": "BSpace",
	"up": "Up", "down": "Down", "left": "Left", "right": "Right",
}

// TmuxBackend drives tmux through its CLI. SocketPath, when set, wins over Socket.
type TmuxBackend struct {
	Socket, SocketPath string
	SelfOverride       *Pane // tests: a pane to split without running inside tmux
}

func (b *TmuxBackend) Name() string { return "tmux" }

func (b *TmuxBackend) argv(args ...string) []string {
	if b.SocketPath != "" {
		return append([]string{"-S", b.SocketPath}, args...)
	}
	if b.Socket != "" {
		return append([]string{"-L", b.Socket}, args...)
	}
	return args
}

func (b *TmuxBackend) run(args ...string) (string, error) {
	cmd, finish := cliCommand("tmux", b.argv(args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err = finish(err); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", BackendErr("tmux is not installed")
		}
		if errors.Is(err, errNoAnswer) {
			return "", BackendErr("tmux %s: %v", args[0], err)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "failed"
		}
		return "", BackendErr("tmux %s: %s", args[0], msg)
	}
	return string(out), nil
}

func (b *TmuxBackend) Available() bool {
	if _, err := exec.LookPath("tmux"); err != nil {
		return false
	}
	_, err := b.run("list-sessions")
	return err == nil
}

func (b *TmuxBackend) ServerRef() map[string]string {
	out, err := b.run("display-message", "-p", "#{socket_path}"+tmuxSep+"#{pid}")
	if err != nil {
		return map[string]string{}
	}
	parts := strings.Split(strings.TrimSpace(out), tmuxSep)
	if len(parts) != 2 {
		return map[string]string{}
	}
	return map[string]string{"socket_path": parts[0], "pid": parts[1]}
}

// selfNative trusts TMUX_PANE only when $TMUX names this server: on another socket the same %N is a different pane.
func (b *TmuxBackend) selfNative() string {
	pane, env := os.Getenv("TMUX_PANE"), os.Getenv("TMUX")
	if pane == "" || env == "" {
		return ""
	}
	out, err := b.run("display-message", "-p", "#{socket_path}")
	if err != nil || strings.TrimSpace(out) != strings.Split(env, ",")[0] {
		return ""
	}
	return pane
}

func (b *TmuxBackend) ListPanes() ([]*Pane, error) {
	parts := make([]string, len(tmuxFields))
	for i, f := range tmuxFields {
		parts[i] = "#{" + f + "}"
	}
	out, err := b.run("list-panes", "-a", "-F", strings.Join(parts, tmuxSep))
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting") {
			return nil, nil // a server that is not running has no panes
		}
		return nil, err
	}
	procs, err := processesByTTY()
	if err != nil {
		return nil, err
	}
	me := b.selfNative()
	var panes []*Pane
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, tmuxSep)
		if len(f) != len(tmuxFields) {
			return nil, BackendErr("tmux list-panes: cannot parse line: %q", line)
		}
		tty := strings.TrimPrefix(f[1], "/dev/")
		name := mainProcess(procs[tty]).name
		if name == "" {
			name = f[5]
		}
		w, _ := strconv.Atoi(f[7])
		h, _ := strconv.Atoi(f[8])
		panes = append(panes, &Pane{
			ID: "tmux:" + f[0], Backend: "tmux", Native: f[0], TTY: tty, Title: CleanTitle(f[2]),
			Label: f[3], Process: name, Folder: Tilde(f[4]), IsSelf: f[0] == me, Tab: f[6], Width: w, Height: h,
		})
	}
	return panes, nil
}

func (b *TmuxBackend) SelfPane() (*Pane, error) {
	if b.SelfOverride != nil {
		return b.SelfOverride, nil
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

var tmuxSessionName = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func (b *TmuxBackend) Spawn(where, cmd, cwd, title string) (*Pane, error) {
	if err := CheckWhere(where); err != nil {
		return nil, err
	}
	where, anchor := PlaceFor(b, where, nil)
	return b.SpawnAt(anchor, where, cmd, cwd, title)
}

// SpawnAt splits anchor, or opens a window right after anchor's window. A nil anchor means the caller's pane.
func (b *TmuxBackend) SpawnAt(at *Pane, where, cmd, cwd, title string) (*Pane, error) {
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
	opts := []string{"-P", "-F", "#{pane_id}"}
	if cwd != "" {
		opts = append(opts, "-c", cwd)
	}
	var argv []string
	switch where {
	case "tab":
		argv = append([]string{"new-window", "-d"}, opts...)
		if at != nil && at.Tab != "" {
			argv = append(argv, "-a", "-t", at.Tab) // right after the anchor's window
		}
		if title != "" {
			argv = append(argv, "-n", title)
		}
	case "split":
		if at == nil {
			return nil, UsageErr("split needs a pane to split: run inside tmux on this server")
		}
		dir := "-v" // below
		if SideBySide(at.Width, at.Height) {
			dir = "-h"
		}
		argv = append([]string{"split-window", "-d", dir, "-t", at.Native}, opts...)
	default:
		base := title
		if base == "" {
			base = "termctl"
		}
		name := fmt.Sprintf("%s-%d", tmuxSessionName.ReplaceAllString(base, "-"), os.Getpid())
		argv = append([]string{"new-session", "-d", "-s", name}, opts...)
	}
	if cmd != "" {
		argv = append(argv, "--", cmd)
	}
	out, err := b.run(argv...)
	if err != nil {
		return nil, err
	}
	native := strings.TrimSpace(out)
	if where == "split" {
		b.tile(native)
	}
	if title != "" {
		if _, err := b.run("select-pane", "-t", native, "-T", title); err != nil {
			return nil, err
		}
	}
	panes, err := b.ListPanes()
	if err != nil {
		return nil, err
	}
	for _, p := range panes {
		if p.Native == native {
			return p, nil
		}
	}
	return &Pane{ID: "tmux:" + native, Backend: "tmux", Native: native, Title: title}, nil
}

func (b *TmuxBackend) SendText(p *Pane, text string) error {
	if err := CheckText(text); err != nil {
		return err
	}
	// tmux reads a trailing ';' of any argument as a command separator.
	if strings.HasSuffix(text, ";") {
		text = text[:len(text)-1] + `\;`
	}
	_, err := b.run("send-keys", "-l", "-t", p.Native, "--", text)
	return err
}

func (b *TmuxBackend) SendKey(p *Pane, key string) error {
	k, err := NormalizeKey(key)
	if err != nil {
		return err
	}
	name, ok := tmuxKeys[k]
	if !ok {
		name = "C-" + strings.Split(k, "-")[1]
	}
	_, err = b.run("send-keys", "-t", p.Native, name)
	return err
}

func (b *TmuxBackend) Capture(p *Pane, lines int) ([]string, error) {
	out, err := b.run("capture-pane", "-p", "-t", p.Native, "-S", fmt.Sprintf("-%d", lines))
	if err != nil {
		return nil, err
	}
	return TrimScreen(strings.Split(out, "\n"), lines), nil
}

// HistoryIsSafe: capture-pane -S reads the pane's history buffer; the program sees nothing.
func (b *TmuxBackend) HistoryIsSafe(*Pane) bool { return true }

func (b *TmuxBackend) Close(p *Pane) error {
	_, err := b.run("kill-pane", "-t", p.Native)
	return err
}

func (b *TmuxBackend) SetLabel(p *Pane, label string) error {
	_, err := b.run("set-option", "-p", "-t", p.Native, tmuxLabelOpt, label)
	return err
}

func (b *TmuxBackend) NativeStatus(*Pane) string { return "" }

// tile evens out the window holding target as a grid. Cosmetic: the panes exist, so a failure warns.
func (b *TmuxBackend) tile(target string) {
	if _, err := b.run("select-layout", "-t", target, "tiled"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: even out the window: %v\n", err)
	}
}

// MovePane joins p to anchor's window, beside or below anchor, then tiles that window as a spawn
// does. join-pane moves the pane itself: its %id, process and history stay. A window left empty
// closes; its pane lives on in the new one.
func (b *TmuxBackend) MovePane(p, anchor *Pane) error {
	dir := "-v" // below
	if SideBySide(anchor.Width, anchor.Height) {
		dir = "-h"
	}
	if _, err := b.run("join-pane", "-d", dir, "-s", p.Native, "-t", anchor.Native); err != nil {
		return err
	}
	b.tile(anchor.Native)
	return nil
}

// BreakPane moves p into a new window right after the one it leaves (break-pane -a).
func (b *TmuxBackend) BreakPane(p *Pane) error {
	argv := []string{"break-pane", "-d", "-s", p.Native}
	if p.Tab != "" {
		argv = append(argv, "-a", "-t", p.Tab)
	}
	_, err := b.run(argv...)
	return err
}

// SwapPanes trades the places of two panes; -d keeps each window's active pane.
func (b *TmuxBackend) SwapPanes(x, y *Pane) error {
	_, err := b.run("swap-pane", "-d", "-s", x.Native, "-t", y.Native)
	return err
}

var tmuxLayouts = map[string]string{
	"even": "tiled", "tiled": "tiled", "rows": "even-vertical", "columns": "even-horizontal",
	"main-left": "main-vertical", "main-top": "main-horizontal",
}

// Arrange applies one of tmux's own layouts to a window. tmux has no layout that keeps the split
// tree and evens the sizes, so even tiles, as a spawn does. A main-* layout puts main first (tmux
// gives the first pane the big side) and sets the window's main-pane-width or -height to 50%.
func (b *TmuxBackend) Arrange(tab, layout string, main *Pane) error {
	name, ok := tmuxLayouts[layout]
	if !ok {
		return CheckLayout(layout)
	}
	switch layout {
	case "main-left":
		if _, err := b.run("set-option", "-w", "-t", tab, "main-pane-width", "50%"); err != nil {
			return err
		}
	case "main-top":
		if _, err := b.run("set-option", "-w", "-t", tab, "main-pane-height", "50%"); err != nil {
			return err
		}
	}
	if main != nil {
		out, err := b.run("list-panes", "-t", tab, "-F", "#{pane_id}")
		if err != nil {
			return err
		}
		if first := strings.Fields(out); len(first) > 0 && first[0] != main.Native {
			if _, err := b.run("swap-pane", "-d", "-s", main.Native, "-t", first[0]); err != nil {
				return err
			}
		}
	}
	_, err := b.run("select-layout", "-t", tab, name)
	return err
}
