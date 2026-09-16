#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

CANDIDATE_IMAGE="${CANDIDATE_IMAGE:-lapis-relay:candidate}"

docker build --file Dockerfile --target relay --tag "$CANDIDATE_IMAGE" .

docker run --rm \
	--read-only \
	--tmpfs /tmp:rw,noexec,nosuid,nodev,size=2g,mode=1777 \
	--cap-drop ALL \
	--security-opt no-new-privileges \
	--volume /var/run/docker.sock:/var/run/docker.sock:ro \
	aquasec/trivy:0.74.0@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969 \
	--cache-dir /tmp/trivy image \
	--scanners vuln --ignore-unfixed --severity HIGH,CRITICAL --exit-code 1 --no-progress \
	"$CANDIDATE_IMAGE"
