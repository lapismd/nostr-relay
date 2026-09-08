# Buzz Desktop project/task/pull-request compatibility

Audit target: Buzz Desktop `v0.5.23`, commit
`3c7f288c60d67df78577b237e27c3dfc8831aaa1`.
This file is the refresh checklist when updating Buzz or the upstream relay.

## HTTP endpoint audit

Buzz does not expose or require project/task/PR-specific HTTP endpoints. Its
relay surface is generic:

| Endpoint | Project/task/PR relevance in this relay |
| --- | --- |
| `POST /events` | Publishes any signed project/task/PR event through the same validation and policy path as WebSocket `EVENT`. |
| `POST /query` | Queries ordinary event filters and supports Buzz's `before_id` pagination tie-breaker. |
| `POST /count` | Generic count only. Buzz Desktop does not depend on a project-scoped count extension. |
| `GET /info` | Generic capability/relay information; no project/task/PR payload. |

Current Desktop project/task/PR code primarily uses the shared WebSocket relay
client. Tauri HTTP commands in the project area concern Git hosting, repository
transport, and local checkout operations. Those are outside this relay's scope:
Lapis repositories use external `clone`/`web` URLs and Project Task Anchors have
neither. The relay must not advertise or synthesize Buzz's `/git/<owner>/<repo>`
transport.

Desktop-local/provider Tauri commands own Git snapshots, diffs, branches,
cloning, merges, and managed-owner signing. They are not relay endpoints. This
relay adds no PR REST route and does not advertise Git smart-HTTP.

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
- project creation eagerly publishes a `kind:30617` default repository with the
  same signer, `d`, and `buzz-channel` as the `kind:30621` project and includes
  its coordinate in the project;
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

The relay adds one deliberately narrow filter extension: exact
`#buzz-channel`. Arbitrary multi-character tag filtering remains unsupported so
upstream query behavior does not change accidentally.

## Refresh procedure

When Buzz changes:

1. Record the audited Buzz commit above.
2. Re-check `docs/nips/NIP-MP.md`, project and NIP-34 builders, project/work-item
   enumeration, issue and PR mutation/fetch/reducer code, reviews, deletion,
   and the shared relay client.
3. Search project, task, and pull-request code for HTTP paths and Tauri
   commands. Classify Git transport separately from Nostr event transport.
4. Update the event/query table and the contract fixtures before changing
   implementation.
5. Run `go test ./internal/projecttask`, the Zooid project/task/PR integration
   tests, `go test ./...`, and the local Docker image build.

No changes for this feature belong under `internal/khatru`. The stable local
seams are `internal/projecttask` for domain rules, Zooid for persistence and
NIP-29 scope enforcement, and `internal/buzzcompat` for generic HTTP transport.
