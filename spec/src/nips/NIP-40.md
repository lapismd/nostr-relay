# NIP-40: Expiration timestamp

## Protocol status and dependencies

Upstream status: **draft, optional, relay**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-40 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/40.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Lets events request that relays stop serving them after an `expiration` timestamp.

## Event and transport contract

An `expiration` tag containing a Unix timestamp.

## Validation, lifecycle and visibility

Khatru contains an expiration manager, but Zooid deliberately does not start it and removes `40` from advertising.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | unsupported | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | not applicable | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Supporting relays should stop serving expired events. | The expiration manager is disabled and the relay does not advertise NIP-40. | Unsupported | Clients must not rely on Lapis to expire data. | `zooid/instance.go`, `internal/khatru/expiration.go` |

## Evidence and refresh

Primary evidence: `zooid/instance.go`, `internal/khatru/expiration.go`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
