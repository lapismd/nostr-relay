# GitHub integration

The GitHub adapter supports `github.com` only. Its REST base is fixed to
`https://api.github.com` and requests pin API version `2026-03-10`; an
integration record cannot supply a host or API URL. Clone and push remain
provider-native and use the user's GitHub credentials outside this relay.

GitHub integration is disabled by default:

```toml
[git]
enabled = true
public_url = "https://relay.example"

[github]
enabled = true
credential_dir = "/run/secrets"
worker_interval = "2s"
reconcile_interval = "5m"
max_webhook_bytes = 4194304
```

The equivalent container variables are `GITHUB_ENABLED`,
`GITHUB_CREDENTIAL_DIR`, `GITHUB_WORKER_INTERVAL`,
`GITHUB_RECONCILE_INTERVAL`, and `GITHUB_MAX_WEBHOOK_BYTES`. Compose exposes
the enable/interval/limit values with `LAPIS_` names and fixes the in-container
credential directory to `/run/secrets`.

For an end-to-end deployment sequence, secret-file mapping, and local Docker
commands, see [Forge deployment and local testing](../operations/deployment.md).

## GitHub App and credentials

Use separate least-privilege GitHub App installations when source
collaboration and CI mirrors need different access:

| Connection | Repository permissions |
| --- | --- |
| source/collaboration | Metadata read, Contents read, Issues read/write, Pull requests read/write |
| CI mirror | Metadata read, Contents write, Actions write |

Subscribe only to `installation`, `installation_repositories`, `repository`,
`push`, `issues`, `issue_comment`, `pull_request`, `pull_request_review`,
`pull_request_review_comment`, and `workflow_run` events. Configure the webhook
URL as `https://<relay>/webhooks/github` and use a distinct high-entropy secret.

PostgreSQL stores references, installation/repository IDs, and the bridge
pubkey, never private keys, webhook secrets, bridge secrets, or installation
tokens. A credential reference is either `env:NAME` or `file:name`; file
references are constrained below `github.credential_dir` (normally
`/run/secrets`). Installation tokens stay in memory, are refreshed early, and
are requested for one immutable repository ID with only the permissions needed
by that operation.
GitHub installation tokens are treated as opaque values and refreshed before
their [one-hour expiry](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app).

Register a connection with relay-admin NIP-98 authentication:

```json
POST /api/forge/connections/github
{
  "app_id": "12345",
  "installation_id": 67890,
  "credential_ref": "file:github-app.pem",
  "webhook_secret_ref": "file:github-webhook",
  "bridge_credential_ref": "file:github-bridge-secret",
  "bridge_pubkey": "<matching-hex-pubkey>"
}
```

`GET /api/forge/connections/github` lists configured connections without
credential references or secrets. `GET
/api/forge/connections/github/<id>/repositories` lists repositories available
to one installation. Connection registration and discovery are relay-admin-only.

## Repository attachment

Attachment requires an existing owner-signed `30617` with `buzz-channel` and
GitHub `clone` and `web` tags matching the provider repository. It also
requires a registered installation with access to the stable GitHub repository
ID. The relay never edits or republishes the announcement.

```json
PUT /api/forge/repositories/<owner>/<repo>/integration
{
  "expected_announcement_id": "<current-30617-event-id>",
  "git_authority_backend": "github",
  "content_backend": "github",
  "collaboration_backend": "github",
  "connection_id": "<connection-id>",
  "external_repository_id": "123456",
  "ci_backend": "github",
  "ci_connection_id": "<connection-id>",
  "ci_external_repository_id": "123456",
  "ci_workflow": "ci.yml"
}
```

To keep GitHub as public clone/push authority while serving file, history, and
diff reads from a private local copy, select WalGit as content and enable the
replica:

