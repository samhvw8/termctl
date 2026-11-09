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
harness-ops which|read|name|whoami           # as agent-msg; send = msg
```

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

Exit codes: 0 ok, 1 backend error, 2 refused target or usage, 4 draft on input line, 5 wait timed out.

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
