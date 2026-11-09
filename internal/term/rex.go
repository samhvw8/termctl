package term

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RexBackend is written against Rex's documented CLI and is UNTESTED against a real Rex:
// only argv mapping is covered, by a stub. https://www.superlogical.com/rex/docs/reference/cli
// JSON field names of `rex block ls --json` are not documented, so parsing is tolerant.
type RexBackend struct {
	run func(args ...string) (stdout string, code int, stderr string, err error)
}

const rexExitTarget = 3

func NewRex() *RexBackend {
	return &RexBackend{run: func(args ...string) (string, int, string, error) {
		cmd := exec.Command("rex", args...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return string(out), exit.ExitCode(), stderr.String(), nil
		}
		return string(out), 0, stderr.String(), err
	}}
}

func (b *RexBackend) Name() string { return "rex" }

func (b *RexBackend) call(args ...string) (string, error) {
	out, code, stderr, err := b.run(args...)
	if err != nil {
		return "", BackendErr("rex is not installed")
	}
	if code == rexExitTarget {
		return "", TargetErr("rex: target missing or ambiguous: %s", strings.TrimSpace(stderr))
	}
	if code != 0 {
		return "", BackendErr("rex %s: exit %d: %s", args[0], code, strings.TrimSpace(stderr))
	}
	return out, nil
}

func (b *RexBackend) Available() bool {
	_, err := exec.LookPath("rex")
	return err == nil
}

func first(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil && v != "" {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

func (b *RexBackend) process(block string) string {
	out, err := b.call("block", "call", "-b", block, "process")
	if err != nil {
		return ""
	}
	var d map[string]any
	if json.Unmarshal([]byte(out), &d) != nil {
		return ""
	}
	name := first(d, "name", "command", "comm", "exe")
	if name == "" {
		return ""
	}
	return strings.SplitN(filepath.Base(name), " ", 2)[0]
}

func (b *RexBackend) ListPanes() ([]*Pane, error) {
	out, err := b.call("block", "ls", "-A", "--json")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	var data any
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		return nil, BackendErr("rex block ls: bad JSON: %v", err)
	}
	if m, ok := data.(map[string]any); ok {
		data = m["blocks"]
	}
	blocks, _ := data.([]any)
	me := os.Getenv("REX_BLOCK")
	var panes []*Pane
	for _, raw := range blocks {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		native := first(m, "id", "block_id")
		panes = append(panes, &Pane{
			ID: "rex:" + native, Backend: "rex", Native: native, Title: CleanTitle(first(m, "title", "name")),
			Label: first(m, "label"), Process: b.process(native), Folder: Tilde(first(m, "cwd", "directory", "working_directory")),
			IsSelf: me != "" && native == me,
		})
	}
	return panes, nil
}

func (b *RexBackend) SelfPane() (*Pane, error) {
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

// rexBlockID finds the new block's ID in a creation result; window and session results nest it.
func rexBlockID(v any) string {
	switch t := v.(type) {
	case []any:
		if len(t) > 0 {
			return rexBlockID(t[0])
		}
	case map[string]any:
		for _, k := range []string{"block", "focused_block", "first_block"} {
			switch b := t[k].(type) {
			case string:
				if b != "" {
					return b
				}
			case map[string]any:
				if id := first(b, "id"); id != "" {
					return id
				}
			}
		}
		if bs, ok := t["blocks"].([]any); ok && len(bs) > 0 {
			return rexBlockID(bs[0])
		}
		return first(t, "id")
	}
	return ""
}

func (b *RexBackend) Spawn(where, cmd, cwd, title string) (*Pane, error) {
	if err := CheckWhere(where); err != nil {
		return nil, err
	}
	opts := []string{"--json"}
	if cwd != "" {
		opts = append(opts, "-c", cwd)
	}
	var argv []string
	switch where {
	case "tab":
		argv = append([]string{"window", "new", "--focus=false"}, opts...)
		if s := os.Getenv("REX_SESSION"); s != "" {
			argv = append(argv, "-s", s)
		}
		if title != "" {
			argv = append(argv, title)
		}
	case "split":
		argv = append([]string{"split", "--focus=false"}, opts...)
		if blk := os.Getenv("REX_BLOCK"); blk != "" {
			argv = append(argv, "-b", blk)
		}
		if title != "" {
			argv = append(argv, "--label", title)
		}
	default:
		argv = append([]string{"new"}, opts...)
		if title != "" {
			argv = append(argv, title)
		}
	}
	if cmd != "" {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		argv = append(argv, "--shell", shell, "--interactive", "--")
		argv = append(argv, shellSplit(cmd)...)
	}
	out, err := b.call(argv...)
	if err != nil {
		return nil, err
	}
	var v any
	if strings.TrimSpace(out) != "" {
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			return nil, BackendErr("rex: bad JSON from creation: %v", err)
		}
	}
	native := rexBlockID(v)
	if native == "" {
		return nil, BackendErr("rex: creation result carries no block ID (untested backend; check rex --json output)")
	}
	return &Pane{ID: "rex:" + native, Backend: "rex", Native: native, Title: title}, nil
}

// shellSplit splits a command line, honouring single quotes, double quotes and backslashes.
func shellSplit(s string) []string {
	var words []string
	var cur strings.Builder
	in, started := rune(0), false
	esc := false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\' && in != '\'':
			esc, started = true, true
		case in != 0:
			if r == in {
				in = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			in, started = r, true
		case r == ' ' || r == '\t':
			if started {
				words = append(words, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if started {
		words = append(words, cur.String())
	}
	return words
}

func (b *RexBackend) SendText(p *Pane, text string) error {
	if err := CheckText(text); err != nil {
		return err
	}
	_, err := b.call("send", "-b", p.Native, text)
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
	_, err := b.call("block", "rename", p.Native, label)
	return err
}

func (b *RexBackend) NativeStatus(*Pane) string    { return "" }
func (b *RexBackend) ServerRef() map[string]string { return map[string]string{} }
