---
type: Decision
title: MIT, with the iTerm2 messages marked GPL-2.0-derived
description: The repo is MIT, except for the two iTerm2 message files, which stay marked as derived from GPL-2.0 iTerm2.
tags: [decision, licence, iterm2]
generated: { by: claude-code/opus-5.5, at: '2026-11-09T11:25:35Z' }
sources:
  - id: iterm2-licence
    resource: https://github.com/gnachman/iTerm2/blob/master/LICENSE
    title: iTerm2 licence (GitHub reports GPL-2.0)
---

# Decision

`LICENSE` is plain MIT, so GitHub detects it. `internal/iterm2api/api.proto` and the generated `api.pb.go` carry a header that marks them as derived from iTerm2 and outside the MIT grant. The README states the same exception.

# Why

Sam chose MIT for our code over GPL-2.0 for the whole repo. iTerm2 is GPL-2.0.[^iterm2-licence] Our proto file copies a subset of its message names and field numbers, needed to speak its API.

# Open question

Whether names and field numbers are copyrightable was not settled, and no lawyer reviewed this. Both files compile into every binary, so a distributed binary may count as a GPL-2.0 combined work. Replacing the proto with a clean description of the wire format would remove the question. See [the Go decision](/decisions/go-own-iterm2-client.md) for why the client exists.

[^iterm2-licence]: GitHub's licence detection for gnachman/iTerm2 returned GPL-2.0 on 2026-11-09.
