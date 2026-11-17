package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/samhvw8/termctl/internal/term"
)

// Option is one numbered choice of a dialog. Persist marks a choice that grants more than this one
// request: "always allow", "don't ask again", or a switch to a mode that stops asking.
type Option struct {
	N       int    `json:"n"`
	Text    string `json:"text"`
	Persist bool   `json:"persist"`
}

// Dialog is a permission or approval prompt on a harness's screen. Fingerprint is a short hash of the
// question, body and option texts; it stays the same while the same dialog is on screen, whatever is selected.
type Dialog struct {
	Question    string   `json:"question"`
	Body        string   `json:"body"`
	Options     []Option `json:"options"`
	Selected    int      `json:"selected"`
	Fingerprint string   `json:"fingerprint"`
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
	footer  *regexp.Regexp // a line under the options that only a dialog shows
	skip    *regexp.Regexp // body lines that are not part of the request, such as tips
	persist *regexp.Regexp
}

var (
	optionRe   = regexp.MustCompile(`^\s*([>❯›])?\s*(\d{1,2})\.\s+(\S.*)$`)
	dashRuleRe = regexp.MustCompile(`^[╌┄-]{10,}$`)
	// Calibrated on Claude Code 2.1.326 and Antigravity CLI 1.5.10. Every choice that widens permission
	// past this one request counts, including agy's "always allow in this conversation" and Claude's
	// "switch to auto mode": the lead must say --persist for those.
	persistRe = regexp.MustCompile(`(?i)always|don['’]t ask again|do not ask again|allow all|during this session|for this session|persist|auto mode|bypass`)

	claudeDialog = dialogSpec{footer: regexp.MustCompile(`(?i)^esc to cancel`), skip: regexp.MustCompile(`^Tip: `), persist: persistRe}
	agyDialog    = dialogSpec{footer: regexp.MustCompile(`^↑/↓ Navigate`), persist: persistRe}
)

func claudeReadDialog(lines []string) *Dialog { return readDialog(lines, claudeDialog) }
func agyReadDialog(lines []string) *Dialog    { return readDialog(lines, agyDialog) }

const (
	footerReach = 6  // the footer is among the last lines of the screen
	bodyReach   = 20 // the request is at most this many lines above the question
)

// readDialog finds the footer near the bottom, the numbered options above it with exactly one
// selected, the question ending in "?" right above option 1, and the request above that up to a rule.
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
	d.Question = strings.TrimSpace(lines[q])
	var body []string
	for i := q - 1; i >= 0 && i >= q-bodyReach; i-- {
		t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "│")) // Claude Code marks a long command with │
		if ruleRe.MatchString(t) {
			break
		}
		if t == "" || dashRuleRe.MatchString(t) || (spec.skip != nil && spec.skip.MatchString(t)) {
			continue
		}
		body = append([]string{t}, body...)
	}
	d.Body = strings.Join(body, "\n")
	for i := range d.Options {
		d.Options[i].Persist = spec.persist.MatchString(d.Options[i].Text)
	}
	d.Fingerprint = fingerprint(d)
	return d
}

func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

// fingerprint ignores spacing, so a redraw that wraps lines differently keeps the same print.
func fingerprint(d *Dialog) string {
	parts := []string{squash(d.Question), squash(d.Body)}
	for _, o := range d.Options {
		parts = append(parts, squash(o.Text))
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
	fmt.Fprintf(&b, "answer:      %s   (a [persist] choice also needs --persist)\n", AnswerCommand(target, d))
	fmt.Fprintf(&b, "deny:        harness-ops task deny %s --expect %s\n", target, d.Fingerprint)
	return b.String()
}
