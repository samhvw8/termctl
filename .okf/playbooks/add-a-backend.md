---
type: Playbook
title: Add a backend
description: Steps to add a terminal backend to termctl.
tags: [playbook, backends]
generated: { by: claude-code/opus-5.5, at: '2026-11-09T05:30:00Z' }
---

# Steps

1. Create `internal/term/<name>.go` with a type that implements `Backend`.
2. Add the name to `BackendNames` and a case to `Make` in `internal/term/term.go`. Add its environment variable to `Detect`, and decide its nesting priority.
3. Keep `SendText` literal and call `CheckText`. Text with line breaks must never reach the terminal as a submission.
4. Map keys through `NormalizeKey`. Unknown keys are a usage error.
5. Set `Pane.IsSelf` from the terminal's own notion of the current pane. An environment variable on the wrong server is a different pane: see how tmux compares `$TMUX` with the socket path.
6. Add an integration test that creates its own pane or server, and closes it. Run the fake-harness acceptance test against the new backend.
7. Record the backend in [Backends](/architecture/backends.md) and the README, with its tested state.
