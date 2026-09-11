# NIP-92: Media attachment metadata

## Protocol status and dependencies

Upstream status: **current upstream document, optional**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-92 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/92.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Describes media embedded in event content through structured `imeta` tags.

## Event and transport contract

`imeta` tag entries including URL, MIME type, hashes, dimensions, and fallback metadata.

## Validation, lifecycle and visibility

The relay transports tags. Community’s composer and renderer preserve supported media metadata and independently verifies registry resources by signed content hash.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | transport-only | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Clients associate an `imeta` entry with media URLs in event content. | Registry resource integrity follows the stricter signed-manifest and Blossom hash policy in addition to presentation metadata. | Additive application policy | Standard metadata renders; registry trust never derives from `imeta` alone. | `src/components/community-chat-composer`, `src/registry/release-inspection.ts` |

## Evidence and refresh

Primary evidence: `src/components/community-chat-composer`, `src/registry/release-inspection.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
