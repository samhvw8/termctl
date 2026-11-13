# Update Log

## 2026-11-14
* **Release**: v0.2.2 — `--where auto` keeps a job's agents in the caller's tab (up to 12 panes, evenly sized), and `msg` reaches named Claude Code sessions. Runs: nothing new.
* **Fix**: `msg` to a named Claude Code session refused with exit 4 ("cannot see claude's input line"). The session name sits in the top rule of the input box (`── voxtract ─`), and the rule pattern wanted only `─`. Found when an agy worker could not reply to its lead. The real screen is now a test in `profiles_test.go`. See [harness profiles](architecture/harness-profiles.md). Runs: nothing new.

## 2026-11-13
* **Update**: `MaxSplit` is 12, and every split now evens out the tab: iTerm2 through `SetTabLayoutRequest` (added to the iTerm2 messages, with `SplitTreeNode.vertical`), tmux through `select-layout tiled`. See [backends](architecture/backends.md#placement). Checked on real iTerm2 3.7.6. Runs: one more terminal call (two on iTerm2) per split.
* **Update**: `--where auto` no longer groups by git project. A new agent splits the caller's tab whatever its folder, up to `MaxSplit` 8 panes (was 3), then an owned tab, then a new tab. The split cuts the largest pane along its long side, from new pane sizes (`grid_size` added to the iTerm2 messages, `pane_width`/`pane_height` in tmux). Cause: a lead's agy workers in `~` opened their own tab. See [backends](architecture/backends.md#placement). Runs: nothing new; the per-folder `git rev-parse` calls are gone.

## 2026-11-11
* **Release**: v0.2.1 with the review fixes below. Runs: nothing new.
* **Fix**: findings from a review of v0.2.0. `--where auto` now also joins a tab of a worker the caller started in the same project, splits the newest pane instead of halving the caller, and falls back to a tab with a warning when listing panes fails. `task new --reuse` labels the pane with the task ID, so teammates can address it. `ls --mine` lists the panes the caller started. See [backends](architecture/backends.md#placement). Runs: one registry read per `auto` spawn.

## 2026-11-10
* **Release**: v0.2.0 — the `hc` backend for Herdr, `--where auto` placement, and the `[agent-ops from ...]` message tag. Runs: nothing new.
* **Update**: `--where auto` is the new default for `spawn` and `task new`: the caller's project splits the caller's tab (up to 3 panes), anything else opens as a new tab right after the caller's, on iTerm2 and tmux — see [backends](architecture/backends.md#placement). Before, every agent opened a tab at the end of the window. `mise run test` covers it on tmux (`TestSpawnAutoPlacement`); iTerm2 was checked by hand. Runs: one `git rev-parse` per folder for each `auto` spawn.
* **Update**: the `agent-msg` skill is now `agent-ops`: messages start with `[agent-ops from ...]`, and long messages go to `~/.cache/agent-ops/`. Runs: nothing new.
* **Update**: replaced the "cause unknown" note on omp and `homebox` in [backends](architecture/backends.md): omp refused the line because it had no API key for its model. A live `task new` on Herdr passed two rounds, and `spawn` with no core refused with exit 1. Runs: nothing new.

## 2026-11-09
* **Fix**: `agent-msg` failed inside a Herdr terminal whose shell exports `XDG_RUNTIME_DIR`; found by a real `RE:` reply test. `hcEnv` now passes the terminal's socket on as `HC_SOCKET`. Runs: nothing new.
* **Update**: `spawn` and `task new` on hc now open as a tab in the Herdr app, through a `herdr.space` label — see [backends](architecture/backends.md). Runs: nothing new.
* **Update**: messages to Herdr agents now show hc's `<hc from=...>` tag; targets in iTerm2 and tmux keep the plain `[agent-msg` line. See [backends](architecture/backends.md). Runs: nothing new.
* **Update**: omp's icon-led shortcut hint on an empty input line no longer counts as a draft (captured on omp 18.16.0). `hc` `Spawn` now refuses when no core runs, so a core never starts with an agent's environment. Real Claude Code and omp messages through `agent-msg` passed on the local core. Runs: nothing new.
* **Addition**: the `hc` backend for Herdr terminals, with the `Submitter` interface and `blocked`/`done`/`error` status handling — see [backends](architecture/backends.md). Tested on a private core with the fake harness (`mise run test:hc`); Claude Code was started with no prompt to find the variable behind "Transcript saving is off". `ServerRef` carries a `core_id`, and tasks compare it when present, because a Herdr core keeps its terminals through a restart. A bare iTerm2 UUID target now routes to iTerm2, so an `RE:` reply from another terminal reaches it. Runs: nothing new.
* **Addition**: tagged releases through GitHub Actions, so `mise use -g github:samhvw8/termctl` works — see [the playbook](playbooks/cut-a-release.md). Runs: one workflow, on version tags and manual runs only.
* **Addition**: MIT licence, with the iTerm2 message files marked GPL-2.0-derived — see [the decision](decisions/mit-with-gpl-proto.md).
* **Update**: guarded the live state directory after a manual cleanup deleted it: tests cannot resolve it, and `harness-ops state clean` refuses it — see [tasks](architecture/tasks.md).
* **Update**: the lead now owns "done": `ready`, `task accept`, follow-up rounds, blind review in a new session, and tabs that survive the harness (`exited`) — see [tasks](architecture/tasks.md). `Submit` retries a dropped Enter. Registry version 2, migrated on read.
* **Update**: rewrote termctl and harness-ops in Go with our own iTerm2 client — see [the decision](decisions/go-own-iterm2-client.md). The Python decision is deprecated. Verified on real iTerm2: a haiku worker reported through `task done`, Esc stopped real claude and omp turns. Found that a session ending within seconds raises a modal that stalls iTerm2's API, so failed commands now stay open briefly.
* **Addition**: tasks (`task new/ls/show/send/wait/done/close`), `close` with ownership and busy rules, and `interrupt` — see [tasks and pane safety](architecture/tasks.md). Real claude haiku reported through `task done` on iTerm2; Esc verified on claude and omp. Fixed a hang from a second iTerm2 backend in one process, and the Claude busy pattern, which matched nothing on real screens.
* **Changed**: real-harness tests on claude and omp: iTerm2 captures turn NUL cells into spaces (a TUI leaves unwritten cells as NUL, which broke grep and draft text); `slash` refuses harness-ops options placed after the command instead of typing them into the session. Known limits added to the README. Runs: nothing new.
* **Creation**: Bundle written with the first version of termctl and harness-ops: two layers, four backends (Rex untested, Ghostty stub), harness profiles calibrated on real claude, omp and opencode panes.
