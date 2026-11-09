package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/samhvw8/termctl/internal/harness"
	"github.com/samhvw8/termctl/internal/term"
)

// Options select the terminal backend.
type Options struct{ Backend, Socket string }

// App is the command line with its seams for tests.
type App struct {
	Out, Err    io.Writer
	Clock       harness.Clock
	Inbox       string
	OpenBackend func(o *Options, target string) (term.Backend, error)
	OpenSender  func(o *Options, b term.Backend) (*term.Pane, error)
}

// NewApp wires the real terminal.
func NewApp() *App {
	return &App{Out: os.Stdout, Err: os.Stderr, OpenBackend: defaultBackend, OpenSender: defaultSender}
}

var itermUUID = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

func backendName(o *Options, target string) (string, error) {
	if o.Backend != "" {
		return o.Backend, nil
	}
	if name, _, ok := strings.Cut(target, ":"); ok && term.IsBackend(name) {
		return name, nil
	}
	if itermUUID.MatchString(target) { // iTerm2 session IDs stand alone: a reply from another terminal still routes
		return "iterm2", nil
	}
	name := term.DetectEnv()
	if name == "" {
		return "", term.UsageErr("no terminal backend detected; pass --backend")
	}
	return name, nil
}

func defaultBackend(o *Options, target string) (term.Backend, error) {
	name, err := backendName(o, target)
	if err != nil {
		return nil, err
	}
	return term.Make(name, o.Socket, "")
}

// defaultSender is the caller's pane: on the selected backend when it is ours, else on the env's backend.
func defaultSender(o *Options, b term.Backend) (*term.Pane, error) {
	name := term.DetectEnv()
	if name == "" {
		return nil, nil
	}
	if name == b.Name() {
		return b.SelfPane()
	}
	socket := ""
	if name == "tmux" {
		socket = o.Socket
	}
	other, err := term.Make(name, socket, "")
	if err != nil {
		return nil, err
	}
	return other.SelfPane()
}

func (a *App) fail(prog string, err error) int {
	var e *term.Error
	if errors.As(err, &e) {
		fmt.Fprintf(a.Err, "%s: %s\n", prog, e.Msg)
		return e.Code
	}
	fmt.Fprintf(a.Err, "%s: %v\n", prog, err)
	return 1
}

func idWidth(panes []*term.Pane) int {
	w := 9
	for _, p := range panes {
		if n := len([]rune(p.Short())) + 1; n > w {
			w = n
		}
	}
	return w
}
