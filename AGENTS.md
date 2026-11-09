# Agent guide — termctl

termctl drives terminal panes through backends (iTerm2, tmux, Rex; Ghostty is a stub).
`harness-ops` sits on top and talks to coding-agent TUIs: draft check, slash commands,
idle waits, spawn, tagged messages. It replaced the logic of the `agent-msg` script;
that script is now a thin wrapper.

Project knowledge lives in `.okf/`. Update it in the same change as the code it
describes, and run the validator.

## Layout

| Path | What |
|---|---|
| `src/termctl/backends/` | Layer 1: one file per backend, `base.py` is the interface |
| `src/termctl/cli.py` | `termctl` CLI (no harness knowledge) |
| `src/termctl/harness/profiles.py` | Layer 2: how each harness looks on screen |
| `src/termctl/harness/ops.py` | Layer 2: draft check, slash, wait-idle, msg, spawn |
| `src/termctl/harness/tasks.py` | Layer 2: tasks, pending notifications, close safety |
| `src/termctl/harness/registry.py` | Layer 2: the locked state file |
| `src/termctl/harness/cli.py` | `harness-ops` CLI |
| `tests/` | Unit (fake backend), tmux integration, acceptance, opt-in iTerm2 |

## Commands

```sh
mise run test          # unit + tmux integration on a private socket
mise run test:iterm    # opt-in: opens one scratch iTerm2 tab, closes it
mise run check         # tests + OKF validator
mise run install       # put termctl and harness-ops on PATH from this checkout
```

## Rules

- Exit codes are the contract: 0 ok, 1 backend error, 2 refused target/usage, 4 draft on input line, 5 wait timeout, 6 refused by a safety limit.
- Never send to a pane resolved by focus. A target that is not unique, or is your own pane, is refused.
- Layer 1 knows nothing about harnesses. Harness logic goes in `harness/`.
- Tests never touch the real registry: `tests/conftest.py` sets `TERMCTL_STATE_DIR`.
- Tests that open panes must create them, and close them. tmux tests use the private socket `termctl-test`.
- Parsing a harness screen against a real capture: add the capture to `tests/test_profiles.py`.
