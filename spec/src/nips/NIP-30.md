# NIP-30: Custom emoji

## Protocol status and dependencies

Upstream status: **draft, optional**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-30 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/30.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Defines custom emoji references and addressable emoji sets.

## Event and transport contract

Kind `30030` emoji sets and `emoji` tags on supported content/profile events.

## Validation, lifecycle and visibility

The relay stores these events generically. Community replaces the connected author’s set, merges community sets deterministically, and renders only known emoji references.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | transport-only | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Clients resolve shortcode content through matching `emoji` tags or sets. | Community owns rendering and selection; relay policy does not validate image contents. | Role separation | Other clients can read the standard events; unsafe or missing images remain a client concern. | `src/community/community-controller.svelte.ts`, `src/components/community-chat-composer` |

## Evidence and refresh

Primary evidence: `src/community/community-controller.svelte.ts`, `src/components/community-chat-composer`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
