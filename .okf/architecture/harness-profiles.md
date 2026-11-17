---
type: Architecture
title: Harness profiles
description: How harness-ops recognises a harness, reads its input line and its permission dialogs, and decides it is idle.
tags: [architecture, harness]
generated: { by: claude-code/opus-5.5, at: '2026-11-17T12:30:00Z' }
---

# Profile fields

`Profile` in `internal/harness/profiles.go`: `Processes`, start `Command`, `Slash`, `Calibrated`, `PromptRequired`, `StableSeconds`, `ReadInput`, `StableView`, `BusyPatterns`, `InterruptKeys`, `InterruptVerified`, `ReadOnlyFlags`, `ReadDialog`.

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

# Dialogs

`ReadDialog` returns the permission or approval dialog on screen, or nil. Code: `internal/harness/dialog.go`. A `Dialog` has the question, the body (the request, such as the command), the numbered options, the selected option, and a fingerprint: 8 hex characters of SHA-256 over the question, body and option texts with spacing squashed. Moving the selection or a different line wrap keeps the fingerprint.

The reader looks for the harness's footer among the last 6 non-blank lines, the numbered options above it (numbered from 1, exactly one with the selection mark `>` or `❯`, other lines continue the option above), the question ending in `?` right above option 1, and the body up to the `─` rule above it. Dashed `╌` rules, `Tip:` lines and Claude's `│` command mark are left out of the body.

`Persist` marks every option that is not known to grant only this one request. The known one-shot texts are the whole option `Yes`, `Yes, run command`, `Yes, proceed`, `Yes, allow access` (and `allow once` forms), and any option starting with `No`, `cancel`, `deny`, `reject` or `exit`. Everything else needs `--persist` on `task answer`: agy's "always allow in this conversation" and "(Persist to settings.json)", Claude's "don't ask again", "switch to auto mode" and "allow all edits during this session", and any wording a harness adds later, such as the plan dialog's "auto-accept edits". Until 2026-11-17 the guard was a keyword list of widening words, and a review found that "Yes, and auto-accept edits" passed it as one-shot; an unknown text now fails safe.

agy draws its `↑/↓ Navigate` footer under every numbered picker, a model choice too. So an agy screen is a dialog only when the question asks for permission (`Run this command?`, `Allow ...?`, or a question naming permission or access) or the body says `Requesting permission`.

| Harness | Footer | Calibrated on | Notes |
|---|---|---|---|
| claude | `Esc to cancel · Tab to amend` | Claude Code 2.1.326, 2026-11-17, real `claude --model haiku` in a scratch iTerm2 tab: a `touch` command and the `task done` report command | The input box is gone while it asks, and no busy pattern shows. Digit keys pick and submit at once ("4" denied on the spot). Arrow keys move the selection, but the first look 0.5 s later still showed the old one, so the answer polls for up to 3 s. Re-checked on iTerm2, tmux, Rex (80x24 blocks: the command wraps into the body) and hc by `mise run test:dialogs` (2026-11-17), with `--permission-mode default --setting-sources project`: the user's settings set `defaultMode: auto`, which asks nothing. After the answer, haiku asks again for `harness-ops task done` (the brief's footer); the test denies that. |
| agy | `↑/↓ Navigate` | Antigravity CLI 1.5.10, a real screen captured on 2026-11-17 ("Run this command?") | Options wrap onto lines of their own. The dialog hides the input box and keeps `esc to cancel` on the status line, so agy looks busy while it waits. Answered and denied on real agy 1.5.10 on iTerm2, tmux, Rex and hc by `mise run test:dialogs` (2026-11-17), started with `--mode accept-edits`: its default `auto` mode runs `touch` without asking. agy's terminal sandbox (the user's `enableTerminalSandbox`) refused the approved `touch` in the scratch folders with "Operation not permitted" and agy asked again with the same fingerprint, so the test turns it off for its session (`--settings`). |
| others | none | | `ReadDialog` is nil. `task answer` refuses (exit 2), and the watcher does not read them. |

`Ops.Answer` reads the dialog again right before the first arrow key, presses up or down from the selected option to the chosen one, waits until the screen shows that option selected and the same fingerprint, reads it once more, and only then presses Enter (or Esc, for a deny with no "No" choice). The last read happens on every path, also when the chosen option was already selected: before 2026-11-17 that path pressed Enter on whatever dialog had replaced the one read. If the dialog changed or the selection did not arrive, it stops without Enter (exit 1). A window of milliseconds between the last read and Enter remains. Digit keys are not used: they submit before anything can be checked, and hc cannot type without submitting. After Enter it waits up to 3 s for the dialog to go; still there means exit 1.

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
