---
type: Architecture
title: Two layers
description: Layer 1 drives terminals, Layer 2 knows about harnesses; the split keeps new terminals cheap to add.
tags: [architecture]
generated: { by: claude-code/opus-5.5, at: '2026-11-10T10:30:00Z' }
sources:
  - id: agent-ops
    resource: ~/.dotfiles/home/.ccp/hub/skills/agent-ops/SKILL.md
    title: agent-ops rules that the tool keeps
---

# Overview

| Layer | Code | Knows |
|---|---|---|
| 1. `termctl` | `internal/term/`, `internal/iterm2api/`, `cmd/termctl` | Panes, text, keys, capture, labels, spawn. |
| 2. `harness-ops` | `internal/harness/`, `internal/cli/` | Draft check, slash commands, idle waits, tagged messages, spawn with readiness. |

Layer 1 has no harness knowledge. Its one exception is `term.MainProcesses`, plain data that says which programs own a pane so its folder is reported correctly. A test keeps it in sync with the profiles.

The `agent-ops` skill (named `agent-msg` until 2026-11-10) calls `harness-ops` directly and holds only the rules for agents; the mechanism lives here.[^agent-ops]

# Message flow

`msg --clear-first` runs these steps in order:

1. Refuse if the input line holds a draft (exit 4), unless `--force`. With `--force`, ctrl-u wipes it.
2. Type `/clear`, wait 0.4 s, press Enter. The delay exists because TUIs drop an Enter that arrives with the pasted text.
3. Wait for idle: see [harness profiles](/architecture/harness-profiles.md).
4. Check the input line again, even with `--force`. Leftover text means `/clear` never ran.
5. Type the tagged message and press Enter.

Step 4 is the fix for the original failure, where the message joined `/clear` on one line.

[^agent-ops]: agent-ops rules that the tool keeps
