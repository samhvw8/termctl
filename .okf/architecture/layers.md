---
type: Architecture
title: Two layers
description: Layer 1 drives terminals, Layer 2 knows about harnesses; the split keeps new terminals cheap to add.
tags: [architecture]
generated: { by: claude-code/opus-5.5, at: '2026-11-27T08:40:00Z' }
sources:
  - id: agent-ops
    resource: ~/.dotfiles/home/.ccp/hub/skills/agent-ops/SKILL.md
    title: agent-ops rules that the tool keeps
---

# Overview

| Layer | Code | Knows |
|---|---|---|
| 1. `termctl` | `internal/term/`, `internal/iterm2api/`, `cmd/termctl` | Panes, text, keys, capture, labels, spawn. |
| 2. `harness-ops` | `internal/harness/`, `internal/cli/` | Draft check, slash commands, idle waits, tagged messages, spawn with readiness, key presses on panes the caller started (`key`). |

Layer 1 has no harness knowledge. Its one exception is `term.MainProcesses`, plain data that says which programs own a pane so its folder is reported correctly. A test keeps it in sync with the profiles.

The `agent-ops` skill (named `agent-msg` until 2026-11-10) calls `harness-ops` directly and holds only the rules for agents; the mechanism lives here.[^agent-ops]

# Message flow

`msg --clear-first` runs these steps in order:

1. Refuse if the input line holds a draft (exit 4), unless `--force`. With `--force`, ctrl-u wipes it.
2. Type `/clear`, wait until it shows on the input line, press Enter (see [Enter after the text shows](#enter-after-the-text-shows)).
3. Wait for idle: see [harness profiles](/architecture/harness-profiles.md).
4. Check the input line again, even with `--force`. Leftover text means `/clear` never ran.
5. Type the tagged message and press Enter.

Step 4 is the fix for the original failure, where the message joined `/clear` on one line.

Without `--clear-first`, `msg --force` (and `task send --force`) also wipes a draft with ctrl-u before it types. Before 2026-11-23 it typed after the draft, and the receiver got the draft and the message as one garbled line.

## Enter after the text shows

Every typed line (`Ops.submit`: `msg`, `slash`, notices, briefs) waits 0.4 s, then reads the input line until the last 24 characters of the text (spaces removed) or a `[Pasted text` marker show there, and only then presses Enter (`awaitTyped`, up to 10 s; after that it warns and presses Enter anyway). A profile that is not calibrated and reads no input line gets Enter at once, as before. After Enter, the line must leave the input line; if the box ends in an empty line (`InputEndsBlank`, Claude Code), a backspace removes it before the next Enter.

Why: on 2026-11-27 a `task progress --notify` notice reached the superagent 2 s after another notice had started its turn. Claude Code's event loop was busy, took the text and the Enter sent 0.4 s later in one read as one paste, and the Enter became a new line. Its screen still showed the empty input line from before, so the check after Enter read the line as sent, and the worker was told so. The notice sat unsent for 37 minutes (the superagent's prompt history and transcript show no enqueue until 15:25). A fixed delay cannot cover a busy event loop; the text on screen proves the TUI took it in. Tests: `TestSubmitWaitsForTheTextBeforeEnter` and `TestSubmitRemovesANewLineBeforeTheNextEnter` (both fail on the old code). Checked on real Claude Code 2.1.332 in an iTerm2 tab: a 420-character message submitted.

[^agent-ops]: agent-ops rules that the tool keeps
