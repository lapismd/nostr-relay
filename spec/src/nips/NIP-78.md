# NIP-78: Application-specific data

## Protocol status and dependencies

Upstream status: **draft, optional, relay**. Dependencies: NIP-01, optionally NIP-44.

## Source and audit

Source: [nostr-protocol/nips NIP-78 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/78.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Provides an addressable container for application-owned synchronized state.

## Event and transport contract

Kind `30078` with application-specific `d` and content, optionally encrypted.

## Validation, lifecycle and visibility

Relay handling is generic addressable storage. Community uses scoped encrypted state through its state-store abstraction and rejects it when the identity lacks NIP-44.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | transport-only | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | partial | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Applications namespace and interpret their own current data. | Only explicitly scoped Community state uses this path; persona definitions use dedicated kind `30175`. | Compatible application profile | No claim is made for arbitrary other applications’ schemas. | `src/community/state-store.ts`, `src/community/state-store.test.ts` |

## Evidence and refresh

Primary evidence: `src/community/state-store.ts`, `src/community/state-store.test.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
