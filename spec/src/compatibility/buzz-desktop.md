# Buzz Desktop compatibility

Audit target: Buzz Desktop `v0.5.23`, commit
`3c7f288c60d67df78577b237e27c3dfc8831aaa1`.
This file is the refresh checklist when updating Buzz or the upstream relay.

## HTTP and Git endpoint audit

Buzz's Nostr project/task/PR relay surface is generic:

| Endpoint | Project/task/PR relevance in this relay |
| --- | --- |
| `POST /events` | Publishes any signed project/task/PR event through the same validation and policy path as WebSocket `EVENT`. |
| `POST /query` | Queries ordinary event filters and supports Buzz's `before_id` pagination tie-breaker. |
| `POST /count` | Generic count only. Buzz Desktop does not depend on a project-scoped count extension. |
| `GET /info` | Generic capability/relay information; no project/task/PR payload. |

Current Desktop project/task/PR collaboration code primarily uses the shared
WebSocket relay client. The pinned Git credential helper reacts to the Git
server's `WWW-Authenticate: Nostr method=...` challenge, signs kind `27235` for
the repository-root URL, and supplies `Authorization: Nostr <base64-event>`.
Git reuses that credential across discovery and pack requests. The relay's Git
authentication intentionally follows this profile rather than requiring a new
one-shot NIP-98 event for each subrequest.

The pinned Desktop uses Tauri/local Git commands for snapshots, file content,
diffs, branches, clone and merge. Those are not relay APIs and remain usable by
the desktop application. They cannot serve a browser build, which has no local
checkout or Git binary. Lapis therefore adds a hosted-Git data plane and a thin
authenticated web read surface without changing Buzz's Nostr wire contract:

| Endpoint | Compatibility role |
| --- | --- |
| `/git/<owner>/<repo>[.git]/...` | Buzz credential-helper-compatible smart HTTP, LFS and bundles. |
| `/api/git/<owner>/<repo>/...` | Lapis web source browsing through provider-neutral refs/resolve/tree/blob/commit results serialized compatibly for WalGit and GitHub. |
| `/api/pull-requests/<event-id>/diff` | Lapis web PR diff after reducing the same Buzz-compatible event graph. |

The last two routes require strict method-and-URL NIP-98 credentials. They are
Lapis web capabilities, not endpoints required or emitted by the audited Buzz
Desktop. WalGit remains private, GitHub clone/push remain provider-native, and
provider objects do not leak into repository-content results. Additive
truncation, continuation, incomplete-result, oversized-object, and capability
limit fields make provider constraints explicit.

Lapis also adds NIP-98 `/api/forge` capability, health, attachment, durable
provider-command, migration, operation, and CI routes. These are application
control APIs, not a second way to query or publish the NIP-34 graph. Native
Buzz clients continue to use WebSocket `REQ`/`EVENT` or generic `/query` and
`/events`; signed relay-origin state is never translated into provider CRUD.

## Desktop event/query contract

The compatibility tests pin these observed behaviors:

- project enumeration fetches `kind:30621` and `kind:30617` in pages of 500,
  draining timestamp boundaries;
- deletion discovery fetches `kind:5` in `#a` chunks of 100;
- channel project discovery queries exact `#buzz-channel` values;
- assignments are `kind:1` root notes fetched by issue IDs in `#e`, initially
  in pages of 500 and widening to 1000; `t` labels are filtered client-side;
- repository/project ordering is descending `created_at`, ascending event ID;
- state reduction orders assignment operations ascending by `created_at` then
  ascending event ID;
- current Desktop comments are `kind:1` root notes, not NIP-22 `kind:1111`;
- project creation eagerly publishes a clone-less `kind:30617` default
  repository with the same signer, `d`, and `buzz-channel` as the `kind:30621`
  project and includes its coordinate in the project; with forge support this
  is a lazily provisioned hosted repository;
- project validation permits at most 64 raw `a` tags, requires two- or
  three-field `a` tags, rejects duplicate coordinates regardless of relay hint,
  and bounds singleton metadata (`d` 1024, `name` 256, `description` 2048,
  `buzz-channel` 256, `buzz-visibility` 256 UTF-8 bytes).

Pull-request compatibility is pinned separately because Desktop writes and
reads do not use one symmetrical encoding:

- the SDK emits NIP-34 roots (`1618`) and updates (`1619`), with 64 KiB content,
  a required subject of at most 256 UTF-8 bytes, a full 40/64-character hex
  commit, and at least one clone locator;
- Desktop fetches roots (`1621`, `1618`), updates (`1619`), kind-1 comments,
  and statuses (`1630`-`1633`) with four repository `#a` WebSocket filters,
  each limited to 2,000 events;
- Desktop publishes direct-user roots, updates, comments, reviews, and status
  events through authenticated WebSocket `EVENT`;
- review requests, approvals, changes requested, and inline review comments
  are labelled kind-1 notes; standards-based NIP-22 `1111` comments with
  `K=1618` are independently accepted and never converted;
- trusted update/status ties use the smallest ID, while equal-timestamp
  decisions by one reviewer use the largest ID;
- raw authorized-member events remain queryable even when the reducer marks
  their update/status effect untrusted or their review metadata historical,
  outdated, ambiguous, or invalid.

The tagged Docker fixture reproduces the four Desktop query filters over both
WebSocket and `POST /query`, publishes over WebSocket and `POST /events`, checks
repository-plus-origin scope intersection, deletes descendants through both
transports, and repeats reads after restart.

The separate `walgit_integration` Docker lane pushes and clones through real
WalGit, covers the browser API and arbitrary comparison, and proves that a
fresh sidecar with an empty cache reconstructs the repository from RustFS.

The separate `github_integration` Docker lane uses a deterministic fake GitHub
App/API/Git service—never live credentials—together with real WalGit and RustFS.
It exercises admin attachment, GitHub-authoritative initial replication,
authenticated browsing from WalGit, durable collaboration, webhook echo
suppression, exact-SHA CI, and restart resolution.

The relay adds one deliberately narrow filter extension: exact
`#buzz-channel`. Arbitrary multi-character tag filtering remains unsupported so
upstream query behavior does not change accidentally.

## Refresh procedure

When Buzz changes:

1. Record the audited Buzz commit above.
2. Re-check `docs/nips/NIP-MP.md`, project and NIP-34 builders, project/work-item
   enumeration, issue and PR mutation/fetch/reducer code, reviews, deletion,
   and the shared relay client.
3. Re-check `git-credential-nostr`, project Git TypeScript, and Tauri Git
   commands. Classify the credential challenge/root-URL behavior, desktop-local
   Git, browser data needs, and Nostr event transport separately.
4. Update the event/query table and the contract fixtures before changing
   implementation.
5. Run `go test ./internal/projecttask ./internal/forge ./internal/gitforge
   ./internal/githubforge`, the Zooid project/task/PR integration tests,
   `go test ./...`, the tagged WalGit and fake-GitHub Docker lanes, and the
   local WalGit, relay, and forge-worker image builds.

No changes for this feature belong under `internal/khatru`. The stable local
seams are `internal/projecttask` for domain rules, `internal/gitforge` for the
storage-independent sidecar contract, `internal/forge` for capabilities,
`internal/githubforge` for provider translation, Zooid for persistence and
NIP-29 scope enforcement, and `internal/buzzcompat` for HTTP route
registration.
