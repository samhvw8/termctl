---
type: Architecture
title: Backends
description: The Backend interface and the state of each terminal backend.
tags: [architecture, backends]
generated: { by: claude-code/opus-5.5, at: '2026-11-09T05:30:00Z' }
sources:
  - id: rex-cli
    resource: https://www.superlogical.com/rex/docs/reference/cli
    title: Rex CLI reference
---

# Interface

`Backend` in `internal/term/term.go`: `ListPanes`, `Spawn(where, cmd, cwd, title)`, `SendText` (literal, never submits), `SendKey`, `Capture(pane, lines)`, `Close`, `SetLabel`, `SelfPane`, `NativeStatus`, `Available`, `ServerRef`.

Pane IDs are `backend:id`. A target with a backend prefix selects that backend, which is how `RE:` replies route across terminals.

# Selection

`Detect` reads the environment: `TMUX`, then `REX_SESSION`, then `ITERM_SESSION_ID`. tmux is first because it is the innermost when nested. Without any of them it falls back to iTerm2 when installed, as agent-msg did.

# Backends

| Backend | Notes |
|---|---|
| iterm2 | Own client over one websocket for the process lifetime; each API call times out after 30 s. Spawns through `$SHELL -lic` so mise and PATH load. A new tab uses `select_tab: false`, to keep the user's focus. A command that fails stays open for 8 s, because a session that ends within seconds makes iTerm2 raise a modal dialog that stalls the whole API until someone clicks OK. |
| tmux | CLI. Labels are the pane option `@termctl_label`. Escapes a final `;`, which tmux reads as a command separator. `TMUX_PANE` is trusted only when `$TMUX` names the same server. |
| rex | Written from the documented CLI.[^rex-cli] Not run against real Rex. |
| ghostty | Stub that raises. |

[^rex-cli]: Rex CLI reference
