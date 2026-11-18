---
type: Architecture
title: Backends
description: The Backend interface and the state of each terminal backend.
tags: [architecture, backends]
generated: { by: claude-code/opus-5.5, at: '2026-11-18T10:30:00Z' }
sources:
  - id: iterm2-status
    resource: https://github.com/gnachman/iTerm2/blob/master/sources/Language/BuiltInFunctions/SetStatusBuiltInFunction.swift
    title: iTerm2 built-in function get_session_status (app context, session_id argument)
  - id: osc7501
    resource: https://www.superlogical.com/rex/docs/build/program-status
    title: Program Status Protocol (OSC 7501)
  - id: alerts-report
    resource: /research/261117-terminal-alerts/REPORT.md
    title: Capturing terminal alerts per pane, research report (2026-11-17)
  - id: rex-cli
    resource: https://www.superlogical.com/rex/docs/reference/cli
    title: Rex CLI reference
  - id: iterm2-move
    resource: https://github.com/gnachman/iTerm2/tree/master/sources/Language/BuiltInFunctions
    title: iTerm2 built-in functions move_session, move_session_to_new_tab, apply_layout (MoveSessionBuiltInFunction.swift, MoveSessionBuiltInFunctions.swift, ApplyLayoutBuiltInFunction.swift)
  - id: iterm2-proto
    resource: https://github.com/gnachman/iTerm2/blob/master/proto/api.proto
    title: iTerm2 API messages (ListSessionsResponse.Tab.minimized_sessions, MenuItemRequest)
---

# Interface

`Backend` in `internal/term/term.go`: `ListPanes`, `Spawn(where, cmd, cwd, title)`, `SendText` (literal, never submits), `SendKey`, `Capture(pane, lines)`, `Close`, `SetLabel`, `SelfPane`, `NativeStatus`, `Available`, `ServerRef`.

`Submitter` is optional: `SubmitText(pane, text, from)` for a backend that cannot type without pressing Enter (hc). `Ops.Submit` uses it when present, still with `CheckText` and the draft check afterwards. Such a backend's `SendText` must refuse loudly, never submit.

`Pane.Tab` names the tab (tmux: window) that holds a pane, for backends that report it (iTerm2, tmux). `ls` shows it in the TAB column and as `tab` in `--json`.

