# NIP-43: Relay access metadata and requests

## Protocol status and dependencies

Upstream status: **draft, optional, relay**. Dependencies: NIP-01, NIP-11, NIP-42.

## Source and audit

Source: [nostr-protocol/nips NIP-43 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/43.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Describes relay membership/access state and a workflow for requesting access.

## Event and transport contract

Relay access metadata and signed admission requests; exact kinds and roles follow the pinned NIP.

## Validation, lifecycle and visibility

Zooid advertises NIP-43 and uses its authenticated relay-membership boundary. Community reads relay identity/capability information for managed-agent discovery.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | partial | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | partial | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Access metadata and request events provide an interoperable membership workflow. | Authoritative membership is the relay management store; advertised events describe state but do not replace it. | Relay policy profile | Requests still require Lapis operator policy; an event alone cannot self-grant membership. | `zooid/management.go`, `zooid/instance.go`, `src/community/agent-session.ts` |

## Evidence and refresh

Primary evidence: `zooid/management.go`, `zooid/instance.go`, `src/community/agent-session.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
