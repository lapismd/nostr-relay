# NIP-16: Event treatment (historical)

## Protocol status and dependencies

Upstream status: **final; normative content moved to NIP-01**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-16 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/16.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Retains the historical identifier once used for event treatment capability signalling.

## Event and transport contract

No independent event shape remains at the pinned upstream revision; treatment rules now live in NIP-01.

## Validation, lifecycle and visibility

The relay follows NIP-01. Community only treats an advertised `16` as a legacy capability hint in agent discovery.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | not applicable | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | partial | Client signing, projection or presentation roles described above. |
| External agent harness | unsupported | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Current NIP-16 points readers to NIP-01. | Community accepts legacy NIP-16 advertising as a capability hint alongside Buzz `AO`. | Historical compatibility | No event semantics are taken from NIP-16; modern relays should advertise the actual feature. | `spec/src/nostr-protocol.md`, `src/community/agent-session.ts` |

## Evidence and refresh

Primary evidence: `spec/src/nostr-protocol.md`, `src/community/agent-session.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
