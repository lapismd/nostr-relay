#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version_file="$repo_root/third_party/khatru/UPSTREAM_VERSION"
patch_dir="$repo_root/third_party/khatru/patches"
target_dir="$repo_root/internal/khatru"
version=$(tr -d '[:space:]' < "$version_file")

case "$target_dir" in
  "$repo_root/internal/khatru") ;;
  *)
    echo "Refusing to replace unexpected target: $target_dir" >&2
    exit 1
    ;;
esac

if [ -z "$version" ]; then
  echo "Khatru upstream version is empty" >&2
  exit 1
fi

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT INT TERM

go mod download "fiatjaf.com/nostr@$version"
module_dir=$(go env GOPATH)/pkg/mod/fiatjaf.com/nostr@${version}
source_dir="$module_dir/khatru"
generated_dir="$scratch/internal/khatru"

if [ ! -d "$source_dir" ]; then
  echo "Downloaded Khatru source was not found at $source_dir" >&2
  exit 1
fi

mkdir -p "$generated_dir/blossom"
cp "$module_dir/LICENSE" "$generated_dir/LICENSE"
cp "$source_dir/README.md" "$generated_dir/README.md"
cp "$source_dir"/*.go "$generated_dir/"
cp "$source_dir/blossom"/*.go "$generated_dir/blossom/"
chmod -R u+w "$generated_dir"

find "$generated_dir" -type f -name '*.go' -exec perl -pi -e \
  's#fiatjaf\.com/nostr/khatru/blossom#zooid/internal/khatru/blossom#g; s#fiatjaf\.com/nostr/khatru#zooid/internal/khatru#g' {} +

for patch in "$patch_dir"/*.patch; do
  [ -e "$patch" ] || continue
  patch -s -d "$scratch" -p1 < "$patch"
done
# BSD patch writes .orig backups; they are not part of the vendored tree.
find "$generated_dir" \( -name '*.orig' -o -name '*.rej' \) -delete

if [ "${1:-}" = "--check" ]; then
  diff -ruN "$generated_dir" "$target_dir"
  exit 0
fi

if [ "$#" -ne 0 ]; then
  echo "usage: $0 [--check]" >&2
  exit 2
fi

rm -rf "$target_dir"
mkdir -p "$(dirname -- "$target_dir")"
cp -R "$generated_dir" "$target_dir"
