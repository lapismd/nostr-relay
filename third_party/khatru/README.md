# Khatru fork maintenance

`internal/khatru` is a narrow vendored copy of the Khatru relay framework from
the `fiatjaf.com/nostr` module. The copy contains the production core package,
its tests, and the Blossom subpackage used by this relay. Examples, generated
documentation, and unused policy helpers remain upstream.

The current source is commit `316ef6591fa2`, represented by Go module version
`v0.0.0-20260902034142-316ef6591fa2`.

## Imported-path manifest

- `LICENSE`
- `khatru/README.md`
- `khatru/*.go`
- `khatru/blossom/*.go`

These are copied to `internal/khatru`; upstream self-imports are then rewritten
to `zooid/internal/khatru`. No other upstream paths are part of the fork.

`UPSTREAM_VERSION` records the exact Go module revision. Local changes are
stored as ordered patches in `patches/` so refreshing the fork does not depend
on manually remembering edits.

Refresh the fork with:

```sh
./scripts/update-khatru.sh
```

Verify that the checked-in copy can be reproduced with:

```sh
./scripts/update-khatru.sh --check
```

When updating upstream, change `UPSTREAM_VERSION`, refresh the copy, rebase the
patches if necessary, and run the vendored package and relay test suites before
committing.
