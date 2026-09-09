# Specification governance

`spec/src` is the canonical functional documentation for Lapis Relay. The
linked chapter order lives in `spec/src/SUMMARY.md`; mdBook configuration lives
in `spec/book.toml`.

This uses the same mdBook generator and source layout as the other Lapis
repositories. Those JavaScript repositories commonly invoke mdBook through
`@lapismd/spec-validator`; this Go-only repository keeps the generator direct
and supplies an equivalent local-link check as a Go test, avoiding a Node
toolchain solely for documentation.

The generated `spec/book` directory is disposable and ignored. Do not commit
generated HTML. Build it locally with:

```sh
just spec-build
```

Validate the chapter graph, links, and generated book with:

```sh
just spec-check
```

Use `just spec-serve` for a local live documentation server.

Changes to relay behavior, policy, routes, event validation, persistence,
provider capabilities, deployment configuration, bootstrap, or operational
limits must update the owning chapter in the same Jujutsu change. Requirements
that are intentionally not implemented belong in [Deferred forge
work](roadmap/deferred-forge-work.md), not in current behavior prose.

Two root files remain intentional exceptions:

- `README.md` is the concise repository entry point and links into this book;
- `UPSTREAM.md` is the fork provenance and patch map required at repository
  root, and is linked from [Upstream maintenance](maintenance/upstream.md).

Historical material may remain under `spec/src/archive` only when it is clearly
labelled as non-current. Historical audits stay under `spec/src/audits` so the
reasons for compatibility boundaries remain reviewable without presenting
them as current operational guidance.
