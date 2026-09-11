# NIP-07: Browser signer capability

## Protocol status and dependencies

Upstream status: **draft, optional**. Dependencies: NIP-01; optional NIP-44.

## Source and audit

Source: [nostr-protocol/nips NIP-07 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/07.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Defines the `window.nostr` browser signing and encryption capability.

## Event and transport contract

Host-injected signer calls for public-key discovery, event signing, and optional NIP-44 encrypt/decrypt.

## Validation, lifecycle and visibility

The relay has no role. Community normalizes NIP-07 and other signer types behind `CommunityIdentity`; presentation code never receives a private key.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | not applicable | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Web clients call a browser-provided `window.nostr` API. | Community receives an equivalent signer interface from its host; NIP-07 is the default browser connector. | Transport abstraction | NIP-07 extensions interoperate; non-browser hosts can supply the same authority boundary. | `src/auth/signer.ts`, `spec/src/nostr-protocol.md` |

## Evidence and refresh

Primary evidence: `src/auth/signer.ts`, `spec/src/nostr-protocol.md`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
