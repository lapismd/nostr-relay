# Upstream provenance

Lapis Relay is maintained from:

- Repository: `https://github.com/unicitynetwork/unicity-relay.git`
- Imported commit: `6c89860dbb1fd2d5cb628cf3978c78cb2dbb7999`
- Imported on: 2026-09-01
- License: MIT, retained in `LICENSE`

The full upstream Git history is retained. The upstream source still uses the
historical `zooid` Go module and package names; Lapis-specific naming is kept at
the repository, executable, image, configuration, and documentation boundary
so upstream updates remain reviewable.

Lapis's Buzz HTTP transport is isolated in `internal/buzzcompat` and connected
through `zooid/buzz_compat.go`. The upstream-derived `zooid/instance.go` should
retain only the adapter registration and calls to the shared authenticated
pubkey accessor. During an upstream update, preserve that narrow seam and avoid
moving Buzz route behavior into the core relay handlers.

Project/task/PR compatibility follows the same rule. No project/task/PR changes
live under `internal/khatru`. The local patch map is:

- `internal/projecttask`: wire validation, Project Task Anchor templates,
  deterministic task and pull-request reduction, and storage-independent
  graph/scope resolution.
- `zooid/project_tasks.go`: the only relay/store adapter for intersecting
  derived NIP-29 scope, durable scope sidecars, and NIP-09 address/event
  projection.
- `zooid/instance.go`: callback wiring only for write checks, query/broadcast
  visibility, anonymous visibility, and post-save scope persistence.
- `zooid/events.go` plus migrations `003`, `004`, and `005`: the exact
  `buzz-channel` index extension and durable many-channel derived-scope table.
- `internal/buzzcompat`: remains generic HTTP transport. It contains no project,
  task, or pull-request domain logic; optional forge handlers are registered as
  callbacks at the existing Zooid seam.
- `internal/gitforge`: repository classification, Buzz Git authentication, and
  the storage-independent WalGit client plus its capability adapter, repository
  importer, and exact-commit CI mirror source.
- `internal/forge`: provider-neutral types, small optional capability
  interfaces, normalized errors, registry, and backend composition.
- `internal/githubforge`: fixed-host GitHub App authentication, normalized Git
  reads, provider collaboration/mirror/event decoding, migration source, and
  exact-SHA Actions adapter. Provider response types do not leave this package.
- `zooid/git_forge.go`, `zooid/git_http.go`, `zooid/forge_*.go`,
  `zooid/github_*.go`, and `zooid/walgit_events.go`: durable desired state,
  integration resolution, outbox/webhook/mapping/migration/CI recovery,
  NIP-29 authorization, public Git/web routes, PR diff reduction, and
  authenticated provider/ref-event ingestion.
- migrations `006` and `007`: Git desired state, idempotent ref-event/current-ref
  projections, and full unsigned-WAL-sequence storage.
- migrations `008`-`012`: additive forge integrations/connections, stable
  object mappings, webhook delivery/outbox/reconciliation state, verified
  migration state, separate CI bindings/runs, and disposable mirror refs.
- `third_party/walgit`: exact upstream pin, reproducible archive verification,
  image recipe, deployment configuration, and the short bounded-compare patch.
- `cmd/forge-worker`, `Dockerfile`, and `docker-compose.yml`: private WalGit plus
  RustFS deployment and the optional worker-only forge process. Neither
  WalGit, RustFS, nor the worker publishes a host port; only the relay is a
  client-facing policy boundary.

The audited Buzz contract and refresh checklist live in
`docs/BUZZ_PROJECT_COMPATIBILITY.md`. Update its pinned Buzz commit and contract
fixtures before adapting behavior to a newer Desktop release.

The provider boundary and operator contract live in `FORGE_ABSTRACTION.md` and
`GITHUB_INTEGRATION.md`. During an upstream update, keep GitHub host/API
selection fixed in `internal/githubforge`, signed-event authority and NIP-29 in
Zooid, and callback registration in `internal/buzzcompat`. Never move provider
or forge domain behavior under `internal/khatru`.

WalGit is a separately updated upstream, pinned at the exact commit and archive
checksum recorded in `third_party/walgit/UPSTREAM_VERSION`. Follow
`WALGIT_INTEGRATION.md` when refreshing it: verify a clean archive, rebase the
small patch stack, run upstream's focused Rust tests, and rerun the real
object-store recovery lane. Do not copy WalGit sources into this fork or move
its Git/storage semantics into Go.

When updating, fetch the upstream remote with Jujutsu, review the complete
upstream range, rebase or merge it into a dedicated update change, and rerun
the unit, integration, and container acceptance lanes before committing.
