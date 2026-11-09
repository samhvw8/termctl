"""How to recognise each harness and read its screen.

`calibrated` means the input-line parser was checked against real screens. A calibrated
harness whose input line cannot be read is refused. An uncalibrated one warns and proceeds.
"""
from __future__ import annotations

import re
from dataclasses import dataclass, field
from typing import Callable

RULE = re.compile(r"^─{10,}$")
# An empty Claude Code box shows a suggestion ("Try ..."). It is not a draft.
CLAUDE_PLACEHOLDER = re.compile(r'Try ".*"')
OPENCODE_PLACEHOLDER = re.compile(r"Ask anything….*")


def _last_rule_pair(lines: list[str]):
    """(top, bottom) indexes of the input box: the last rule pair whose body starts with ❯."""
    rules = [i for i, ln in enumerate(lines) if RULE.match(ln.strip())]
    for k in range(len(rules) - 1, 0, -1):
        top, bottom = rules[k - 1], rules[k]
        body = lines[top + 1 : bottom]
        if body and body[0].lstrip().startswith("❯"):
            return top, bottom
    return None


def claude_input(lines: list[str]) -> str | None:
    pair = _last_rule_pair(lines)
    if pair is None:
        return None
    body = lines[pair[0] + 1 : pair[1]]
    parts = [body[0].lstrip()[1:].strip()] + [ln.strip() for ln in body[1:]]
    text = "\n".join(parts).strip()
    return "" if CLAUDE_PLACEHOLDER.fullmatch(text) else text


def claude_stable_view(lines: list[str]) -> list[str]:
    """Only what is above the input box: the status lines below it tick constantly."""
    pair = _last_rule_pair(lines)
    return lines[: pair[0]] if pair else lines


def omp_input(lines: list[str]) -> str | None:
    for ln in reversed(lines):
        m = re.match(r"^\s*╰─\s?(.*)$", ln)
        if m:
            return m.group(1).strip()
    return None


def omp_stable_view(lines: list[str]) -> list[str]:
    return lines[:-3]


def opencode_input(lines: list[str]) -> str | None:
    """The input is the box of ┃ lines above a ╹▀ edge. Its last text line is the mode status."""
    end = next((i for i in range(len(lines) - 1, -1, -1) if lines[i].strip().startswith("╹▀")), None)
    if end is None:
        return None
    start = end
    while start > 0 and lines[start - 1].strip().startswith("┃"):
        start -= 1
    text = [ln.strip().lstrip("┃").strip() for ln in lines[start:end]]
    text = [t for t in text if t]
    if not text:
        return None
    body = "\n".join(text[:-1])
    return "" if OPENCODE_PLACEHOLDER.fullmatch(body) else body


def generic_input(lines: list[str]) -> str | None:
    last = next((ln for ln in reversed(lines) if ln.strip()), "")
    m = re.match(r"^\s*[>❯›] ?(.*)$", last)
    return m.group(1).strip() if m else None


def _same(lines: list[str]) -> list[str]:
    return lines


def _bottom(lines: list[str], n: int = 4) -> str:
    return "\n".join(lines[-n:])


@dataclass(frozen=True)
class Profile:
    name: str
    processes: tuple[str, ...] = ()
    command: str | None = None
    slash: bool = True
    calibrated: bool = False
    prompt_required: bool = False  # idle only when the input line is visible
    stable_seconds: float = 1.5
    read_input: Callable[[list[str]], str | None] = generic_input
    stable_view: Callable[[list[str]], list[str]] = _same
    busy_patterns: tuple[re.Pattern, ...] = field(default=())

    def busy(self, lines: list[str]) -> bool:
        tail = _bottom(lines, 12)
        return any(p.search(tail) for p in self.busy_patterns)


PROFILES: dict[str, Profile] = {
    "claude": Profile(
        "claude", ("claude",), "claude", calibrated=True, prompt_required=True,
        read_input=claude_input, stable_view=claude_stable_view,
        busy_patterns=(re.compile(r"esc to interrupt", re.I),),
    ),
    "omp": Profile(
        "omp", ("omp",), "omp", calibrated=True, prompt_required=True,
        read_input=omp_input, stable_view=omp_stable_view,
        busy_patterns=(re.compile(r"^\s*[⠀-⣿]\s", re.M),),
    ),
    # Input box read on a fresh screen only; not calibrated once a conversation is on screen.
    "opencode": Profile("opencode", ("opencode",), "opencode", read_input=opencode_input),
    "codex": Profile("codex", ("codex",), "codex"),
    "gemini": Profile("gemini", ("gemini",), "gemini"),
    "aider": Profile("aider", ("aider",), "aider"),
    "pi": Profile("pi", ("pi",), "pi"),
    "generic": Profile("generic", prompt_required=True),
}

_BY_PROCESS = {proc: prof for prof in PROFILES.values() for proc in prof.processes}


def for_process(process: str) -> Profile:
    return _BY_PROCESS.get(process, PROFILES["generic"])
