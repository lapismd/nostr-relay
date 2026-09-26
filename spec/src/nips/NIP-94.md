# NIP-94: File metadata

## Protocol status and dependencies

Upstream status: **draft, optional**. Dependencies: NIP-01, NIP-11.

## Source and audit

Source: [nostr-protocol/nips NIP-94 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/94.md). Lapis implementation audit: repository revision containing this page, 2026-09-26.

## Purpose, roles and trust boundaries

Kind `1063` describes a downloadable file. The author-signed event supplies its URI, MIME type and SHA-256 digest; metadata never proves that a profile, plugin release or other application record uses the file.

## Event and transport contract

The relay advertises NIP-94 in NIP-11, accepts valid signed kind `1063` events through the normal authenticated write policy, stores them as regular events and indexes the standard single-letter `m` and `x` tags for NIP-01 filters. One absolute `url`, lowercase MIME `m`, and lowercase SHA-256 `x` tag are required. Optional `ox`, `size`, and `dim` tags are validated when present.

## Validation, lifecycle and visibility

Normal authentication, membership, ban, visibility and deletion rules apply. File bytes remain governed by Blossom storage and signed application references; accepting metadata neither uploads nor retains the described bytes.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Advertises, validates, stores and serves queryable kind `1063` metadata. |
| Community | unsupported | Community verifies BUD-08 descriptor tags but does not publish or present kind `1063` events. |
| External agent harness | not applicable | No runtime capability is inferred from file metadata. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Kind `1063` describes file metadata. | The relay validates the required core tags and unambiguous optional numeric tags before storage. | Additive relay policy | Malformed metadata is rejected instead of being stored as an opaque regular event. | `zooid/file_metadata.go`, `zooid/file_metadata_test.go` |
| `url`, `size`, and `dim` use multi-character keys. | They are preserved in event JSON but are not NIP-01 tag-filter keys; `m` and `x` remain searchable. | Protocol boundary | NIP-01 filters index only single-letter tag names. | `zooid/events.go` |

## Evidence and refresh

Primary evidence: `zooid/file_metadata.go`, `zooid/file_metadata_test.go`, `zooid/instance.go`. Re-audit this page when the pinned source, NIP-11 advertising, validators, authorization hooks, storage/query code or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
