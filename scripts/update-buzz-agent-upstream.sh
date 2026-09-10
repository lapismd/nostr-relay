#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
module_dir="$repo_root/modules/buzzagents"
version_file="$module_dir/UPSTREAM_VERSION"
manifest="$module_dir/UPSTREAM_FILES.sha256"

case "${1:---check}" in
  --check)
    commit=$(tr -d '[:space:]' < "$version_file")
    update=false
    ;;
  --update)
    commit=${2:-}
    update=true
    ;;
  *)
    echo "usage: $0 [--check | --update FULL_COMMIT_SHA]" >&2
    exit 2
    ;;
esac

if [ "${#commit}" -ne 40 ]; then
  echo "Buzz pin must be a full 40-character lowercase commit SHA" >&2
  exit 2
fi
case "$commit" in
  *[!0-9a-f]*)
    echo "Buzz pin must be a full 40-character lowercase commit SHA" >&2
    exit 2
    ;;
esac

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT INT TERM
curl -fsSL "https://github.com/block/buzz/archive/$commit.tar.gz" |
  tar -xz --strip-components=1 -C "$scratch"

if [ "$update" = true ]; then
  next_manifest="$scratch/UPSTREAM_FILES.sha256"
  while read -r _ path; do
    if [ ! -f "$scratch/$path" ]; then
      echo "Pinned Buzz contract file is missing at $commit: $path" >&2
      exit 1
    fi
    (cd "$scratch" && shasum -a 256 "$path") >> "$next_manifest"
  done < "$manifest"
  printf '%s\n' "$commit" > "$version_file"
  cp "$next_manifest" "$manifest"
  echo "Updated Buzz agent audit pin to $commit; review the pinned source diff before implementation."
else
  (cd "$scratch" && shasum -a 256 -c "$manifest")
fi

(cd "$module_dir" && GOWORK=off go test ./...)
