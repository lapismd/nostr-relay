# NIP-AM: Agent turn metrics

## Protocol status and dependencies

Upstream status: **draft, optional, relay**. Dependencies: NIP-01, NIP-44 and NIP-OA.

## Source and audit

Source: [Buzz NIP-AM at `cec5c8fd9280d30f56effac701e1e19d5cfe6fea`](https://github.com/block/buzz/blob/cec5c8fd9280d30f56effac701e1e19d5cfe6fea/docs/nips/NIP-AM.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Persists encrypted per-turn usage measurements for an agent’s verified owner.

## Event and transport contract

Append-only agent-signed kind `44200` with exact `p` owner and `agent` tags, no `h`, and opaque NIP-44 v2 content.

## Validation, lifecycle and visibility

Lapis verifies ownership and envelope shape, rate-limits agents, keeps all accepted rows append-only, excludes content from search, and restricts read/count/live delivery to the exact owner and agent.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | unsupported | Client signing, projection or presentation roles described above. |
| External agent harness | planned | Lapis does not fork or bundle an agent harness in this release. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| A harness emits one encrypted metric for each completed turn. | The relay accepts and protects valid metrics but no Lapis runtime emits them and Community has no metrics presentation. | Endpoint capability absent | External publishers interoperate; absence of Lapis emission must not be reported as zero usage. | `modules/buzzagents/protocol/envelope.go`, `zooid/agent_policy.go`, `zooid/count.go` |

## Evidence and refresh

Primary evidence: `modules/buzzagents/protocol/envelope.go`, `zooid/agent_policy.go`, `zooid/count.go`. Re-audit this page when the pinned Buzz source, NIP-11 advertising, protocol module, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are tracked separately and never activate runtime behavior by documentation alone.
