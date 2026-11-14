// Package harness is Layer 2: how to talk to coding-agent TUIs on top of the terminal layer.
package harness

import (
	"regexp"
	"strings"
)

var (
	ruleRe              = regexp.MustCompile(`^─{10,}(?: .+ ─+)?$`) // a named session ends the top rule with "── name ─"
	claudePlaceholder   = regexp.MustCompile(`^Try ".*"$`)          // an empty Claude Code box shows a suggestion
	claudeWorking       = regexp.MustCompile(`(?m)^\s*\S\s+\S+…\s*\(`)
	opencodePlaceholder = regexp.MustCompile(`^Ask anything….*$`)
	ompPrompt           = regexp.MustCompile(`^\s*╰─\s?(.*)$`)
	ompPlaceholder      = regexp.MustCompile(`^[\x{e000}-\x{f8ff}\x{f0000}-\x{10ffff}\s]+to \S`) // an empty omp input can show an icon-led shortcut hint
	genericPrompt       = regexp.MustCompile(`^\s*[>❯›] ?(.*)$`)
	agyPlaceholder      = regexp.MustCompile(`\(shift\+tab to cycle\)$`) // an empty agy box names the permission mode
)

func str(s string) *string { return &s }

// claudeBox finds the input box: the last rule pair whose body starts with ❯.
func claudeBox(lines []string) (int, int, bool) { return ruledBox(lines, "❯", false) }

// ruledBox finds the last pair of rules whose body starts with mark. widest keeps only the widest
// rules on screen: agy draws past messages under shorter rules that also start with its mark.
func ruledBox(lines []string, mark string, widest bool) (int, int, bool) {
	var rules []int
	width := 0
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if !ruleRe.MatchString(t) {
			continue
		}
		n := len([]rune(t))
		if widest && n > width {
			width, rules = n, nil
		}
		if !widest || n == width {
			rules = append(rules, i)
		}
	}
	for k := len(rules) - 1; k > 0; k-- {
		top, bottom := rules[k-1], rules[k]
		if bottom > top+1 && strings.HasPrefix(strings.TrimLeft(lines[top+1], " \t"), mark) {
			return top, bottom, true
		}
	}
	return 0, 0, false
}

// boxText joins the body of an input box, without its mark, one screen line per line.
func boxText(lines []string, top, bottom int, mark string) string {
	body := lines[top+1 : bottom]
	parts := []string{strings.TrimSpace(strings.TrimPrefix(strings.TrimLeft(body[0], " \t"), mark))}
	for _, ln := range body[1:] {
		parts = append(parts, strings.TrimSpace(ln))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func claudeInput(lines []string) *string {
	top, bottom, ok := claudeBox(lines)
	if !ok {
		return nil
	}
	text := boxText(lines, top, bottom, "❯")
	if claudePlaceholder.MatchString(text) {
		return str("")
	}
	return &text
}

// agyInput reads Antigravity CLI's box: "> " between two full-width rules, status and task panel below.
func agyInput(lines []string) *string {
	top, bottom, ok := ruledBox(lines, ">", true)
	if !ok {
		return nil
	}
	text := boxText(lines, top, bottom, ">")
	if agyPlaceholder.MatchString(text) {
		return str("")
	}
	return &text
}

func agyStableView(lines []string) []string {
	if top, _, ok := ruledBox(lines, ">", true); ok {
		return lines[:top]
	}
	return lines
}

// claudeStableView is what is above the input box: the status lines below it tick constantly.
func claudeStableView(lines []string) []string {
	if top, _, ok := claudeBox(lines); ok {
		return lines[:top]
	}
	return lines
}

func ompInput(lines []string) *string {
	for i := len(lines) - 1; i >= 0; i-- {
		if m := ompPrompt.FindStringSubmatch(lines[i]); m != nil {
			text := strings.TrimSpace(m[1])
			if ompPlaceholder.MatchString(text) {
				return str("")
			}
			return &text
		}
	}
	return nil
}

func ompStableView(lines []string) []string {
	if len(lines) <= 3 {
		return nil
	}
	return lines[:len(lines)-3]
}

// opencodeInput reads the box of ┃ lines above a ╹▀ edge. Its last text line is the mode status.
func opencodeInput(lines []string) *string {
	end := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "╹▀") {
			end = i
			break
		}
	}
	if end < 0 {
		return nil
	}
	start := end
	for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "┃") {
		start--
	}
	var text []string
	for _, ln := range lines[start:end] {
		if t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "┃")); t != "" {
			text = append(text, t)
		}
	}
	if len(text) == 0 {
		return nil
	}
	body := strings.Join(text[:len(text)-1], "\n")
	if opencodePlaceholder.MatchString(body) {
		return str("")
	}
	return &body
}

