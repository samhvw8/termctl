# Agent guide — termctl

termctl drives terminal panes through backends (iTerm2, tmux, Herdr `hc`, Rex; Ghostty is a stub).
`harness-ops` sits on top and talks to coding-agent TUIs: draft check, slash commands,
idle waits, spawn, tagged messages. It replaced the logic of the old `agent-msg` script;
the `agent-ops` skill now calls `harness-ops` directly.

Project knowledge lives in `.okf/`. Update it in the same change as the code it
describes, and run the validator.

## Layout

| Path | What |
|---|---|
| `cmd/` | `termctl` and `harness-ops` entry points |
| `internal/term/` | Layer 1: backends (`tmux.go`, `iterm2.go`, `hc.go`, `rex.go`, `ghostty.go`), target resolution, process helpers |
| `internal/iterm2api/` | iTerm2 client: `api.proto` holds only the messages we use, `client.go` connects and calls |
| `internal/harness/` | Layer 2: `profiles.go` (screens), `ops.go` (draft, slash, wait-idle, msg, spawn), `tasks.go`, `registry.go` |
| `internal/cli/` | Both command lines |
| `internal/termtest/` | Fake backend and the fake harness REPL for tests |
| `e2e/` | Real binaries against tmux on a private socket, and the opt-in iTerm2 and hc tests |
| `scripts/dist.sh`, `.github/workflows/release.yml` | Release archives, built and published on a version tag |

## Commands

```sh
mise run test          # unit + tmux integration on a private socket
mise run test:iterm    # opt-in: opens one scratch iTerm2 tab, closes it
mise run test:hc       # opt-in: a private herdr-core (own HC_SESSION and XDG paths), stopped at the end
mise run check         # tests + OKF validator
mise run install       # build termctl and harness-ops into ~/.local/bin
mise run proto         # regenerate the iTerm2 messages after editing api.proto
```

Releases: push a `v*` tag. See `.okf/playbooks/cut-a-release.md`.

## Rules

- Exit codes are the contract: 0 ok, 1 backend error, 2 refused target/usage, 4 draft on input line, 5 wait timeout, 6 refused by a safety limit.
- Never send to a pane resolved by focus. A target that is not unique, or is your own pane, is refused.
- Layer 1 knows nothing about harnesses. Harness logic goes in `internal/harness/`.
- **`~/.local/state/termctl` is live data for other agents' running tasks. Never delete it or anything in it, by hand or by script.** Run every experiment, test and manual script with `TERMCTL_STATE_DIR` set to a temp directory. Clean up with `harness-ops state clean`, which refuses the live directory.
- Tests never touch the real registry: a test binary that resolves the default state directory panics.
- Tests that open panes must create them, and close them. tmux tests use private sockets named `termctl-test-*`; hc tests use a private core: `HC_SESSION=termctl-test-*` with their own `XDG_RUNTIME_DIR`, `XDG_STATE_HOME` and `XDG_CONFIG_HOME`. Never run `hc` without those set: every hc command auto-starts a core, and the user's core is live data.
- Parsing a harness screen against a real capture: add the capture to `internal/harness/profiles_test.go`.
- A session that ends within seconds makes iTerm2 raise a modal that stalls its whole API. Never spawn something that can exit at once without the wrapper in `iterm2Command`.
