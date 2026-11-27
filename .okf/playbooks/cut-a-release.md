---
type: Playbook
title: Cut a release
description: Tag main; GitHub Actions tests, builds four archives and publishes the release.
tags: [playbook, release]
generated: { by: claude-code/opus-5.5, at: '2026-11-27T12:25:00Z' }
---

# Steps

1. Merge the work to `main` and run `mise run check`.
2. Tag the commit: `git tag v1.2.3`.
3. Push the tag: `git push origin v1.2.3`.
4. Watch the run: `gh run watch`. The `release` workflow runs the tests on Linux, builds, and publishes.
5. Check the install in an empty data directory: `MISE_DATA_DIR=$(mktemp -d) mise exec github:samhvw8/termctl@1.2.3 -- termctl --version`.
6. Update the local install: `MISE_USE_VERSIONS_HOST=0 mise up github:samhvw8/termctl`. Without the variable, mise reads its version list from a shared versions server that lags new tags, and reports the old version as up to date (seen for v0.13.0 on 2026-11-27; `mise cache clear` does not help).

To test the workflow without publishing, run `gh workflow run release.yml`. It runs the tests and the build, and skips the publish step.

# v3 registry cutover (v0.10.x to v3)

The first write by a v3 binary backs up `registry.json` to `registry.v2-backup-<unix>.json` and removes the v2 `tasks`, `panes`, and `pending` keys. Any v0.10.x process still running after that first v3 write fails on the missing v2 fields:

- In-flight `v0.10.x task wait` panics on `d.Tasks[id]`; `task wait --any` exits 2; `--wait-ack` times out with exit 5.
- In-flight `v0.10.x task done` in its 10 s `notifyRetry` loop panics on `d.Tasks[tid]`.
- A running `v0.10.x watch` reads empty v2 maps and stops as idle if a v3 write runs before two watcher rounds (~6 s) pass after binary replacement for `reexec`.
- Worker panes started under pre-v3 builds embed the old binary path in their shell exit hook (`os.Executable()`), so an exit after cutover runs the old binary (exit 2); v3 `Reconcile()` still marks the task `exited` without an exit code.

Follow this sequence when cutting over on a host with live sessions:

1. Install the v3 binary onto `PATH` first.
2. Run `pgrep -fl 'harness-ops|termctl'` and wait until zero `task new`, `task done`, `task wait`, or `--wait-ack` processes remain.
3. Wait at least 2 watcher rounds (~7 s) after `PATH` points to v3 (or confirm every running `harness-ops watch` has `reexec`d into the v3 binary) before running any v3 command that writes `registry.json`.

# What is built

`scripts/dist.sh` builds `darwin` and `linux` on `arm64` and `amd64`. Each `termctl-<version>-<os>-<arch>.tar.gz` holds `termctl`, `harness-ops`, `LICENSE` and `README.md` at its root. mise needs that name and layout to find the right archive and put the binaries on `PATH` with no options.

The release also carries the [licence exception](/decisions/mit-with-gpl-proto.md).
