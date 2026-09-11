# NIP-17: Private direct messages

## Protocol status and dependencies

Upstream status: **draft, optional, relay**. Dependencies: NIP-01, NIP-44, NIP-59.

## Source and audit

Source: [nostr-protocol/nips NIP-17 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/17.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Defines private direct messages as encrypted rumors inside sealed, gift-wrapped events.

## Event and transport contract

Kind `14` rumor, kind `13` seal, kind `1059` gift wrap, and kind `10050` inbox relay list.

## Validation, lifecycle and visibility

The relay transports opaque gift wraps. Community builds sender and recipient wraps, verifies the wrap/seal/rumor chain, decrypts with its signer, and uses advertised inbox relays.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | transport-only | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Clients validate and decrypt a NIP-59 gift-wrap chain. | The relay does not decrypt or infer direct-message membership; Community rejects invalid chains and missing inbox routes. | Role separation | Interoperable ciphertext remains end-to-end; delivery depends on usable recipient relay metadata. | `src/community/nip17.ts`, `src/community/nip29-community-source.ts` |

## Evidence and refresh

Primary evidence: `src/community/nip17.ts`, `src/community/nip29-community-source.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
