---
type: Architecture
title: Backends
description: The Backend interface and the state of each terminal backend.
tags: [architecture, backends]
generated: { by: claude-code/opus-5.5, at: '2026-11-10T15:00:00Z' }
sources:
  - id: rex-cli
    resource: https://www.superlogical.com/rex/docs/reference/cli
    title: Rex CLI reference
---

# Interface

`Backend` in `internal/term/term.go`: `ListPanes`, `Spawn(where, cmd, cwd, title)`, `SendText` (literal, never submits), `SendKey`, `Capture(pane, lines)`, `Close`, `SetLabel`, `SelfPane`, `NativeStatus`, `Available`, `ServerRef`.

`Submitter` is optional: `SubmitText(pane, text, from)` for a backend that cannot type without pressing Enter (hc). `Ops.Submit` uses it when present, still with `CheckText` and the draft check afterwards. Such a backend's `SendText` must refuse loudly, never submit.

`Pane.Tab` names the tab (tmux: window) that holds a pane, for backends that report it (iTerm2, tmux).

Pane IDs are `backend:id`. A target with a backend prefix selects that backend, which is how `RE:` replies route across terminals.

# Selection

`Detect` reads the environment: `TMUX`, then `HC_TERMINAL_ID`, then `REX_SESSION`, then `ITERM_SESSION_ID`. tmux is first because it is the innermost when nested; hc comes before iTerm2 because a Herdr terminal can inherit `ITERM_SESSION_ID`. Without any of them it falls back to iTerm2 when installed, as agent-msg did. A bare iTerm2 UUID as a target always selects iTerm2, so a reply from another terminal routes.

# Placement

`--where auto` is the default for `spawn`, `task new` and `task review`. harness-ops resolves it in `Ops.Spawn` with `term.PlaceFor` (`internal/term/place.go`), passing the panes the caller started (registry `PaneRecord.Owner`). `termctl spawn` has no registry and resolves it in the backend with no owned panes.

| Case | Result |
|---|---|
| A tab with fewer than `MaxSplit` (12) panes: first the caller's tab, then a tab holding a pane the caller started. The new agent's folder does not matter | `split` of the largest pane in that tab (the newest on a tie, or when the backend reports no sizes) |
| No such tab | `tab`, opened right after the caller's tab |
| A backend that does not report tabs (Rex) | `split` of the caller, with no cap |
| Listing panes fails (an iTerm2 session closed meanwhile) | `tab`, with a warning; placement never fails a spawn |

So the agents of one job stay in the lead's tab, whatever folder each one works in. Before 2026-11-13 placement grouped by git project, and a worker started in `~` or a scratch folder opened its own tab; Sam wanted the job together instead. Another agent's panes are never joined. iTerm2 and tmux take the chosen pane through `term.Anchored.SpawnAt`; other backends get only the resolved `where`.

**Split direction.** iTerm2 and tmux cut the pane's long side: `term.SideBySide` puts the new pane beside a pane with `cols >= 2*rows` (a cell is about twice as tall as wide), and below it otherwise. A full screen fills as a grid, not as twelve thin columns.

**Even layout.** After every split the backend resizes the tab's panes evenly; a failure is a warning, never a failed spawn. iTerm2: `SetTabLayoutRequest` with the same split tree and new grid sizes from `evenTree` (`internal/term/iterm2_layout.go`). Each pane gets an even share of its row or column, counted in panes across, not by tree depth: in `[a | b/c]` a and the b/c column get half the width each. The totals stay the same, because a different total resizes the window. tmux: `select-layout tiled`, which rebuilds the window as a grid and ignores the split tree. Checked on real iTerm2 3.7.6 (2026-11-13): eight spawns gave a 4x2 grid of 61-62x39 panes, the window kept its bounds, and repeated layouts lost no cells. 3.7.6 predates iTerm2's fix F-343 (2026-10-18) for a layout call that lost a cell per pane; termctl keeps the totals, so it did not trigger here. Sizes come from `SessionSummary.grid_size` in iTerm2's `ListSessions` and `pane_width`/`pane_height` in tmux; they are `Pane.Width`/`Pane.Height`, separate from `Pane.Rows`, which caps captures. An explicit `--where split` uses the same direction rule. `TestSpawnAutoPlacement` fails if twelve panes in a 200x50 tmux window end up narrower than 40 columns or uneven.

hc ignores `--where`, so placement never lists hc panes. Checked on real iTerm2 (2026-11-10): a new tab opened at the next tab index. iTerm2's tab index assumes `ListSessions` returns tabs in visual order, which that check confirmed.

`harness-ops ls --mine` lists the panes the caller started, so a lead can find what it must close.

# Backends

