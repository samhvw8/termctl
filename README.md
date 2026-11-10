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
termctl name <label>
termctl whoami

harness-ops ls [--json] [--all] [--mine]     # --mine: only panes you started
harness-ops msg [--force] [--clear-first] [--timeout S] <target> <text...>
harness-ops slash [--force] [--wait] <target> </command>
harness-ops clear [--force] <target>         # /clear alone, then wait for idle
harness-ops wait-idle [--timeout S] <target>
harness-ops spawn <harness> [--where auto|tab|split|window] --cwd D [--prompt T | --prompt-file F] [--label L] [--command CMD]
harness-ops close [--force] <target>         # only panes made by spawn/task new; never your own
harness-ops interrupt [--key K] <target>     # stop the current turn (Esc on claude and omp)
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

`--where auto` is the default. A new agent joins a tab that already works on its project (same git repository, worktrees included): the caller's tab, or a tab of an agent the caller started. It splits the newest pane there, up to 3 panes per tab. Otherwise it opens a new tab right after the caller's tab. Herdr ignores `--where`.

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
- A calibrated harness (claude, omp) whose input line cannot be read also refuses. Others warn and proceed.
- `slash --force` wipes the draft with ctrl-u: a slash command must be the whole line.
- `msg --clear-first` sends `/clear` alone, waits for idle, re-checks the input line, then sends.
- A message over 800 characters or with line breaks goes to a file in `~/.cache/agent-ops/`.
- Put options before the target; everything after the target is message text. Use `--` to send text starting with a flag.

Exit codes: 0 ok, 1 backend error, 2 refused target or usage, 4 draft on input line, 5 wait timed out, 6 refused by a safety limit.

## Backends

| Backend | State |
|---|---|
| iterm2 | Tested. Python API, native `it2` status helper. |
| tmux | Tested on a private socket. |
| hc | Tested on a real private herdr-core with the fake harness (`mise run test:hc`). `termctl send-text` is refused: `hc send` always submits; use `harness-ops msg` or `slash`. `--where` is ignored: a spawned agent opens as a tab in the Herdr app. A blocked program cannot be messaged, and there is no `--force` for it. |
| rex | **Untested against real Rex** (not installed here). argv mapping is unit-tested against a stub. JSON field names of `rex block ls` are not documented, so parsing is tolerant. |
| ghostty | Stub: raises "not supported: Ghostty has no control API yet". |

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
