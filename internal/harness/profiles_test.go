package harness

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/samhvw8/termctl/internal/term"
)

// Parsers are checked against screens read from live panes.
var rule = strings.Repeat("─", 80)

var claudeIdle = []string{"✻ Churned for 50s · done 7:22 PM", "", rule, "❯", rule,
	"  ◆ Opus 5.5 · Med | ◎ STE Lite | # someone", "  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← 1 agent"}

var ompIdle = []string{" ※ recap: something", "      󰏘 A title", " 󰵗   Gemini   ~/w/app   main  󰙺 6.12 ──9%──󰁨──", "╰─"}

func draftOf(t *testing.T, name string, lines []string) *string {
	t.Helper()
	return Profiles[name].ReadInput(lines)
}

func eq(t *testing.T, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("got %v, want %q", deref(got), want)
	}
}

func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func TestEveryMainProcessHasAProfile(t *testing.T) {
	for _, n := range term.MainProcesses {
		if _, ok := Profiles[n]; !ok {
			t.Error(n)
		}
	}
	for _, n := range []string{"claude", "omp", "opencode", "codex", "gemini"} {
		if p := Profiles[n]; p == nil || !p.Slash {
			t.Error(n)
		}
	}
}

func TestForProcessFallsBackToGeneric(t *testing.T) {
	if ForProcess("zsh").Name != "generic" || ForProcess("claude").Name != "claude" {
		t.Fatal()
	}
}

func TestClaudeInput(t *testing.T) {
	eq(t, draftOf(t, "claude", claudeIdle), "")
	eq(t, draftOf(t, "claude", []string{"output", rule, "❯ half typed thought", rule, "  status"}), "half typed thought")
	eq(t, draftOf(t, "claude", []string{rule, "❯ first line", "  second line", rule}), "first line\nsecond line")
	eq(t, draftOf(t, "claude", []string{"❯ an earlier message", "⏺ a reply", rule, "❯", rule}), "")
	eq(t, draftOf(t, "claude", []string{rule, `❯ Try "fix typecheck errors"`, rule}), "")
	if draftOf(t, "claude", []string{"just output", "more"}) != nil {
		t.Fatal("input not visible")
	}
}

// A named Claude Code session prints its name in the top rule of the input box.
var claudeNamedIdle = []string{
	"          - speaks as                              1 new message (click) ↓",
	"                                                                                                              533030 tokens",
	strings.Repeat("─", 114) + " voxtract ─",
	"❯ ",
	strings.Repeat("─", 125),
	"  ◆ Opus 5.5 · High | ◎ STE Lite | # voxtract                                                          ◷ 12h26m | v 2.1.323",
	"  ▣ voxtract | ▤ samhvw8/voxtract | ⎇ main ● ↑23 | ± +15884 -395",
	"  ◔ ▰▰▰▰▰▰▱▱▱▱▱▱ 53% · 533k/1M · 467k left | ∿ ▁▂▅▇█ +8.5k | ⇲ 434k to compact · ~19 turns | ≈ 59:56 · 99% hit",
	"                                                                                                    $ $150 | ∑ $14.10 today",
	"  ◷ ▀▀▀▀▀▀▀▀▀▀▀▀ 7d 76% · resets in 1d14h at Sun 21:00 ⇣1%",
	"  ⏵⏵ auto mode on (shift+tab to cycle) · ← 1 agent",
}

func TestClaudeInputInANamedSession(t *testing.T) {
	eq(t, draftOf(t, "claude", claudeNamedIdle), "")
	typed := append([]string{}, claudeNamedIdle...)
	typed[3] = "❯ half typed"
	eq(t, draftOf(t, "claude", typed), "half typed")
	if got := Profiles["claude"].StableView(claudeNamedIdle); len(got) != 2 {
		t.Fatalf("stable view kept %d lines, want the 2 above the box", len(got))
	}
}

func TestClaudeBusyFromRealTurns(t *testing.T) {
	p := Profiles["claude"]
	for _, line := range []string{"✽ Gesticulating… (6s · ↓ 243 tokens · thought for 3s)",
		"· Forming… (running Stop hook · 12s · ↓ 329 tokens)", "· Pouncing… (6s · ↓ 82 tokens · thinking)", "✶ Thinking… (12s · esc to interrupt)"} {
		if !p.IsBusy([]string{line, rule, "❯", rule, "  status"}) {
			t.Error(line)
		}
	}
	if p.IsBusy(claudeIdle) {
		t.Error("idle read as busy")
	}
}

