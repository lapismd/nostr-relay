# NIP-AE: Agent engrams

## Protocol status and dependencies

Upstream status: **draft, optional**. Dependencies: NIP-01, NIP-44, NIP-OA.

## Source and audit

Source: [Buzz NIP-AE at `cec5c8fd9280d30f56effac701e1e19d5cfe6fea`](https://github.com/block/buzz/blob/cec5c8fd9280d30f56effac701e1e19d5cfe6fea/docs/nips/NIP-AE.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Stores addressable, encrypted agent memory that both an agent and its verified owner can read.

## Event and transport contract

Agent-signed kind `30174`, exact lowercase owner `p`, valid memory `d`, and a standard-base64 NIP-44 v2 envelope.

## Validation, lifecycle and visibility

The relay validates the public envelope and owner relationship, stores the addressable head, excludes it from full-text search, and limits visibility to the author and exact owner.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | unsupported | Client signing, projection or presentation roles described above. |
| External agent harness | planned | Lapis does not fork or bundle an agent harness in this release. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Agent and owner endpoints decrypt and interpret memory records. | The relay implements envelope, ownership, storage and visibility only; Community and Lapis do not execute an engram runtime. | Transport and policy subset | Compatible harnesses can use the store, but no Lapis UI or runtime promises memory behavior. | `modules/buzzagents/protocol/envelope.go`, `zooid/agent_policy.go`, `zooid/migrations/014_private_agent_search.sql` |

## Evidence and refresh

Primary evidence: `modules/buzzagents/protocol/envelope.go`, `zooid/agent_policy.go`, `zooid/migrations/014_private_agent_search.sql`. Re-audit this page when the pinned Buzz source, NIP-11 advertising, protocol module, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are tracked separately and never activate runtime behavior by documentation alone.
