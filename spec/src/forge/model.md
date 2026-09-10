# Project forge model

The Lapis forge keeps signed Nostr collaboration, Git storage, and external
provider automation as separate authorities:

```text
Nostr relay              Git authority / content          provider automation
projects/tasks/PRs       WalGit, GitHub / WalGit replica  GitHub App + Actions
comments/lifecycle       clone+refs / tree+blob+diff      optional sync/mirror/CI
        \_____________________ Zooid NIP-29 policy _____________________/
```

The canonical identity is always `30617:<owner-pubkey>:<d>`. A repository's
operational integration independently selects Git authority, repository
content, collaboration, outbound Git replication, collaboration mirror, and CI
backends; it never rewrites the signed announcement. See
[Capability-based forge architecture](capabilities.md) for the interface and persistence
model.

## Repository classes and defaults

| Announcement | Legacy-default integration |
| --- | --- |
| exact `project-task-anchor=true` | no Git; relay collaboration |
| no `clone` | WalGit; relay collaboration |
| canonical relay clone URL | WalGit; relay collaboration |
| other clone locator | unmanaged provider-native clone; relay collaboration |

WalGit-hosted repository names use a bounded ASCII contract. Every repository
requires a non-empty `buzz-channel`. Replacing or deleting an announcement
changes desired operational state but does not destroy Git data. Once a relay
admin explicitly attaches an integration, later clone URLs do not silently
retarget it.

## Public surfaces

The unchanged data/event surfaces are:

- WebSocket `REQ` and `EVENT`;
- NIP-98 `POST /events`, `/query`, and `/count`;
- `/git/<owner>/<repo>.git` for backends with relay Smart HTTP;
- `/api/git/<owner>/<repo>/...` for normalized browser reads; and
- `/api/pull-requests/<event-id>/diff` for reduced PR comparison.

WalGit keeps Buzz-compatible NIP-98 Smart HTTP. GitHub clone/push are direct
provider traffic and `/git` returns `provider_git_required`. Both backends
translate into provider-neutral repository content results; Zooid preserves
the observable v1 `/api/git` shape so web clients can browse without a checkout
or local Git. Results can explicitly report truncation, continuation,
incomplete data, oversized objects, and capability limits.

The additive NIP-98 `/api/forge` routes expose capability/health discovery,
admin integration, durable provider commands, explicit import/migration, and
CI operations. Provider commands return `202`; their durable operation state
is read separately. [GitHub integration](github.md) documents
the complete route and credential contract.

Repository reads require NIP-29 access to `buzz-channel`; writes additionally
use the group's `CanWrite` rule. Public/private existence hiding follows the
existing group policy. PR reads and commands require the complete derived
repository-plus-origin scope intersection.

## State and failure semantics

Publishing a hosted `30617` succeeds after its signed event is stored even if
WalGit is unavailable. A generation-guarded durable intent is retried with
bounded backoff; public Git returns `503 repository_provisioning` until ready.

WalGit ref webhooks are HMAC authenticated and deduplicated by repository,
unsigned decimal WAL sequence, and ref. They are inputs for future native
automation and do not themselves grant signing authority.

GitHub commands, deliveries, mappings, migrations, and CI runs are operational
state. Workers lease them, retry transient failures, redact failure details,
and preserve restart recovery. Deterministic correlation markers close the
provider-success/local-crash gap without creating duplicate provider objects.

PR diffs first reduce the existing `1618/1619/163x` graph, then compare the
current immutable head through the selected content backend. For a
GitHub-authoritative WalGit replica, missing PR-only commits are fetched by
exact SHA and pinned privately before local comparison. An explicit merge base
is used directly; otherwise the target branch is resolved. Full SHA-1/SHA-256
IDs and configured file/patch bounds apply to every backend.

## Collaboration authority

Native relay objects remain normal signed events and stock Buzz reduction is
unchanged. The relay never signs for a user.

Provider-origin roots use a dedicated bridge identity and deterministic
provenance tags. The bridge may publish canonical provider-origin lifecycle.
For relay-origin roots, GitHub lifecycle and reviews are observational kind-1
notes (`t=external-state` or `t=external-review`), never native trusted state
or review decisions. GitHub mirror mode imports no unrelated roots.

When GitHub is the selected collaboration backend, signer-authored review
decisions and inline review comments are projected outward as native GitHub
review objects. The GitHub App remains the provider-side author and a visible
Lapis/Nostr signer footer preserves attribution. Correlated webhooks do not
replace or duplicate the authoritative signed source event.

A private Git replica remains independent of collaboration. GitHub can own
clone/push and collaboration while WalGit serves content, or collaboration can
remain relay-native. The GitHub clone locator stays signed in `30617`; users
cannot clone or push the private replica. See [Git repository replicas](replicas.md).

CI terminal results are bridge-signed `t=ci` activity bound to the exact
commit. They remain historical when a PR tip changes and do not authorize a
merge.

## Deliberate boundaries

WalGit is private to the relay and its S3-compatible object store is the
durable Git backing. Production uses Cloudflare R2 and local tests use RustFS;
GitHub is accessed only through its fixed public API. Provider credentials and
types remain inside the GitHub adapter. No client can supply a WalGit service
token, trusted principal header, arbitrary provider API host, or bridge secret.

Current unavailable behavior—including protected-ref compilation, automatic
`1619` publication, and the WalGit merge coordinator—is tracked in
[Deferred forge work](../roadmap/deferred-forge-work.md).
