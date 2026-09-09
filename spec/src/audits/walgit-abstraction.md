# WalGit abstraction audit

This document freezes the observable WalGit-backed forge contract before the
capability-based forge registry is introduced. It is a characterization of the
landed code at `809484b2`, not a statement about planned behavior.

## Ownership boundary

The relay owns signed Nostr collaboration state and NIP-29 authorization.
WalGit owns Git objects, refs, LFS data, bundles, and its object-store WAL. The
only public entry point to WalGit is the relay; clients never receive the
sidecar URL, relay-to-sidecar bearer token, or trusted WalGit principal header.

`internal/gitforge` contains repository classification, NIP-98 Git credentials,
and the storage-independent WalGit client. Zooid contains desired-state
persistence, reconciliation, NIP-29 enforcement, PR reduction, and webhook
storage. No forge code lives in `internal/khatru`.

## Frozen public surface

| Surface | Current behavior |
| --- | --- |
| `/git/<owner>/<repo>[.git]/info/refs` | GET only; allowlisted upload-pack or receive-pack discovery |
| `/git/<owner>/<repo>[.git]/git-upload-pack` | POST, repository read permission |
| `/git/<owner>/<repo>[.git]/git-receive-pack` | POST, repository write permission |
| `/git/<owner>/<repo>[.git]/info/lfs/...` | allowlisted LFS batch/object/verify operations |
| `/git/<owner>/<repo>[.git]/bundles/...` | GET or HEAD read-only bundle operations |
| `/api/git/<owner>/<repo>/refs` | GET or HEAD, streamed WalGit JSON |
| `/api/git/<owner>/<repo>/refs/branches` | GET or HEAD, streamed WalGit JSON |
| `/api/git/<owner>/<repo>/refs/tags` | GET or HEAD, streamed WalGit JSON |
| `/api/git/<owner>/<repo>/resolve[/<revision>]` | GET or HEAD, streamed WalGit JSON |
| `/api/git/<owner>/<repo>/tree/<revision>[/<path>]` | GET or HEAD, streamed WalGit JSON |
| `/api/git/<owner>/<repo>/blob/<revision>/<path>` | GET or HEAD, streamed WalGit response |
| `/api/git/<owner>/<repo>/commits?ref=...` | GET or HEAD, streamed WalGit JSON |
| `/api/git/<owner>/<repo>/commit/<oid>` | GET or HEAD, streamed WalGit JSON |
| `/api/pull-requests/<event-id>/diff` | GET or HEAD, application-owned normalized comparison JSON |
| `/internal/walgit/events` | HMAC-authenticated POST of bounded ref-event batches |

Every Git route authenticates before resolving a repository. Smart HTTP uses
Buzz's reusable repository-root NIP-98 credential. Browser and PR-diff routes
use strict method and full-URL NIP-98. Repository reads require access to the
repository `buzz-channel`; writes additionally require `CanWrite`. PR diffs
require access to every derived repository and origin channel.

Private or hidden scope denial does not disclose the repository or PR. Invalid
routes return `404`; disallowed methods return JSON `405 method_not_allowed`.
Authentication failures return JSON `401 authentication_required` with the
existing `WWW-Authenticate` header. Git errors are JSON with `Cache-Control:
no-store`. Browser responses keep safe entity/cache headers, remove cookies and
internal headers, and force private caching. PR-diff responses use
`Cache-Control: private, no-store`.

## Repository classification and recovery

The canonical coordinate is `30617:<owner>:<d>`.

- Exact `project-task-anchor=true` announcements have no Git backend.
- Clone-less unmarked announcements are WalGit-hosted.
- A canonical relay clone locator is WalGit-hosted.
- Any other clone locator is external and is not proxied or provisioned.

WalGit-hosted repository names use the bounded ASCII contract in
`internal/gitforge`. Publishing the signed event records a durable intent and
does not wait for provisioning. Reconciliation is idempotent, generation
guarded, and retried with bounded backoff. Missing or non-ready intent returns
`503 repository_provisioning` and `Retry-After: 2`.

On startup, intents are rebuilt from visible current `30617` events. Tombstones
leave inactive operational rows and do not delete Git data. WalGit ref events
are deduplicated by repository, unsigned decimal WAL sequence, and ref; the
current-ref projection accepts only a higher sequence. The tagged recovery lane
proves a fresh WalGit cache can reconstruct repository data from object storage.

## Normalized v1 contract

The current `/api/git` response bytes are the v1 browser contract. The relay
does not deserialize and reserialize those WalGit responses. The
capability-based adapter must preserve those shapes and status codes during the
WalGit cutover. The PR diff response is owned by the relay and contains:

```text
pull_request, repository, lifecycle, base_source, target_branch,
base, head, merge_base, files[], patch
```

Each changed file contains `path`, optional `old_path`, `status`, `additions`,
`deletions`, and optional `binary`. Comparisons use full SHA-1 or SHA-256 IDs,
configured file/patch bounds, `413 diff_too_large`, `422 unrelated_histories`,
and `404 git_object_not_found`.

## Historical capability truth table

| Capability | WalGit baseline |
| --- | --- |
| provider-native clone URL | false |
| clone authentication | `nostr` |
| relay Smart HTTP read/write | true/true |
| repository browsing | true |
| immutable comparison | true |
| ref event ingestion | true |
| repository provisioning | true |
| repository import | false |
| provider issues/PRs/reviews | false |
| provider merge | false |
| outbound mirror | false |
| protected-ref compilation | false |
| automatic `kind:1619` publication | false |
| merge coordination/execution | false |
| CI dispatch | false |

These values describe the frozen `809484b2` baseline. The abstraction was
implemented without changing that route contract. WalGit now additionally
implements the optional repository importer and exact-commit source export used
by verified GitHub-to-WalGit migration and a disposable GitHub Actions mirror.
Those orchestration features live in the forge/Zooid layer rather than WalGit's
public routes.

The following values intentionally remain false for WalGit itself:

- provider issue/PR/review CRUD and provider merge;
- protected-ref policy compilation;
- automatic `kind:1619` publication;
- native merge coordination/execution; and
- CI execution (GitHub implements `CIBackend`; WalGit only supplies the exact
  source commit to its configured one-way CI mirror).

Current callers resolve every value through `internal/forge`; they never infer
functionality from `git_backend=walgit`.

## Refactor acceptance

The WalGit adapter cutover is accepted by the existing focused unit tests, full
Go suite, tagged PR/project tests, tagged WalGit recovery test, and local image
build without externally observable route changes. New provider work must keep
rerunning those lanes in addition to its own contract/Docker tests.

The live architecture and update checklist are in
[Capability-based forge architecture](../forge/capabilities.md); this file remains the historical
pre-refactor characterization artifact.