func TestClaudeStabilityIgnoresStatusLines(t *testing.T) {
	p := Profiles["claude"]
	b := append(append([]string{}, claudeIdle[:len(claudeIdle)-2]...), "  ◆ changed clock", "  ◷ changed")
	if !reflect.DeepEqual(p.StableView(claudeIdle), p.StableView(b)) {
		t.Fatal()
	}
}

func TestOmp(t *testing.T) {
	p := Profiles["omp"]
	eq(t, p.ReadInput(ompIdle), "")
	draft := append(append([]string{}, ompIdle[:3]...), "╰─ a draft here")
	eq(t, p.ReadInput(draft), "a draft here")
	// Captured from omp 18.16.0: a shortcut hint that an empty input line shows for a moment.
	hint := append(append([]string{}, ompIdle[:3]...), "╰─ \U000f0636 \U000f0312 to change thinking effort")
	eq(t, p.ReadInput(hint), "")
	if !p.IsBusy([]string{"  󱊷 Read stats.go", " ⠧ 3m  󰪣 Gemini ─󰁨18%─1M─", "╰─"}) || !p.IsBusy([]string{"  󱊷 Working…", "╰─"}) || p.IsBusy(ompIdle) {
		t.Fatal("busy")
	}
}

func TestGenericPrompt(t *testing.T) {
	p := Profiles["generic"]
	eq(t, p.ReadInput([]string{"out", "> "}), "")
	eq(t, p.ReadInput([]string{"out", "> typed"}), "typed")
	if p.ReadInput([]string{"out", "no prompt here"}) != nil {
		t.Fatal()
	}
}

func TestOpencode(t *testing.T) {
	p := Profiles["opencode"]
	empty := []string{"┃", `┃  Ask anything… "What is the tech stack of this project?"`, "┃", "┃  Build · GPT · medium", "╹▀▀▀▀▀▀▀▀", "…/app  shift+tab agents"}
	eq(t, p.ReadInput(empty), "")
	eq(t, p.ReadInput([]string{"┃", "┃  calibration draft text", "┃", "┃  Build · GPT · medium", "╹▀▀▀▀▀▀▀▀"}), "calibration draft text")
	if p.ReadInput([]string{"nothing here"}) != nil {
		t.Fatal()
	}
}

func TestCalibrationMarks(t *testing.T) {
	if !Profiles["claude"].Calibrated || !Profiles["omp"].Calibrated || Profiles["generic"].Calibrated {
		t.Fatal()
	}
	if !Profiles["claude"].InterruptVerified || !Profiles["omp"].InterruptVerified || Profiles["opencode"].InterruptVerified {
		t.Fatal()
	}
}

// agy screens, read from a live Antigravity CLI 1.5.9 pane 140 columns wide. Past messages sit under a
// 60-column rule; the input box rules span the pane, and a task panel can add a third rule below.
var (
	agyRule  = strings.Repeat("─", 140)
	agyShort = strings.Repeat("─", 60)
	agyPanel = []string{"  ● [15:09:35] sleep 120 running", agyRule}
)

func agyScreen(box []string, status string, panel bool) []string {
	s := []string{agyShort, "> Read BRIEF.md in this folder and do everything it asks.", "",
		"○ Thought for 2m 53s (The presence of an outdated brief...)", "● Explored 1 file (BRIEF.md)", "",
		agyShort, "> [agent-ops from voxtract (claude) id=4668AAA8 folder=~/w/voxtract] The brief is", "  at this absolute path.", ""}
	s = append(s, box...)
	if panel {
		s = append(s, agyPanel...)
	}
	return append(s, status)
}

func agyBox(body ...string) []string {
	return append(append([]string{agyRule}, body...), agyRule)
}

