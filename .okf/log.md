# Update Log

## 2026-11-09
* **Changed**: real-harness tests on claude and omp: iTerm2 captures turn NUL cells into spaces (a TUI leaves unwritten cells as NUL, which broke grep and draft text); `slash` refuses harness-ops options placed after the command instead of typing them into the session. Known limits added to the README. Runs: nothing new.
* **Creation**: Bundle written with the first version of termctl and harness-ops: two layers, four backends (Rex untested, Ghostty stub), harness profiles calibrated on real claude, omp and opencode panes.
