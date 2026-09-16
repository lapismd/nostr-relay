#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

image="${LAPIS_RELAY_CI_IMAGE:-lapis-relay-ci:local}"
cache_root="${LAPIS_RELAY_CI_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/lapis-relay-ci}"
mkdir -p "$cache_root/go-cache" "$cache_root/go"

if [ "$#" -eq 0 ]; then
	set -- ./scripts/ci-verify.sh
fi

if [ -n "${DOCKER_SOCK:-}" ]; then
	docker_sock="$DOCKER_SOCK"
elif [ -S /var/run/docker.sock ]; then
	docker_sock=/var/run/docker.sock
else
	docker_host=$(docker context inspect --format '{{.Endpoints.docker.Host}}' 2>/dev/null || true)
	case "$docker_host" in
	unix://*)
		docker_sock=${docker_host#unix://}
		;;
	*)
		docker_sock=""
		;;
	esac
fi
if [ -z "$docker_sock" ] || [ ! -S "$docker_sock" ]; then
	echo "Docker socket not found (set DOCKER_SOCK or use a local Docker context)" >&2
	exit 1
fi

# zooid TestMain starts PostgreSQL through the host Docker daemon. The socket
# mount is a local/CI test facility, same as nostr-auth's test-runner.
# Published container ports are reached on the host, not inside this image.
host_override="${TESTCONTAINERS_HOST_OVERRIDE:-host.docker.internal}"

# Same invocation GitHub Actions uses: mount the checkout into the pinned
# toolchain image. Go caches stay on the host so local reruns stay warm.
docker run --rm \
	--volume "$repo_root:/workspace" \
	--volume "$cache_root/go-cache:/tmp/go-cache" \
	--volume "$cache_root/go:/tmp/go" \
	--volume "$docker_sock:/var/run/docker.sock" \
	--add-host=host.docker.internal:host-gateway \
	--workdir /workspace \
	--env CI=true \
	--env GOCACHE=/tmp/go-cache \
	--env GOMODCACHE=/tmp/go/pkg/mod \
	--env GOPATH=/tmp/go \
	--env GOTOOLCHAIN=local \
	--env GOWORK=off \
	--env GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}" \
	--env DOCKER_HOST=unix:///var/run/docker.sock \
	--env TESTCONTAINERS_HOST_OVERRIDE="$host_override" \
	--env TESTCONTAINERS_RYUK_DISABLED="${TESTCONTAINERS_RYUK_DISABLED:-true}" \
	"$image" \
	"$@"
