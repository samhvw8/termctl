package harness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/samhvw8/termctl/internal/term"
)

// TrustRoots is the user's standing grant to accept a harness's folder-trust prompt at startup: folders
// under one of Roots may be trusted by spawn and task new with --accept-trust. Nothing else may: the file
// is written by the user, never by an agent, and with no file nothing is accepted.
type TrustRoots struct {
	File  string   // where the roots were read, for messages
	Roots []string // absolute patterns; * matches within one path element (filepath.Match)
}

// ParseTrustRoots reads one pattern per line; blank lines and lines starting with # are skipped, and a
// leading ~ is the home folder. A pattern that would match the home folder, or a folder above it, is
// refused: it would trust every repo a lead can reach, including one just cloned.
func ParseTrustRoots(file, text, home string) (*TrustRoots, error) {
	t := &TrustRoots{File: file}
	for i, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		pat := ln
		if pat == "~" || strings.HasPrefix(pat, "~/") {
			pat = home + pat[1:]
		}
		if !filepath.IsAbs(pat) {
			return nil, term.UsageErr("%s:%d: %q is not an absolute folder (or ~/...)", file, i+1, ln)
		}
		pat = filepath.Clean(pat)
		if pat == "/" {
			return nil, term.UsageErr("%s:%d: / is too broad, it would trust every folder", file, i+1)
		}
		if _, err := filepath.Match(pat, ""); err != nil {
			return nil, term.UsageErr("%s:%d: %q: %v", file, i+1, ln, err)
		}
		for dir := filepath.Clean(home); ; dir = filepath.Dir(dir) {
			if trustRootOf([]string{pat}, dir) != "" {
				return nil, term.UsageErr("%s:%d: %q matches %s: too broad, it would trust every folder below it", file, i+1, ln, dir)
			}
			if dir == filepath.Dir(dir) {
				break
			}
		}
		t.Roots = append(t.Roots, pat)
	}
	return t, nil
}

// LoadTrustRoots reads the roots file, or returns nil when there is none.
func LoadTrustRoots(file, home string) (*TrustRoots, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, term.BackendErr("trust roots: %v", err)
	}
	return ParseTrustRoots(file, string(data), home)
}

// Allows returns the root a folder is in or under, or "". The folder is also read with its symlinks
// resolved, so /tmp and /private/tmp on macOS name the same place.
func (t *TrustRoots) Allows(dir string) string {
	if t == nil || dir == "" {
		return ""
	}
	dir = filepath.Clean(ExpandHome(dir))
	if root := trustRootOf(t.Roots, dir); root != "" {
		return root
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
		return trustRootOf(t.Roots, real)
	}
	return ""
}

// trustRootOf returns the first pattern that matches dir or one of the folders above it.
func trustRootOf(roots []string, dir string) string {
	if !filepath.IsAbs(dir) {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(dir, "/"), "/")
	for _, root := range roots {
		n := len(strings.Split(strings.TrimPrefix(root, "/"), "/"))
		if root == "/" || n > len(parts) {
			continue
		}
		if ok, _ := filepath.Match(root, "/"+strings.Join(parts[:n], "/")); ok {
			return root
		}
	}
	return ""
}

// TrustScreen is a folder-trust prompt on screen: the line indexes of its two choices, and which one
// is selected.
type TrustScreen struct {
	No, Yes     int
	YesSelected bool
}

var (
	trustNoLine  = regexp.MustCompile(`^\s*(❯\s*)?(\d+\.\s*)?No, exit\s*$`)
	trustYesLine = regexp.MustCompile(`^\s*(❯\s*)?(\d+\.\s*)?Yes, I trust this folder\s*$`)
)

// readClaudeTrust reads Claude Code's folder-trust prompt (real screen: TestStartupPromptReadsTheRealTrustScreen):
// "No, exit" directly above "Yes, I trust this folder", the selection mark on exactly one of them, and the
// footer "Enter to confirm" below. Anything else is no trust screen, so no key is pressed.
func readClaudeTrust(lines []string) *TrustScreen {
	tail := lines
	if len(tail) > 20 {
		tail = tail[len(tail)-20:]
	}
	no, yes, footer := -1, -1, -1
	for i, ln := range tail {
		switch {
		case trustNoLine.MatchString(ln):
			if no >= 0 {
				return nil
			}
			no = i
		case trustYesLine.MatchString(ln):
			if yes >= 0 {
				return nil
			}
			yes = i
		case strings.Contains(ln, "Enter to confirm"):
			footer = i
		}
	}
	if no < 0 || yes != no+1 || footer < yes {
		return nil
	}
	marks := 0
	for _, ln := range tail {
		if strings.Contains(ln, "❯") {
			marks++
		}
	}
	noSel, yesSel := strings.Contains(tail[no], "❯"), strings.Contains(tail[yes], "❯")
	if marks != 1 || noSel == yesSel {
		return nil
	}
	return &TrustScreen{No: no, Yes: yes, YesSelected: yesSel}
}

