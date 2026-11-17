# NIP-11: Relay information document

## Protocol status and dependencies

Upstream status: **draft, optional, relay**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-11 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/11.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Publishes relay metadata, limits, and advertised protocol support over HTTP.

## Event and transport contract

HTTP `GET` with `Accept: application/nostr+json`; JSON includes `supported_nips`, limits, and Lapis relay metadata.

## Validation, lifecycle and visibility

Khatru builds base metadata and Zooid adds configured or active capabilities, including NIP-94 when its kind `1063` validator is active. The compatibility inventory is audited against those additions.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Advertised identifiers describe relay capabilities. | Standard numeric identifiers and Buzz string identifiers coexist; Community must not infer unadvertised client-side support from the list. | Additive extension | JSON consumers must tolerate string draft identifiers as allowed unknown values. | `internal/khatru/nip11.go`, `zooid/buzz_compat.go`, `src/community/agent-session.ts` |

## Evidence and refresh

Primary evidence: `internal/khatru/nip11.go`, `zooid/buzz_compat.go`, `zooid/file_metadata.go`, `src/community/agent-session.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
