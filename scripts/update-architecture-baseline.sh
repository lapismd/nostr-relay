#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

architecture_dir="$repo_root/internal/architecture"
manifest="$architecture_dir/upstream-paths.txt"
pin=$(sed -n 's/.*UpstreamPin = "\([0-9a-f]\{40\}\)".*/\1/p' "$architecture_dir/budget.go")

# The mounted checkout may be owned by a different user than the container user.
git_cmd() {
	git -c safe.directory='*' "$@"
}

case "${1:---check}" in
  --check)
    update=false
    ;;
  --update)
    update=true
    ;;
  *)
    echo "usage: $0 [--check | --update]" >&2
    exit 2
    ;;
esac

if [ "${#pin}" -ne 40 ]; then
  echo "Could not read the 40-character upstream pin from budget.go" >&2
  exit 1
fi

if ! git_cmd cat-file -e "${pin}^{commit}" 2>/dev/null; then
  echo "Upstream pin $pin is not present in this checkout." >&2
  echo "Fetch full history first (actions/checkout needs fetch-depth: 0)." >&2
  exit 1
fi

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT INT TERM

# Every .go path in the pinned upstream commit is inherited rather than
# Lapis-authored, so it is excluded from the counted-line budgets.
git_cmd ls-tree -r --name-only "$pin" |
	LC_ALL=C grep '\.go$' |
	LC_ALL=C sort >"$scratch/generated"

if [ "$update" = true ]; then
	{
		sed -n '/^#/p' "$manifest"
		cat "$scratch/generated"
	} >"$manifest.tmp"
	mv "$manifest.tmp" "$manifest"

	ARCH_BASELINE_UPDATE=1 GOWORK=off go test ./internal/architecture -run '^TestWriteBaseline$'
	echo "Refreshed $manifest and $architecture_dir/baseline.json."
	echo "Review the diff: budgets may only be lowered or removed."
	exit 0
fi

LC_ALL=C grep -v '^#' "$manifest" |
	LC_ALL=C grep -v '^[[:space:]]*$' |
	LC_ALL=C sort >"$scratch/declared"

if ! diff -u "$scratch/declared" "$scratch/generated" >"$scratch/diff"; then
	echo "internal/architecture/upstream-paths.txt does not match the pin $pin:" >&2
	cat "$scratch/diff" >&2
	echo >&2
	echo "Paths present in the pin are inherited and must stay excluded. Paths" >&2
	echo "absent from the pin are Lapis-authored and must not be excluded." >&2
	echo "Refresh with ./scripts/update-architecture-baseline.sh --update." >&2
	exit 1
fi
