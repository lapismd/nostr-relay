# Capability-based forge architecture

The forge layer composes repository capabilities without changing the Nostr
wire model. `kind:30617` remains the repository identity; projects, tasks,
pull requests, comments, reviews, and lifecycle events retain their existing
Buzz-compatible kinds and tags. NIP-29 remains the authorization boundary for
every relay-hosted surface.

```text
signed Nostr events             operational integration
30617 coordinate     +     Git / collaboration / mirror / CI backends
        |                                  |
        +---------- Zooid policy ----------+
                         |
        provider-neutral content results
                         |
               stable relay API encoding
```

`internal/forge` owns normalized types, small optional interfaces, capability
resolution, and stable provider-neutral errors. `internal/gitforge` remains
the storage-independent WalGit client and is exposed through an adapter.
`internal/githubforge` contains all GitHub SDK/REST translation. Zooid owns
persistence, NIP-29 checks, Nostr authority, workers, and HTTP orchestration.
No forge behavior lives in `internal/khatru`.

## Capability composition

`GitAuthorityBackend` owns clone/default-branch/ref authority.
`RepositoryContentBackend` answers resolve, tree, blob, history, commit, and
diff operations. Their values are independent: GitHub can remain clone/push
authority while a private WalGit replica serves application reads.
Provisioning, import, Smart HTTP, collaboration commands, collaboration
mirroring, outbound Git replication, provider events, and CI are separate
optional interfaces. A backend name does not imply an optional capability:
callers use the resolved capability document.

| Composition | Git authority | Content | Collaboration | Git replication target | CI |
| --- | --- | --- | --- | --- | --- |
| Project Task Anchor | none | none | relay | none | none |
| external legacy repository | unmanaged | unmanaged | relay | none | none |
| hosted repository | WalGit | WalGit | relay | optional GitHub | optional GitHub Actions |
| GitHub without replica | GitHub | GitHub | relay or GitHub | none | optional GitHub Actions |
| GitHub with replica | GitHub | WalGit | relay or GitHub | none | optional GitHub Actions |

Capabilities distinguish clone availability and `clone.auth` (`nostr` or
`provider`), relay Smart HTTP read/write, browsing, comparison, ref events,
provision/import, issues, PRs, reviews, merge, mirror, CI, provider limits,
and possible truncation. Known unavailable WalGit behavior is reported as
`false`; it is never inferred from future plans.

Operational `repository_integrations` rows select each backend independently.
Existing announcements are backfilled without changing signed events:

- hosted -> `authority=walgit`, `content=walgit`, `collaboration=relay`;
- external -> `authority=unmanaged`, `content=unmanaged`, `collaboration=relay`;
- task anchor -> `authority=none`, `content=none`, `collaboration=relay`.

These rows use `configuration_source=legacy-default` and continue tracking the
signed announcement classification. An admin attachment switches the row to
`explicit`; later clone tags cannot silently retarget it. Updates use the
current announcement ID as an optimistic-concurrency guard.

## Stable API contract

The existing WebSocket, `/events`, `/query`, `/count`, `/git`, `/api/git`, and
PR-diff routes retain their request, response, error, cache, and ACL behavior.
The abstraction is not WalGit's JSON schema. `internal/forge` defines
provider-neutral `Revision`, `Ref`, `Tree`, `Blob`, `Commit`, and `Diff`
results. WalGit and GitHub translate into those types, and Zooid serializes
them so existing WalGit responses remain compatible. Provider objects and
provider SDK types never escape an adapter.

Every content result can explicitly report `truncated`, an opaque
`continuation`, incomplete portions, a structured `limit_exceeded`, and
`capability_limits`. This prevents a GitHub result capped at 300 changed files
or 1,000 tree entries from looking equivalent to a complete local result.

`/git/<owner>/<repo>.git` is only available when the selected authority backend
implements relay Smart HTTP. GitHub-authoritative repositories return
`provider_git_required`; their capability document supplies the GitHub clone
URL and `clone.auth=provider`, even when WalGit is the content backend.

