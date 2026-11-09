---
type: Architecture
title: Tasks and pane safety
description: How harness-ops tracks a job given to another harness session, and what stops it closing or spawning the wrong thing.
tags: [architecture, harness, tasks, safety]
generated: { by: claude-code/opus-5.5, at: '2026-11-09T06:00:00Z' }
---

# Overview

A task is one harness session doing one job. The registry is `~/.local/state/termctl/registry.json` (`TERMCTL_STATE_DIR` moves it). Code: `internal/harness/tasks.go` and `registry.go`.

Writes hold an exclusive `flock` on `registry.lock` and replace the file atomically, so readers need no lock. A corrupt file is an error, never a reset, because a reset would forget running tasks.

# State directory is live data

The default directory holds other agents' running tasks. It was deleted by hand during development, and two live tasks lost their records. So a test binary that resolves it panics, the e2e suite sets `TERMCTL_STATE_DIR` for every child, and `harness-ops state clean` refuses the live directory, anything containing it, and any directory holding something else.

# Lifecycle

The lead owns "done". The worker's session has the files loaded, so it is the right place for a non-blind verdict.

| Status | Set by | Meaning |
|---|---|---|
| running | `task new`, `task send` | The worker has the job. A slot is reserved under the lock before the spawn, so parallel calls cannot exceed the cap. |
| ready | worker's `task done --status ok` | Waiting for the lead. The session stays open. |
| failed, blocked | worker's `task done` | Waiting for the lead. The session stays open. |
| done | owner's `task accept` | Only this makes a task done. Only a ready task can be accepted, so a failure is never recorded as done. |
| exited | exit hook, or reconcile | The pane is alive and the harness is gone. The owner is told. |
| lost | reconcile | The pane is gone, or the task never got a pane within 10 minutes. |

`task send` on a ready, failed or blocked task returns it to `running` and adds one to `round`. The worker's next `task done` returns it to `ready`.

A backend error during reconcile leaves the status alone: unknown is not gone. Registry version 1 files are migrated on read.

# Surviving tabs

`Ops.Spawn` wraps the command as `<cmd>; s=$?; <hook>; <message>; exec "$SHELL" -l`, so a shell remains and a crash stays readable. The hook is `harness-ops task exited <id> --code <s>`. Without it an owner waiting for the `RE: task` line would never hear, because nothing polls. Reconcile is the fallback: a launched task whose pane now runs a bare shell has exited. `launched` keeps a login shell at startup from counting.

# Blind review

`task review --blind` starts a new task with `review_of` set. Its brief is the lead's requirements, or `--brief-file`, never the worker's report, summary or conversation. The reviewed task is hidden from the reviewer in `task ls` and `task show`. Blind is a convention plus these checks, not a sandbox: a reviewer that reads the registry file directly can see more.

The read-only flags per harness are in [harness profiles](/architecture/harness-profiles.md).

# Notifications

`task done` stores a pending entry, then tries for 10 seconds. A draft on the owner's input line means retry, never join. Any later harness-ops call retries, for one hour. Claims under the lock stop two processes from delivering the same result; a claim older than 30 seconds belongs to a process that died.

# Safety rules

| Rule | Exit |
|---|---|
| Your own pane is never closed, even with `--force`. | 2 |
| `close` needs a pane recorded by `spawn` or `task new`, no unaccepted task on it, and an idle harness. `--force` overrides these. | 6 |
| `task close` refuses a task the lead has not accepted, without `--force`. | 6 |
| A pane reused with `--reuse` is never closed by its task. | none |
| 6 running tasks per owner, depth 2. Depth comes from the registry: the owner pane's own task depth plus 1. | 6 |
| A record from before a terminal server restart does not count: tmux pane IDs are reused, so `ServerRef` holds the server pid. | 6 or lost |

# Verified on real harnesses

On 2026-11-09 a real `claude --model haiku` in a scratch iTerm2 tab read a one-line brief, ran it, and reported through `task done` by itself. The owner received the tagged result.

Esc stopped a running `sleep 45` in under a second on claude and on omp. Other harnesses are unverified, and `interrupt` warns.
