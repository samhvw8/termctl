# termctl

A terminal driver with backends, and `harness-ops`, a layer on top for talking to coding-agent TUIs.

```sh
termctl ls [--json] [--all]                  # panes
termctl which <target>                       # resolve a target; never sends
termctl spawn --where tab|split|window [--cwd D] [--title T] [CMD...]
termctl send-text <target> <text...>         # literal, no Enter
termctl send-key <target> <key...>           # enter, escape, ctrl-c, ctrl-u, up, ...
termctl capture <target> [-n 40]
termctl close <target>
termctl name <label>
termctl whoami

harness-ops ls [--json] [--all]
harness-ops msg [--force] [--clear-first] [--timeout S] <target> <text...>
harness-ops slash [--force] [--wait] <target> </command>
harness-ops clear [--force] <target>         # /clear alone, then wait for idle
harness-ops wait-idle [--timeout S] <target>
harness-ops spawn <harness> --where tab|split|window --cwd D [--prompt T | --prompt-file F] [--label L] [--command CMD]
harness-ops close [--force] <target>         # only panes made by spawn/task new; never your own
harness-ops interrupt [--key K] <target>     # stop the current turn (Esc on claude and omp)
harness-ops which|read|name|whoami           # as agent-msg; send = msg

harness-ops task new <harness> --title T (--brief TEXT | --brief-file F) --cwd D [--where tab|split|window] [--reuse <target>] [--max N]
harness-ops task ls [--all] [--json]
harness-ops task show <id> [--json]
harness-ops task send [--force] <id> <text...>
harness-ops task wait <id> | --any [--timeout S]
harness-ops task done <id> --status ok|failed|blocked (--summary TEXT | --summary-file F)   # called by the worker
harness-ops task close [--force] <id>
```

## Tasks

A task is one harness session doing one job, tracked in `~/.local/state/termctl/registry.json`.
`task new` starts the harness and submits the brief, followed by a footer that tells the worker how to report. It prints the task id, which is also the pane's label.

- The owner is told with a tagged `RE: task ...` message when the worker reports. If the owner's input line holds a draft, the result stays pending. Any later harness-ops call delivers it, for one hour. `task ls` always shows the result.
- `task wait --any` returns each finished task once, so repeated calls walk through the results.
- `task send` on a finished or blocked task reopens it: the worker reports again.
- Guards (exit 6): 6 running tasks per owner (`--max`, `TERMCTL_MAX_TASKS`), spawn depth 2 (`TERMCTL_MAX_DEPTH`).
- A task whose pane is gone reads as `lost`. If the terminal cannot be reached, its state stays as it was.
- `--reuse` needs an idle pane running that harness. Closing such a task leaves the pane open, because it was not created for the task.

## Closing panes

`close` acts only on panes recorded by `spawn` or `task new`, and refuses a busy harness or a pane that holds a running task. `--force` overrides those. Nothing closes your own pane. `termctl spawn` is not recorded: that is Layer 1.

Global options come before the subcommand: `--backend iterm2|tmux|rex|ghostty`, `-L <tmux-socket>`.
The backend is detected from `TMUX`, `REX_SESSION`, `ITERM_SESSION_ID`. A target such as `tmux:%3` selects its backend.

## Target resolution

First rule with matches wins: exact pane ID, label, exact title, folder basename, title substring, ID prefix (4+ chars).
No match, several matches, or your own pane exits 2 and lists candidates. It never falls back to the focused pane.

## Rules of harness-ops

- `msg` and `slash` refuse (exit 4) when the target's input line holds a draft, unless `--force`.
- A calibrated harness (claude, omp) whose input line cannot be read also refuses. Others warn and proceed.
- `slash --force` wipes the draft with ctrl-u: a slash command must be the whole line.
- `msg --clear-first` sends `/clear` alone, waits for idle, re-checks the input line, then sends.
- A message over 800 characters or with line breaks goes to a file in `~/.cache/agent-msg/`.
- Put options before the target; everything after the target is message text. Use `--` to send text starting with a flag.

Exit codes: 0 ok, 1 backend error, 2 refused target or usage, 4 draft on input line, 5 wait timed out, 6 refused by a safety limit.

## Backends

| Backend | State |
|---|---|
| iterm2 | Tested. Python API, native `it2` status helper. |
| tmux | Tested on a private socket. |
| rex | **Untested against real Rex** (not installed here). argv mapping is unit-tested against a stub. JSON field names of `rex block ls` are not documented, so parsing is tolerant. |
| ghostty | Stub: raises "not supported: Ghostty has no control API yet". |

## Install

```sh
mise run install   # uv tool install --force --python 3.14 -e .
```

mise's `pipx:` backend installs from PyPI or a Git URL, not a local checkout. This repo has no remote yet, so it uses `uv tool install -e`.

## Known limits (found with real harnesses, 2026-11-09)

- `slash --wait` times out (exit 5) on commands that open a panel, such as Claude's `/help` or `/status`: the harness shows the panel until someone presses Esc.
- A slash command the harness does not know goes to the model as a normal prompt (omp has no `/help`).
- omp's input box can show autocomplete ghost text, which draft detection reads as part of the draft.
- Claude keeps many blank rows between the conversation and its input box. Use `read -n 200` to see a reply.
