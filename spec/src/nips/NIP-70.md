# NIP-70: Protected events

## Protocol status and dependencies

Upstream status: **draft, optional, relay**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-70 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/70.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Prevents third parties from republishing an event to a relay without the author’s participation.

## Event and transport contract

Presence of a `-` tag marks a protected event.

## Validation, lifecycle and visibility

Khatru checks that a protected event is submitted by an authenticated connection matching its author. Community can transport such events but has no separate protected-event workflow.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | transport-only | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Relays accept protected events only from their author. | The author must also satisfy ordinary tenant and feature authorization. | Standards-permitted policy | Protection does not bypass membership, group, or content validation. | `internal/khatru/handlers.go` |

## Evidence and refresh

Primary evidence: `internal/khatru/handlers.go`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
