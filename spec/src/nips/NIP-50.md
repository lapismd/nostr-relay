# NIP-50: Search capability

## Protocol status and dependencies

Upstream status: **draft, optional, relay**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-50 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/50.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Adds textual search terms to Nostr filters.

## Event and transport contract

Filter `search` string alongside ordinary authors, kinds, tags, and time fields.

## Validation, lifecycle and visibility

Zooid maintains PostgreSQL search vectors for eligible public content. Private agent data and personas are excluded; group visibility is applied before results and pagination.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | partial | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | partial | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Supporting relays interpret the `search` filter over indexed events. | Search is restricted to safe indexed kinds and authorized visibility; Community renders only known profiles, rooms, and messages. | Partial implementation and privacy policy | Results can be narrower than a generic full-content index, preventing private text disclosure. | `zooid/events.go`, `zooid/migrations/015_private_persona_search.sql`, `src/community/nip29-community-source.ts` |

## Evidence and refresh

Primary evidence: `zooid/events.go`, `zooid/migrations/015_private_persona_search.sql`, `src/community/nip29-community-source.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