`Mover`, `Breaker`, `Swapper` and `Arranger` are optional too: see [moving and arranging](#moving-and-arranging).

`StatusReader` is optional: `ProgramStatuses(panes)` gives many panes' program status in one call; its cost per backend is in the table below, and `FreeStatus` marks the backends whose read starts no process. See [program status](#program-status).

# Program status

Agents report what they do with OSC 7501 (`ESC ] 7501 ; state=blocked:kind=permission:msg=<base64> BEL`; pairs split by `:`), but only where the terminal answers the `OSC 7501 ; ?` check.[^osc7501] `term.ProgramStatus{State, Kind, Msg}` is the common form; `State` is lower case, and iTerm2's `waiting` is given as `blocked`. `Blocked()` is a hint that a person is needed, never proof of a dialog: the watcher still reads the screen. A pane that ended since the list is left out of the result; any other failed read is returned as an error with what was read, and the watcher logs it once (`status: ...`) until it changes. On iTerm2 a failed call for a session that is still listed is such an error, so an iTerm2 older than 3.7.6 shows its hint in the watcher's log. Until 2026-11-17 iTerm2 dropped every failed read silently.

| Backend | Source | Cost | Kind, Msg |
|---|---|---|---|
| iTerm2 | built-in `iterm2.get_session_status(session_id:)` in the app context, over the open websocket (`InvokeFunctionRequest`)[^iterm2-status] | one function call per pane, no subprocess | Msg is the `detail` (the decoded msg); no kind |
| Rex | block method `program_status` for every wanted block in one `rex do` Lua script; each session attached once | one CLI call per read | State, Kind and Msg from the newest record (`updated_at`); urgency (blocked, error, working, done, idle) breaks a tie. Until 2026-11-18 the most urgent record of any id won, so a `blocked` under an id the program stopped updating stuck (review A11) |
| hc | `hc ls --json` (the word for every terminal), then `hc get` for a blocked one | one CLI call, plus one per blocked terminal | from `get`'s `status` |
| tmux | none | | tmux does not answer the check, so agents send nothing there; screen only |
| Ghostty | none | | unsupported, see the table below |

`NativeStatus` on iTerm2 now calls the same function over the websocket and returns what `it2 session get-status` printed (`status` or `status: detail`), compared on live panes on 2026-11-17. It used to start the `it2` Python CLI per call (0.44 s, about 21 MB). Its callers (`ls` status column, `WaitIdle`, `busy`) are unchanged; iTerm2's `waiting` is still not counted as busy there, as before.

Captured on 2026-11-17: Rex 0.1.0-preview.19 `program_status` answers `{records: [{state, kind, msg, owner, updated_at}]}` per block, and hc 0.1.19 `get` answers `status: {state, kind, msg}` (fixtures in `internal/term/testdata`). Seen live in `mise run test:dialogs`: agy on iTerm2 reported `Approve Bash: touch …`, which reached the lead's notice (6 of 9 agy notices in the 6-worker run). Claude Code 2.1.326 on iTerm2 reported no blocked status during its permission dialog (0 of 9), so its dialogs are found by the 3 s screen round.[^alerts-report]

Pane IDs are `backend:id`. A target with a backend prefix selects that backend, which is how `RE:` replies route across terminals.

# Selection

`Detect` reads the environment: `TMUX`, then `HC_TERMINAL_ID`, then `REX_SESSION`, then `ITERM_SESSION_ID`. tmux is first because it is the innermost when nested; hc comes before iTerm2 because a Herdr terminal can inherit `ITERM_SESSION_ID`. Without any of them it falls back to iTerm2 when installed, as agent-msg did. A bare iTerm2 UUID as a target always selects iTerm2, so a reply from another terminal routes.

# Placement

`--where auto` is the default for `spawn`, `task new` and `task review`. harness-ops resolves it in `Ops.Spawn` with `term.PlaceFor` (`internal/term/place.go`), passing the panes the caller started (registry `PaneRecord.Owner`). `termctl spawn` has no registry and resolves it in the backend with no owned panes.

| Case | Result |
|---|---|
| A tab with fewer than `MaxSplit` (12) panes: first the caller's tab, then a tab holding a pane the caller started. The new agent's folder does not matter | `split` of the largest pane in that tab (the newest on a tie, or when the backend reports no sizes) |
| No such tab | `tab`, opened right after the caller's tab |
| A backend that does not report tabs | `split` of the caller, with no cap |
| Listing panes fails (an iTerm2 session closed meanwhile) | `tab`, with a warning; placement never fails a spawn |

So the agents of one job stay in the lead's tab, whatever folder each one works in. Before 2026-11-13 placement grouped by git project, and a worker started in `~` or a scratch folder opened its own tab; Sam wanted the job together instead. Another agent's panes are never joined. iTerm2 and tmux take the chosen pane through `term.Anchored.SpawnAt`; other backends get only the resolved `where`.

**Split direction.** iTerm2 and tmux cut the pane's long side: `term.SideBySide` puts the new pane beside a pane with `cols >= 2*rows` (a cell is about twice as tall as wide), and below it otherwise. A full screen fills as a grid, not as twelve thin columns.

**Even layout.** After every split the backend resizes the tab's panes evenly; a failure is a warning, never a failed spawn. iTerm2: `SetTabLayoutRequest` with the same split tree and new grid sizes from `evenTree` (`internal/term/iterm2_layout.go`). Each pane gets an even share of its row or column, counted in panes across, not by tree depth: in `[a | b/c]` a and the b/c column get half the width each. The totals stay the same, because a different total resizes the window. tmux: `select-layout tiled`, which rebuilds the window as a grid and ignores the split tree. Checked on real iTerm2 3.7.6 (2026-11-13): eight spawns gave a 4x2 grid of 61-62x39 panes, the window kept its bounds, and repeated layouts lost no cells. 3.7.6 predates iTerm2's fix F-343 (2026-10-18) for a layout call that lost a cell per pane; termctl keeps the totals, so it did not trigger here. Sizes come from `SessionSummary.grid_size` in iTerm2's `ListSessions` and `pane_width`/`pane_height` in tmux; they are `Pane.Width`/`Pane.Height`, separate from `Pane.Rows`, which caps captures. An explicit `--where split` uses the same direction rule. `TestSpawnAutoPlacement` fails if twelve panes in a 200x50 tmux window end up narrower than 40 columns or uneven.

hc ignores `--where`, so placement never lists hc panes. Checked on real iTerm2 (2026-11-10): a new tab opened at the next tab index. iTerm2's tab index assumes `ListSessions` returns tabs in visual order, which that check confirmed.

`harness-ops ls --mine` lists the panes the caller started, so a lead can find what it must close.

# Moving and arranging

`move`, `pull`, `swap` and `arrange` (both CLIs) regroup live panes. Code: `internal/term/arrange.go` (resolution, refusals, checks), each backend's `MovePane`, `BreakPane`, `SwapPanes`, `Arrange`, and `internal/harness/arrange.go` (harness-ops guards). Rules:

- **Nothing restarts.** Every backend moves the pane itself, so the process, PID, scrollback and label stay. A backend that cannot do that has no method, and the command exits 1 with `not supported on <backend>` before anything changes. Never fake a move by starting the program again.
- **IDs stay.** After every change `term` lists the panes again and fails if a pane is no longer there under its ID; a label the change dropped is set again. The registry stores pane IDs and no tabs, so a moved worker keeps its task and `PaneRecord` with no update. Checked in `TestMovePullSwapArrangeKeepThePaneAlive`: the worker's task stays `running` on the same pane, and it answers `msg` after the moves.
- **Targets** resolve like `close`: exactly one pane, never the caller's own (exit 2). A busy harness may be moved. A tab target is `self`, `tab-of:<pane>`, or a tab ID from `ls`; `move` also takes `new`. IDs, not positions, because a position shifts when another tab opens or closes. A move that changes nothing (same tab, a pane already alone) is refused.
- **Placement** reuses spawn's: a moved pane splits the largest pane of the destination tab along its long side, and the destination tab is evened out. The tab it leaves is not re-laid out; a tab left empty closes (its pane lives on).
- **Layouts**: `even`, `rows`, `columns`, `tiled`, `main-left`, `main-top`. Main is `--main`, else the caller when it is in the tab, else the first pane. A tab with one pane is left alone.

| Command | tmux | iTerm2 (3.7+) | Rex | hc | Ghostty |
|---|---|---|---|---|---|
| `move --to <tab>`, `pull` | `join-pane -d`, then `select-layout tiled` | `iterm2.move_session`, then the even layout | `session.move_block` through `rex api`, then the window is evened out; within one session only | no | no |
| `move --to new` | `break-pane -d -a` (right after its window) | `iterm2.move_session_to_new_tab` (right after its tab) | a new window with a placeholder `sleep 60`, the block moved in, the placeholder closed, the window moved right after the old one | no | no |
| `swap` | `swap-pane -d` | `iterm2.apply_layout` with the two exchanged, then the old sizes back by `SetTabLayoutRequest` | `rex swap` | no | no |
| `arrange` | `select-layout`: even and tiled → `tiled`, rows → `even-vertical`, columns → `even-horizontal`, main-left/top → `main-vertical`/`main-horizontal` with the window's `main-pane-width`/`-height` set to 50% and main swapped first | even → `SetTabLayoutRequest` (same tree, even sizes); others → `iterm2.apply_layout` with a new tree, then even sizes | even → split ratios by `resize_pane`; others → every block but main detached, then placed again with even ratios (iTerm2's layout definitions), focus given back | no | no |

