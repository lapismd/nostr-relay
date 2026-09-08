#!/bin/sh
set -eu

root=${1:?usage: verify.sh /path/to/walgit-source}
patch_dir=$(CDPATH= cd -- "$(dirname "$0")/patches" && pwd)

for patch_file in "$patch_dir"/*.patch; do
    patch --directory "$root" --dry-run --silent --batch --forward -p1 < "$patch_file"
done

echo "WalGit patch stack applies cleanly"
