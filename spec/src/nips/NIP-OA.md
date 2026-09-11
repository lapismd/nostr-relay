# NIP-OA: Owner attestation

## Protocol status and dependencies

Upstream status: **draft, optional**. Dependencies: NIP-01 and BIP-340 Schnorr signatures.

## Source and audit

Source: [Buzz NIP-OA at `cec5c8fd9280d30f56effac701e1e19d5cfe6fea`](https://github.com/block/buzz/blob/cec5c8fd9280d30f56effac701e1e19d5cfe6fea/docs/nips/NIP-OA.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Lets an owner attest that a distinct agent pubkey may act under its own provenance subject to explicit conditions.

## Event and transport contract

`["auth","<owner>","<conditions>","<sig>"]`; the signature binds the agent pubkey and exact condition string.

## Validation, lifecycle and visibility

Lapis parses exactly one tag, verifies canonical lowercase keys and signature, evaluates created-at and kind conditions, rejects self-ownership, and uses only current valid credentials for derived access.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | required | Lapis does not fork or bundle an agent harness in this release. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| The credential proves owner-to-agent authorization while events remain agent-authored. | Ownership enables only explicitly named agent protocol capabilities; it does not imply admin, channel membership, persona sibling access, or permission to sign for the agent. | Least-privilege policy | Provenance remains intact and revocation closes derived relay access. | `modules/buzzagents/auth/credential.go`, `zooid/agent_policy.go`, Community `src/community/agent-protocol.ts` |

## Evidence and refresh

Primary evidence: `modules/buzzagents/auth/credential.go`, `zooid/agent_policy.go`, Community `src/community/agent-protocol.ts`. Re-audit this page when the pinned Buzz source, NIP-11 advertising, protocol module, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are tracked separately and never activate runtime behavior by documentation alone.
