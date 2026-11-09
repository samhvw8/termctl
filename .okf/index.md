---
okf_version: '0.2'
---

# termctl

Terminal driver with backends, plus harness-ops for coding-agent TUIs.

* [Getting started](getting-started.md) - reading order.
* [Architecture: two layers](architecture/layers.md) - Layer 1 terminals, Layer 2 harnesses, the message flow.
* [Architecture: backends](architecture/backends.md) - the interface and state of each backend.
* [Architecture: harness profiles](architecture/harness-profiles.md) - input parsing, calibration, the idle rule.
* [Architecture: tasks and pane safety](architecture/tasks.md) - the registry, notifications, guards, close rules.
* [Decision: Go](decisions/go-own-iterm2-client.md) - a single binary with our own iTerm2 client.
* [Decision: Python](decisions/python-for-iterm2.md) - superseded.
* [Playbook: add a backend](playbooks/add-a-backend.md) - the steps.
