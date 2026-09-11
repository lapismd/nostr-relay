# NIP-38: User statuses

## Protocol status and dependencies

Upstream status: **draft, optional**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-38 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/38.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Publishes current general or scoped user status.

## Event and transport contract

Addressable kind `30315`, normally with `d=general`, optional expiry and content metadata.

## Validation, lifecycle and visibility

The relay provides generic addressable treatment. Community selects the deterministic head, maps general status into profile presentation, and clears it with a newer empty value.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | transport-only | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Clients publish and select addressable status heads. | Community currently exposes general status and session-local presence choices, not every specialized status type. | Partial client profile | General status interoperates; unimplemented categories are ignored. | `src/community/materialize.ts`, `src/community/community-controller.svelte.ts` |

## Evidence and refresh

Primary evidence: `src/community/materialize.ts`, `src/community/community-controller.svelte.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
