# NIP-77: Negentropy syncing

## Protocol status and dependencies

Upstream status: **draft, optional, relay**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-77 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/77.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Efficiently reconciles differing event sets between peers.

## Event and transport contract

`NEG-OPEN`, `NEG-MSG`, `NEG-CLOSE`, and error flows over WebSocket.

## Validation, lifecycle and visibility

Zooid enables Khatru negentropy sessions. Community uses ordinary queries/subscriptions and has no negentropy client.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | unsupported | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Both peers exchange range fingerprints and missing IDs. | Available on the relay transport; Community does not invoke it. | Component limitation | Other NIP-77 clients may sync; Community behavior is unchanged. | `zooid/instance.go`, `internal/khatru/relay.go` |

## Evidence and refresh

Primary evidence: `zooid/instance.go`, `internal/khatru/relay.go`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
