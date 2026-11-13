package harness

import (
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
