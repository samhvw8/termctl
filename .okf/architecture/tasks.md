---
type: Architecture
title: Tasks and pane safety
description: How harness-ops tracks a job given to another harness session, and what stops it closing or spawning the wrong thing.
tags: [architecture, harness, tasks, safety]
generated: { by: claude-code/opus-5.5, at: '2026-11-09T03:40:00Z' }
---

# Overview

A task is one harness session doing one job. The registry is `~/.local/state/termctl/registry.json` (`TERMCTL_STATE_DIR` moves it). Code: `src/termctl/harness/tasks.py` and `registry.py`.

Writes hold an exclusive `flock` on `registry.lock` and replace the file atomically, so readers need no lock. A corrupt file is an error, never a reset, because a reset would forget running tasks.

# Lifecycle

| Status | Meaning |
|---|---|
| running | Worker has the job. A slot is reserved under the lock before the spawn, so parallel `task new` calls cannot exceed the cap. |
| done, failed, blocked | Reported through `task done`. `blocked` may report again. `task send` reopens any of the three. |
| lost | The pane is gone, or the task never got a pane within 10 minutes. |

A backend error during reconcile leaves the status alone: unknown is not gone.

# Notifications

`task done` stores a pending entry, then tries for 10 seconds. A draft on the owner's input line means retry, never join. Any later harness-ops call retries, for one hour. Claims under the lock stop two processes from delivering the same result; a claim older than 30 seconds belongs to a process that died.

# Safety rules

| Rule | Exit |
|---|---|
| Your own pane is never closed, even with `--force`. | 2 |
| `close` needs a pane recorded by `spawn` or `task new`, no running task on it, and an idle harness. `--force` overrides these. | 6 |
| `task close` refuses a running task without `--force`. | 6 |
| A pane reused with `--reuse` is never closed by its task. | none |
| 6 running tasks per owner, depth 2. Depth comes from the registry: the owner pane's own task depth plus 1. | 6 |
| A record from before a terminal server restart does not count: tmux pane IDs are reused, so `server_ref` holds the server pid. | 6 or lost |

# Verified on real harnesses

On 2026-11-09 a real `claude --model haiku` in a scratch iTerm2 tab read a one-line brief, ran it, and reported through `task done` by itself. The owner received the tagged result.

Esc stopped a running `sleep 45` in under a second on claude and on omp. Other harnesses are unverified, and `interrupt` warns.