func TestAgyInput(t *testing.T) {
	idle := "? for shortcuts                                                                 auto · Gemini 4 Carbon · low"
	eq(t, draftOf(t, "agy", agyScreen(agyBox(">"), idle, false)), "")
	eq(t, draftOf(t, "agy", agyScreen(agyBox(">"), idle, true)), "")
	for _, ph := range []string{"Auto mode: edits & safe commands auto-approved (shift+tab to cycle)",
		"Plan mode: research & plan only (shift+tab to cycle)", "Accept-edits mode: file edits auto-approved (shift+tab to cycle)"} {
		eq(t, draftOf(t, "agy", agyScreen(agyBox("> "+ph), idle, false)), "")
	}
	// A queued message that an interrupt put back, wrapped over three lines.
	back := agyBox("> [agent-ops from Sams-MacBook-Pro-2.local (zsh) id=tmux:%0",
		"  folder=/private/tmp/agyrun] Baseline", "  queued message.")
	eq(t, draftOf(t, "agy", agyScreen(back, strings.Repeat(" ", 90)+"auto · Gemini 4 Carbon · low", true)),
		"[agent-ops from Sams-MacBook-Pro-2.local (zsh) id=tmux:%0\nfolder=/private/tmp/agyrun] Baseline\nqueued message.")
	// A dialog hides the input box: the past messages under short rules are not an input box.
	dialog := append(agyScreen(nil, "", false)[:10], agyRule, "Read: /Users/x/SKILL.md", "Allow access to this file?",
		"> 1. Yes, allow access", "  2. No, deny access", "  ↑/↓ Navigate", "esc to cancel      auto · Gemini 4 Carbon · low")
	if d := draftOf(t, "agy", dialog); d != nil {
		t.Fatalf("a dialog is not an input line: %q", *d)
	}
}

func TestAgyBusy(t *testing.T) {
	p := Profiles["agy"]
	running := agyScreen(agyBox(">"), "esc to cancel                                  auto · Gemini 4 Carbon · low", false)
	running = append(running[:len(running)-4], append([]string{"⢿  Running command..."}, running[len(running)-4:]...)...)
	queued := agyScreen(agyBox(">"), "  Press up to edit queued messages                auto · Gemini 4 Carbon · low", true)
	queued = append(queued[:len(queued)-5], append([]string{"⣟  Loading...", "▸ QUEUED-TEXT banana (alt+m to send immediately)"}, queued[len(queued)-5:]...)...)
	idle := agyScreen(agyBox(">"), "? for shortcuts                                  auto · Gemini 4 Carbon · low", true)
	banner := append([]string{" ┃ Introducing Gemini 4 Hydrogen", " ┃ esc to dismiss"}, idle...)
	if !p.IsBusy(running) || !p.IsBusy(queued) || p.IsBusy(idle) || p.IsBusy(banner) {
		t.Fatal(p.IsBusy(running), p.IsBusy(queued), p.IsBusy(idle), p.IsBusy(banner))
	}
	if got := p.StableView(idle); len(got) != 10 {
		t.Fatalf("stable view is what is above the input box: %d lines", len(got))
	}
	if !p.Calibrated || !p.PromptRequired || !p.InterruptVerified || !reflect.DeepEqual(p.InterruptKeys, []string{"escape"}) {
		t.Fatal(p)
	}
	if ForProcess("agy") != p {
		t.Fatal("agy is detected by its process name")
	}
}

// A real Antigravity CLI 1.5.10 screen, 72 columns, waiting on "Run this command?". Options wrap onto
// lines of their own; option 3 also persists to settings.json.
var agyRunCommandDialog = []string{
	"      ▄▀▀▄        Antigravity CLI 1.5.10",
	"",
	agyShort,
	"> [task t5f0410 from Switching gh CLI accounts] Read the file",
	"  /Users/samhv/.local/state/termctl/briefs/t5f0410.md and do what it",
	"",
	"● Ran 5 commands (cd /Users/samhv/work... ×5) (ctrl+o to expand)",
	"",
	"Command",
	strings.Repeat("─", 72),
	"",
	"Reason: the auto-run safety check did not reach a decision",
	"Requesting permission for:",
	"   cd /Users/samhv/workspace/work/growtrics/growtrics_workflows &&",
	"uv run pytest -q",
	"",
	"Run this command?",
	"> 1. Yes, run command",
	"  2. Yes, and always allow in this conversation for commands that start",
	"with 'uv run pytest'",
	"  3. Yes, and always allow for commands that start with 'uv run pytest'",
	"(Persist to settings.json)",
	"  4. No, cancel",
	"",
	"  ↑/↓ Navigate · tab Amend · ctrl+g edit/expand command",
	"esc to cancel                          auto · Gemini 4 Hydrogen · medium",
}

