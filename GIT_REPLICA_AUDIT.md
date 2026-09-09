# Git Replica Abstraction Audit

This audit records the pre-replica boundary at the capability/GitHub baseline
(`e8eebe03`). It is a compatibility checklist, not signed Nostr authority.

## Existing boundary

- `internal/forge.GitBackend` combined clone/default-ref ownership with
  tree/blob/history/diff reads.
- Its repository reads returned an HTTP-shaped `forge.Response`; WalGit streamed
  its sidecar JSON and GitHub privately recreated that JSON.
- `RepositoryAPIBackend` allowed each provider to own public `/api/git` JSON.
- `repository_integrations.git_backend` selected both the public Git authority
  and the content reader.
- WalGit already provides provisioning, authenticated Smart HTTP, bounded
  compare, durable object storage, ref-event delivery, and full import.
- GitHub already provides fixed-host App authentication, stable repository-ID
  attachment, verified/deduplicated webhook delivery, ref snapshots, an
  authenticated clone source, collaboration, migration, and Actions.

## Reusable seams

- `internal/gitforge.WalGitAdapter` remains the adapter over the existing
  WalGit package. The implementation is not moved into `internal/forge`.
- `RepositoryImporter` already uses an ephemeral bare repository, askpass for
  the source token, a temporary destination config, `fetch --prune`, and
  `push --mirror`; it supports force moves and deletions without another
  durable Git store in Go.
- GitHub push deliveries already retain delivery ID, repository ID, ref, and
  target OID. WalGit ref events already retain pusher and correlation ID.
- NIP-29/NIP-98 checks already happen before repository browser and PR diff
  calls and remain transport-independent.

## Required split

The implementation separates:

- `GitAuthorityBackend`: clone descriptor, default branch, canonical refs, and
  ref snapshots;
- `RepositoryContentBackend`: resolve, tree, blob, commits, commit, and diff;
- optional provisioning/import/Smart HTTP/replica target capabilities.

Provider-neutral result types own the application semantics. WalGit and GitHub
translate into `Revision`, `RefPage`, `Tree`, `Blob`, `CommitPage`,
`CommitDetail`, and `Diff`. Public serialization is centralized in Zooid.
Results explicitly model truncation, opaque continuation, incomplete portions,
limits exceeded, and capability limits.

## Schema and migration risks

- `git_backend` must remain readable for old rows and clients while independent
  authority/content columns are backfilled.
- Explicit attachments must never be reclassified from a later clone URL.
- A replica is operational state. Rebuilding it must not change a `30617`, its
  coordinate, project membership, tasks, PRs, or collaboration mappings.
- Replica writes create WalGit ref events. The internal replica principal and
  correlation ID must prevent those projection events from becoming provider
  writes, automatic PR revisions, CI starts, or duplicate activity.
- GitHub remains canonical until an explicit, ref-verified authority cutover;
  drift is repaired only GitHub to WalGit in that mode.

## Observable compatibility gates

- Existing `/git`, `/api/git`, and `/api/pull-requests/.../diff` routes, NIP-98
  authentication, NIP-29 ACL behavior, current JSON fields, error codes, and
  private cache behavior remain supported.
- Additive content metadata is allowed: `truncated`, `continuation`,
  `incomplete`, `limit_exceeded`, and `capability_limits`.
- GitHub clone/push remains provider-native; an internal WalGit replica never
  publishes or replaces the owner-signed clone locator.
- `internal/khatru` is outside this implementation boundary.
