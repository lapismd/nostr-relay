# Architecture budgets

Lapis-authored Go sources in this fork are governed by two machine-checked
budgets: a counted-line file size budget and a package boundary matrix. Both
live in [`internal/architecture`](../../../internal/architecture) and run as
ordinary Go tests, so `go test ./...` enforces them in every lane that already
runs that command.

This chapter is the authority for what those budgets are. The checker is the
enforcement; it does not define policy on its own.

## Scope

The budgets cover only sources this fork owns. Three groups are excluded, and
each exclusion is itself verified rather than trusted.

| Group | Paths | Why it is out of scope |
| --- | --- | --- |
| Unicity-inherited | The 33 `.go` paths present in the pin recorded in [`UPSTREAM.md`](../../../UPSTREAM.md) | Inherited code. Splitting it widens the merge seam every upstream refresh has to cross. |
| Vendored Khatru | `internal/khatru/**` | Regenerated wholesale by `scripts/update-khatru.sh` from a pinned upstream archive. Any local edit is discarded at the next refresh. |
| Generated and fixture output | `spec/book`, `third_party`, `vendor`, `testdata`, `node_modules` | Not maintained sources. |

The exclusion lists are recorded in
`internal/architecture/upstream-paths.txt` and the vendored prefix in
`internal/architecture/budget.go`. `scripts/update-architecture-baseline.sh
--check` regenerates the upstream list from the pinned commit and fails when the
manifest disagrees, so a change cannot quietly exempt a Lapis-authored file by
adding it to that manifest. Both `.github/workflows/ci.yml` and
`.github/workflows/publish-image.yml` therefore check out full history: a
shallow clone cannot read the pin.

Two Unicity-inherited files grew past budget inside this fork and stay excluded
deliberately: `zooid/instance.go` and `zooid/config.go`. Keeping them whole
preserves the narrow adapter seam that `UPSTREAM.md` asks an upstream update to
leave intact.

## Counted-line metric

A counted line is a physical line that is neither blank nor comment-only. A line
carrying code plus a trailing comment still counts, and a trailing newline does
not add a line. This is the same definition the other LapisMD repositories use.

Comments are located with `go/parser` rather than a line-prefix scan. That
matters in two places a naive port gets wrong:

- Comment markers inside a raw string literal are not comments.
- A line beginning with `*` is counted when it is a pointer dereference or a
  multiplication rather than a documentation continuation.

The definition matches the workspace metric; the implementation is Go-specific
because the JavaScript heuristic it replaces is unsound for Go.

## Size budgets

| File kind | Budget for a file that is not baselined |
| --- | --- |
| Production | 300 counted lines |
| Test | 500 counted lines |

No in-scope file may exceed the 500-line hard ceiling without a recorded budget.
A file that was already over budget when this gate landed carries an entry in
`internal/architecture/baseline.json` recording its counted size.

Baselines may only shrink. A baselined file fails when it grows past its
recorded budget, and it fails when it has shrunk far enough that the recorded
budget is stale by more than twenty lines. `scripts/update-architecture-baseline.sh
--update` regenerates the file and never raises a budget, so recording an
allowance cannot launder growth. Adding a budget for a file that has none fails
unless the regeneration is run with `ARCH_BASELINE_ALLOW_NEW=1`, which is
reserved for bootstrapping the gate and for a deliberately reviewed decision to
record new debt.

The checker does not exempt itself. `internal/architecture` is in scope, and a
test fails if one of its own files is excluded or given a baseline allowance.

## Package boundary matrix

Every package directory has a layer rank in
`internal/architecture/boundaries.go`, derived from the ownership boundaries in
[Architecture](../architecture.md). A lower rank sits closer to the process
entry point; a higher rank is more foundational. An import must point strictly
downward.

| Rank | Layer | Packages |
| --- | --- | --- |
| -1 | Executables | `cmd/relay`, `cmd/forge-worker`, `cmd/local-admin`, `cmd/migrate` |
| 0 | Composition root | `zooid` |
| 1 | Relay adapters | `internal/agentbunker`, `internal/blossom`, `internal/buzzcompat` |
| 2 | Provider adapters | `internal/auth`, `internal/gitforge`, `internal/githubforge`, `modules/buzzagents/auth`, `modules/buzzagents/policy` |
| 3 | Domain leaves | `internal/forge`, `internal/keywrap`, `internal/presence`, `internal/projecttask`, `modules/buzzagents/presence` |
| 4 | Wire format | `modules/buzzagents/protocol` |
| 5 | Vendored transport | `internal/khatru`, `internal/khatru/blossom` |
| 6 | Repository checks | `internal/architecture`, `internal/specdocs` |

A package directory with no rank fails, so a new package must be placed
deliberately rather than escaping the matrix. Rank 6 is isolated in both
directions: a repository check must not import product code, and product code
must not import a repository check.

Imports inside the vendored Khatru tree are exempt from the rank ordering,
because that tree is regenerated from upstream. Only its isolation from Lapis
product code is asserted.

### Allowlist

A horizontal edge between packages of equal rank inverts the documented
ownership and must be recorded in `internal/architecture/allowlist.json` with a
reason naming the call site. Two edges are recorded today:

- `internal/auth` to `internal/gitforge`, for the `VerifyRequestAuth` call in
  `internal/auth/service_session.go`.
- `internal/forge` to `internal/projecttask`, for the `RepositoryCoordinate`
  identity used by `internal/forge/types.go` and `internal/forge/types_ci.go`.

The allowlist may only shrink. An entry that no longer matches a real edge, or
that has become a legitimate downward edge, fails. Adding an entry requires a
reviewed justification in the same change as the import.

### Import cycles

No import cycle is permitted among `zooid/` packages, across both modules. A
cycle would defeat the rank ordering and make the layering unenforceable.

### Provider SDK containment

`golang.org/x/oauth2`, `github.com/coreos/go-oidc`, and `github.com/go-webauthn`
may only be imported by `internal/auth`. This makes the promise in
[Architecture](../architecture.md) and [`UPSTREAM.md`](../../../UPSTREAM.md)
that provider types do not leave their adapter a checked property rather than a
review convention.

Containment of `internal/githubforge` response types is not checked here. It is
a type-level promise, and the `zooid` composition root legitimately wires that
backend, so an import graph cannot express it.

## Commands

```sh
just arch              # run the size and boundary gates
just arch-baseline     # refresh upstream-paths.txt and baseline.json
just spec-check        # specification, architecture, and generated book
```

`scripts/ci-verify.sh` runs all three gates plus the manifest check, so
`just ci` and GitHub Actions enforce them without a separate job.

## Update checklist

When changing a package, adding one, or refreshing upstream:

1. Keep new production files at or under 300 counted lines. Split rather than
   record a baseline allowance.
2. Place a new package in `packageRanks` in the same change that creates it.
3. Add an import that points horizontally or upward only with a reviewed
   allowlist entry naming the call site, and retire it when the shared code
   moves to a leaf.
4. Lower or remove the `baseline.json` entry for any file that shrinks, in the
   same change.
5. After an upstream refresh, run `scripts/update-architecture-baseline.sh
   --update` and review the diff. Budgets may only be lowered or removed.
6. Run the gates in [Verification](../verification.md) before committing.
