# NIP-02: Follow lists

## Protocol status and dependencies

Upstream status: **final, optional**. Dependencies: NIP-01.

## Source and audit

Source: [nostr-protocol/nips NIP-02 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/02.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Represents the set of profiles followed by one author.

## Event and transport contract

Replaceable kind `3` with `p` tags. Community uses it for the owner’s agent-follow state and Pulse Following feed.

## Validation, lifecycle and visibility

The relay stores the winning replaceable head. Community verifies the event and treats the current owner-authored head as a set.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | transport-only | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Kind `3` is a user-authored follow list. | The relay applies generic replaceable storage; Community also uses it to follow and unfollow managed agents. | Compatible application use | No wire incompatibility; list replacement remains standard. | `src/community/agent-session.ts`, `src/community/nip29-community-source.ts` |

## Evidence and refresh

Primary evidence: `src/community/agent-session.ts`, `src/community/nip29-community-source.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
