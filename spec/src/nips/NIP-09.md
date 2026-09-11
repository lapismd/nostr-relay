# NIP-09: Event deletion requests

## Protocol status and dependencies

Upstream status: **draft, optional, relay**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-09 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/09.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Lets an author request deletion of their own events or addressable coordinates.

## Event and transport contract

Kind `5` with `e` and/or `a` tags. Persona deletion uses an `a` tag for `30175:<owner>:<slug>`.

## Validation, lifecycle and visibility

Zooid checks author authority, applies deletion, and removes stored rows. Community validates deletion authors before removing projected profiles, personas, reactions, or content.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Relays should delete referenced events authored by the deletion signer. | Accepted deletions physically remove stored heads, so an older retained revision does not reappear. | Retention policy | Compatible clients see the coordinate absent; historical resurrection is deliberately prevented. | `zooid/deletion.go`, `src/community/agent-protocol.ts` |

## Evidence and refresh

Primary evidence: `zooid/deletion.go`, `src/community/agent-protocol.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
