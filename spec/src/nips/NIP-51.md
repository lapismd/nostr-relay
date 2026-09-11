# NIP-51: Lists

## Protocol status and dependencies

Upstream status: **draft, optional**. Dependencies: NIP-01, optionally NIP-44.

## Source and audit

Source: [nostr-protocol/nips NIP-51 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/51.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Defines public and private addressable lists for bookmarks, people, relays, emoji, and application categories.

## Event and transport contract

Several replaceable/addressable list kinds with public tags and optional encrypted private content.

## Validation, lifecycle and visibility

The relay provides generic event transport. Community implements follow data and selected application state but retains several user preferences in device-local storage.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | transport-only | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | partial | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Supported list kinds synchronize their current author-owned head. | Community does not claim server synchronization for all settings or every NIP-51 list kind. | Partial client implementation | Implemented list events interoperate; device-only settings do not roam. | `src/community/state-store.ts`, `src/community/nip29-community-source.ts` |

## Evidence and refresh

Primary evidence: `src/community/state-store.ts`, `src/community/nip29-community-source.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
