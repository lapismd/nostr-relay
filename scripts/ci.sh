#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

./scripts/ci-images.sh
./scripts/ci-in-image.sh ./scripts/ci-verify.sh
./scripts/ci-integration.sh
./scripts/ci-image.sh
