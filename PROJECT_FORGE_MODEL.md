# Project forge model

The Lapis forge has two authorities with a strict process boundary:

```text
Nostr relay                         WalGit + object store
projects, tasks, PRs, reviews       Git objects and refs
comments, lifecycle, membership     branches, tags, pushes and LFS
```

WalGit is an internal sidecar. Public clients use the Go relay for both Nostr
and Git; they never receive the sidecar address or service credential.

## Repository classes

The canonical identity is always `30617:<owner-pubkey>:<d>` and maps directly
to WalGit `<owner-pubkey>/<d>`. No second repository-ID mapping exists.

| Announcement | Class | Behavior |
| --- | --- | --- |
| exact `project-task-anchor=true` | task anchor | logical NIP-34 issue namespace; never provision Git |
| no `clone` | hosted | lazily ensure WalGit repository |
| canonical relay clone URL in `clone` | hosted | lazily ensure WalGit repository |
| other `clone` locator | external | retain NIP-34 metadata; do not provision or proxy |

Hosted repository names additionally match WalGit's bounded ASCII contract.
Every hosted repository must have a non-empty `buzz-channel`. Replacing or
deleting its announcement changes durable desired state; Git data is not
destructively deleted by that projection.

## Public operations

The relay exposes only these forge surfaces:

| Route | Purpose | Authentication |
| --- | --- | --- |
| `/git/<owner>/<repo>[.git]/...` | Git smart HTTP, LFS and bundles | Buzz Git NIP-98 credential signed for the repository root |
| `/api/git/<owner>/<repo>/...` | refs, resolve, tree, blob, commits and commit | exact URL/method NIP-98 |
| `/api/pull-requests/<event-id>/diff` | reduced PR base-to-current-head comparison | exact URL/method NIP-98 and full PR scope |

Repository reads require NIP-29 access to the repository `buzz-channel`.
Writes additionally use the group's existing `CanWrite` rule. Public/private
existence hiding follows the existing group behavior. PR diffs require every
derived repository and origin channel, not merely the repository channel.

The Go gateway allowlists Git data-plane and read API routes. It does not expose
WalGit settings, policy mutation, maintenance, diagnostics or repository
deletion. It removes public `Authorization`, cookies and all public
`X-Walgit-*` headers, then adds its internal bearer credential and trusted
principal. Internal response headers are removed on the way back.

## State and failure semantics

Publishing a hosted `30617` succeeds after its signed event is stored even if
WalGit is unavailable. A durable desired-state row is reconciled with bounded
backoff; public Git returns `503 repository_provisioning` until it is ready.
Generation checks prevent a slow response for an old announcement from marking
a replacement ready.

WalGit ref webhooks are authenticated with HMAC, validated before storage and
deduplicated by `(owner, repo, WAL sequence, ref)`. A separate monotonic ref
projection is input for future activity, PR-revision and CI work; it is not a
Nostr event and does not alter PR authority.

PR diffs reduce the existing `1618/1619/163x` graph first. An explicit current
merge base is used directly; otherwise the target branch is resolved and
WalGit computes a deterministic merge base. Both comparison endpoints accept
only full SHA-1/SHA-256 object IDs and enforce configured file/patch bounds.

## Deferred capabilities

Protected-target policy compilation, merge coordination/execution, automatic
`1619` publication, CI providers/results, external repository import, and
`30618` publication remain deliberate follow-up slices. No GitHub or GitLab
issue/PR synchronization is introduced.