```json
{
  "expected_announcement_id": "<current-30617-event-id>",
  "git_authority_backend": "github",
  "content_backend": "walgit",
  "collaboration_backend": "github",
  "connection_id": "<connection-id>",
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

This does not change the owner-signed GitHub clone/web locators and does not
enable relay Smart HTTP for users. See [Git repository replicas](replicas.md).

For GitHub source, CI must target that same repository. For WalGit source, the
CI fields instead identify a separately pre-provisioned private disposable
mirror. `mirror_backend=github` enables one-way collaboration projection; it
does not import unrelated provider roots.

`GET .../capabilities` requires repository read access. `GET .../health` and
attachment require relay management access. GitHub renames update operational
metadata by stable repository ID and report stale signed clone/web locators as
degraded health. Installation removal disables provider access and outbound
writes without deleting Nostr history.

## Collaboration commands

The application command routes are:

```text
POST /api/forge/repositories/<owner>/<repo>/issues
POST /api/forge/repositories/<owner>/<repo>/pull-requests
POST /api/forge/work-items/<event-id>/comments
POST /api/forge/work-items/<event-id>/state
POST /api/forge/pull-requests/<event-id>/merge
GET  /api/forge/operations/<operation-id>
POST /api/forge/operations/<operation-id>/retry
```

Commands require NIP-29 write access and return `202`. Merge also requires the
repository owner or relay admin and a full expected current head SHA. The
durable outbox retries provider outages and recovers uncertain creates through
an invisible deterministic correlation marker. Webhook echoes acknowledge
mapped commands instead of creating duplicate Nostr events. Manual operation
retry is relay-admin-only. Retry scheduling honors GitHub `Retry-After` and
rate-limit reset metadata as recommended by the
[REST rate-limit guidance](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api).

Provider-origin issues (`1621`), PR roots/updates (`1618`/`1619`), and lifecycle
(`1630`-`1633`) are bridge-signed with deterministic provenance tags including
`external-provider`, stable ID/number, URL, and external author identity.
Relay-origin roots remain Buzz-strict and Nostr-authoritative. Provider state
for them is a kind-1 `t=external-state` observation. GitHub reviews are
kind-1 `t=external-review` observations with external reviewer, decision, URL,
and reviewed `c`; they never impersonate a Nostr reviewer.

Initial import is explicit with `POST .../import`. It is bounded to open issues
and PRs, an update window of 90 days, at most 1,000 roots, and at most 500
comments per root; truncation is surfaced in the operation result. Periodic
conditional reconciliation overlaps its cursor and deduplicates through stable
mappings.

Webhook handlers cap the raw body, verify `X-Hub-Signature-256` before JSON
parsing, deduplicate `X-GitHub-Delivery`, persist the delivery, and immediately
return `202`. A leased worker performs projection asynchronously, following
GitHub's [signature verification](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries)
and [timely delivery handling](https://docs.github.com/en/webhooks/using-webhooks/handling-webhook-deliveries)
guidance.

## GitHub-to-WalGit migration and Git replication

Migration is an explicit relay-admin operation:

1. `POST .../migration` with the current announcement ID and
   `acknowledge_provider_write_freeze=true`.
2. The worker reconciles and reuses a verified current WalGit replica when one
   exists. Otherwise it imports through an ephemeral bare Git directory
   (including LFS).
3. It verifies managed refs, object connectivity, and open PR head/merge-base
   objects, then re-reads GitHub refs.
4. Any provider ref change aborts the migration.
5. The repository owner publishes a replacement `30617` that explicitly names
   the canonical relay clone URL.
6. `POST .../migration/activate` with that new announcement ID atomically
   selects WalGit for authority and content and disables incoming replication.

Collaboration can temporarily remain on GitHub, and GitHub can remain a
one-way Git target. `git_replication_target_backend=github` exact-projects
WalGit heads/tags after cutover; `mirror_backend=github` is distinct and
projects collaboration objects. Existing provider-origin roots keep their
bridge signer and mappings; no Nostr event ID or history is rewritten. The two
Git directions cannot be active simultaneously.

## GitHub Actions

Start CI with a full immutable commit:

```json
POST /api/forge/repositories/<owner>/<repo>/ci
{
  "commit": "<40-or-64-character-object-id>",
  "pull_request_event_id": "<optional-1618-id>",
  "inputs": {"suite": "unit"}
}
```

The adapter resolves the exact commit, creates
`refs/heads/lapis-ci/<operation-id>`, dispatches the configured
`workflow_dispatch`, correlates and verifies the returned run's head SHA, and
deletes the temporary ref after terminal completion. Cancellation is
`POST /api/forge/operations/<operation-id>/cancel`. The design uses a temporary
ref because GitHub's [workflow dispatch API](https://docs.github.com/en/rest/actions/workflows)
requires a ref.

For WalGit, the worker fetches the exact commit into an ephemeral bare
directory and pushes only that commit to the configured private GitHub CI
mirror before dispatch. Credentials cross an askpass/file-descriptor boundary
and do not appear in URLs or logs. Nothing is mirrored back into WalGit.

Terminal provider state is operationally durable and is also a bridge-signed
kind-1 `t=ci` activity carrying repository `a`, optional PR `e`, exact `c`,
status, provider run ID, and URL. Results for an old PR tip remain historical;
CI does not implement merge coordination.

## Provider limits and testing

GitHub capability responses disclose the adapter limits: compare can include
at most 300 changed files, a directory listing at most 1,000 entries, and the
contents API does not support files over 100 MB. These limits follow GitHub's
[compare](https://docs.github.com/en/rest/commits/commits) and
[contents](https://docs.github.com/en/rest/repos/contents) APIs. Truncation is
explicit; the adapter does not pretend complete WalGit equivalence.

The deterministic Docker lane uses a local fake GitHub App/API/Git service,
real WalGit and RustFS, and no live credentials:

```sh
go test -tags github_integration ./zooid \
  -run '^TestGitHubForgeDockerLane$' -count=1
```

It exercises attachment, initial Git replication into WalGit, authenticated
local browsing, durable collaboration, webhook echo suppression, CI, and
restart resolution. See [Deferred forge work](../roadmap/deferred-forge-work.md) for
the deliberately unsupported provider and WalGit workflows.

The fake service is test-only and injected without weakening the production
adapter's fixed `github.com` boundary. The complete local test and optional
real-App Compose workflows are in
[Forge deployment and local testing](../operations/deployment.md#test-locally-without-github-credentials).
