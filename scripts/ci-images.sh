#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

target="${1:-all}"
ci_image="${LAPIS_RELAY_CI_IMAGE:-lapis-relay-ci:local}"
walgit_image="${LAPIS_WALGIT_IMAGE:-lapis-walgit:local}"
fake_github_image="${LAPIS_FAKE_GITHUB_IMAGE:-lapis-fake-github:forge-integration}"

build_image() {
	tag=$1
	shift
	echo "Building $tag"
	docker build --tag "$tag" "$@"
}

smoke_toolchain() {
	docker run --rm "$ci_image" \
		sh -ec 'go version && mdbook --version && docker --version && command -v git && command -v patch'
}

build_toolchain() {
	build_image "$ci_image" --file docker/Dockerfile.ci docker
	smoke_toolchain
}

build_fixtures() {
	build_image "$walgit_image" --file third_party/walgit/Containerfile .
	build_image "$fake_github_image" --file testdata/fakegithub/Dockerfile testdata/fakegithub
}

case "$target" in
all)
	build_toolchain
	build_fixtures
	;;
toolchain)
	build_toolchain
	;;
fixtures)
	build_fixtures
	;;
*)
	echo "usage: $0 [all|toolchain|fixtures]" >&2
	exit 2
	;;
esac