**iTerm2.** `api.proto` has no message that moves a session; `SetTabLayoutRequest` changes sizes only and refuses another tree. The moves go through built-in functions invoked with `InvokeFunctionRequest` in the app context (`app`, field 4, added to our messages), the same ones the Python API calls: `move_session` (API 1.11), `move_session_to_new_tab` (1.13), `apply_layout` (1.14, base64 JSON spec; it refuses a splitter with one child and same-orientation nesting, so specs are flattened). iTerm2 3.7.6 speaks API 1.21.[^iterm2-move] `move_session` returns success even when it did nothing (a locked layout or session, no room to split), so the result is read back from `ListSessions` and a no-op exits 1. It briefly puts every window in split-selection mode, as a drag does. Checked on real iTerm2 3.7.6 (2026-11-15, `TestMoveOnITerm2`, two scratch tabs of `sleep`): every command kept each `sleep`'s PID.

**Maximized panes (iTerm2).** While a pane is maximized (View > Maximize Active Pane, Shift-Cmd-Return), `ListSessions` puts only that pane in the tab's split tree (`root`). The other panes of the tab still run, and iTerm2 lists them in `Tab.minimized_sessions` (field 6), with no grid size.[^iterm2-proto] Before 2026-11-16 termctl walked only `root`, so those panes vanished from `ls`, and `send`, `read`, `which` and `wait-idle` failed with "no pane matches" until the pane was restored. Now:

