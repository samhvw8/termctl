---
type: Decision
title: Go, with our own iTerm2 client
description: termctl is a single Go binary that speaks iTerm2's API directly, because Python added an operational risk and a pin for no benefit.
tags: [decision, go, iterm2]
generated: { by: claude-code/opus-5.5, at: '2026-11-09T05:30:00Z' }
---

# Decision

Write termctl in Go. Generate a small `internal/iterm2api/api.proto` holding only the messages we use, and speak the API ourselves.

# Why

iTerm2's API is protobuf over a WebSocket on a unix socket; Python is one client of it. A running `harness-ops` is what every agent's `agent-msg` depends on, so what it can break matters most.

| Cost | Python | Go |
|---|---|---|
| Runtime | `uv` tool tied to a mise Python; removing that Python breaks every agent's `agent-msg` | one binary |
| Pin | `iterm2==2.30`, bumped together with `it2` | none |
| Global state | library binds a global `App` to its first connection; a second connection hangs | our client has none |
| Protocol ownership | library | about 10 calls |

It was measured not to be a speed change: most of the 2 s `ls` is iTerm2 round trips, `ps`, `lsof` and one `it2` call per pane.

# Alternatives rejected

- Third-party Go clients (`taumatix/iterm2-go`, `phpgao/term2go`): young, and this tool types into live agent panes.
- Rust: an async runtime and no gain for a short-lived CLI.

# Protocol details

`api.proto` was generated from the descriptors of iTerm's Python library, so field numbers cannot be mistyped. Unknown fields are ignored on the wire. Auth is a cookie and key from AppleScript per connection, sent as headers with the `api.iterm2.com` subprotocol.

The iTerm2 project is GPLv2, and the field names and numbers in `api.proto` come from it.
