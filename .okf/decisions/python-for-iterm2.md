---
type: Decision
title: Use Python
description: The tool is Python because the iTerm2 backend needs iTerm's Python API.
tags: [decision]
generated: { by: claude-code/opus-5.5, at: '2026-11-09T01:30:00Z' }
---

# Decision

Write termctl in Python, as a `uv` tool, with `iterm2==2.30` as its one dependency.

# Why

iTerm2's complete control surface is its Python API: session variables for labels, screen contents, profile customizations for spawn. The native `it2` binary covers part of it but not labels with `user.agent` semantics or spawn with a custom command and folder. The prior `agent-msg` was Python on this API, so its process and folder detection carry over.

# Install

`uv tool install -e` from the checkout. mise's `pipx:` backend takes PyPI names or Git URLs, not a local path, and the project has no remote yet. When it has one, switch to `mise use -g pipx:samhvw8/termctl`.
