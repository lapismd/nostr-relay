# WalGit integration

WalGit runs as a private sidecar. It is the canonical Git engine for hosted
repositories and may also be the private content replica for a
GitHub-authoritative repository. The relay is the only public policy boundary;
RustFS provides the initial local S3-compatible durable store and WalGit's disk
directory is a reconstructable cache.

```text
web / desktop / Git credential helper
                 |
        public Go relay
      NIP-98 + NIP-29 ACL
                 |
       private service token
                 |
              WalGit
                 |
          RustFS object store
```

Nostr remains authoritative for `30621` projects, `30617` repository identity,
`1621` tasks, `1618/1619` pull requests, comments, reviews and lifecycle.
WalGit is authoritative only for Git objects, refs, pushes, LFS and its WAL.
It is registered through the capability model described in
[FORGE_ABSTRACTION.md](FORGE_ABSTRACTION.md); the existing
`internal/gitforge` implementation is wrapped rather than rewritten.

## Deployment

The complete base-stack, GitHub App, secret-mount, and local-test runbook is
[FORGE_DEPLOYMENT.md](FORGE_DEPLOYMENT.md). This document records WalGit's
specific storage and protocol contract.

`docker-compose.yml` starts relay, PostgreSQL, WalGit, RustFS and an idempotent
bucket initializer. WalGit and helper images are digest-pinned. Its source is
pinned in `third_party/walgit/UPSTREAM_VERSION`; the image downloads and
checksums that source, applies the local patch stack, builds the web assets and
Rust binaries, and exposes only port 8080 on the Compose network.

Required local variables are:

```text
LAPIS_RELAY_SECRET
LAPIS_RELAY_OWNER_PUBKEY
LAPIS_RELAY_ADMIN_PUBKEYS
LAPIS_WALGIT_RELAY_TOKEN
LAPIS_WALGIT_WEBHOOK_SECRET
LAPIS_RUSTFS_ACCESS_KEY
LAPIS_RUSTFS_SECRET_KEY
```

Use independently generated values. Never reuse the relay signing key as a
service or object-store credential. Production deployments should keep WalGit
and object storage on private networking and replace the local RustFS choices
with normal secret injection, backup and lifecycle controls.

GitHub and its forge worker are optional. Their configuration, separate App
permissions, secret references, private WalGit replica, and one-way CI mirror
are documented in [GITHUB_INTEGRATION.md](GITHUB_INTEGRATION.md) and
[GIT_REPLICAS.md](GIT_REPLICAS.md).

The relay `[git]` configuration is:

```toml
[git]
enabled = true
public_url = "https://app.example"
backend_url = "http://walgit:8080"
reconcile_interval = "5s"
max_diff_bytes = 8388608
max_diff_files = 2000
```

`public_url` is the externally signed base URL and may include a deployment
path prefix. `backend_url` must be internal. Enabling Git also requires the
runtime-only `WALGIT_RELAY_TOKEN` and `WALGIT_WEBHOOK_SECRET` environment
variables. On upgrade, the container entrypoint adds this section only to an
older file bearing its auto-generated marker; operator-managed config files
are never rewritten.

## Repository lifecycle and identity

`30617:<pubkey>:<d>` maps to WalGit `<pubkey>/<d>`. A task anchor is explicitly
marked `project-task-anchor=true` and is never provisioned. An unmarked
clone-less announcement, or one containing the canonical relay clone URL, is a
hosted repository. Any other clone locator denotes an external repository.

After a hosted announcement is stored, the reconciler idempotently issues the
internal repository ensure request. Sidecar failure never rolls back the signed
event. Replacement and tombstone state are retained without automatically
destroying Git objects.

An explicit admin migration can import GitHub into WalGit through an ephemeral
bare Git directory, verify refs/connectivity, re-read the frozen source, and
wait for the owner to publish a canonical relay clone announcement before
activation. It never rewrites signed history. Destructive repository deletion
remains deferred.

## Authentication and authorization

Buzz's Git credential helper signs the canonical repository-root URL and Git
reuses that credential across discovery and pack requests. The gateway checks
kind `27235`, event ID/signature, empty content, a 60-second time window, a
method tag and the canonical root URL. Method, payload and replay are not bound
to each individual Git subrequest because doing so would reject the standard
Git request sequence produced by Buzz.

