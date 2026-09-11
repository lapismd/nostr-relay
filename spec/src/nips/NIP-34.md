# NIP-34: Git collaboration

## Protocol status and dependencies

Upstream status: **draft, optional**. Dependencies: NIP-01, NIP-09, NIP-22.

## Source and audit

Source: [nostr-protocol/nips NIP-34 at `a2494f4f81d46684e5814a9bf35e2b1df978f955`](https://github.com/nostr-protocol/nips/blob/a2494f4f81d46684e5814a9bf35e2b1df978f955/34.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Defines repository announcements, issues, patches, pull requests, statuses, and Git collaboration state.

## Event and transport contract

Kinds `30617`, `1621`, `1617`, `1618`, `1619`, `1630`–`1633`, plus comments and status tags.

## Validation, lifecycle and visibility

Lapis validates repository, task, pull-request, status, comment, and deletion subsets and projects them to WalGit or GitHub. Patch application and `30618` repository-state publication are deferred.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | partial | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | partial | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | No runtime capability is inferred from relay transport. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| NIP-34 defines a broad Git collaboration event graph. | Tasks and pull requests are supported; patch application, repository-state events, and some SDK flows remain deferred. | Partial implementation | Supported events are wire-compatible, but clients must not expect complete NIP-34 workflows. | `internal/projecttask`, `spec/src/protocol/projects-tasks-and-pull-requests.md`, `src/projects` |

## Evidence and refresh

Primary evidence: `internal/projecttask`, `spec/src/protocol/projects-tasks-and-pull-requests.md`, `src/projects`. Re-audit this page when the pinned source, NIP-11 advertising, relevant validators, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are recorded separately and do not change runtime behavior by documentation alone.
