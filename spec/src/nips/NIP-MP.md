# NIP-MP: Multi-repository projects

## Protocol status and dependencies

Upstream status: **draft, optional, relay**. Dependencies: NIP-01, NIP-09, NIP-29, NIP-34 and NIP-OA.

## Source and audit

Source: [Buzz NIP-MP at `3c7f288c60d67df78577b237e27c3dfc8831aaa1`](https://github.com/block/buzz/blob/3c7f288c60d67df78577b237e27c3dfc8831aaa1/docs/nips/NIP-MP.md). Lapis implementation audit: repository revision containing this page, 2026-09-11.

## Purpose, roles and trust boundaries

Defines an addressable project that groups one or more NIP-34 repository coordinates.

## Event and transport contract

Owner-signed kind `30621` with a stable `d`, repository `a` tags, optional channel binding, and project metadata.

## Validation, lifecycle and visibility

Lapis validates and stores project heads and deletion, resolves project/task/repository graphs, and projects supported collaboration to Community, WalGit, and GitHub.

## Component support

| Component | Status | Scope |
| --- | --- | --- |
| Lapis Relay | partial | Relay validation, policy, persistence, query and delivery roles described above. |
| Community | partial | Client signing, projection or presentation roles described above. |
| External agent harness | not applicable | Lapis does not fork or bundle an agent harness in this release. |

## Divergences and limitations

| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |
| --- | --- | --- | --- | --- |
| Buzz associates projects and repositories through the `#buzz-channel` convention. | Lapis accepts that convention and adds an explicit clone-less `30617` Project Task Anchor marker to give task-only projects a stable NIP-34 coordinate. | Additive extension | Buzz projects remain readable; Lapis-aware clients can distinguish a task anchor from a Git repository without string inference. | `internal/projecttask`, `spec/src/protocol/projects-tasks-and-pull-requests.md`, Community `src/projects` |

## Evidence and refresh

Primary evidence: `internal/projecttask`, `spec/src/protocol/projects-tasks-and-pull-requests.md`, Community `src/projects`. Re-audit this page when the pinned Buzz source, NIP-11 advertising, protocol module, authorization hooks, storage/query code, Community reducer, configuration, or cited tests change. Upstream changes after the pin are tracked separately and never activate runtime behavior by documentation alone.
