# Update Log

## 2026-11-09
* **Update**: rewrote termctl and harness-ops in Go with our own iTerm2 client — see [the decision](decisions/go-own-iterm2-client.md). The Python decision is deprecated. Verified on real iTerm2: a haiku worker reported through `task done`, Esc stopped real claude and omp turns. Found that a session ending within seconds raises a modal that stalls iTerm2's API, so failed commands now stay open briefly.
* **Addition**: tasks (`task new/ls/show/send/wait/done/close`), `close` with ownership and busy rules, and `interrupt` — see [tasks and pane safety](architecture/tasks.md). Real claude haiku reported through `task done` on iTerm2; Esc verified on claude and omp. Fixed a hang from a second iTerm2 backend in one process, and the Claude busy pattern, which matched nothing on real screens.
* **Changed**: real-harness tests on claude and omp: iTerm2 captures turn NUL cells into spaces (a TUI leaves unwritten cells as NUL, which broke grep and draft text); `slash` refuses harness-ops options placed after the command instead of typing them into the session. Known limits added to the README. Runs: nothing new.
* **Creation**: Bundle written with the first version of termctl and harness-ops: two layers, four backends (Rex untested, Ghostty stub), harness profiles calibrated on real claude, omp and opencode panes.
