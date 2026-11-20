package harness

import (
	"strings"

	"github.com/samhvw8/termctl/internal/term"
)

// Pressed is what Keys did: the pane, and the keys pressed on it by their canonical names.
type Pressed struct {
	Pane *term.Pane
	Keys []string
}

// Keys presses keys on a pane the caller started (spawn or task new), an attached scope, or the worker of a
// task it owns: a menu, a picker, a mode switch (shift-tab), anything a harness reads as keys. It is refused
// on your own pane (exit 2), on a pane you do not supervise (exit 6: termctl send-key is the raw tool),
// while a permission dialog is on screen (exit 7: answer and deny check what they choose), and, unless
// force, while a calibrated harness shows text on its input line (exit 4). Every key is checked against the
// backend before the first is pressed.
func (t *Tasks) Keys(target string, keys []string, force bool) (*Pressed, error) {
	if len(keys) == 0 {
		return nil, term.UsageErr("key needs at least one key")
	}
	for _, k := range keys { // a misspelt key is a usage error before any pane is looked up
		if _, err := term.NormalizeKey(k); err != nil {
			return nil, err
		}
	}
	b, p, _, xl, err := t.leaseTarget(target, true)
	if err != nil {
		return nil, err
	}
	names, err := term.CheckKeys(b, keys)
	if err != nil {
		return nil, err
	}
	lines, err := b.Capture(p, screenLines(p))
	if err != nil {
		return nil, err
	}
	prof := ForProcess(p.Process)
	if prof.ReadDialog != nil {
		if d := readPaneDialog(b, p, prof, lines); d != nil {
			return nil, term.Errf(term.CodeDialog, "a permission dialog (%s) waits on %s: %q. Use harness-ops answer or deny, which check what they choose; keys could pick a choice unread", d.Fingerprint, p.Address(), d.Question)
		}
	}
	if prof.Calibrated && !force {
		if d := prof.ReadInput(lines); d != nil && *d != "" {
			return nil, term.Errf(term.CodeDraft, "%s has text on its input line: %q; keys would act on it. Use --force to press them anyway", p.Address(), term.Cut(*d, 80))
		}
	}
	ops := t.leaseOps(b, t.Sender, xl)
	e := ops.entry("key", p)
	e.Text = strings.Join(names, " ")
	if force {
		e.Status = "forced"
	}
	var sendErr error
	sent := 0
	for _, k := range names {
		if sendErr = b.SendKey(p, k); sendErr != nil {
			break
		}
		sent++
	}
	if sendErr != nil {
		e.Error = errText(sendErr)
	}
	ops.Journal.Record(e)
	return &Pressed{Pane: p, Keys: names[:sent]}, sendErr
}

func notYours(what string) error {
	return term.Errf(term.CodeGuard, "%s is not a pane you started or attached; harness-ops key acts only on those. termctl send-key is the raw tool for any pane", what)
}
