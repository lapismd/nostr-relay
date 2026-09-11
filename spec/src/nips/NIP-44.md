# NIP-44: Versioned encrypted payloads

## Protocol status and dependencies

Upstream status: **optional**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-44 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/44.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Defines versioned key-pair encryption used by private messages, app state, and Buzz agent protocols.

## Event and transport contract

Standard-base64 versioned ciphertext; endpoints derive conversation keys and authenticate plaintext.

## Validation, lifecycle and visibility

The relay validates only public envelope properties for supported Buzz kinds and otherwise stores opaque content. Community encrypts and decrypts through its signer.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | transport-only | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | required | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Endpoints perform encryption, MAC verification, and decryption. | Relay-side checks establish plausible v2 encoding and size only; they never establish cryptographic validity or plaintext meaning. | Role separation | Storage of ciphertext is not reported as encryption support; endpoint verification remains mandatory. | `modules/buzzagents/protocol/envelope.go`, `src/auth/signer.ts`, `src/community/nip17.ts` |

## Evidence and refresh

Primary evidence: `modules/buzzagents/protocol/envelope.go`, `src/auth/signer.ts`, `src/community/nip17.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
