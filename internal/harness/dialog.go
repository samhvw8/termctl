package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/samhvw8/termctl/internal/term"
)

// Option is one numbered choice of a dialog. Persist marks a choice that is not known to grant only this
// one request ("Yes", "Yes, run command", "No..."): "always allow", "don't ask again", a switch to a mode
// that stops asking, and any wording not seen before. Answering it needs --persist.
type Option struct {
	N       int    `json:"n"`
	Text    string `json:"text"`
	Persist bool   `json:"persist"`
}

// Dialog is a permission or approval prompt on a harness's screen. Fingerprint is a short hash of the
// question, body and option texts; it stays the same while the same dialog is on screen, whatever is selected.
// Truncated: the rule above the request is not on screen, so the request's start is unread and the
// fingerprint does not cover it.
type Dialog struct {
	Question    string   `json:"question"`
	Body        string   `json:"body"`
	Options     []Option `json:"options"`
	Selected    int      `json:"selected"`
	Fingerprint string   `json:"fingerprint"`
	Truncated   bool     `json:"truncated,omitempty"`
}

// Option returns choice n, or nil.
func (d *Dialog) Option(n int) *Option {
	for i := range d.Options {
		if d.Options[i].N == n {
			return &d.Options[i]
		}
	}
	return nil
}

var denyRe = regexp.MustCompile(`(?i)^no\b`)

// DenyOption is the first choice that starts with "No", or 0 when there is none.
func (d *Dialog) DenyOption() int {
	for _, o := range d.Options {
		if denyRe.MatchString(o.Text) {
			return o.N
		}
	}
	return 0
}

// dialogSpec says how one harness draws a numbered dialog.
type dialogSpec struct {
	footer   *regexp.Regexp // a line under the options that only a dialog shows
	skip     *regexp.Regexp // body lines that are not part of the request, such as tips
	question *regexp.Regexp // when set, only a question that matches is a permission dialog
	asks     *regexp.Regexp // when set, a body that matches also makes it a permission dialog
	// wrapped: the question may wrap onto lines above its "?" line, up to a blank line or a rule (Claude
	// Code always draws one of these between the request and the question).
	wrapped bool
}

var (
	optionRe   = regexp.MustCompile(`^\s*([>❯›])?\s*(\d{1,2})\.\s+(\S.*)$`)
	dashRuleRe = regexp.MustCompile(`^[╌┄-]{10,}$`)
	// oneShotRe is the whole text of a choice known to grant this one request only, or nothing: "Yes",
	// "Yes, run command", "Yes, allow access", "Yes, proceed", and every "No..." or cancel. Calibrated on
	// Claude Code 2.1.326 and Antigravity CLI 1.5.10. Any other choice counts as persist, so wording a
	// harness adds later ("Yes, and auto-accept edits") needs --persist until it is listed here.
	oneShotRe = regexp.MustCompile(`(?i)^(yes|yes, (run (this |the )?command|proceed|allow( access)?( once)?|allow (this )?once)|allow( once)?|(no|cancel|deny|reject|exit)\b.*)$`)

	claudeDialog = dialogSpec{footer: regexp.MustCompile(`(?i)^esc to cancel`), skip: regexp.MustCompile(`^Tip: `), wrapped: true}
	// agy's "↑/↓ Navigate" footer is under every numbered picker (a model choice too), so only a question
	// that asks for permission counts: "Run this command?", "Allow access to this file?".
	agyDialog = dialogSpec{footer: regexp.MustCompile(`^↑/↓ Navigate`),
		question: regexp.MustCompile(`(?i)^(run this command|allow\b.*|.*\b(permission|access)\b.*)\?$`),
		asks:     regexp.MustCompile(`(?i)requesting permission`)}
)

func claudeReadDialog(lines []string) *Dialog { return readDialog(lines, claudeDialog) }
func agyReadDialog(lines []string) *Dialog    { return readDialog(lines, agyDialog) }

const footerReach = 6 // the footer is among the last lines of the screen

