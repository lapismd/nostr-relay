# NIP-42: Relay authentication

## Protocol status and dependencies

Upstream status: **draft, optional, relay**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-42 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/42.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Authenticates a client connection to a relay with a challenge-bound signed event.

## Event and transport contract

Relay `AUTH` challenge and client kind `22242` response with `relay` and `challenge` tags.

## Validation, lifecycle and visibility

Khatru verifies the event, origin, challenge, and freshness. Zooid binds the authenticated pubkey to the connection; Buzz NIP-AA may derive connection-local agent admission after this verification. An authenticated application session may request a challenge-bound kind `22242` signature from its managed or NIP-46 signer through `/api/auth/relay/sign-auth`; the signing policy fixes the relay URL and event kind.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | required | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Authentication proves control of the event signer for one relay connection. | Most protected operations require authentication; NIP-AA can additionally derive virtual membership from a verified owner credential. | Additive authorization profile | Generic NIP-42 clients work; owner-derived agents can use an extra credential path. | `internal/khatru/handlers.go`, `zooid/agent_policy.go`, `src/nostr/transport.ts` |

## Evidence and refresh

Primary evidence: `internal/khatru/handlers.go`, `zooid/agent_policy.go`, `src/nostr/transport.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
