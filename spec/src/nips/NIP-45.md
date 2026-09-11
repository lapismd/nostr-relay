# NIP-45: Event counts

## Protocol status and dependencies

Upstream status: **draft, optional, relay**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-45 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/45.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Adds count queries for filters without returning all matching events.

## Event and transport contract

`COUNT` request/response, including multiple filter union and optional HyperLogLog metadata in Lapis.

## Validation, lifecycle and visibility

Khatru and Zooid count authorized rows, deduplicate overlapping filters, and ignore pagination limits. Private or group filters are authorized before counting; unsafe shapes fail closed.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Counts correspond to events matching a filter. | Multi-filter union and optional HLL are additive; visibility-relevant filters may be rejected unless they prove exact authorized scope. | Additive extension and privacy policy | No private cardinality leaks; clients must handle a rejected broad count. | `internal/khatru/count.go`, `zooid/count.go`, `src/nostr/transport.ts` |

## Evidence and refresh

Primary evidence: `internal/khatru/count.go`, `zooid/count.go`, `src/nostr/transport.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
