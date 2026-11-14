---
type: Architecture
title: Harness profiles
description: How harness-ops recognises a harness, reads its input line and decides it is idle.
tags: [architecture, harness]
generated: { by: claude-code/opus-5.5, at: '2026-11-14T08:30:00Z' }
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
| agy | `>` between the widest `─` rules | Calibrated on Antigravity CLI 1.5.9, 2026-11-14. Past messages sit under shorter 60-column rules that also start with `>`, so only the widest rules count. A task panel can add a third rule below the box. An empty box names the mode, `... (shift+tab to cycle)`: treated as empty. A dialog (trust, file access) hides the box. Detected by the process `agy`; before it was a main process, its MCP children made it look like `node`. |
| codex, gemini, aider, pi | generic | Not installed or not checked. Uncalibrated: warn and proceed. |

# Busy lines from real turns

Claude shows `✽ Gesticulating… (6s · ↓ 243 tokens)`, and the wording changes; `esc to interrupt` did not appear. The match is a word, `…`, then `(`. omp shows `Working…` or a braille spinner.

agy shows a braille spinner (`⢿  Running command...`, `⣟  Loading...`) and `esc to cancel` on the status line. While a message is queued, the status line says `Press up to edit queued messages` instead. A banner's `esc to dismiss` is not busy.

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

A TUI can drop an Enter that arrives while long text is still being taken in. `Submit` checks that the text left the input line, presses Enter up to three more times, and then fails with exit 4. When it cannot read the input line afterwards, it warns that the submit is not confirmed; it does not count that as success.

`msg` looks once before it types: when the harness is working (terminal status or a busy pattern), it prints `queued for ...` instead of `sent to ...`. A busy harness may hold the message until its turn ends.

# Interrupt

`Interrupt` sends the profile's keys (Esc on claude, omp and agy), then reads the input line for up to 2 s. Claude Code and agy put queued messages back on the input line when a turn is stopped, unsent. New text there fails with exit 4 and shows the start and end of the text, so a caller does not take the text on screen for a turn in progress. A draft that was there before the interrupt only warns. An unreadable input line warns.

Checked on agy 2026-11-14: a message queued during a long reply came back on the input line within 0.5 s of Esc. Found when an agent interrupted agy to make it read a queued brief sooner, saw the brief on screen, and reported agy as working.

# Idle rule

`WaitIdle` returns when all hold: the terminal's own status is not `working`, no busy pattern is on screen, the input line is visible (where `PromptRequired`), and `StableView` has not changed for `StableSeconds`.

A calibrated harness whose input line cannot be read is refused (exit 4). Sending blind could join a draft, and crashing beats corrupting. An uncalibrated harness warns and proceeds, because its parser is unproven.
