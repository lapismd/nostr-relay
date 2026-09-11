# NIP-98: HTTP authentication

## Protocol status and dependencies

Upstream status: **draft, optional**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-98 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/98.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Authorizes an HTTP request with a short-lived signed event bound to method, URL, and optional payload.

## Event and transport contract

Kind `27235` in the `Authorization: Nostr …` header with `u`, `method`, and optional `payload` tags.

## Validation, lifecycle and visibility

Lapis verifies signature, age, exact URL/method, payload hash, and replay for protected HTTP APIs. Community creates standard-base64 authorization through its signer.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Generic HTTP endpoints verify the request-bound authorization event. | Git credential-helper exchange additionally issues a short-lived repository credential after NIP-98; browser and API routes use exact generic request binding. | Protocol profile | The helper token flow is Lapis-specific, while its initial authorization event remains NIP-98 compatible. | `internal/buzzcompat/handler.go`, `internal/gitforge/auth.go`, `src/registry/nip29-data-source.ts` |

## Evidence and refresh

Primary evidence: `internal/buzzcompat/handler.go`, `internal/gitforge/auth.go`, `src/registry/nip29-data-source.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