- `ls` lists them in their tab, marked `(hidden)` after the title and `"hidden": true` in `--json` (`Pane.Hidden`). `which`, `read`, `msg` and `wait-idle` reach them, and none of them restores the tab.
- `move`, `pull`, `swap` and `arrange` refuse a pane in a maximized tab, or a maximized destination tab, with exit 2 before anything changes: a layout built from `root` would drop the hidden panes. The message says to restore the tab first. The even layout after a spawn warns and skips such a tab.
- A split of the maximized pane (an `auto` spawn into that tab picks it, as the largest pane) restores the tab: iTerm2 puts every pane back in `root`. Checked on iTerm2 3.7.6; placement does not avoid this yet.

Checked on real iTerm2 3.7.6 (2026-11-16) by `TestMaximizedPaneOnITerm2` in `mise run test:iterm`: a scratch tab of three panes, one maximized through `MenuItemRequest` "Maximize Active Pane" (field 121, added to the messages for the test). The item acts on the key window's active pane, so the test first activates its own pane, checks that no other tab changed, and restores the tab before it closes it. It fails with `DISABLED` while another app is in front. Unit tests: `internal/term/iterm2_maximize_test.go`.

**Rex.** Rex has no single call that breaks a block out into a new window or applies a layout, so `move --to new` and `arrange` are built from `session.move_block`, `detach_block`, `resize_pane`, `move_window` and `new_window`. Checked on a private Rex 0.1.0-preview.19 server (2026-11-15, `mise run test:rex`): every command kept each `sleep`'s PID, and a fake-harness task was messaged, moved to a new window and messaged again.

**hc.** herdr-core has no tabs or splits; the Herdr app lays out terminals (`herdr.space`/`herdr.tab` labels, and its own `herdr-app do pane.swap` command). termctl does not drive the app and cannot test it without Sam's live app, so hc refuses all four.

# Backends

