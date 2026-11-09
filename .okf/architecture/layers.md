---
type: Architecture
title: Two layers
description: Layer 1 drives terminals, Layer 2 knows about harnesses; the split keeps new terminals cheap to add.
tags: [architecture]
generated: { by: claude-code/opus-5.5, at: '2026-11-09T01:30:00Z' }
sources:
  - id: agent-msg
    resource: ~/.dotfiles/home/.ccp/hub/skills/agent-msg/SKILL.md
    title: agent-msg rules that the tool keeps
---

# Overview

| Layer | Code | Knows |
|---|---|---|
| 1. `termctl` | `src/termctl/backends/`, `cli.py`, `resolve.py`, `proc.py` | Panes, text, keys, capture, labels, spawn. |
| 2. `harness-ops` | `src/termctl/harness/` | Draft check, slash commands, idle waits, tagged messages, spawn with readiness. |

Layer 1 has no harness knowledge. Its one exception is `proc.MAIN_PROCESSES`, plain data that says which programs own a pane so its folder is reported correctly. A test keeps it in sync with the profiles.

The `agent-msg` skill script is a thin wrapper around `harness-ops`, so the existing rules live in one place.[^agent-msg]

# Message flow

`msg --clear-first` runs these steps in order:

1. Refuse if the input line holds a draft (exit 4), unless `--force`. With `--force`, ctrl-u wipes it.
2. Type `/clear`, wait 0.4 s, press Enter. The delay exists because TUIs drop an Enter that arrives with the pasted text.
3. Wait for idle: see [harness profiles](/architecture/harness-profiles.md).
4. Check the input line again, even with `--force`. Leftover text means `/clear` never ran.
5. Type the tagged message and press Enter.

Step 4 is the fix for the original failure, where the message joined `/clear` on one line.

[^agent-msg]: agent-msg rules that the tool keeps
