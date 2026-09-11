# NIP-25: Reactions

## Protocol status and dependencies

Upstream status: **draft, optional**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-25 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/25.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Associates a user reaction with an event.

## Event and transport contract

Kind `7` with target tags and user-generated content such as `+` or an emoji.

## Validation, lifecycle and visibility

Community verifies reactions, handles same-author kind-5 removal, and projects supported values. Relay group and registry policy require valid channel context where applicable.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | partial | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| A reaction references its target with standard tags. | NIP-29 group reactions must carry the target channel `h`; GitHub projection maps only the provider’s supported reaction set. | Context policy and partial projection | Public standalone reactions still transport; group-scoped and provider-backed uses are narrower. | `zooid/registry_policy.go`, `internal/githubforge/reactions.go`, `src/community/community-controller.svelte.ts` |

## Evidence and refresh

Primary evidence: `zooid/registry_policy.go`, `internal/githubforge/reactions.go`, `src/community/community-controller.svelte.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
