"""Process and path helpers shared by backends that know a pane's tty."""
from __future__ import annotations

import re
import subprocess
from pathlib import Path

# Programs that "own" a pane: a pane running one of these is that program, even
# when it has child shells. Layer 2 profiles must cover every name here.
MAIN_PROCESSES = ("claude", "omp", "opencode", "codex", "gemini", "aider", "pi")


def clean_title(t: str) -> str:
    """Drop spinner (braille) and icon-font glyphs that harnesses put in titles."""
    t = re.sub(r"[⠀-⣿-\U000f0000-\U0010ffff]", "", t)
    t = re.sub(r"^\s*π\s*>?\s*", "", t)  # omp's prefix
    return re.sub(r"\s+", " ", t).strip()


def tilde(p: str) -> str:
    home = str(Path.home())
    return "~" + p[len(home) :] if p == home or p.startswith(home + "/") else p


def processes_by_tty() -> dict[str, list[tuple[int, str]]]:
    out = subprocess.run(["ps", "-axo", "pid=,tty=,comm="], capture_output=True, text=True, check=True).stdout
    by: dict[str, list[tuple[int, str]]] = {}
    for line in out.splitlines():
        parts = line.split(None, 2)
        if len(parts) == 3 and parts[1] != "??":
            by.setdefault(parts[1], []).append((int(parts[0]), parts[2].rsplit("/", 1)[-1]))
    return by


def main_process(procs: list[tuple[int, str]], names=MAIN_PROCESSES) -> tuple[int, str]:
    """(pid, name) of the pane's main program: the last known harness, else the last process."""
    hits = [(p, c) for p, c in procs if c in names]
    if hits:
        return hits[-1]
    return procs[-1] if procs else (0, "")


def cwd_of(pids: list[int]) -> dict[int, str]:
    if not pids:
        return {}
    out = subprocess.run(
        ["lsof", "-a", "-d", "cwd", "-Fpn", "-p", ",".join(map(str, pids))],
        capture_output=True, text=True,
    ).stdout
    res: dict[int, str] = {}
    pid = None
    for line in out.splitlines():
        if line.startswith("p"):
            pid = int(line[1:])
        elif line.startswith("n") and pid is not None:
            res[pid] = line[1:]
    return res
