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

Buzz agent protocol rules are independently pinned to `block/buzz` commit
`cec5c8fd9280d30f56effac701e1e19d5cfe6fea` in
`modules/buzzagents/UPSTREAM_VERSION`. The separate `zooid/buzzagents` Go module
contains only transport-neutral authentication, event-envelope, visibility,
and presence rules. `zooid/agent_policy.go` is its relay adapter, and
`scripts/update-buzz-agent-upstream.sh` verifies or refreshes the exact upstream
contract-file manifest. A Buzz refresh must update that pin, module tests, and
`spec/src/compatibility/buzz-agents.md` together. Do not copy ACP runtime,
model, context, or tool-execution code into the relay.

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
  importer, exact-object pinning, one-way Git replication source, and
  exact-commit CI mirror source.
- `internal/forge`: provider-neutral types, small optional capability
  interfaces, normalized errors, registry, and backend composition.
- `internal/githubforge`: fixed-host GitHub App authentication, normalized Git
  reads, provider collaboration/mirror/event decoding, migration source, and
  exact-SHA Actions adapter. Provider response types do not leave this package.
- `internal/auth`: provider-neutral accounts and sessions, GitHub and Google
  login adapters, Nostr login, encrypted managed keys, the signer boundary,
  passkeys, NIP-46 connections, and custody transfer. Zooid and forge code use
  this package only through narrow service and credential-resolver seams.
- `zooid/git_forge.go`, `zooid/git_http.go`, `zooid/forge_*.go`,
  `zooid/git_replicas.go`, `zooid/git_replication.go`, `zooid/github_*.go`, and
  `zooid/walgit_events.go`: durable desired state,
  integration resolution, outbox/webhook/mapping/migration/CI recovery,
  replica/ref/object synchronization, outbound Git projection, NIP-29
  authorization, public Git/web routes, PR diff reduction, and authenticated
  provider/ref-event ingestion.
- migrations `006` and `007`: Git desired state, idempotent ref-event/current-ref
  projections, and full unsigned-WAL-sequence storage.
- migrations `008`-`012`: additive forge integrations/connections, stable
  object mappings, webhook delivery/outbox/reconciliation state, verified
  migration state, separate CI bindings/runs, and disposable mirror refs.
- migration `013`: independent authority/content/replication-target selection,
  private replica state/ref/jobs/object pins, replica-origin event marking, and
  outbound Git replication state/jobs.
- migrations `016`-`019`: accounts and signer bindings, login/session/provider
  state, durable GitHub credential attribution, encrypted passkeys, and custody
  transfer state.
- `third_party/walgit`: exact upstream pin, reproducible archive verification,
  image recipe, deployment configuration, and the short bounded-compare patch.
- `cmd/forge-worker`, `Dockerfile`, `docker-compose.yml`, and the storage
  overlays: private WalGit, local RustFS, production Cloudflare R2, and the
  optional worker-only forge process. Neither WalGit, local object storage, nor
  the worker publishes a host port; only the relay is a client-facing policy
  boundary.

The project/Git Buzz contract and refresh checklist live in
`spec/src/compatibility/buzz-desktop.md`; its existing audit pin remains scoped
to that contract. The agent-protocol audit has its own pin and checklist in
`spec/src/compatibility/buzz-agents.md`.

The provider boundary and operator contract live in
`spec/src/forge/capabilities.md`, `spec/src/forge/github.md`, and
`spec/src/forge/replicas.md`. During an upstream update, keep GitHub host/API
selection fixed in `internal/githubforge`, signed-event authority and NIP-29 in
Zooid, and callback registration in `internal/buzzcompat`. Never move provider
or forge domain behavior under `internal/khatru`.

WalGit is a separately updated upstream, pinned at the exact commit and archive
checksum recorded in `third_party/walgit/UPSTREAM_VERSION`. Follow
`spec/src/forge/walgit.md` when refreshing it: verify a clean archive, rebase the
small patch stack, run upstream's focused Rust tests, and rerun the real
object-store recovery lane. Do not copy WalGit sources into this fork or move
its Git/storage semantics into Go.

When updating, fetch the upstream remote with Jujutsu, review the complete
upstream range, rebase or merge it into a dedicated update change, and rerun
the unit, integration, and container acceptance lanes before committing.
