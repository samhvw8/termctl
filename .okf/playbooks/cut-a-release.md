---
type: Playbook
title: Cut a release
description: Tag main; GitHub Actions tests, builds four archives and publishes the release.
tags: [playbook, release]
generated: { by: claude-code/opus-5.5, at: '2026-11-09T11:27:18Z' }
---

# Steps

1. Merge the work to `main` and run `mise run check`.
2. Tag the commit: `git tag v1.2.3`.
3. Push the tag: `git push origin v1.2.3`.
4. Watch the run: `gh run watch`. The `release` workflow runs the tests on Linux, builds, and publishes.
5. Check the install in an empty data directory: `MISE_DATA_DIR=$(mktemp -d) mise exec github:samhvw8/termctl@1.2.3 -- termctl --version`.

To test the workflow without publishing, run `gh workflow run release.yml`. It runs the tests and the build, and skips the publish step.

# What is built

`scripts/dist.sh` builds `darwin` and `linux` on `arm64` and `amd64`. Each `termctl-<version>-<os>-<arch>.tar.gz` holds `termctl`, `harness-ops`, `LICENSE` and `README.md` at its root. mise needs that name and layout to find the right archive and put the binaries on `PATH` with no options.

The release also carries the [licence exception](/decisions/mit-with-gpl-proto.md).