// A real Claude Code 2.1.326 screen waiting on a Bash command. The input box is gone while it asks.
var claudeBashDialog = []string{
	"❯ [agent-ops from lead (claude) id=EFD186D6 folder=~/w] Run exactly this shell command with the Bash tool: touch x",
	"",
	"⏺ Bash(touch x)",
	"  ⎿  Waiting…",
	"",
	strings.Repeat("─", 292),
	" Bash command",
	" Tip: auto mode handles these prompts for you — choose \"switch to auto mode\" below",
	" Create empty file named x",
	strings.Repeat("╌", 292),
	" touch x",
	strings.Repeat("╌", 292),
	" Do you want to proceed?",
	" ❯ 1. Yes",
	"   2. Yes, and always allow access to /private/tmp/claude-run from this project",
	"   3. Yes, and switch to auto mode · auto mode handles these prompts for you",
	"   4. No",
	"",
	" Esc to cancel · Tab to amend",
}

func optionTexts(d *Dialog) []string {
	var out []string
	for _, o := range d.Options {
		s := fmt.Sprintf("%d %s", o.N, o.Text)
		if o.Persist {
			s += " [persist]"
		}
		out = append(out, s)
	}
	return out
}

func TestAgyRunCommandDialog(t *testing.T) {
	p := Profiles["agy"]
	d := p.ReadDialog(agyRunCommandDialog)
	if d == nil {
		t.Fatal("no dialog read")
	}
	want := []string{"1 Yes, run command",
		"2 Yes, and always allow in this conversation for commands that start with 'uv run pytest' [persist]",
		"3 Yes, and always allow for commands that start with 'uv run pytest' (Persist to settings.json) [persist]",
		"4 No, cancel"}
	if d.Question != "Run this command?" || d.Selected != 1 || !reflect.DeepEqual(optionTexts(d), want) || d.DenyOption() != 4 {
		t.Fatalf("%+v\n%q", d, optionTexts(d))
	}
	if d.Body != "Reason: the auto-run safety check did not reach a decision\nRequesting permission for:\n"+
		"cd /Users/samhv/workspace/work/growtrics/growtrics_workflows &&\nuv run pytest -q" {
		t.Fatalf("body %q", d.Body)
	}
	if len(d.Fingerprint) != 8 || d.Unrecognized {
		t.Fatal(d.Fingerprint, d.Unrecognized)
	}
	if p.ReadInput(agyRunCommandDialog) != nil || !p.IsBusy(agyRunCommandDialog) {
		t.Fatal("the dialog hides the input box, and agy looks busy")
	}
	// Moving the selection keeps the fingerprint; a wider pane that wraps the options differently too.
	moved := append([]string{}, agyRunCommandDialog...)
	moved[17], moved[18] = "  1. Yes, run command", "> 2. Yes, and always allow in this conversation for commands that start"
	wide := append(append(append([]string{}, agyRunCommandDialog[:18]...),
		"  2. Yes, and always allow in this conversation for commands that start with 'uv run pytest'"), agyRunCommandDialog[20:]...)
	for _, s := range [][]string{moved, wide} {
		if e := p.ReadDialog(s); e == nil || e.Fingerprint != d.Fingerprint {
			t.Fatalf("fingerprint changed: %+v", e)
		}
	}
	if e := p.ReadDialog(moved); e.Selected != 2 {
		t.Fatal(e.Selected)
	}
}

func TestAgyFileAccessDialog(t *testing.T) {
	dialog := append(agyScreen(nil, "", false)[:10], agyRule, "Read: /Users/x/SKILL.md", "Allow access to this file?",
		"> 1. Yes, allow access", "  2. No, deny access", "  ↑/↓ Navigate", "esc to cancel      auto · Gemini 4 Carbon · low")
	d := Profiles["agy"].ReadDialog(dialog)
	if d == nil || d.Question != "Allow access to this file?" || d.Body != "Read: /Users/x/SKILL.md" || d.DenyOption() != 2 || d.Options[0].Persist || d.Unrecognized {
		t.Fatalf("%+v", d)
	}
}

