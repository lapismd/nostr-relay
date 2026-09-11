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

The relay transports ordinary kind `24133` events and can also act as a NIP-46 client for an application account. The auth package stores an encrypted client key and a sanitized bunker locator, applies a local kind/purpose policy before `sign_event`, and verifies the returned event and unchanged account pubkey. Community accepts a host-provided remote signer through the same `CommunityIdentity` interface.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Kind `24133` transport plus account-bound NIP-46 client signing. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Client and remote signer maintain an encrypted request/response session. | Connection lifecycle and secret persistence belong to the embedding host, not Community UI components. | Host integration profile | Compatible signers can be used when the host supplies the connector. | `src/auth/host-signer-broker.ts`, `src/auth/signer.ts`, `spec/src/nostr-protocol.md` |
| A managed account moves to an independent signer without changing its pubkey. | A passkey-gated X25519 transfer and same-pubkey proof complete an atomic managed-to-NIP-46 binding change. | Additive custody profile | Desktop support can consume the documented transfer contract without changing relay identity. | `internal/auth/custody.go`, `internal/auth/nip46.go`, `spec/src/auth.md` |

## Evidence and refresh

Primary evidence: `internal/auth/nip46.go`, `internal/auth/custody.go`, `src/auth/host-signer-broker.ts`, `src/auth/signer.ts`, `spec/src/auth.md`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
