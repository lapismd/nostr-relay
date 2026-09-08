# Buzz Desktop project/task compatibility

Audit target: Buzz commit `3c7f288c60d67df78577b237e27c3dfc8831aaa1`.
This file is the refresh checklist when updating Buzz or the upstream relay.

## HTTP endpoint audit

Buzz does not expose or require project/task-specific HTTP endpoints. Its relay
surface is generic:

| Endpoint | Project/task relevance in this relay |
| --- | --- |
| `POST /events` | Publishes any signed project/task event through the same validation and policy path as WebSocket `EVENT`. |
| `POST /query` | Queries ordinary event filters and supports Buzz's `before_id` pagination tie-breaker. |
| `POST /count` | Generic count only. Buzz Desktop does not depend on a project-scoped count extension. |
| `GET /info` | Generic capability/relay information; no project/task payload. |

Current Desktop project/task code primarily uses the shared WebSocket relay
client. Tauri HTTP commands in the project area concern Git hosting, repository
transport, and local checkout operations. Those are outside this relay's scope:
Lapis repositories use external `clone`/`web` URLs and Project Task Anchors have
neither. The relay must not advertise or synthesize Buzz's `/git/<owner>/<repo>`
transport.

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

The relay adds one deliberately narrow filter extension: exact
`#buzz-channel`. Arbitrary multi-character tag filtering remains unsupported so
upstream query behavior does not change accidentally.

## Refresh procedure

When Buzz changes:

1. Record the audited Buzz commit above.
2. Re-check `docs/nips/NIP-MP.md`, project event builders, project enumeration,
   issue mutation/fetch code, assignment reduction, deletion, and the shared
   relay client.
3. Search project and task code for HTTP paths and Tauri commands. Classify Git
   transport separately from Nostr event transport.
4. Update the event/query table and the contract fixtures before changing
   implementation.
5. Run `go test ./internal/projecttask`, the Zooid project/task integration
   tests, `go test ./...`, and the local Docker image build.

No changes for this feature belong under `internal/khatru`. The stable local
seams are `internal/projecttask` for domain rules, Zooid for persistence and
NIP-29 scope enforcement, and `internal/buzzcompat` for generic HTTP transport.
