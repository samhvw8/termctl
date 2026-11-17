# Capturing terminal alerts per pane: research report

Date: 2026-11-17. Question: how can a background process (the harness-ops watcher) capture the alerts a program in a pane posts (BEL, OSC 9, OSC 777, OSC 99, OSC 7501, iTerm2-style "ready for input") per backend, as push or poll, with the pane identity attached? Which escapes do Claude Code and agy actually emit?

Method: binary and changelog reads of the installed agents, source reads (iTerm2, tmux, Ghostty on GitHub; Rex docs; `hc --help` and `--skill`), and real runs. Each backend got the same test script, which prints BEL, OSC 9, OSC 777, OSC 99, OSC 7501 blocked, OSC 21337, OSC 7501 done, OSC 1337 RequestAttention and OSC 9;4, a few seconds apart:
- **iTerm2 3.7.6:** one test tab opened with `termctl spawn --where tab`. I watched it with `it2 monitor variable`, polled it with `it2 session get-status`, then closed it.
- **tmux 3.8:** private socket `termctl-test-research`, with hooks, `pipe-pane -O` and a `-C` control client. Server killed afterwards.
- **hc 0.1.19:** private core (`HC_SESSION=termctl-test-research`, private XDG folders) with `hc events --json`. Core stopped afterwards.
- **Rex 0.1.0-preview.19:** private server with its own socket, state and config, watched with `rex events --all --json`. Server stopped afterwards.

Versions: iTerm2 3.7.6 (it2 1.0.0), tmux 3.8, Rex 0.1.0-preview.19 (API revision 14), hc 0.1.19, Ghostty 1.4.0 (latest release, 2026-10-27; not installed), Claude Code 2.1.326, agy 1.5.10, omp 18.22.0, macOS 27.2.1.

## 1. What the agents emit