// readDialog finds the footer near the bottom, the numbered options above it with exactly one
// selected, the question ending in "?" right above option 1, and the request above that up to a rule. A
// request whose rule is not on screen is read as far as the screen goes, and marked Truncated.
func readDialog(lines []string, spec dialogSpec) *Dialog {
	footer, seen := -1, 0
	for i := len(lines) - 1; i >= 0 && seen < footerReach; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			continue
		}
		seen++
		if spec.footer.MatchString(t) {
			footer = i
			break
		}
	}
	if footer < 0 {
		return nil
	}
	first := -1
	for i := footer - 1; i >= 0 && i >= footer-40; i-- {
		if m := optionRe.FindStringSubmatch(lines[i]); m != nil && m[2] == "1" {
			first = i
			break
		}
	}
	if first < 0 {
		return nil
	}
	d := &Dialog{}
	marks := 0
	for _, ln := range lines[first:footer] {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		if m := optionRe.FindStringSubmatch(ln); m != nil && m[2] == fmt.Sprint(len(d.Options)+1) {
			o := Option{N: len(d.Options) + 1, Text: strings.TrimSpace(m[3])}
			if m[1] != "" {
				d.Selected = o.N
				marks++
			}
			d.Options = append(d.Options, o)
			continue
		}
		last := &d.Options[len(d.Options)-1]
		last.Text += " " + t
	}
	if len(d.Options) < 2 || marks != 1 {
		return nil
	}
	q := first - 1
	for q >= 0 && strings.TrimSpace(lines[q]) == "" {
		q--
	}
	if q < 0 || !strings.HasSuffix(strings.TrimSpace(lines[q]), "?") {
		return nil
	}
	top := q
	for spec.wrapped && top > 0 {
		t := strings.TrimSpace(lines[top-1])
		if t == "" || ruleRe.MatchString(t) || dashRuleRe.MatchString(t) {
			break
		}
		top--
	}
	var question []string
	for _, ln := range lines[top : q+1] {
		question = append(question, strings.TrimSpace(ln))
	}
	d.Question = strings.Join(question, " ")
	var body []string
	d.Truncated = true
	for i := top - 1; i >= 0; i-- {
		t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "│")) // Claude Code marks a long command with │
		if ruleRe.MatchString(t) {
			d.Truncated = false
			break
		}
		if t == "" || dashRuleRe.MatchString(t) || (spec.skip != nil && spec.skip.MatchString(t)) {
			continue
		}
		body = append([]string{t}, body...)
	}
	d.Body = strings.Join(body, "\n")
	if spec.question != nil && !spec.question.MatchString(d.Question) && (spec.asks == nil || !spec.asks.MatchString(d.Body)) {
		return nil // a picker that asks for no permission, such as a model choice
	}
	for i := range d.Options {
		d.Options[i].Persist = !oneShotRe.MatchString(squash(d.Options[i].Text))
	}
	d.Fingerprint = fingerprint(d)
	return d
}

func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

// unspaced drops all whitespace: a terminal wraps a line anywhere, inside a word too, and the capture
// cannot tell a wrap from a space.
func unspaced(s string) string { return strings.Join(strings.Fields(s), "") }

// fingerprint ignores whitespace, so a redraw that wraps lines differently (a re-tiled pane) keeps the same
// print. Two requests that differ only in where their spaces are share it.
func fingerprint(d *Dialog) string {
	parts := []string{unspaced(d.Question), unspaced(d.Body)}
	for _, o := range d.Options {
		parts = append(parts, unspaced(o.Text))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:4])
}

// AnswerCommand is the command a lead runs to answer, with a placeholder for the choice.
func AnswerCommand(target string, d *Dialog) string {
	return fmt.Sprintf("harness-ops task answer %s <n> --expect %s", target, d.Fingerprint)
}

// DialogLine is a dialog on one line, for a notification.
func DialogLine(target string, d *Dialog) string {
	var opts []string
	persist := false
	for _, o := range d.Options {
		s := fmt.Sprintf("%d. %s", o.N, squash(o.Text))
		if o.Persist {
			s += " [persist]"
			persist = true
		}
		opts = append(opts, s)
	}
	cmd := "answer: " + AnswerCommand(target, d)
	if persist {
		cmd += " (add --persist for a [persist] choice)"
	}
	cmd += fmt.Sprintf("; deny: harness-ops task deny %s --expect %s", target, d.Fingerprint)
	if d.Truncated {
		cmd += "; the request is longer than the screen and its start is not read: task answer refuses it"
	}
	return fmt.Sprintf("DIALOG %s: %s | %s | %s | %s", d.Fingerprint, squash(d.Question), term.Cut(squash(d.Body), 300), strings.Join(opts, " "), cmd)
}

// DialogText is a dialog for a person or agent to read, one part per line.
func DialogText(target string, d *Dialog) string {
	var b strings.Builder
	fmt.Fprintf(&b, "question:    %s\n", d.Question)
	for i, ln := range strings.Split(d.Body, "\n") {
		if i == 0 {
			fmt.Fprintf(&b, "body:        %s\n", ln)
		} else {
			fmt.Fprintf(&b, "             %s\n", ln)
		}
	}
	b.WriteString("options:\n")
	for _, o := range d.Options {
		mark, persist := " ", ""
		if o.N == d.Selected {
			mark = ">"
		}
		if o.Persist {
			persist = " [persist]"
		}
		fmt.Fprintf(&b, "  %s %d. %s%s\n", mark, o.N, squash(o.Text), persist)
	}
	fmt.Fprintf(&b, "fingerprint: %s\n", d.Fingerprint)
	if d.Truncated {
		b.WriteString("truncated:   the request is longer than the screen and its start is not read: task answer refuses it, task deny works\n")
	}
	fmt.Fprintf(&b, "answer:      %s   (a [persist] choice also needs --persist)\n", AnswerCommand(target, d))
	fmt.Fprintf(&b, "deny:        harness-ops task deny %s --expect %s\n", target, d.Fingerprint)
	return b.String()
}
