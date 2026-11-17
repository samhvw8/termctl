# BRIEF: terminal alerts to the lead, on every backend

Written 2026-11-17, before any edit for this round. Approval: auto mode on (counts as approval per the ambition skill).

## Goal question (Altitude)
I read the goal as: **a worker never waits on a dialog without its lead knowing, and the lead answers it safely, on every backend Sam uses.** So I build the alert source and prove it on real harnesses.
If the goal is instead **"Sam and the lead stop being the bottleneck for routine approvals"**, the next step is a policy file the watcher applies by itself (no lead turn per dialog). That is Next rung, not built here.

## Baseline (measured, read-only)
- `it2 session get-status`: 0.44 s per call (0.21 s real, 21 MB RSS), one python process per call. The watcher calls it serially for every worker without a dialog, every tick.
- Tick ≈ 3 s sleep + N × ~0.5 s. With 6 workers: notice latency ≈ 6 s (estimate). Builder saw "about 5 s" with 1 worker.
- Real-harness proof so far: Claude Code 2.1.326 on iTerm2 only. agy 1.5.10: screen capture only, never answered live. tmux/Rex/hc: fake harness only.
- Installed: agy 1.5.10, claude 2.1.326, tmux 3.8, hc, Rex 0.1.0-preview.19 (app bundle). Ghostty: not installed, and it has no screen-read API.

## Plan
- **Raw material:** real harness screens and their OSC 7501 status records (state, kind=permission, msg), not fake screens.
- **Instruments:** iTerm2 `iterm2.get_session_status` over the open websocket (InvokeFunctionRequest); Rex `program_status` in one batched `rex do` Lua call; `hc get`; tmux screen only. `ps` sampling for process count; timestamps for latency; the file system as the judge of what a harness ran.
- **Fields:** SRE (alert on a symptom with a budget; measure p95, not one run), test engineering (contract tests against the real dependency, drift detection), aviation checklists (read back before act: already in answer).
- **Axes:**
  - Proof: rung 1 → 3 — real claude and agy dialogs answered on real iTerm2, tmux, Rex and hc, judged by files on disk.
  - Altitude: rung 1 → 2 — the notice carries the harness's own reason (OSC 7501 msg) and the exact answer command, so the lead decides in one turn.
  - Horizon: rung 0 → 2 — the real-harness matrix is a re-runnable `mise run test:dialogs` that fails when a new claude/agy version changes its dialog.

## Contract (locked)

| Line | Content |
|---|---|
| Judge | 1. `mise run check` exits 0; no existing test edited to pass. 2. `mise run test:dialogs` (self-built driver; outside judge = the file system): per cell {iterm2, tmux, rex-private, hc-private} × {claude, agy}, the lead receives the notice, `task answer 1` makes `touch <scratch>/<cell>-yes` run (file exists), `task deny` on a second dialog leaves `<cell>-no` absent. Pass: 8/8 cells. 3. During a watcher run over iTerm2 workers, `ps` sampled every 0.5 s sees 0 `it2` processes started by the watcher. |
| Target | Notice latency p95 ≤ 3.5 s with 6 workers on iTerm2 and tmux (guess, no measured baseline at 6 workers; estimate ≈ 6 s). 8/8 matrix cells. 0 it2 processes. |
| Stop line | 3 real attempts per failing cell, then report the miss with output, gap and blocker. |
| Scope | (1) Status source (option B): iTerm2 over websocket, Rex batched program_status, hc get; tmux and others screen only. A `blocked`/`waiting` status triggers an immediate screen read; its msg goes into the notice. (2) Watcher: no it2 subprocess, workers read in parallel per tick, re-exec itself when its binary changes. (3) Backends: iTerm2, tmux, Rex, hc verified on real harnesses; Ghostty documented as unsupported with its blocker. (4) agent-ops skill (dotfiles): dialog notices, task answer/deny, --persist. (5) `mise run test:dialogs` opt-in task (the judge). (6) OKF and AGENTS.md updates. |
| Runs | One `harness-ops watch` per lead while it has open tasks (as now) · owner: the lead · kill when: no open tasks (automatic). Per tick: one screen read and one status read per worker, no subprocess on iTerm2. `test:dialogs` runs only when called; it costs model turns (claude haiku, agy). |

Not committed or pushed: Sam has not asked.
