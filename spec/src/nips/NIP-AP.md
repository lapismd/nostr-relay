# NIP-AP: Agent personas

## Protocol status and dependencies

Upstream status: **draft, optional**. Dependencies: NIP-01, NIP-09, NIP-OA; NIP-44 only for other agent data.

## Source and audit

Source: [Buzz NIP-AP at `cec5c8fd9280d30f56effac701e1e19d5cfe6fea`](https://github.com/block/buzz/blob/cec5c8fd9280d30f56effac701e1e19d5cfe6fea/docs/nips/NIP-AP.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Separates an owner-authored reusable persona definition from a specific owner-authored agent instance and the agent-signed public profile.

## Event and transport contract

Persona kind `30175` uses exact `d=<slug>` and JSON-string content with required `display_name`, optional `avatar_url` and `system_prompt`. Instance kind `30177` uses `d=<agent-pubkey>` and optional `persona_id`, `name`, `respond_to`, and `parallelism`. Lapis adds explicit `name_mode: inherited|instance`.

## Validation, lifecycle and visibility

Heads use timestamp then lowest ID. Personas are author-only unless shared, with additional exact access for the currently linked, NIP-OA-verified agent. Relay validation limits content to 65,535 bytes, rejects known secret-bearing fields, excludes persona text from search, authorizes counts before paging, and invalidates access when the link, profile credential, or deletion changes.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | planned | Lapis does not fork or bundle an agent harness in this release. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Persona readers are the author or, for a shared head, foreign authenticated readers. | The exact currently linked agent may also read its private persona when a valid current NIP-OA owner relationship exists. | Additive authorization extension | The runtime can fetch only its linked configuration; siblings and strangers remain denied and relinking revokes the old grant. | `zooid/agent_policy.go`, `zooid/agent_policy_test.go` |
| Instance `name` is independent state but the source has no explicit inheritance switch. | Lapis adds `name_mode` with `inherited` or `instance`; missing legacy modes mean `instance` even when strings match. | Additive wire field | Inheritance remains explicit across rename and migration; older readers ignore the unknown field and retain the stored name. | Community `src/community/agent-protocol.ts`, `agent-protocol.test.ts` |
| Persona presentation uses `display_name` and `avatar_url`. | Community also preserves an optional `description` extension and resolves avatar independently of name mode, falling back to kind `0`. | Additive content field | Unknown-field tolerance keeps the event readable by Buzz; avatar updates appear while the agent is offline. | Community `src/community/agent-session.ts`, `CommunityAgentProfile.svelte` |
| Persona content is plaintext and must not include secrets. | The relay and Community reject/remove known secret-bearing field names, but do not claim arbitrary prompt text is secret-free. | Defensive validation | Obvious key/env containers cannot be serialized; owners must still treat every authorized reader as able to read the prompt. | `modules/buzzagents/protocol/envelope.go`, Community `src/community/agent-session.ts` |
| Definition-level fields live on `30175`; per-instance state lives on `30177`; the public Nostr identity remains agent-signed kind `0`. | Community saves prompts as inactive desired configuration and never writes kind `0`; Lapis ships no harness fork or runtime reconciliation. | Partial endpoint implementation | The persona/instance/profile distinction is preserved, while compatible external runtimes may consume saved configuration in a later milestone. | Community `spec/src/agents.md`, `src/community/agent-session.test.ts` |

## Evidence and refresh

Primary evidence: `modules/buzzagents/protocol/envelope.go`, `zooid/agent_policy.go`, Community `src/community/agent-protocol.ts` and `agent-session.ts`. Re-audit this page when the pinned Buzz source, NIP-11 advertising, protocol module, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are tracked separately and never activate runtime behavior by documentation alone.