func genericInput(lines []string) *string {
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			last = lines[i]
			break
		}
	}
	if m := genericPrompt.FindStringSubmatch(last); m != nil {
		return str(strings.TrimSpace(m[1]))
	}
	return nil
}

func same(lines []string) []string { return lines }

// Profile says how to recognise a harness and read its screen.
//
// Calibrated means the input-line parser was checked against real screens. A calibrated
// harness whose input line cannot be read is refused; an uncalibrated one warns and proceeds.
type Profile struct {
	Name              string
	Processes         []string
	Command           string
	Slash             bool
	Calibrated        bool
	PromptRequired    bool // idle only when the input line is visible
	StableSeconds     float64
	ReadInput         func([]string) *string // the draft, "" for empty, nil when not on screen
	StableView        func([]string) []string
	BusyPatterns      []*regexp.Regexp
	InterruptKeys     []string
	InterruptVerified bool   // the keys were seen to stop a real turn
	ReadOnlyFlags     string // verified flags for a mode that cannot change files; "" when none is verified
}

// IsBusy looks for a busy pattern near the bottom of the screen.
func (p *Profile) IsBusy(lines []string) bool {
	tail := lines
	if len(tail) > 12 {
		tail = tail[len(tail)-12:]
	}
	text := strings.Join(tail, "\n")
	for _, re := range p.BusyPatterns {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

func simple(name string) *Profile {
	return &Profile{Name: name, Processes: []string{name}, Command: name, Slash: true, StableSeconds: 1.5,
		ReadInput: genericInput, StableView: same, InterruptKeys: []string{"escape"}}
}

// Profiles by name. "generic" covers any other program.
var Profiles = func() map[string]*Profile {
	claude := simple("claude")
	claude.Calibrated, claude.PromptRequired, claude.InterruptVerified = true, true, true
	claude.ReadInput, claude.StableView = claudeInput, claudeStableView
	// Checked on a real session. dontAsk denies what is not listed, without prompting. User settings are
	// excluded because their allow rules would re-open writes. Plan mode cannot be used: it stops at a
	// "ready to execute?" dialog and cannot run the report command, even when that is allowed.
	claude.ReadOnlyFlags = `--setting-sources project --permission-mode dontAsk --disallowedTools Edit Write MultiEdit NotebookEdit ` +
		`--allowedTools Read Grep Glob 'Bash(git status *)' 'Bash(git diff *)' 'Bash(git log *)' 'Bash(git show *)' 'Bash(harness-ops task done *)'`
	claude.BusyPatterns = []*regexp.Regexp{regexp.MustCompile(`(?i)esc to interrupt`), claudeWorking}

	omp := simple("omp")
	omp.Calibrated, omp.PromptRequired, omp.InterruptVerified = true, true, true
	omp.ReadInput, omp.StableView = ompInput, ompStableView
	omp.BusyPatterns = []*regexp.Regexp{regexp.MustCompile(`(?m)^\s*[\x{2800}-\x{28ff}]\s`), regexp.MustCompile(`Working…`)}

	// Input box read on a fresh screen only; not calibrated once a conversation is on screen.
	opencode := simple("opencode")
	opencode.ReadInput = opencodeInput

	// Checked on Antigravity CLI 1.5.9. Esc stops a turn and puts queued messages back on the input line.
	// While a message is queued the status line says so instead of "esc to cancel".
	agy := simple("agy")
	agy.Calibrated, agy.PromptRequired, agy.InterruptVerified = true, true, true
	agy.ReadInput, agy.StableView = agyInput, agyStableView
	agy.BusyPatterns = []*regexp.Regexp{regexp.MustCompile(`esc to cancel`), regexp.MustCompile(`Press up to edit queued messages`),
		regexp.MustCompile(`(?m)^\s*[\x{2800}-\x{28ff}]\s`)}

	aider := simple("aider")
	aider.InterruptKeys = []string{"ctrl-c"}

	generic := simple("generic")
	generic.Processes, generic.Command, generic.PromptRequired = nil, "", true
	generic.InterruptKeys = []string{"ctrl-c"}

	return map[string]*Profile{"claude": claude, "omp": omp, "opencode": opencode, "codex": simple("codex"),
		"gemini": simple("gemini"), "agy": agy, "aider": aider, "pi": simple("pi"), "generic": generic}
}()

// ForProcess picks the profile of a pane's main program.
func ForProcess(process string) *Profile {
	for _, p := range Profiles {
		for _, name := range p.Processes {
			if name == process {
				return p
			}
		}
	}
	return Profiles["generic"]
}