The additive `/api/forge` routes are NIP-98 authenticated. Capabilities require
repository read access; provider commands require repository write access.
Connection, attachment, health, import, migration, and mirror administration
require `Config.CanManage`. Merge requires `CanMaintain` on the repository channel and the expected
current head. Hosted WalGit repositories with `collaboration=relay` advertise
`capabilities.merge=true` plus the relay-owned default-branch protection
document. `GET`/`PUT /api/forge/repositories/<owner>/<repo>/protection`
stores extra patterns, required-approval count, and admin bypass in relay
state so deleting a Git file cannot disable the live default. The capability
document advertises the live default as `protected_ref`, compiled extras as
`protected_patterns`, `required_approvals`, and `admin_bypass`. Announcement
`protected-ref` tags remain additive. GitHub-authoritative repositories keep
clone/push/protection on GitHub and say so in that document. `PUT` requires
`CanMaintain` and is rejected when GitHub is authoritative.

Provider commands are durable and return `202` plus an operation ID. A caller
can poll `/api/forge/operations/<operation-id>`. An `Idempotency-Key` is scoped
to the actor, method, and path; when absent, the NIP-98 event ID supplies the
key. Correlation markers let retries recover objects created before a timeout
without duplicating them.

## Authority and access

Native relay collaboration is signed-event-first. A relay collaboration
adapter may submit an already-signed event through the existing validation and
storage path, and the relay never signs as a user. When GitHub is the selected
collaboration backend, the adapter may project the signed event into the
corresponding provider operation without replacing the source event.

GitHub-origin roots are signed by a dedicated configured bridge identity.
That identity signs later canonical status for those roots. Relay-origin roots
remain Nostr-authoritative: GitHub lifecycle changes are observable kind-1
activity with `t=external-state`, not trusted `163x` state. External reviews
remain attributed kind-1 `t=external-review` notes and never gain native
`approval` or `changes-requested` labels.

Signer-authored pull-request replies with native review labels project as
GitHub reviews, while inline anchors project as immediate GitHub review
comments. GitHub App authorship and visible Nostr signer attribution remain
distinct.

Repository and PR-origin channel scope still comes from the signed event graph.
Backend selection cannot widen NIP-29 access. GitHub clone and push are direct
provider traffic using the user's GitHub credentials, so the relay cannot make
a public GitHub repository private or grant direct GitHub access.

## Persistence and workers

Migrations `008`-`012` add connection, integration, mapping, webhook delivery,
outbox, reconciliation, migration, CI-run, and mirror-ref state. Migration
`013` splits authority from content and adds incoming replica state/ref/jobs,
object pins, replica-origin markers, and separate outbound Git replication
state/jobs. Existing WalGit intent/ref tables remain intact. Signed Nostr
events stay authoritative; operational tables are retry/recovery state and can
be rebuilt where specified.

Workers lease durable rows, use bounded exponential backoff, retain sanitized
terminal failures, and honor provider retry/rate-limit timing. Set
`FORGE_WORKER_MODE` to:

- `embedded` (default): the relay processes forge jobs;
- `disabled`: the relay serves APIs but does not process forge jobs;
- `only`: run workers without the HTTP listener via the forge-worker image.

When using the Compose `external-forge-worker` profile, set
`LAPIS_FORGE_WORKER_MODE=disabled` for the relay; the worker container is
already fixed to `only`.

See [Git repository replicas](replicas.md) for GitHub-authoritative WalGit read
replicas, freshness policy, PR object pinning, event-loop prevention, and the
authority cutover path.

## Update checklist

When adding a provider or updating WalGit/GitHub behavior:

1. Characterize the existing normalized route and error behavior first.
2. Implement only the smallest applicable interfaces and explicit capabilities.
3. Keep provider types and credentials inside the adapter.
4. Preserve signed-event authority and NIP-29 scope in Zooid.
5. Add additive migrations and prove restart/idempotency behavior.
6. Test mixed backend compositions, not only a provider in isolation.
7. Run focused contract tests, `go test ./...`, relevant tagged Docker lanes,
   Compose validation, and local image builds.
8. Record the local patch seam in `UPSTREAM.md`; do not modify
   `internal/khatru`.