Browser repository and PR reads use strict NIP-98: the signed method and full
URL, including query, must match the request. All forge routes reject missing,
ambiguous or oversized authorization headers.

After authentication, the `30617` is resolved from the event store. Reads
require access to its `buzz-channel`; pushes additionally require the existing
group write policy. The PR diff path checks the complete derived scope
intersection. The public client cannot supply the service token or trusted
WalGit principal: the reverse proxy strips them and creates both internally.

## Git and browser APIs

The smart-HTTP proxy allowlists upload-pack, receive-pack, required LFS object
operations and read-only bundle routes. It streams request and response bodies;
WalGit retains responsibility for Git protocol, LFS and cache behavior.

The read-only browser routes preserve the observable WalGit v1 response shape
for:

```text
refs and refs/branches or refs/tags
resolve/<revision>[/<path>]
tree/<revision>[/<path>]
blob/<revision>/<path>
commits?ref=...
commit/<object-id>
```

No browser needs a checkout or local Git binary. The abstraction itself is
provider-neutral: the adapter decodes WalGit into `Ref`, `Tree`, `Blob`,
`Commit`, and `Diff` results, and Zooid serializes the compatible public JSON.
Provider-specific truncation, continuation, incomplete data, oversized blobs,
and capability limits are additive metadata rather than hidden differences.
Backend cache headers are retained but rewritten as private; absent cache
metadata defaults to `private, no-store`, preventing shared caches from leaking
repository content.

`GET /api/pull-requests/<event-id>/diff` is application-owned because it must
reduce Nostr PR state. It calls the patched internal WalGit
`/api/compare?base=...&head=...` endpoint and returns changed files, additions,
deletions, rename/binary state and unified patch. Large comparisons return 413;
unrelated histories return 422.

## Ref events

WalGit posts JSON arrays to `/internal/walgit/events` with
`X-Walgit-Signature: sha256=<HMAC>`. The handler caps request size and batch
count, rejects unknown fields, validates repository/ref/action/full object IDs,
and safely ignores events for unknown or non-hosted repositories. Decimal
uint64 WAL sequences are stored in `NUMERIC(20,0)` columns. Replayed deliveries
do not change the current-ref projection.

These events are durable integration inputs only. Replica-origin events are
marked from the exact `github-replica:<connection-id>` principal and do not
enter the native authoritative ref projection or trigger an outbound Git write,
PR revision, CI run, or activity. Native WalGit events may enqueue a separately
configured post-cutover Git replication target. CI is explicitly requested for
an immutable commit; a ref update alone grants no signing or execution
authority.

The remaining ref-policy, activity, automatic PR-revision, native merge, and
repository-retirement work is tracked with its acceptance boundaries in
[FORGE_DEFERRED_WORK.md](FORGE_DEFERRED_WORK.md).

## Failure and recovery

The signed repository event and desired-state projection survive a WalGit
outage. Reconciliation retries without logging credentials. WalGit reconstructs
an empty local cache from its object-store WAL, so the cache volume is not the
backup. Protect and back up the bucket instead.

The tagged recovery test pushes a real repository, verifies the complete read
surface, destroys the first WalGit container/cache, starts a fresh sidecar on
the same RustFS store, and repeats API and clone checks:

```sh
go test -tags walgit_integration ./zooid \
  -run '^TestWalGitObjectStoreRecovery$' -count=1 -v
```

## Updating WalGit

1. Choose and review an exact upstream commit.
2. Update the commit and archive SHA-256 in `UPSTREAM_VERSION`, the Containerfile
   build arguments and Compose.
3. Recreate the patch against a pristine archive; do not vendor upstream source.
4. Run `third_party/walgit/verify.sh <unpacked-source>`.
5. Run its focused Rust compare tests, the tagged recovery test, full Go tests,
   the GitHub Docker lane when importer/CI seams changed, Compose validation,
   and the local WalGit, relay, and forge-worker image builds.

The patch adds only the bounded arbitrary compare API required for PR review.
Import and CI mirroring use ordinary authenticated Git through an ephemeral
bare directory. All public repository operations continue through the upstream
WalGit interface; no policy or provider logic is moved into the sidecar.
