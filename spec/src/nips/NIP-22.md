# NIP-22: Comments

## Protocol status and dependencies

Upstream status: **draft, optional**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-22 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/22.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Defines scoped comment threads with explicit root and immediate-parent metadata.

## Event and transport contract

Kind `1111`; uppercase `E/K/P` identify the root and lowercase `e/k/p` identify the immediate parent.

## Validation, lifecycle and visibility

Relay project policy validates task and pull-request ancestry. Community creates and reduces forum and project comments, keeping replies out of root timelines.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | partial | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Root and parent tag sets consistently identify a supported target. | Relay validation is implemented for group forum and project work-item targets; arbitrary comment roots receive generic transport only. | Partial implementation | Supported roots are stricter and deterministic; other NIP-22 applications receive no semantic validation. | `internal/projecttask/model.go`, `src/protocol/nip29.ts`, `src/projects/data-source.ts` |

## Evidence and refresh

Primary evidence: `internal/projecttask/model.go`, `src/protocol/nip29.ts`, `src/projects/data-source.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
