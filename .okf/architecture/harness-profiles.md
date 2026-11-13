---
type: Architecture
title: Harness profiles
description: How harness-ops recognises a harness, reads its input line and decides it is idle.
tags: [architecture, harness]
generated: { by: claude-code/opus-5.5, at: '2026-11-09T06:00:00Z' }
---

# Profile fields

`Profile` in `internal/harness/profiles.go`: `Processes`, start `Command`, `Slash`, `Calibrated`, `PromptRequired`, `StableSeconds`, `ReadInput`, `StableView`, `BusyPatterns`, `InterruptKeys`, `InterruptVerified`.

`ReadInput` returns the draft, `""` for an empty line, or nil when the input line is not on screen.

# Calibration

Parsers were checked against screens of real harnesses on 2026-11-09. A scratch tab was opened, a draft typed without Enter, and the screen read.

| Harness | Input line | Notes |
|---|---|---|
| claude | `❯` between `─` rules | Calibrated. A named session ends the top rule with `── name ─`. An empty box can show `Try "..."`: treated as empty. Status lines below the box tick, so idle ignores them. |
| omp | `╰─` | Calibrated. ctrl-u clears. Braille spinner marks busy. |
| opencode | `┃` box above `╹▀` | Read on a fresh screen only. Placeholder `Ask anything…` is treated as empty. Not marked calibrated. |
| codex, gemini, aider, pi | generic | Not installed or not checked. Uncalibrated: warn and proceed. |

# Busy lines from real turns

Claude shows `✽ Gesticulating… (6s · ↓ 243 tokens)`, and the wording changes; `esc to interrupt` did not appear. The match is a word, `…`, then `(`. omp shows `Working…` or a braille spinner.

# Read-only mode for blind reviewers

Checked on real sessions on 2026-11-09.

| Harness | Mode | Result |
|---|---|---|
| claude | `--setting-sources project --permission-mode dontAsk`, write tools denied, read tools and the report command allowed | Writes through the Write tool and the shell were denied; the report command ran. |
| claude | `--permission-mode plan` | Unusable: it stops at a "ready to execute?" dialog and cannot report, even with the report command allowed. |
| claude | `--permission-mode dontAsk` with user settings loaded | Not read-only: the user's allow rules let a write through. |
| omp | none | `--plan-yolo` approves its own plan and then executes. `harness-ops` warns. |
| opencode, codex, gemini, aider, pi | none verified | `harness-ops` warns, and the reviewer can change files. |

# Submit

A TUI can drop an Enter that arrives while long text is still being taken in. `Submit` checks that the text left the input line, presses Enter up to three more times, and then fails with exit 4.

# Idle rule

`WaitIdle` returns when all hold: the terminal's own status is not `working`, no busy pattern is on screen, the input line is visible (where `PromptRequired`), and `StableView` has not changed for `StableSeconds`.

A calibrated harness whose input line cannot be read is refused (exit 4). Sending blind could join a draft, and crashing beats corrupting. An uncalibrated harness warns and proceeds, because its parser is unproven.
