# NIP-AA: Agent authentication

## Protocol status and dependencies

Upstream status: **draft, optional, relay**. Dependencies: NIP-01, NIP-42, NIP-43 and NIP-OA.

## Source and audit

Source: [Buzz NIP-AA at `cec5c8fd9280d30f56effac701e1e19d5cfe6fea`](https://github.com/block/buzz/blob/cec5c8fd9280d30f56effac701e1e19d5cfe6fea/docs/nips/NIP-AA.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Allows an agent that is not an explicit relay member to authenticate through a current member owner’s NIP-OA credential.

## Event and transport contract

A NIP-42 kind `22242` AUTH event carries exactly one four-field `auth` tag. The owner attestation identifies the agent event author and constrains time and optionally event kinds.

## Validation, lifecycle and visibility

Lapis first verifies NIP-42, then verifies the NIP-OA Schnorr proof, canonical pubkeys, conditions, owner membership and revocation. Admission is connection-local and vanishes on disconnect.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | not applicable | Client signing, projection or presentation roles described above. |
| External agent harness | required | Lapis does not fork or bundle an agent harness in this release. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| A valid owner credential for an active member grants virtual relay membership to the agent. | The grant is connection-local, never creates a member record, and grants no role, NIP-29 membership, or private owner scope. | Standards-compatible security profile | Agent connectivity follows Buzz while privilege remains explicitly separated. | `modules/buzzagents/auth`, `zooid/agent_policy.go`, `zooid/agent_policy_test.go` |

## Evidence and refresh

Primary evidence: `modules/buzzagents/auth`, `zooid/agent_policy.go`, `zooid/agent_policy_test.go`. Re-audit this page when the pinned Buzz source, NIP-11 advertising, protocol module, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are tracked separately and never activate runtime behavior by documentation alone.
