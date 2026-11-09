#!/bin/sh
# Build release archives into dist/. Usage: scripts/dist.sh <version>
# Archive names and layout are what `mise use github:samhvw8/termctl` expects:
# termctl-<version>-<os>-<arch>.tar.gz with the binaries at the root.
set -eu

version=${1:?usage: scripts/dist.sh <version>}
cd "$(dirname "$0")/.."
rm -rf dist
mkdir -p dist

for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
	os=${target%/*}
	arch=${target#*/}
	name="termctl-$version-$os-$arch"
	stage="dist/$name"
	mkdir -p "$stage"
	GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -trimpath \
		-ldflags "-s -w -X github.com/samhvw8/termctl/internal/cli.Version=$version" \
		-o "$stage/" ./cmd/termctl ./cmd/harness-ops
	cp LICENSE README.md "$stage/"
	tar -C "$stage" -czf "dist/$name.tar.gz" termctl harness-ops LICENSE README.md
	rm -rf "$stage"
done

ls -l dist