// agy draws the same footer under every numbered picker. One that asks no known permission question is
// never dropped silently (round-3 review N2): it is an unrecognized dialog, where every choice but a clear
// No or cancel needs --persist, so no picker choice is ever a one-shot answer, and deny still works.
func TestAgyPickerIsAnUnrecognizedDialog(t *testing.T) {
	picker := []string{"Which model do you want to use?", "> 1. Gemini 3 Pro", "  2. Gemini 3 Flash", "", "↑/↓ Navigate · Enter Select · Esc Cancel"}
	d := Profiles["agy"].ReadDialog(picker)
	if d == nil || !d.Unrecognized || d.DenyOption() != 0 ||
		!reflect.DeepEqual(optionTexts(d), []string{"1 Gemini 3 Pro [persist]", "2 Gemini 3 Flash [persist]"}) {
		t.Fatalf("a picker must be a loud unrecognized dialog with every choice persist: %+v", d)
	}
	unknown := []string{"Do you want to continue with this plan?", "> 1. Yes", "  2. Yes, proceed", "  3. Cancel", "  4. No, stop here", "↑/↓ Navigate"}
	d = Profiles["agy"].ReadDialog(unknown)
	want := []string{"1 Yes [persist]", "2 Yes, proceed [persist]", "3 Cancel", "4 No, stop here"}
	if d == nil || !d.Unrecognized || !reflect.DeepEqual(optionTexts(d), want) || d.DenyOption() != 4 {
		t.Fatalf("%+v\n%q", d, optionTexts(d))
	}
}

func TestClaudeBashDialog(t *testing.T) {
	p := Profiles["claude"]
	d := p.ReadDialog(claudeBashDialog)
	if d == nil {
		t.Fatal("no dialog read")
	}
	want := []string{"1 Yes", "2 Yes, and always allow access to /private/tmp/claude-run from this project [persist]",
		"3 Yes, and switch to auto mode · auto mode handles these prompts for you [persist]", "4 No"}
	if d.Question != "Do you want to proceed?" || d.Body != "Bash command\nCreate empty file named x\ntouch x" ||
		d.Selected != 1 || !reflect.DeepEqual(optionTexts(d), want) || d.DenyOption() != 4 || d.Unrecognized {
		t.Fatalf("%+v\n%q", d, optionTexts(d))
	}
	if p.ReadInput(claudeBashDialog) != nil {
		t.Fatal("the dialog hides the input box")
	}
}

// The same session asking to run the report command: the command is marked with │, and "don’t" has a curly quote.
var claudeReportDialog = []string{
	strings.Repeat("─", 292),
	" Bash command",
	" Tip: auto mode handles these prompts for you — choose \"switch to auto mode\" below",
	" Report task tb1d260 done to lead",
	strings.Repeat("╌", 292),
	` │ harness-ops task done tb1d260 --status ok --summary "Ran 'touch z' successfully; created empty file z in the working directory."`,
	strings.Repeat("╌", 292),
	" This command requires approval",
	"",
	" Do you want to proceed?",
	"   1. Yes",
	"   2. Yes, and don’t ask again for: harness-ops task *",
	"   3. Yes, and switch to auto mode · auto mode handles these prompts for you",
	" ❯ 4. No",
	"",
	" Esc to cancel · Tab to amend",
}

func TestClaudeReportDialog(t *testing.T) {
	d := Profiles["claude"].ReadDialog(claudeReportDialog)
	want := []string{"1 Yes", "2 Yes, and don’t ask again for: harness-ops task * [persist]",
		"3 Yes, and switch to auto mode · auto mode handles these prompts for you [persist]", "4 No"}
	if d == nil || d.Selected != 4 || !reflect.DeepEqual(optionTexts(d), want) ||
		d.Body != "Bash command\nReport task tb1d260 done to lead\n"+
			`harness-ops task done tb1d260 --status ok --summary "Ran 'touch z' successfully; created empty file z in the working directory."`+
			"\nThis command requires approval" {
		t.Fatalf("%+v\n%q", d, optionTexts(d))
	}
}

func TestNoDialogOnOrdinaryScreens(t *testing.T) {
	list := []string{"Steps:", "1. build it", "2. test it", "", rule, "❯", rule, "  esc to cancel"}
	for name, s := range map[string][]string{"claude idle": claudeIdle, "claude named": claudeNamedIdle, "numbered list": list,
		"agy idle": agyScreen(agyBox(">"), "? for shortcuts", true)} {
		if d := Profiles["claude"].ReadDialog(s); d != nil {
			t.Errorf("%s: claude read %+v", name, d)
		}
		if d := Profiles["agy"].ReadDialog(s); d != nil {
			t.Errorf("%s: agy read %+v", name, d)
		}
	}
	for _, n := range []string{"omp", "opencode", "codex", "gemini", "aider", "pi", "generic"} {
		if Profiles[n].ReadDialog != nil {
			t.Errorf("%s has no calibrated dialog reader", n)
		}
	}
}
