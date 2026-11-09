---
type: Playbook
title: Add a backend
description: Steps to add a terminal backend to termctl.
tags: [playbook, backends]
generated: { by: claude-code/opus-5.5, at: '2026-11-09T01:30:00Z' }
---

# Steps

1. Create `src/termctl/backends/<name>.py` with a `Backend` subclass and every abstract method.
2. Add the name to `NAMES` and a branch to `make()` in `backends/__init__.py`. Add its environment variable to `detect()`, and decide its nesting priority.
3. Keep `send_text` literal and call `check_text`. Text with line breaks must never reach the terminal as a submission.
4. Map keys through `normalize_key`. Unknown keys are a usage error.
5. Set `Pane.is_self` from the terminal's own notion of the current pane. An environment variable on the wrong server is a different pane: see how tmux compares `$TMUX` with the socket path.
6. Add an integration test that creates its own pane or server, and closes it. Run the fake-harness acceptance test against the new backend.
7. Record the backend in [Backends](/architecture/backends.md) and the README, with its tested state.
