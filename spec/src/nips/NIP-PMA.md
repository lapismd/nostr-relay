# NIP-PMA: Private managed-agent aggregate

## Protocol status and dependencies

Upstream status: **draft reservation; protocol/codec only**. Dependencies: NIP-01, NIP-44, NIP-OA and migration/CAS rules.

## Source and audit

Source: [Buzz NIP-PMA at `cec5c8fd9280d30f56effac701e1e19d5cfe6fea`](https://github.com/block/buzz/blob/cec5c8fd9280d30f56effac701e1e19d5cfe6fea/docs/nips/NIP-PMA.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Reserves an owner-authored private aggregate intended to become durable authority for one runnable managed agent after a verified migration.

## Event and transport contract

Reserved addressable kind `30179` keyed by agent pubkey.

## Validation, lifecycle and visibility

Lapis rejects every client publication and does not advertise, store, query, migrate, or decrypt this kind.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | unsupported | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | unsupported | Client signing, projection or presentation roles described above. |
| External agent harness | unsupported | Lapis does not fork or bundle an agent harness in this release. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Relays must reject the reserved kind until privacy, transactional CAS, migration, backup/restore, revocation, and capability gates are complete. | The relay rejects kind `30179`; Community omits it and continues using separate `30175`, `30177`, and kind `0` records. | Required deferral | No client can mistake partial storage for safe aggregate support. | `modules/buzzagents/protocol/kinds.go`, `modules/buzzagents/protocol/envelope_test.go`, `spec/src/agents.md` |

## Evidence and refresh

Primary evidence: `modules/buzzagents/protocol/kinds.go`, `modules/buzzagents/protocol/envelope_test.go`, `spec/src/agents.md`. Re-audit this page when the pinned Buzz source, NIP-11 advertising, protocol module, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are tracked separately and never activate runtime behavior by documentation alone.
