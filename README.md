# termctl

A terminal driver with backends, and `harness-ops`, a layer on top for talking to coding-agent TUIs.

```sh
termctl ls [--json] [--all]                  # panes
termctl which <target>                       # resolve a target; never sends
termctl spawn [--where auto|tab|split|window] [--cwd D] [--title T] [CMD...]
termctl send-text <target> <text...>         # literal, no Enter
termctl send-key <target> <key...>           # enter, escape, ctrl-c, ctrl-u, up, ...
termctl capture <target> [-n 40]
termctl close <target>
termctl move <target> --to <tab>             # into another tab; <tab>: new, self, tab-of:<pane>, or a tab ID from ls
termctl pull <target>                        # into your tab (move --to self)
termctl swap <a> <b>                         # trade places
termctl arrange [<tab>] --layout even|rows|columns|tiled|main-left|main-top [--main <pane>]
termctl name <label>
termctl whoami

harness-ops ls [--json] [--all] [--mine]     # --mine: only panes you started
harness-ops msg [--force] [--clear-first] [--timeout S] <target> <text...>
harness-ops slash [--force] [--wait] <target> </command>
harness-ops clear [--force] <target>         # /clear alone, then wait for idle
harness-ops wait-idle [--timeout S] <target>
harness-ops spawn <harness> [--where auto|tab|split|window] --cwd D [--prompt T | --prompt-file F] [--label L] [--command CMD]
harness-ops close [--force] <target>         # only panes made by spawn/task new; never your own
harness-ops interrupt [--key K] <target>     # stop the current turn (Esc on claude, omp and agy)
harness-ops move|pull|swap|arrange ...       # as termctl; a moved pane keeps its label and task
harness-ops which|read|name|whoami           # as agent-ops; send = msg

harness-ops task new <harness> --title T (--brief TEXT | --brief-file F) --cwd D [--where auto|tab|split|window] [--reuse <target>] [--max N]
harness-ops task ls [--all] [--json]
harness-ops task show <id> [--json]
harness-ops task send [--force] <id> <text...>       # on a ready task: opens the next round
harness-ops task wait <id> | --any [--timeout S]
harness-ops task done <id> --status ok|failed|blocked (--summary TEXT | --summary-file F)   # the worker reports
harness-ops task accept <id> [--close]               # the lead's verdict: only this makes a task done
harness-ops task review <id> --blind --harness H [--model M] [--brief-file F]
harness-ops task close [--force] <id>                # refused until accepted, unless --force
```

`--where auto` is the default. A new agent splits the caller's tab, whatever its folder, or a tab of an agent the caller started when the caller's tab is full. It splits the largest pane there along its long side and evens out the pane sizes, up to 12 panes per tab. Otherwise it opens a new tab right after the caller's tab. Herdr ignores `--where`.

## Moving panes

`move`, `pull`, `swap` and `arrange` regroup running panes. The program keeps running: same PID, scrollback, label, task and registry record. A busy harness may be moved; your own pane may not, and a target must resolve to exactly one pane (exit 2).

- A tab is named `self`, `tab-of:<pane>`, or by the tab ID in the TAB column of `ls` (IDs, not positions, which shift). `move --to new` gives the pane a tab of its own, right after its old one.
- A moved pane splits the largest pane of the destination tab, and that tab is evened out, as after a spawn.
- `arrange` lays out a whole tab (default: yours). Main is `--main`, else you when you are in the tab.

| Backend | move, pull | move --to new | swap | arrange |
|---|---|---|---|---|
| tmux | yes (`join-pane`) | yes (`break-pane`) | yes (`swap-pane`) | yes (`select-layout`; `even` = `tiled`) |
| iterm2 (3.7+) | yes (`move_session`) | yes (`move_session_to_new_tab`) | yes (`apply_layout`) | yes (`apply_layout`, then even sizes) |
| rex | yes (`session.move_block`), within one session | yes (a new window, the block moved in, a placeholder closed) | yes (`rex swap`), within one session | yes (`session.resize_pane` for `even`; the others detach and place every block) |
| hc, ghostty | no | no | no | no |

An unsupported command exits 1 with `not supported on <backend>` and changes nothing.

## Tasks

A task is one harness session doing one job, tracked in `~/.local/state/termctl/registry.json`.
`task new` starts the harness and submits the brief, followed by a footer that tells the worker how to report. It prints the task id, which is also the pane's label.

| Status | Meaning |
|---|---|
| running | The worker has the job. |
| ready | The worker reported success. The session stays open for the lead. |
| failed, blocked | The worker reported that. The session stays open for the lead. |
| done | The lead accepted it. Only `task accept` sets this. |
| exited | The pane is alive but the harness is gone. The error stays on screen. |
| lost | The pane is gone, or the task never got one. |

- Worker's `task done` means "ready for the lead". It never closes the session and never produces `done`.
- `task send` on a ready, failed or blocked task opens the next round in the same session. `task ls` shows the round.
- `task close` is refused until the task is accepted, unless `--force`.
- A spawned harness leaves a shell behind, so a crash stays readable. The owner is told when the harness exits.
- The owner is told with a tagged `RE: task ...` message. If the owner's input line holds a draft, the result stays pending. Any later harness-ops call delivers it, for one hour.
- `task wait --any` returns each finished task once, so repeated calls walk through the results.
- Guards (exit 6): 6 running tasks per owner (`--max`, `TERMCTL_MAX_TASKS`), spawn depth 2 (`TERMCTL_MAX_DEPTH`).
- `--reuse` needs an idle pane running that harness. Closing such a task leaves the pane open, because it was not created for the task.

