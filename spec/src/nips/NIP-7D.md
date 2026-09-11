# NIP-7D: Forum threads

## Protocol status and dependencies

Upstream status: **draft, optional**. Dependencies: NIP-01, NIP-22.

## Source and audit

Source: [nostr-protocol/nips NIP-7D at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/7D.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Defines top-level forum thread events and their comments.

## Event and transport contract

Kind `11` thread with `title`; replies use NIP-22 kind `1111`.

## Validation, lifecycle and visibility

Community creates, verifies, reduces, and displays threads and replies. Zooid applies NIP-29 `h` membership and visibility policy where forums belong to relay groups.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | partial | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Forum roots and comments are public Nostr events. | Group forums require exact NIP-29 context and inherit the group’s read/write policy. | Standards-compatible policy profile | Threads outside a Lapis group receive generic handling; group threads require authorization. | `zooid/groups.go`, `src/protocol/nip29.ts`, `src/community/nip29-community-source.ts` |

## Evidence and refresh

Primary evidence: `zooid/groups.go`, `src/protocol/nip29.ts`, `src/community/nip29-community-source.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
