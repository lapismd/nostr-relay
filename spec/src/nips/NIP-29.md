# NIP-29: Relay-based groups

## Protocol status and dependencies

Upstream status: **draft, optional, relay**. Dependencies: NIP-01, NIP-09, NIP-42.

## Source and audit

Source: [nostr-protocol/nips NIP-29 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/29.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Defines relay-hosted groups, management commands, messages, metadata, roles, and membership snapshots.

## Event and transport contract

Commands `9000`–`9022`; group content kinds including `9`, `10`, `11`, `1111`; relay-signed state `39000`–`39003`; exact `h` group tag.

## Validation, lifecycle and visibility

Zooid enforces group creation, membership, posting, management, visibility, replacement, and deletion at the relay boundary. Community projects authoritative state per relay URL plus group ID.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | implemented | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Relays may allow ordinary users to create groups unless policy says otherwise. | `groups.admin_create_only = true` is the default; authenticated members may still use existing public groups according to group policy. | Standards-permitted policy | Group creation requires an administrator by default; existing group interoperability is preserved. | `zooid/groups.go`, `config/default.toml`, `src/community/nip29-community-source.ts` |

## Evidence and refresh

Primary evidence: `zooid/groups.go`, `config/default.toml`, `src/community/nip29-community-source.ts`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
