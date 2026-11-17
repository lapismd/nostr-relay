# Non-NIP protocols and Lapis extensions

These interfaces are intentionally kept outside the NIP inventory. A NIP-11
identifier is never assigned to them, and accepting their events does not make
them Nostr standards.

| Interface | Purpose and authority | Lapis support | Compatibility boundary |
| --- | --- | --- | --- |
| [Blossom BUDs](../buds/index.md) | Content-addressed media upload and retrieval, authorized by signed Nostr events | Mixed; see the per-BUD inventory | Resource hashes, sizes, publisher signatures, and registry policy remain authoritative; a successful upload is not plugin approval. |
| Buzz-compatible HTTP bridge | Authenticated query, count, event publication, and presence projection over HTTP | Relay implemented; Community primarily uses WebSocket transport | NIP-98 authenticates calls, while route shapes and response bodies are application contracts. |
| Git Smart HTTP and forge APIs | Repository clone/push, browser, diff, review, and provider integration | Relay implemented for documented WalGit and GitHub profiles | Repository authorization is derived from signed Nostr state and NIP-29 policy; HTTP endpoints are not NIPs. |
| Lapis plugin registry events | Signed release manifests, publisher authorization, curator decisions, and deterministic registry heads | Relay and Community implemented | These are application schemas carried by NIP-29 events. Relay membership does not establish catalog authority. |
| Project Task Anchor | Clone-less kind `30617` marked `project-task-anchor=true` | Relay and Community implemented | This additive marker distinguishes a task-only project coordinate from a Git repository. Generic NIP-34 clients may ignore it. |
| Buzz `buzz-channel` project tag | Associates a project/repository graph with a Community channel | Relay and Community implemented | Lapis retains the Buzz tag and adds the explicit task-anchor marker rather than inferring repository class from missing strings. |

Implementation details are linked from [Relay surfaces](../relay/surfaces.md),
[Projects, tasks, and pull requests](../protocol/projects-tasks-and-pull-requests.md),
[Forge](../forge/model.md), and [Buzz agent compatibility](../compatibility/buzz-agents.md).
Re-audit this page whenever an extension is advertised through NIP-11 or is
submitted to an upstream standards process.