## State directory

`~/.local/state/termctl` is live data: the tasks other agents are running. For tests and experiments, set `TERMCTL_STATE_DIR` to a temp directory. `harness-ops state clean` removes that directory, and refuses the live one.

## Review

- **Same session (`task send`)** is the normal flow. The worker has the files loaded, so reviewer findings go there for a verdict.
- **Blind (`task review --blind`)** starts a new session that gets the requirements and the files, not the worker's report, summary or conversation. Its result reaches the owner like any task.

Blind reviewers run read-only where the harness supports it. Only Claude Code is verified: `--setting-sources project --permission-mode dontAsk`, with write tools denied and only read tools, read-only git and the report command allowed. On a real session the Write tool and shell writes were denied. Plan mode cannot be used: it stops at an approval dialog and cannot report. Other harnesses get a warning and can change files.

## Closing panes

`close` acts only on panes recorded by `spawn` or `task new`, and refuses a busy harness or a pane that holds a task not yet accepted. `--force` overrides those. Nothing closes your own pane. `termctl spawn` is not recorded: that is Layer 1.

Global options come before the subcommand: `--backend iterm2|tmux|rex|hc|ghostty`, `-L <tmux-socket>`.
The backend is detected from `TMUX`, `HC_TERMINAL_ID`, `REX_SESSION`, `ITERM_SESSION_ID`, in that order. A target such as `tmux:%3` or `hc:t7` selects its backend.

## Target resolution

First rule with matches wins: exact pane ID, label, exact title, folder basename, title substring, ID prefix (4+ chars).
No match, several matches, or your own pane exits 2 and lists candidates. It never falls back to the focused pane.

## Rules of harness-ops

- `msg` and `slash` refuse (exit 4) when the target's input line holds a draft, unless `--force`.
- A calibrated harness (claude, omp, agy) whose input line cannot be read also refuses. Others warn and proceed.
- `slash --force` wipes the draft with ctrl-u: a slash command must be the whole line.
- `msg --clear-first` sends `/clear` alone, waits for idle, re-checks the input line, then sends.
- A message over 800 characters or with line breaks goes to a file in `~/.local/state/termctl/projects/<project>/`. Every message and task event is a line of the journal: `harness-ops log` and `harness-ops resume` read it back.
- Put options before the target; everything after the target is message text. Use `--` to send text starting with a flag.
- `task wait` also returns when a worker waits on a permission dialog, and `ls --mine` shows a pane you started that waits on one as STATUS `dialog <fp>`; `harness-ops answer TARGET N --expect FP` or `harness-ops deny TARGET --expect FP` answers it (`--expect` is required). TARGET is the task ID, or a pane you started with `spawn` or `task new` (pair partners and live-team members); a pane you did not start is refused. Any choice not known to grant only this one request ("Yes", "Yes, run command", "No...") needs `--persist`: "always allow", "don't ask again", and wording not seen before. `task new` (and `spawn` with a prompt, or a `msg` to a pane you started) starts a `watch` that sends each new dialog on the panes you started to you, until none of them is working or blocked, with the harness's own reason when the terminal keeps one (OSC 7501 status on iTerm2, Rex and hc). The watcher reads every worker's screen each 3 s in parallel, asks iTerm2 for program status every second in between (a free websocket call; Rex and hc are asked once per round, since each read starts a process), and restarts itself when its executable is replaced. `mise run test:dialogs` checks this against real Claude Code and agy on all four backends.

Exit codes: 0 ok, 1 backend error, 2 refused target or usage, 4 draft on input line, 5 wait timed out, 6 refused by a safety limit, 7 no dialog on screen or not the one expected.

## Backends

| Backend | State |
|---|---|
| iterm2 | Tested. termctl's own Go client for the iTerm2 API over one websocket; program status through iTerm2's built-in `get_session_status`, no `it2` process. |
| tmux | Tested on a private socket. |
| hc | Tested on a real private herdr-core with the fake harness (`mise run test:hc`). `termctl send-text` is refused: `hc send` always submits; use `harness-ops msg` or `slash`. `--where` is ignored: a spawned agent opens as a tab in the Herdr app. A blocked program cannot be messaged, and there is no `--force` for it. |
| rex | Tested on a real private Rex server, 0.1.0-preview.19 (`mise run test:rex`). The CLI is found through `REX_BIN_DIR`, then `PATH`, then the Rex app bundle. The server is `REX_SERVER` when set, else the local socket whose server holds `REX_SESSION`; termctl never starts a server. A Rex window is a tab; `--where window` opens a new session. Blocks move and swap only within their session. |
| ghostty | Unsupported: raises "not supported". Ghostty 1.4's AppleScript can type and split but has no screen read and no status or notification event, so termctl could not see a prompt or a dialog there. |

## Install

```sh
mise use -g github:samhvw8/termctl   # latest release, macOS or Linux
```

From a checkout:

```sh
mise run install   # go build -o ~/.local/bin/ ./cmd/harness-ops ./cmd/termctl
```

The binaries have no runtime dependency. iTerm2's API must be enabled (Settings > General > Magic > Enable Python API; the name is iTerm's, and no Python is used).

## Licence

termctl is under the [MIT licence](LICENSE), with one exception.

`internal/iterm2api/api.proto` and the generated `api.pb.go` copy message names and field numbers from iTerm2's `proto/api.proto`. iTerm2 is distributed under GPL-2.0, so these two files are marked as derived from it and are not covered by the MIT licence. Both are compiled into the binaries. If GPL-2.0 applies to those files, it also applies to a distributed binary.
