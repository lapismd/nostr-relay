# NIP-86: Relay management API

## Protocol status and dependencies

Upstream status: **draft, optional**. Dependencies: NIP-11, NIP-42.

## Source and audit

Source: [nostr-protocol/nips NIP-86 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/86.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Defines authenticated HTTP methods for managing relay users and events.

## Event and transport contract

JSON-RPC-like HTTP calls authenticated as configured relay administrators.

## Validation, lifecycle and visibility

Khatru exposes standard methods and Zooid persists membership, bans, and roles. Lapis-specific role methods map known capabilities; unknown roles remain stored but inert.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | implemented | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | not applicable | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Administrative methods are restricted to authenticated relay operators. | Lapis adds role-management methods and capability mapping without granting semantics to arbitrary role strings. | Additive extension | Standard management clients keep core behavior; extensions require Lapis awareness. | `internal/khatru/nip86.go`, `zooid/management.go` |

## Evidence and refresh

Primary evidence: `internal/khatru/nip86.go`, `zooid/management.go`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
