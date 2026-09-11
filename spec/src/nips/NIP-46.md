# NIP-46: Nostr remote signing

## Protocol status and dependencies

Upstream status: **current upstream document, optional**. Dependencies: NIP-01, NIP-44.

## Source and audit

Source: [nostr-protocol/nips NIP-46 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/46.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Lets a client delegate signing and encryption operations to a remote signer.

## Event and transport contract

Nostr Connect/bunker request and response events using encrypted RPC content.

## Validation, lifecycle and visibility

The relay transports events without participating in signer sessions. Community accepts a host-provided remote signer through the same `CommunityIdentity` interface.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | transport-only | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Client and remote signer maintain an encrypted request/response session. | Connection lifecycle and secret persistence belong to the embedding host, not Community UI components. | Host integration profile | Compatible signers can be used when the host supplies the connector. | `src/auth/host-signer-broker.ts`, `src/auth/signer.ts`, `spec/src/nostr-protocol.md` |

## Evidence and refresh

Primary evidence: `src/auth/host-signer-broker.ts`, `src/auth/signer.ts`, `spec/src/nostr-protocol.md`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
