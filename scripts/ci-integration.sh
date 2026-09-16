#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

# Fixture images are built by the same script GitHub Actions runs, so a
# cold runner never tries to pull lapis-walgit or lapis-fake-github.
./scripts/ci-images.sh fixtures

# Ryuk is the testcontainers reaper. CI runners and local TestMain already
# terminate containers, so disabling it avoids a class of flakes.
export TESTCONTAINERS_RYUK_DISABLED="${TESTCONTAINERS_RYUK_DISABLED:-true}"

GOWORK=off go test -tags=integration ./zooid/ -count=1
GOWORK=off go test -tags=github_integration ./zooid/ \
	-run '^TestGitHubForgeDockerLane$' -count=1
GOWORK=off go test -tags=walgit_integration ./zooid/ \
	-run '^TestWalGitObjectStoreRecovery$' -count=1