| Backend | Notes |
|---|---|
| iterm2 | Own client over one websocket, kept open; each API call times out after 30 s. A call that fails for any reason other than an error iTerm2 answered with (a dropped socket, a timeout) has closed the websocket, so the client is dropped and the next call connects again (a new cookie when the one from the environment is refused). Before 2026-11-17 one such error broke every later call of a long-lived watcher. Spawns through `$SHELL -lic` so mise and PATH load. A new tab uses `select_tab: false`, to keep the user's focus, and `tab_index` to open right after the caller's tab. A command that fails stays open for 8 s, because a session that ends within seconds makes iTerm2 raise a modal dialog that stalls the whole API until someone clicks OK. `Pane.Rows` is the grid height from `ListSessions` (0 for a hidden pane). `Capture` reads the screen (`screen_contents_only`), or, asked for more lines than `Rows`, the last lines of the buffer (`trailing_lines`, capped at 100,000; iTerm2 refuses a range with both set). Both are one `GetBufferRequest` that iTerm2 answers from its own copy, so iTerm2 is a `term.SafeHistory`; the scrollback goes back the profile's Scrollback Lines ([harness profiles](/architecture/harness-profiles.md#dialogs)). Until 2026-11-18 it read the screen only. |
| tmux | CLI. Labels are the pane option `@termctl_label`. Escapes a final `;`, which tmux reads as a command separator. A new window opens right after the caller's (`new-window -a -t <window_id>`; a pane ID there fails with "can't specify pane here"). `TMUX_PANE` is trusted only when `$TMUX` names the same server. `capture-pane -S` reads the history buffer and the program sees nothing, so tmux is a `term.SafeHistory`: a dialog taller than the pane is read from the scrollback ([harness profiles](/architecture/harness-profiles.md#dialogs)). Panes carry no `Rows`, so a screen read there is the last 200 lines, scrollback included. |
| rex | CLI, checked on Rex 0.1.0-preview.19 on a private server (`mise run test:rex`) and listed on the live app. A Rex window is a tab; its ID is the window's last 6 hex digits, as Rex shows it. Details: [the Rex section below](#rex). |
| hc | Herdr (herdr-core `hc` 0.1.19). Tested on a real private core with the fake harness and a no-prompt Claude Code (`mise run test:hc`); real Claude Code and omp messages passed on the live core. `SendText` always refuses: `hc send` always submits, so lines go through `SubmitText`. Pane ID `hc:t7`; label is the `termctl_label` label. `Process` is `foreground.name`, or a known agent found in `foreground.argv`. `where` is validated and ignored: a new terminal opens as a tab in the Herdr app. Details: [the hc section below](#hc). |
| ghostty | Unsupported: every call raises "not supported". Blocker: Ghostty 1.4 (not installed here) has an AppleScript dictionary with input, split, focus and close, but no screen read and no notification, bell or status event; OSC 7501 is parsed by libghostty and ignored by the app, and OSC 9/777 go to Notification Center only. Without a screen read termctl cannot see an input line or a dialog, so no harness-ops command could be safe there. See the research report.[^alerts-report] |

## Deadlines

Every backend call ends, so a terminal that hangs fails one call (exit 1) instead of hanging a watcher round or `task wait` past its `--timeout`. A run of the tmux, hc or Rex CLI is killed after `term.CallTimeout`, 30 s, and fails with `<cli> <command>: no answer after 30s`; an iTerm2 API call has the same 30 s. These calls answer in milliseconds (a Rex CLI call about 180 ms), so 30 s fits a machine under heavy load. A killed CLI whose child still holds its output gets 1 s more before the pipes are closed. `hc send` (through `hc connect`) keeps its own `term.SendTimeout`, 60 s, because it waits until nobody has typed in the pane for about 3 s. Code: `cliCommand` in `internal/term/proc.go`; test: `TestHungCLIFailsAfterItsDeadline`. `ps` and `lsof`, which tmux and iTerm2 listings run to name each pane's program and folder, have the same deadline; a failed `lsof` leaves folders empty, as before. Before 2026-11-17 none of these runs but `hc send` had a deadline.


# Rex

Measured on Rex 0.1.0-preview.19,[^rex-cli] and the reason the code is the way it is:

- **Which server.** Rex sets no server variable in its terminals (only `REX_SESSION=session:<uuid>`, `REX_BLOCK=block:<uuid>`, `REX_BIN_DIR`, `TERM_PROGRAM=rex`). A bare `rex` reads `REX_SERVER`, then `$REX_RUNTIME_DIR/server.sock`, then `$XDG_RUNTIME_DIR/rex/server.sock`, then the app's `~/Library/Application Support/rex/server.sock`, and **autostarts** a server where it looked first. A shell that exports `XDG_RUNTIME_DIR` therefore misses the app. The backend uses `REX_SERVER`, else the one socket that exists, else the one whose server holds `REX_SESSION`, and passes `-S <addr> --autostart=false` on every call. termctl never starts a Rex server: with none, `ls` is empty and other commands exit 1.
- **Which CLI.** The CLI is not on `PATH` by default; it sits in the app's `Rex Server.app`. Order: `REX_BIN_DIR`, `PATH`, the app bundle.
- **Listing.** `block ls` and `window ls --json` need `-s`, so `ListPanes` runs one `rex do -e` Lua script over every session. Title, cwd, process and size come from `rex block call -b <id> process|title|size`. Each CLI call costs about 180 ms.
- **CLI quirks.** `rex send` and `block rename` read text that starts with `-` as a flag, so the backend passes `--`. With `REX_SESSION` set, a block ID resolves only inside that session, so the backend drops `REX_SESSION`, `REX_BLOCK` and `REX_CLIENT` from the environment of the `rex` processes it runs. Exit 3 is an unknown target (exit 2 for us), exit 4 no server.
- **Spawn.** `split` splits the anchor on its long side, then evens the window; `tab` is `window new` in the caller's session, moved right after the caller's window; `window` is `rex new`, a new session. Commands run as `$SHELL -lic`.
- **Screen and scrollback.** `rex capture -b <id> --trim` prints the block's scrollback (about 64 MiB) and then its screen, from the server's copy; `Capture` keeps the last lines asked for, so a read past `Pane.Rows` reaches into the scrollback. A probe in raw mode logging its input saw nothing during repeated reads, on the main and the alternate screen (2026-11-18), so Rex is a `term.SafeHistory`. Every read moves the whole scrollback over the CLI, whatever lines are asked for. `--trim` drops blank rows at the bottom, so a screen read of a screen with blank rows below its text starts a few lines into the scrollback.
- **Sessions are walls.** Moves and swaps work only inside one session ("blocks are in different sessions"); a cross-session move is refused before anything changes.
- **Not checked:** real pane sizes (a headless server reports 80x24 for every block) and choosing between several live servers (unit test only). Program status is read through `ProgramStatuses` (one Lua call); `NativeStatus` still returns nothing on Rex, so `WaitIdle` does not use it there.

# hc

Measured on hc 0.1.19 and the reason the code is the way it is:

- **Any hc command auto-starts a core**, including `hc ls` and `hc server status`. A core keeps the environment of whoever started it, so an `agent-ops ls` from an agent's pane must not create one. Read-only paths check the socket (`$XDG_RUNTIME_DIR` or `~/.local/run`, `herdr-core/<HC_SESSION or default>.sock`; `HC_TERMINAL_SOCKET` and `HC_SOCKET` override) and return no panes when nothing listens. `Spawn` and every other command refuse with exit 1 when no core runs: open Herdr first. Tasks list panes through `term.ListLive` (the optional `LiveLister`), which fails instead of returning nothing: during `hc server restart` the terminals keep running, so a recorded pane is unknown, not gone, and reconcile leaves its task alone for up to 10 minutes; a core gone longer is gone for good, and its tasks are lost (see [tasks](/architecture/tasks.md#lifecycle)). Before 2026-11-17 an unreachable core listed no panes, and a watcher's reconcile (every 3 s) could mark every hc task lost.
- **Programs get the core's environment**, not the caller's (`hc run` adds only `TERM`, `TERM_PROGRAM`, `COLORTERM` and the `HC_*` variables). A core started from Claude Code therefore passes `CLAUDE_CODE_CHILD_SESSION` to everything it runs, which makes a child Claude print "Transcript saving is off". `Ops.Spawn` clears that one variable on hc; the API's per-terminal `env` can only set variables. The backend's own spawn script clears `ITERM_SESSION_ID`, `TERM_SESSION_ID`, `TMUX`, `TMUX_PANE`, `REX_SESSION`, `REX_BLOCK`, so a worker never detects the wrong backend.
- **Messages to a Herdr agent show `<hc from="<backend>/<id>">`** around the `[agent-ops ...]` line. hc adds the tag when the caller holds a slash and the target reports status. `SubmitText` calls the API's `terminal.send` through `hc connect`, where the caller is optional: `Msg` names the sender, and slash commands and spawn prompts send none, because a tag would stop them working. Targets in iTerm2 or tmux never go through hc and keep the plain line. Other hc subprocesses do not see `HC_TERMINAL_ID`, so polling does not count as that terminal looking at its children.
- **A spawned agent shows in the Herdr app**: the app hides a terminal without a `herdr.space` label, and an id it does not know becomes a new space named after the folder. `Spawn` uses the caller's space when the caller is a Herdr terminal with one, then the space of another terminal in the same folder, then an id hashed from the folder. It sets no `herdr.tab`, so each agent gets its own tab, and it needs no app commands. Checked on the live app: focus stays where it was, and a space closes itself with its last agent.
- **Socket path**: a core started by the Herdr app has no `XDG_RUNTIME_DIR`, so its socket is under `~/.local/state/herdr-core/<session>/`. A shell that exports `XDG_RUNTIME_DIR` looks elsewhere and needs `HC_SOCKET`; the dotfiles `.zshrc` sets it on macOS. `hc` does not read `HC_TERMINAL_SOCKET`, even inside a Herdr terminal, so `hcEnv` passes it on as `HC_SOCKET` unless one is already set.
- **`hc ls --json` has no labels, title or foreground**, so `ListPanes` runs one `hc get` per terminal. `hc ls` reports the program as `zsh` when a shell launched it; only `get`'s `foreground` names the real program.
- **Status words**: `idle`, `working`, `done`, `blocked`, `error`, from the program's OSC 7501 reports (`ESC ] 7501 ; state=<word> BEL`). `blocked` is not ready for input: `WaitIdle` and `close` treat it as busy. hc refuses to send to a blocked program; the refusal maps to exit 6 and there is no `--force` for it.
- **Screen reads**: `Capture` uses `--lines 0` when the request fits the screen. History reads can scroll a full-screen app, so Layer 2 asks for at most the pane's height (`Pane.Rows`, from `get`), and hc is not a `term.SafeHistory`.
- **Restart identity**: `core.id` survives `hc server restart`, under which terminals keep running; the core pid does not. `ServerRef` returns both, and tasks compare `core_id` when it exists.
- **A harness can refuse a submitted line**: `hc send` delivers Enter, but omp with no API key for its current model shows `No API key found for <provider>` and keeps the text on the input line. `Submit` then reports the text as typed but not submitted; `read` shows the harness's error. Found on `homebox` (2026-11-09), where omp started on a `tokenharbor` model that has no key on that box; on a model with a key, sends from the Mac submitted.
[^rex-cli]: Rex CLI reference
[^iterm2-move]: iTerm2 built-in functions move_session, move_session_to_new_tab, apply_layout
[^iterm2-proto]: iTerm2 API messages (ListSessionsResponse.Tab.minimized_sessions, MenuItemRequest)
[^iterm2-status]: iTerm2 built-in function get_session_status
[^osc7501]: Program Status Protocol (OSC 7501)
[^alerts-report]: Capturing terminal alerts per pane, research report (2026-11-17)
