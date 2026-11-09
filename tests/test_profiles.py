"""Parsers checked against screens read from live panes (2026-11-09)."""
from termctl.harness.profiles import PROFILES, for_process
from termctl.proc import MAIN_PROCESSES

RULE = "─" * 80

CLAUDE_IDLE = [
    "✻ Churned for 50s · done 7:22 PM",
    "",
    RULE,
    "❯",
    RULE,
    "  ◆ Opus 5.5 · Med | ◎ STE Lite | # samhv",
    "  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← 1 agent",
]

CLAUDE_DRAFT = [
    "output",
    RULE,
    "❯ half typed thought",
    RULE,
    "  status",
]

CLAUDE_DRAFT_MULTI = [
    RULE,
    "❯ first line",
    "  second line",
    RULE,
]

CLAUDE_WITH_MESSAGE_ABOVE = [
    "❯ an earlier user message in the transcript",
    "⏺ a reply",
    RULE,
    "❯",
    RULE,
]

CLAUDE_BUSY = [
    "✶ Thinking… (12s · esc to interrupt)",
    RULE,
    "❯",
    RULE,
]

OMP_IDLE = [
    " ※ recap: something",
    "                                                 󰏘 REVAMP: Generate",
    " 󰵗   Gemini 3.8 Flash   ~/workspace/apps/kagemusha   main  󰙺 6.12 ──9%──󰁨──",
    "╰─",
]

OMP_DRAFT = OMP_IDLE[:-1] + ["╰─ a draft here"]

OMP_BUSY = [
    "  󱊷 Read stats.go, decide.rs",
    " ⠧ 3m  󰪣 Gemini 4 Argon   …tform   feature/x ─󰁨18%─1M─",
    "╰─",
]


def test_all_main_processes_have_a_profile():
    for name in MAIN_PROCESSES:
        assert name in PROFILES, name


def test_required_profiles_exist():
    for name in ("claude", "omp", "opencode", "codex", "gemini"):
        assert PROFILES[name].name == name and PROFILES[name].slash


def test_for_process_falls_back_to_generic():
    assert for_process("zsh").name == "generic"
    assert for_process("claude").name == "claude"


def test_claude_empty_input():
    assert PROFILES["claude"].read_input(CLAUDE_IDLE) == ""


def test_claude_draft():
    assert PROFILES["claude"].read_input(CLAUDE_DRAFT) == "half typed thought"


def test_claude_multiline_draft():
    assert PROFILES["claude"].read_input(CLAUDE_DRAFT_MULTI) == "first line\nsecond line"


def test_claude_ignores_transcript_prompts():
    assert PROFILES["claude"].read_input(CLAUDE_WITH_MESSAGE_ABOVE) == ""


def test_claude_input_not_visible():
    assert PROFILES["claude"].read_input(["just output", "more"]) is None


def test_claude_busy_marker():
    p = PROFILES["claude"]
    assert p.busy(CLAUDE_BUSY) and not p.busy(CLAUDE_IDLE)


def test_claude_stability_ignores_status_lines():
    p = PROFILES["claude"]
    a = CLAUDE_IDLE
    b = CLAUDE_IDLE[:-2] + ["  ◆ changed clock", "  ◷ changed"]
    assert p.stable_view(a) == p.stable_view(b)


def test_omp_empty_input():
    assert PROFILES["omp"].read_input(OMP_IDLE) == ""


def test_omp_draft():
    assert PROFILES["omp"].read_input(OMP_DRAFT) == "a draft here"


def test_omp_busy_spinner():
    p = PROFILES["omp"]
    assert p.busy(OMP_BUSY) and not p.busy(OMP_IDLE)


def test_generic_prompt():
    p = PROFILES["generic"]
    assert p.read_input(["out", "> "]) == ""
    assert p.read_input(["out", "> typed"]) == "typed"
    assert p.read_input(["out", "no prompt here"]) is None


def test_uncalibrated_profiles_are_marked():
    assert PROFILES["claude"].calibrated and PROFILES["omp"].calibrated
    assert not PROFILES["generic"].calibrated


def test_claude_placeholder_is_not_a_draft():
    fresh = [RULE, '❯ Try "fix typecheck errors"', RULE]
    assert PROFILES["claude"].read_input(fresh) == ""


OPENCODE_EMPTY = [
    "┃",
    '┃  Ask anything… "What is the tech stack of this project?"',
    "┃",
    "┃  Build · GPT Astra Latest OpenRouter · medium",
    "╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀",
    "…/vision-platform  shift+tab agents  ctrl+p commands",
]
OPENCODE_DRAFT = ["┃", "┃  calibration draft text", "┃", "┃  Build · GPT · medium", "╹▀▀▀▀▀▀▀▀"]


def test_opencode_placeholder_is_empty():
    assert PROFILES["opencode"].read_input(OPENCODE_EMPTY) == ""


def test_opencode_draft():
    assert PROFILES["opencode"].read_input(OPENCODE_DRAFT) == "calibration draft text"


def test_opencode_no_box():
    assert PROFILES["opencode"].read_input(["nothing here"]) is None


def test_claude_working_line_from_a_real_turn():
    p = PROFILES["claude"]
    assert p.busy(["✽ Gesticulating… (6s · ↓ 243 tokens · thought for 3s)", RULE, "❯", RULE, "  status"])
    assert p.busy(["· Forming… (running Stop hook · 12s · ↓ 329 tokens)", RULE, "❯", RULE])
    assert p.busy(["· Pouncing… (6s · ↓ 82 tokens · thinking)", RULE, "❯", RULE])
    assert not p.busy(["✻ Churned for 50s · done 7:22 PM", RULE, "❯", RULE])


def test_omp_working_line_from_a_real_turn():
    assert PROFILES["omp"].busy(["  󱊷 Working…", "╰─"])
