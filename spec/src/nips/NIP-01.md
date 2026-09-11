# NIP-01: Basic protocol flow

## Protocol status and dependencies

Upstream status: **draft, mandatory, relay**. Dependencies: None.

## Source and audit

Source: [nostr-protocol/nips NIP-01 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/01.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Defines signed Nostr events, WebSocket request and publication envelopes, filters, subscriptions, and regular, replaceable, and addressable event treatment.

## Event and transport contract

Signed events; `EVENT`, `REQ`, `CLOSE`, `EOSE`, `OK`, and `NOTICE` envelopes. Lapis also exposes the same filter semantics through authenticated HTTP query routes.

## Validation, lifecycle and visibility

Khatru verifies signatures and envelope structure. Zooid applies tenant membership, group, private-data, and feature policy before storage or delivery. Replaceable heads use `created_at`, then lowest event ID for a tie.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| A conforming relay accepts and serves valid events through the standard flow. | Authentication and tenant policy can reject otherwise valid events or hide them from a reader. | Standards-permitted policy | NIP-01 permits relay policy; clients must handle `OK` rejection and incomplete visibility. | `internal/khatru/handlers.go`, `zooid/instance.go`, `src/nostr/transport.ts` |

## Evidence and refresh

Primary evidence: `internal/khatru/handlers.go`, `zooid/instance.go`, `src/nostr/transport.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