| Agent | Status (OSC 7501) | One-shot alert | Hooks a watcher could use | Evidence | Confidence |
|---|---|---|---|---|---|
| Claude Code 2.1.326 | Yes. It sends `OSC 7501 ; ?` at startup and reports `state`, `progress`, `title` and `msg` only if the terminal answers. Under tmux it sends nothing, because tmux doesn't answer. | Setting `preferredNotifChannel`, default `auto`. With `auto`: OSC 9 in `iTerm.app`, OSC 99 in kitty, OSC 777 in ghostty, BEL in Apple Terminal (if its bell is on), nothing elsewhere. Inside tmux: nothing when `allow-passthrough` is off; when on, it wraps the sequence in `ESC P tmux;` for the outer terminal. Fixed values: `iterm2`, `iterm2_with_bell`, `terminal_bell`, `kitty`, `ghostty`, `notifications_disabled`. | `Notification` hook, with matchers such as `permission_prompt`, `idle_prompt`, `agent_needs_input`, `agent_completed`, `elicitation_dialog`. The hook runs with the agent's environment, so it sees `TMUX_PANE`, `ITERM_SESSION_ID`, `REX_BLOCK` and `HC_TERMINAL_ID`. | Strings in `~/.local/share/mise/installs/claude/2.1.326/claude`; docs [terminal-config](https://docs.anthropic.com/en/docs/claude-code/terminal-config) and [hooks](https://docs.anthropic.com/en/docs/claude-code/hooks). Live: every Claude pane in iTerm2 shows a status set by OSC 7501, and its `detail` is the current task text. | High for OSC 7501 (seen live); medium for the OSC 9 path (from the binary, not captured) |
| agy 1.5.10 | Yes, after checking that the terminal supports it. `AGY_CLI_OSC7501=on` forces it on (e.g. in tmux), `off` turns it off, `AGY_CLI_OSC7501_MESSAGES=brief` drops the details. | With `"notifications": true`, which is set in `~/.gemini/antigravity-cli/settings.json`, the binary holds `ESC ]9;` and `%v is ready for input` followed by BEL. That reads as an OSC 9 saying "<name> is ready for input". The changelog says it also fires for pending questions and permission prompts. It also sends OSC 9;4 progress. | Hooks exist: `PreToolUse`, `PostToolUse`, `Stop`, `SessionStart`, `PreInvocation`. I found no Notification event. | `agy changelog` (the OSC 7501 entry and the notifications-for-questions entry), binary strings. Live: agy panes in iTerm2 show `done` with a detail line. | High for OSC 7501; medium for the OSC 9 text (inferred, not captured) |
| omp 18.22.0 | Yes: it sends `OSC 7501 ; ?`, then reports. | OSC 9, OSC 99 and `OSC 777;notify` encoders, chosen per terminal from a table in the binary. | Not checked | Binary strings. Live: omp panes in iTerm2 show `done`. | High for OSC 7501; low for which one-shot alert it picks |

So OSC 7501 is the one signal all three agents send, and only where the terminal answers the check. iTerm2 3.7.6, Rex and hc answer; tmux doesn't. In iTerm2's tmux integration (`-CC`) the answer only arrives from 3.7.8beta2.

## 2. Per-backend findings

### iTerm2 (3.7.6 installed)

| Mechanism | Push or poll | Pane id? | Setup / permission | Version | Confidence | Source |
|---|---|---|---|---|---|---|
| Session variable `status` changes on OSC 7501 and OSC 21337. Subscribe with `NOTIFY_ON_VARIABLE_CHANGE`, using `VariableMonitorRequest{name:"status", scope:SESSION, identifier:<session>}`. | Push | Yes, one subscription per session. Variable changes don't use `NotificationRequest.session`, so the watcher subscribes per session and follows `NEW_SESSION` / `TERMINATE_SESSION`. | None beyond the API access termctl already has | OSC 7501 support since 2026-10-09, included in 3.7.6 | High: `it2 monitor variable status` printed `waiting`, then `Waiting`, then `done` | [iTermProgramStatus.swift](https://github.com/gnachman/iTerm2/blob/master/sources/VT100/iTermProgramStatus.swift), [iTermVariables.m](https://github.com/gnachman/iTerm2/blob/master/sources/Language/iTermVariables.m) |
| Session variable `bellCount` counts bells | Push (same monitor) or poll | Yes | None | In 3.7.6 | High: went to 1 on BEL | same |
| Built-in function `iterm2.get_session_status(session_id)` returns `status`, `detail`, `dot_color`, `text_color`, `background_tasks`, `turn_open`. Reachable via `InvokeFunctionRequest` (termctl already uses it for `move_session`) or `it2 session get-status -s <id>` (what `NativeStatus` runs today). | Poll | Yes | None | Since 2026-04 | High: tested on live and test panes | [SetStatusBuiltInFunction.swift](https://github.com/gnachman/iTerm2/blob/master/sources/Language/BuiltInFunctions/SetStatusBuiltInFunction.swift) |
| How OSC 7501 states show up: `blocked` becomes `waiting`, with `detail` set to the decoded `msg`; `done` and `working` keep their names. The Claude Code integration hook adds a `background` status. | n/a | | | 3.7.6, 3.7.8beta2 | High | [notes-3.7.8beta2](https://github.com/gnachman/iTerm2/blob/master/docs/notes-3.7.8beta2.txt) |
| OSC 9, OSC 777 and `1337;Notification` go to `terminalPostUserNotification`, which only posts to Notification Center. No API event and no variable change. | Nothing reaches the API | n/a | iTerm2 posts only for background sessions whose profile filter allows escape-sequence alerts | all | High: source read, and no variable moved in the test | [VT100Terminal.m](https://github.com/gnachman/iTerm2/blob/master/sources/VT100/VT100Terminal.m) |
| Triggers: Post Notification, Invoke Script Function, Set Tab Status (regex on output) | Push | Yes | Edit each profile (user config) | all | Medium: not tested | [iTermUserNotificationTrigger.m](https://github.com/gnachman/iTerm2/blob/master/sources/Triggers/iTermUserNotificationTrigger.m) |
| `NOTIFY_ON_PROMPT` (shell integration) | Push | Yes | Shell integration | all | High | api.proto |

Note: `it2 monitor` exited with "Socket recv failed: Resource temporarily unavailable" shortly after the first change in all three of my runs. That's a bug in the it2 client, not an API limit.

### tmux (3.8 installed)

| Mechanism | Push or poll | Pane id? | Setup | Confidence |
|---|---|---|---|---|
| Hook `pane-bell` (`run-shell "… #{hook_pane}"`) | Push | Yes; fired `pane-bell %0` | A server option: free on a private server, a config change on the user's server | High: tested |
| Hook `alert-bell` (needs `monitor-bell on`) | Push | Window only (`@0`) | same | High: tested |
| Control mode (`tmux -C attach`): `%output %<pane> <raw>` carries the pane's raw output. OSC 9, 777, 99, 7501, 21337, 1337 and 9;4 all arrived unchanged. | Push | Yes | A long-lived control client per server. It gets all output of the session, and an attached client affects window sizes unless flagged | High: tested |
| `pipe-pane -O`: the same raw bytes, one pipe per pane | Push | Yes | Replaces any pipe the user has | High: tested |
| `refresh-client -B 'pb:%*:#{pane_pb_state}/#{pane_pb_progress}'` reports `%subscription-changed … %0 : normal/50` when OSC 9;4 progress changes | Push, at most once a second | Yes | Control client | High: tested |

tmux's own parser handles OSC 9 only as `9;4` progress; OSC 9 text, 777, 99 and 7501 are dropped ([input.c @3.8](https://github.com/tmux/tmux/blob/3.8/input.c)). tmux doesn't answer `OSC 7501;?`. `allow-passthrough` (off by default) forwards `ESC P tmux;` sequences to the outer terminal, which then credits the iTerm2 session that hosts tmux, not the tmux pane. A `window_bell_flag` subscription did not turn to 1 for the attached client's current window.

### Rex (0.1.0-preview.19 installed)

| Mechanism | Push or poll | Pane id? | Confidence | Source |
|---|---|---|---|---|
| `rex events --all --json`: block events carrying `block_id`. Seen: `bell`; `desktop_notification` {title, body} for **OSC 9 and OSC 777**; `program_status_changed` {id, record{state, kind, app, msg, owner}, reason}; `program_status_removed`; `progress_report` for OSC 9;4; `title_changed`, `process_changed`, `child_exited`. OSC 99 produced no event. | Push, one stream for the whole server | Yes: `block_id`, the same id termctl uses | High: tested on a private server | [Events](https://www.superlogical.com/rex/docs/reference/events), [Lua events](https://www.superlogical.com/rex/docs/automate/lua-events) |
| `rex do watch.lua --all` with `rex.on("terminal.desktop_notification" / "terminal.bell" / "terminal.program_status_changed", …)` | Push (Lua) | Yes (`ctx.block_id`) | High from the docs; not run | same |
| Block method `com.superlogical.terminal.program_status` (`rex block call program_status -b <block>`) | Poll | Yes | Medium: listed by `rex api list`, not exercised | `rex api list` |
| Mac app settings: "Terminal notifications" (OSC 9/777 become native notifications) and "Program Activity" (tab dots, notifications, Dock badge and sounds for blocked, error and done) | Display only | | High | [Mac settings](https://www.superlogical.com/rex/docs/reference/mac-settings) |

### hc / Herdr (0.1.19)

| Mechanism | Push or poll | Pane id? | Confidence |
|---|---|---|---|
| `hc events --json` (filters: `--terminal`, `--label`, `--children-of`). Event types: `created`, `foreground`, `status` {status{state, kind, app, msg}, records, reported}, plus heartbeats. Seen: blocked (kind=permission, msg decoded), then done. | Push, one stream per core. Check the socket first, since any hc command auto-starts a core. | Yes (`terminal: "t1"`) | High: tested on a private core |
| `hc get <t> --json` gives `status`, `status_ms`, `reported` (what `NativeStatus` reads today) | Poll | Yes | High: tested |
| BEL, OSC 9, 777, 99 and 21337 | Ignored: no event and no field | | High: tested |
| The `notify` field on a terminal | Not a desktop alert: it's hc's own switch for exit and blocked notes to the parent agent (`--no-notify`) | | High |

### Ghostty (1.4.0 is the latest release; not installed)

- OSC 9 and OSC 777 become macOS notifications (`desktop-notifications = true` by default). OSC 99 is parsed but not implemented. OSC 7501 is parsed by libghostty, but "the Ghostty GUI ignores it for now" ([1.4.0 notes](https://ghostty.org/docs/install/release-notes/1-4-0)).
- AppleScript (1.3+) exposes windows, tabs and terminals with `id`, `name`, `working directory`, `pid` and `tty` (pid and tty since 1.4). Commands: input and keys, split, focus, close, `perform action`.
- `Ghostty.sdef` has no screen-content read and no notification or bell event. Using AppleScript triggers the macOS Automation permission prompt.
- `notify-on-command-finish` (based on OSC 133) only produces a bell or a notification.
- Sources: [AppleScript docs](https://ghostty.org/docs/features/applescript), [Ghostty.sdef](https://github.com/ghostty-org/ghostty/blob/main/macos/Ghostty.sdef).

### macOS Notification Center database (last resort)

- The store is at `~/Library/Group Containers/group.com.apple.usernoted/db2/db` (SQLite, WAL); it exists on this Mac. Before Sequoia it lived at `$(getconf DARWIN_USER_DIR)/com.apple.notificationcenter/db2/db`.
- Reading it needs Full Disk Access, and the schema is undocumented.
- Rows hold the app's bundle id and the text, but no pane identity. iTerm2's notification title names the session; Ghostty's doesn't.
- It only holds what the terminal chose to post.
- Not tested: private data, and it needs an FDA grant.

## 3. Summary matrix

| Signal | iTerm2 | tmux | Rex | hc | Ghostty |
|---|---|---|---|---|---|
| OSC 7501 | Push (`status` variable) and poll (`get_session_status`) | Not parsed; visible in the raw stream, but agents don't send it there | Push (`program_status_changed`) and poll | Push (`status` event) and poll (`hc get`) | None |
| BEL | Push or poll (`bellCount`) | Push (`pane-bell`, raw stream) | Push (`bell`) | None | None |
| OSC 9 / 777 text | None (Notification Center only) | Raw stream only | Push (`desktop_notification`) | None | None (Notification Center only) |
| OSC 99 | None | Raw stream only | None | None | None |
| OSC 9;4 progress | Drawn only | Push (`pane_pb_state` subscription) | Push (`progress_report`) | None | Drawn only |

## 4. Design options for an "alert source" feeding the existing watcher

Context:
- `internal/harness/watch.go` runs one watcher per owner, every 3 s.
- `screenSource` reads each worker's screen for a dialog, and treats a `NativeStatus` starting with `blocked` as a reason to look again.
- `Signal` and `Source` were left open for "a terminal's own notification events, behind an optional Layer 1 interface".
- `NativeStatus` works for iTerm2 (it starts `it2 session get-status` for each pane) and hc, and returns "" for Rex, tmux and Ghostty.

These are options, not a ranking.

**Option A: push adapters per backend (an optional Layer 1 `Alerts` interface).**
- How it works: the watcher subscribes per backend and turns native events into signals (`status`, `bell`, `notify`) with the pane id. iTerm2: variable monitors on `status` and `bellCount` over the existing websocket. Rex: one `rex events` stream. hc: one `hc events` stream. tmux: the `pane-bell` hook or a control client parsing `%output`. Screen reads run when an event arrives.
- Gains: lowest latency, and no new process forked per pane every 3 s. It's the only option that catches Rex's OSC 9/777 text and bells between polls.
- Running cost: no new daemon (it lives in the watcher), but each stream must survive restarts (iTerm2 restart, `hc server restart` via `core_id`), pick up new sessions, and absorb bursts. The tmux control client sees all output of a session and can change sizing. The `pane-bell` hook changes the user's tmux config.
- Risks: event formats may change (Rex is preview, hc is 0.1.x), and each adapter needs its own end-to-end test.
- Gaps: no OSC 7501 in tmux, and nothing for Ghostty.

**Option B: poll native status inside the existing loop.**
- How it works:
  - iTerm2: call `get_session_status` through `InvokeFunctionRequest` on the open websocket instead of starting `it2`, and read `bellCount` each tick (the counter catches bells between polls).
  - Rex: add `program_status` (one Lua call for all blocks, as `ListPanes` already does).
  - hc: keep `hc get`.
  - tmux: add `#{pane_pb_state}`.
  - A `blocked` or `waiting` status (with its `msg`) becomes a signal, confirmed by a screen read.
- Gains: smallest change and stateless. Nothing to reconnect or resubscribe; same 3 s rhythm, lock and registry.
- Running cost: calls per pane per tick. iTerm2 calls over the websocket are cheap; a Rex CLI call costs about 180 ms unless batched. No permissions.
- Misses: one-shot OSC 9/777/99 alerts (including Rex's `desktop_notification`), up to 3 s of latency, and tmux and Ghostty stay screen-only.

**Option C: the agent reports itself (hooks write to a termctl inbox).**
- How it works: Claude Code's `Notification` hook runs a small `harness-ops alert` command. That command reads the pane from inherited variables (`TMUX_PANE`, `ITERM_SESSION_ID`, `REX_BLOCK`, `HC_TERMINAL_ID`, in the same order termctl's `Detect` uses), and `notification_type` and `message` from the JSON on stdin. It appends a line to an inbox file, which the watcher reads as another source.
- agy can only use its `Stop` hook (it has no Notification event), so its permission prompts still rely on status or screen reads. omp is unchecked.
- Gains: works on every backend, including tmux and Ghostty, and carries the agent's own reason, so there's no screen guessing.
- Running cost: hook config for each harness. It can be injected per worker at spawn (Claude `--settings`, agy `--settings`) rather than in global settings, but it still has to track each harness's hook format. It adds a second writer to the state directory, so the inbox needs its own locking rules.
- Risks: workers not spawned by harness-ops have no hook, and a slow hook can delay the agent (it must be fire-and-forget with a short timeout).

**Other sources considered, not full options:**
- **Output tap:** tmux `pipe-pane -O`, or a wrapper such as `termctl tap -- <agent>`. It sees every escape, but replaces the user's own `pipe-pane`, and a wrapper sits in the agent's I/O path (resize, signals, a crash breaks its I/O). In tmux, agents still skip OSC 7501 because nothing answers the check.
- **Notification Center database:** needs Full Disk Access, has no pane id, and its schema is undocumented.
- **iTerm2 triggers:** this is just screen matching moved into iTerm2, with config per profile.

## 5. Safety log

- **Live panes:** the panes listed at the start were 71287783, FA0E8C87, 6DD9BB35, 6B3DC822, D3F69CBA, DB539708, 6A617B57, 96B4FFCC, 63CBF9C0, 7838671C, 70153E99, E5429F45, 7D7E8C6C, 068F3655 and EFD186D6. They were only read, never sent anything.
- **Accidental pane:** a bare `termctl spawn` (run to see its usage) opened `iterm2:F7BB19FC-…`, closed within seconds. The test tab `iterm2:B0407953-…` was created and closed by the experiment.
- **Private rigs:** the tmux server on socket `termctl-test-research` was killed afterwards, the hc core was stopped, and the Rex server was stopped. Their temporary folders were removed.
- **State directory:** every termctl and harness-ops call used `TERMCTL_STATE_DIR` pointing to the scratchpad, cleaned with `harness-ops state clean`.
- No commits, and no code edits.

## 6. Not verified

- Not tested: the OSC 9 bytes from agy and Claude, captured live (it would cost a model turn per agent). Risk: the exact text or the auto-detected channel may differ from what the binaries suggest.
- Not tested: Rex's `program_status` block call, and reading the Notification Center database.
- Not tested: Ghostty (not installed). Everything about it comes from docs and the sdef.