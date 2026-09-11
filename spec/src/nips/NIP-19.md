# NIP-19: bech32-encoded entities

## Protocol status and dependencies

Upstream status: **draft, optional**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-19 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/19.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Defines human-readable encodings such as `npub`, `nevent`, and `naddr`.

## Event and transport contract

Client-side bech32 encode/decode; no relay envelope.

## Validation, lifecycle and visibility

Community decodes public keys and group invitations and emits `nostr:` entity links. It validates decoded types before use.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | not applicable | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Applications use defined HRPs and TLV fields. | Community accepts supported identity and address forms only at the UI boundary. | Standards-compatible subset | Unsupported HRPs fail validation rather than being guessed. | `src/community/group-invites.ts`, `src/components/community-application/CommunityApplication.svelte` |

## Evidence and refresh

Primary evidence: `src/community/group-invites.ts`, `src/components/community-application/CommunityApplication.svelte`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
