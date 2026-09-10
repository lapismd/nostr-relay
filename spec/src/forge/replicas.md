# Git repository replicas

Repository integration separates the host that decides Git state from the
backend that answers application content reads. For a GitHub repository with a
local replica:

```text
GitHub (authority and public clone/push)
   | verified webhook + periodic ref snapshot
   v
Go forge worker (credentials and orchestration)
   | ephemeral fetch, exact force/prune push
   v
WalGit (private content replica) -> /api/git and PR diff
```

The owner-signed `kind:30617` continues to advertise GitHub. The relay does not
publish its private WalGit URL and `/git/<owner>/<repo>.git` returns
`provider_git_required` because Smart HTTP follows the authority backend, not
the content backend. NIP-29 still gates every relay-hosted file, history, and
diff read.

## Configuration and lifecycle

An admin attaches the existing GitHub repository by stable provider ID:

```json
{
  "expected_announcement_id": "<current-30617-id>",
  "git_authority_backend": "github",
  "content_backend": "walgit",
  "collaboration_backend": "github",
  "connection_id": "<github-connection>",
  "external_repository_id": "123456",
  "replica": {
    "enabled": true,
    "direction": "authority-to-replica",
    "sync_branches": true,
    "sync_tags": true,
    "mode": "continuous",
    "read_policy": "fallback",
    "wait_milliseconds": 1500
  }
}
```

Omitted direction, branch/tag selection, mode, read policy, and wait use those
shown defaults. Initial provisioning snapshots `refs/heads/*` and
`refs/tags/*`, creates the internal WalGit repository, transfers through an
ephemeral bare Git directory, force/prunes the managed refs, verifies the
source did not change and the destination is exact, then marks the replica
`current`. Provider-specific and pull-request refs are not copied implicitly.

GitHub `push` webhooks mark the affected projection stale and enqueue a durable
job keyed by delivery ID. A job always re-snapshots GitHub, so redelivery and
out-of-order webhooks cannot roll a replica backwards. Force pushes, moved
tags, and deletion are ordinary exact-projection operations. Periodic
reconciliation repeats the complete comparison to repair missing webhooks and
worker crash gaps. The direction is never reversed while GitHub is authority.

Replica states are `provisioning`, `syncing`, `current`, `stale`, `failed`, or
`disabled`. The management-only health endpoint reports timestamps, sanitized
failure, and the count of refs whose authority and replica object IDs differ.

## Credentials and internal writes

The Go worker resolves only the configured `github.com` repository ID and
mints a repository-narrowed GitHub App token. The token is supplied to native
Git with a temporary askpass program and environment, never in a URL, Nostr
event, database row, WalGit configuration, or log. The temporary repository,
askpass program, and Git config are removed after each attempt.

WalGit writes use `github-replica:<connection-id>`. WalGit webhook rows persist
whether a change came from that replica principal. Replica-generated changes
do not enter the native authoritative ref projection and therefore cannot
create a provider write, PR update, CI run, or activity loop. Normal users have
no relay Smart HTTP path to a GitHub-authoritative replica.

## Content freshness and pull requests

Immutable full-SHA reads use WalGit immediately, even if the provider is
unavailable. If a requested SHA is missing, the worker fetches exactly that
object and pins it under WalGit's private `refs/lapis/pins/` namespace before
retrying. This is how PR commits not reachable from normal branches or tags
remain locally reviewable.

Mutable reads use the verified replica only while it is current. Policies are:

- `fallback` (default): wait for the configured bounded interval, then use the
  GitHub content adapter;
- `wait`: wait for the replica, otherwise return `replica_stale`; and
- `stale`: return the local result with `incomplete: ["stale-replica"]`.

Backend failures may fall back only under `fallback`. Missing data is never
invented. Result JSON remains provider-neutral and can carry `truncated`, an
opaque `continuation`, `incomplete`, `limit_exceeded`, and
`capability_limits`.

The PR diff route reduces the unchanged Buzz-compatible Nostr graph, prefers
its immutable merge-base or base snapshot over the live target branch, ensures
both baseline and current head exist in WalGit, and compares locally. Old tips
remain addressable by SHA.

## Authority migration and outbound Git replication

Migration first reconciles and reuses a current WalGit replica; it performs a
full import only when no verified replica is available. It also verifies all
managed refs plus every retained PR head/base/merge-base object, rechecks frozen GitHub refs,
and waits for the owner to publish the canonical relay clone URL. Activation
atomically selects WalGit for both authority and content and disables the
GitHub-to-WalGit worker. Nostr coordinates, projects, issues, PRs, comments,
reviews, and mappings are unchanged.

`git_replication_target_backend=github` is a separate capability from
`mirror_backend=github`: the former exact-mirrors WalGit heads/tags to GitHub;
the latter projects collaboration objects. Post-cutover Git replication uses a
durable worker, a Contents-write installation token, force/prune semantics,
and verification. It is accepted only with WalGit authority, so the two Git
directions cannot be enabled together.

## Recovery and tests

Deleting the WalGit replica loses no canonical Git state while GitHub remains
authority: provisioning can repeat from GitHub. Once WalGit is authority, its
configured S3-compatible object store is the durable source and normal WalGit
recovery applies. Production uses Cloudflare R2; the Docker test lanes use
RustFS as a deterministic local fixture.

The focused Go fixtures cover provisioning, ref scope, force/delete/tag
changes, delivery deduplication, out-of-order events, reconciliation,
freshness policies, missing-object pinning, migration reuse, and event loops.
The `github_integration` Docker lane combines a deterministic fake GitHub Git
and App/API service with real WalGit and RustFS; the `walgit_integration` lane
independently proves empty-cache object-store recovery.