func (o *Ops) readTrust(p *term.Pane, prof *Profile) (*TrustScreen, error) {
	lines, err := o.Backend.Capture(p, screenLines(p))
	if err != nil {
		return nil, err
	}
	return prof.ReadTrust(lines), nil
}

// acceptTrust answers a folder-trust prompt at startup with "Yes, I trust this folder", only when both the
// folder the harness was started in and the folder the backend reports for the pane are under one of the
// user's trust roots. Like answer, it moves the selection with the arrow keys, checks it on screen, and only
// then presses Enter; it never types a digit.
func (o *Ops) acceptTrust(p *term.Pane, prof *Profile, cwd string, roots *TrustRoots) error {
	if roots == nil || len(roots.Roots) == 0 {
		return term.Errf(term.CodeGuard, "--accept-trust: no trust roots in %s, so the prompt on %s stays for the user", roots.fileOr(), p.Address())
	}
	root := roots.Allows(cwd)
	if root == "" {
		return term.Errf(term.CodeGuard, "--accept-trust: %s is not under a trust root in %s; the prompt on %s stays for the user", cwd, roots.File, p.Address())
	}
	if folder := o.paneFolder(p); folder != "" && roots.Allows(folder) == "" {
		return term.Errf(term.CodeGuard, "--accept-trust: %s runs in %s, which is not under a trust root in %s; the prompt stays for the user", p.Address(), folder, roots.File)
	}
	s, err := o.readTrust(p, prof)
	if err != nil {
		return err
	}
	if s == nil {
		return term.Errf(term.CodeDialog, "no folder-trust prompt on %s that termctl can read; nothing was pressed", p.Address())
	}
	e := o.entry("trust", p)
	e.Text = fmt.Sprintf("Yes, I trust this folder: %s (root %s)", cwd, root)
	defer func() { o.Journal.Record(e) }()
	fail := func(err error) error { e.Error = err.Error(); return err }
	if !s.YesSelected {
		key := "down"
		if s.Yes < s.No {
			key = "up"
		}
		if err := o.Backend.SendKey(p, key); err != nil {
			return fail(err)
		}
		for waited := 0.0; ; waited += poll {
			o.Clock.Sleep(poll)
			now, err := o.readTrust(p, prof)
			if err != nil {
				return fail(err)
			}
			if now == nil {
				return fail(term.BackendErr("the trust prompt on %s changed while choosing; Enter was not pressed", p.Address()))
			}
			if now.YesSelected {
				break
			}
			if waited >= selectLook {
				return fail(term.BackendErr("the selection on %s is not on \"Yes, I trust this folder\" %gs after the arrow key; Enter was not pressed", p.Address(), selectLook))
			}
		}
	}
	now, err := o.readTrust(p, prof)
	if err != nil {
		return fail(err)
	}
	if now == nil || !now.YesSelected {
		return fail(term.BackendErr("the trust prompt on %s changed before Enter; Enter was not pressed", p.Address()))
	}
	if err := o.Backend.SendKey(p, "enter"); err != nil {
		return fail(err)
	}
	for waited := 0.0; ; waited += poll {
		o.Clock.Sleep(poll)
		now, err := o.readTrust(p, prof)
		if err != nil {
			return fail(err)
		}
		if now == nil {
			return nil
		}
		if waited >= answerLook {
			return fail(term.BackendErr("the trust prompt is still on %s %gs after Enter", p.Address(), answerLook))
		}
	}
}

func hasCode(err error, code int) bool {
	var e *term.Error
	return errors.As(err, &e) && e.Code == code
}

func (t *TrustRoots) fileOr() string {
	if t == nil || t.File == "" {
		return "the trust roots file"
	}
	return t.File
}

// paneFolder is the folder the backend reports for a pane now, or "".
func (o *Ops) paneFolder(p *term.Pane) string {
	panes, err := o.Backend.ListPanes()
	if err != nil {
		return ""
	}
	for _, q := range panes {
		if q.ID == p.ID {
			return q.Folder
		}
	}
	return ""
}