| Backend | Notes |
|---|---|
| iterm2 | Own client over one websocket for the process lifetime; each API call times out after 30 s. Spawns through `$SHELL -lic` so mise and PATH load. A new tab uses `select_tab: false`, to keep the user's focus, and `tab_index` to open right after the caller's tab. A command that fails stays open for 8 s, because a session that ends within seconds makes iTerm2 raise a modal dialog that stalls the whole API until someone clicks OK. |
| tmux | CLI. Labels are the pane option `@termctl_label`. Escapes a final `;`, which tmux reads as a command separator. A new window opens right after the caller's (`new-window -a -t <window_id>`; a pane ID there fails with "can't specify pane here"). `TMUX_PANE` is trusted only when `$TMUX` names the same server. |
| rex | Written from the documented CLI.[^rex-cli] Not run against real Rex. |
| hc | Herdr (herdr-core `hc` 0.1.19). Tested on a real private core with the fake harness and a no-prompt Claude Code (`mise run test:hc`); real Claude Code and omp messages passed on the live core. `SendText` always refuses: `hc send` always submits, so lines go through `SubmitText`. Pane ID `hc:t7`; label is the `termctl_label` label. `Process` is `foreground.name`, or a known agent found in `foreground.argv`. `where` is validated and ignored: a new terminal opens as a tab in the Herdr app. Details: [the hc section below](#hc). |
| ghostty | Stub that raises. |


# hc

Measured on hc 0.1.19 and the reason the code is the way it is:

- **Any hc command auto-starts a core**, including `hc ls` and `hc server status`. A core keeps the environment of whoever started it, so an `agent-ops ls` from an agent's pane must not create one. Read-only paths check the socket (`$XDG_RUNTIME_DIR` or `~/.local/run`, `herdr-core/<HC_SESSION or default>.sock`; `HC_TERMINAL_SOCKET` and `HC_SOCKET` override) and return no panes when nothing listens. `Spawn` and every other command refuse with exit 1 when no core runs: open Herdr first.
- **Programs get the core's environment**, not the caller's (`hc run` adds only `TERM`, `TERM_PROGRAM`, `COLORTERM` and the `HC_*` variables). A core started from Claude Code therefore passes `CLAUDE_CODE_CHILD_SESSION` to everything it runs, which makes a child Claude print "Transcript saving is off". `Ops.Spawn` clears that one variable on hc; the API's per-terminal `env` can only set variables. The backend's own spawn script clears `ITERM_SESSION_ID`, `TERM_SESSION_ID`, `TMUX`, `TMUX_PANE`, `REX_SESSION`, `REX_BLOCK`, so a worker never detects the wrong backend.
- **Messages to a Herdr agent show `<hc from="<backend>/<id>">`** around the `[agent-ops ...]` line. hc adds the tag when the caller holds a slash and the target reports status. `SubmitText` calls the API's `terminal.send` through `hc connect`, where the caller is optional: `Msg` names the sender, and slash commands and spawn prompts send none, because a tag would stop them working. Targets in iTerm2 or tmux never go through hc and keep the plain line. Other hc subprocesses do not see `HC_TERMINAL_ID`, so polling does not count as that terminal looking at its children.
- **A spawned agent shows in the Herdr app**: the app hides a terminal without a `herdr.space` label, and an id it does not know becomes a new space named after the folder. `Spawn` uses the caller's space when the caller is a Herdr terminal with one, then the space of another terminal in the same folder, then an id hashed from the folder. It sets no `herdr.tab`, so each agent gets its own tab, and it needs no app commands. Checked on the live app: focus stays where it was, and a space closes itself with its last agent.
- **Socket path**: a core started by the Herdr app has no `XDG_RUNTIME_DIR`, so its socket is under `~/.local/state/herdr-core/<session>/`. A shell that exports `XDG_RUNTIME_DIR` looks elsewhere and needs `HC_SOCKET`; the dotfiles `.zshrc` sets it on macOS. `hc` does not read `HC_TERMINAL_SOCKET`, even inside a Herdr terminal, so `hcEnv` passes it on as `HC_SOCKET` unless one is already set.
- **`hc ls --json` has no labels, title or foreground**, so `ListPanes` runs one `hc get` per terminal. `hc ls` reports the program as `zsh` when a shell launched it; only `get`'s `foreground` names the real program.
- **Status words**: `idle`, `working`, `done`, `blocked`, `error`, from the program's OSC 7501 reports (`ESC ] 7501 ; state=<word> BEL`). `blocked` is not ready for input: `WaitIdle` and `close` treat it as busy. hc refuses to send to a blocked program; the refusal maps to exit 6 and there is no `--force` for it.
- **Screen reads**: `Capture` uses `--lines 0` when the request fits the screen. History reads can scroll a full-screen app, so Layer 2 asks for at most the pane's height (`Pane.Rows`, from `get`).
- **Restart identity**: `core.id` survives `hc server restart`, under which terminals keep running; the core pid does not. `ServerRef` returns both, and tasks compare `core_id` when it exists.
- **A harness can refuse a submitted line**: `hc send` delivers Enter, but omp with no API key for its current model shows `No API key found for <provider>` and keeps the text on the input line. `Submit` then reports the text as typed but not submitted; `read` shows the harness's error. Found on `homebox` (2026-11-09), where omp started on a `tokenharbor` model that has no key on that box; on a model with a key, sends from the Mac submitted.
[^rex-cli]: Rex CLI reference
