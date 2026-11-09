---
type: Reference
title: Getting started — termctl
description: Reading order for the termctl knowledge bundle.
tags: [getting-started]
status: stable
generated: { by: claude-code/opus-5.5, at: '2026-11-09T01:30:00Z' }
---

# Overview

termctl is a terminal driver with backends, plus `harness-ops`, a layer that talks to coding-agent TUIs. It exists because `agent-msg` typed a message into a pane that held `/clear` as a draft, and the harness received one joined prompt.

Read in this order:

1. [Architecture](/architecture/layers.md) - the two layers and what each may know.
2. [Backends](/architecture/backends.md) - iTerm2, tmux, Rex, Ghostty.
3. [Harness profiles](/architecture/harness-profiles.md) - recognising a harness, reading its input line, idle detection.
4. [Why Python](/decisions/python-for-iterm2.md) - the language decision.
5. [Add a backend](/playbooks/add-a-backend.md) - the steps.
