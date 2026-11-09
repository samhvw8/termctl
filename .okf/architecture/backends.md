---
type: Architecture
title: Backends
description: The Backend interface and the state of each terminal backend.
tags: [architecture, backends]
generated: { by: claude-code/opus-5.5, at: '2026-11-09T03:40:00Z' }
sources:
  - id: rex-cli
    resource: https://www.superlogical.com/rex/docs/reference/cli
    title: Rex CLI reference
---

# Interface

`Backend` in `src/termctl/backends/base.py`: `list_panes`, `spawn(where, cmd, cwd, title)`, `send_text` (literal, never submits), `send_key`, `capture(pane, lines)`, `close`, `set_label`, `self_pane`, and optional `native_status` and `available`.

Pane IDs are `backend:id`. A target with a backend prefix selects that backend, which is how `RE:` replies route across terminals.

# Selection

`detect()` reads the environment: `TMUX`, then `REX_SESSION`, then `ITERM_SESSION_ID`. tmux is first because it is the innermost when nested. Without any of them it falls back to iTerm2 when installed, as agent-msg did.

# Backends

| Backend | Notes |
|---|---|
| iterm2 | iTerm Python API over one websocket for the process lifetime. `make` returns one instance per process: the library binds a global App to its first connection, and a second instance hangs. Each API call times out after 30 s. Spawns through `$SHELL -lic` so mise and PATH load. A new tab re-selects the previous tab, to keep the user's focus. |
| tmux | CLI. Labels are the pane option `@termctl_label`. Escapes a final `;`, which tmux reads as a command separator. `TMUX_PANE` is trusted only when `$TMUX` names the same server. |
| rex | Written from the documented CLI.[^rex-cli] Not run against real Rex. |
| ghostty | Stub that raises. |

[^rex-cli]: Rex CLI reference
