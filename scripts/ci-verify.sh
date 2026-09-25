#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

# Scan repository sources only. A mounted Go module cache must not fail CI.
unformatted=$(gofmt -l -s cmd internal modules zooid testdata)
if [ -n "$unformatted" ]; then
	echo "The following files need gofmt -s:"
	echo "$unformatted"
	exit 1
fi

GOWORK=off go test ./internal/specdocs
GOWORK=off go test ./internal/architecture
(cd modules/buzzagents && GOWORK=off go test ./...)
GOWORK=off go test ./...
./scripts/update-khatru.sh --check
./scripts/update-buzz-agent-upstream.sh --check
./scripts/update-architecture-baseline.sh --check
mdbook build spec
