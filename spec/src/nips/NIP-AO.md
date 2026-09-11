# NIP-AO: Agent observability

## Protocol status and dependencies

Upstream status: **draft, optional**. Dependencies: NIP-01, NIP-29, NIP-44 and NIP-OA.

## Source and audit

Source: [Buzz NIP-AO at `cec5c8fd9280d30f56effac701e1e19d5cfe6fea`](https://github.com/block/buzz/blob/cec5c8fd9280d30f56effac701e1e19d5cfe6fea/docs/nips/NIP-AO.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Streams encrypted ephemeral telemetry and control frames between an agent and its verified owner.

## Event and transport contract

Kind `24200` with exact `p`, `agent`, and `frame` tags; optional `h`; bounded opaque NIP-44 content. Telemetry is agent-to-owner and control is owner-to-agent.

## Validation, lifecycle and visibility

The relay verifies route, ownership, time, size, rate and optional mutual channel membership. It never stores or decrypts frames. Community decrypts authorized frames and stores only its separate owner-scoped local activity archive.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | required | Lapis does not fork or bundle an agent harness in this release. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Observer frames are ephemeral and end-to-end encrypted. | Community may retain decrypted owner-visible activity locally in an explicitly scoped archive; the relay remains persistence-free. | Client-local extension | No durable relay transcript is created; clearing the local archive does not alter Nostr events. | `modules/buzzagents/protocol/observer.go`, `zooid/agent_policy.go`, `src/community/agent-controller.ts` |

## Evidence and refresh

Primary evidence: `modules/buzzagents/protocol/observer.go`, `zooid/agent_policy.go`, `src/community/agent-controller.ts`. Re-audit this page when the pinned Buzz source, NIP-11 advertising, protocol module, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are tracked separately and never activate runtime behavior by documentation alone.
