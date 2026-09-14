# Forge deployment and local testing

This is the operator runbook for the relay, WalGit, and the optional GitHub
App integration. The checked-in Compose stack is a reproducible local/reference
deployment backed by RustFS. The production storage target is Cloudflare R2
through its S3-compatible API. Production must additionally provide TLS, a
production PostgreSQL service, durable backups, and a secret manager.

## Choose a repository topology

| Repository mode | Git authority | Content reads | Public clone/push |
| --- | --- | --- | --- |
| Native hosted repository | WalGit | WalGit | relay `/git/...` |
| GitHub without replica | GitHub | GitHub | GitHub |
| GitHub with private replica | GitHub | WalGit | GitHub |

Collaboration is configured independently as `relay` or `github`. A private
WalGit replica is never advertised as a clone URL and cannot be pushed by an
application user. See [Git repository replicas](../forge/replicas.md) for freshness and
authority-cutover semantics.

## Prepare the base deployment

The local stack contains:

- the public Go relay;
- PostgreSQL for signed events and operational forge state;
- private WalGit;
- private RustFS object storage; and
- an idempotent RustFS bucket initializer.

Only the relay publishes a host port. Keep PostgreSQL, WalGit, RustFS, and any
external forge worker on private networking in production.

Copy [.env.example](../../../.env.example) to an ignored `.env` and replace every
placeholder. The relay secret and the GitHub bridge secret are Nostr secret
keys; their configured hexadecimal public keys must be derived from those exact
keys. Generate the WalGit service token, webhook secret, and object-store
credentials independently. Never reuse a signing key as a service credential.

Validate and start the reference stack:

```sh
docker compose config --quiet
docker compose build relay walgit
docker compose up -d postgres rustfs init-local-walgit-bucket walgit relay
docker compose ps
curl --fail http://127.0.0.1:8281/health
```

`LAPIS_GIT_PUBLIC_URL` must be the exact external HTTPS origin used by clients
to sign NIP-98 requests. Put a TLS reverse proxy in front of the relay for a
real deployment. Do not publish WalGit or RustFS ports.

The generated relay configuration is persisted in the `relay-config` volume.
Environment variables initialize a new generated file; they do not overwrite
an existing `[git]` or `[github]` section. When enabling GitHub on an existing
deployment, update the persisted/operator-managed configuration and restart,
or start a new Compose project with a fresh configuration volume.

For production, also replace the Compose development PostgreSQL password,
back up PostgreSQL and the R2 bucket, test restore procedures, and pin all
externally supplied secret/config paths through the deployment platform.
WalGit's cache volume is reconstructable; the configured object store is the
durable Git source when WalGit is authoritative.

## Enable application authentication

Application auth is independent of relay NIP-42 enforcement and is disabled by
default. Create a 32-byte master wrapping key, encode it as hexadecimal or
base64, and place it in a deployment secret file. Keep that file outside the
repository and back it up separately from PostgreSQL. Losing it makes managed
Nostr keys, passkeys, provider tokens, and NIP-46 client keys unrecoverable.

Add the base overlay plus either or both provider overlays:

```sh
docker compose -f docker-compose.yml \
  -f docker-compose.auth.yml \
  -f docker-compose.auth-github.yml \
  -f docker-compose.auth-google.yml \
  config --quiet
```

Set `LAPIS_AUTH_PUBLIC_URL` to the relay's browser-facing HTTPS origin. Set
`LAPIS_AUTH_ALLOWED_BROWSER_ORIGINS` to a TOML array of exact Community HTTPS
origins, for example `'["https://community.example"]'`. Production deployments
that support the native Lapis Notes desktop app MUST also allow the configured
desktop renderer origin. If the renderer is served from Deno Desktop's dynamic
loopback port, use the bounded loopback wildcard, for example
`'["https://community.example","http://127.0.0.1:*"]'`, and set
`LAPIS_AGENT_BUNKER_ALLOWED_BROWSER_ORIGINS` the same way when managed-agent
provisioning is enabled. Set `LAPIS_AUTH_WEBAUTHN_RP_ID` to the Community host
or a shared parent domain and `LAPIS_AUTH_WEBAUTHN_ORIGINS` to the exact
Community origins that may create or assert passkeys. Do not put loopback
wildcards in WebAuthn origins. Local sibling development uses
`community.localhost` and `'["http://community.localhost:5173"]'`. Register
these provider callbacks:

```text
https://<relay>/api/auth/callback/github
https://<relay>/api/auth/callback/google
```

The GitHub OAuth client must belong to the same GitHub App whose installation
is used by the repository integration. Enable expiring user access tokens so
the relay receives refresh tokens. The Google client is a web OIDC client with
the relay callback in its authorized redirect URI list.

Mount the master key through `docker-compose.auth.yml`. Mount provider secrets
through their corresponding overlays. If an external forge worker is enabled,
give it the same auth master key and GitHub OAuth secret so it can decrypt and
refresh the user credential selected by the durable outbox operation.

The endpoint and transfer contract are documented in [Application
authentication and signing](../auth.md). Community supplies the browser
account UI; native Desktop OAuth, passkey, keychain, and remote-signer wiring
remain consumer work.

## Use Cloudflare R2 in production

The deployment-facing object-store variables are provider-neutral. WalGit can
therefore use Cloudflare R2 without presenting the storage provider as AWS or
RustFS:

```dotenv
LAPIS_S3_ENDPOINT=https://<cloudflare-account-id>.r2.cloudflarestorage.com
LAPIS_S3_REGION=auto
LAPIS_S3_BUCKET=lapis-walgit-production
LAPIS_S3_PREFIX=
LAPIS_S3_FORCE_PATH_STYLE=true
LAPIS_S3_ACCESS_KEY_ID=<r2-access-key-id>
LAPIS_S3_SECRET_ACCESS_KEY=<r2-secret-access-key>
```

Create the bucket before starting WalGit and issue an R2 S3 API token with
Object Read & Write access restricted to that bucket. Keep the bucket private;
application users access repository content through the relay, never directly
through R2. Store both credential values in the deployment secret manager, not
in a committed `.env` file.

R2 uses an account endpoint and the `auto` S3 region. If the bucket has a
jurisdictional restriction, use its jurisdiction-specific endpoint, for
example `https://<account-id>.eu.r2.cloudflarestorage.com`. Cloudflare's
[S3 setup](https://developers.cloudflare.com/r2/get-started/s3/),
[API compatibility](https://developers.cloudflare.com/r2/api/s3/api/), and
[data-location](https://developers.cloudflare.com/r2/reference/data-location/)
documentation are the source of truth for endpoint and region behavior.

The optional [docker-compose.r2.yml](../../../docker-compose.r2.yml) overlay removes
the local RustFS dependency and bucket initializer while retaining the same
WalGit image:

```sh
docker compose -f docker-compose.yml -f docker-compose.r2.yml config --quiet
docker compose -f docker-compose.yml -f docker-compose.r2.yml up --build -d
```

When GitHub is enabled as well, compose the independent overlays together:

```sh
docker compose -f docker-compose.yml \
  -f docker-compose.r2.yml -f docker-compose.github.yml \
  up --build -d
```

This overlay demonstrates the production storage topology; the deployment
platform should still provide production PostgreSQL, TLS, backups, and secret
injection. Local bucket creation uses the official RustFS `rc` client. No AWS
CLI image or `AWS_*` credential variable is part of either topology.

## Configure a GitHub App

Register and install a private GitHub App on only the repositories it needs.
GitHub Apps begin with no permissions, and Git-over-HTTP using an installation
token requires the Contents permission. Use the smallest applicable set:

| Use | Repository permissions |
| --- | --- |
| Repository reads and private WalGit replica | Metadata read, Contents read |
| Provider collaboration | Metadata read, Contents write for merge, Issues read/write, Pull requests read/write |
| Outbound Git target | Metadata read, Contents write |
| GitHub Actions | Metadata read, Contents write, Actions write |

Enable the webhook and set its URL to:

```text
https://<relay-public-host>/webhooks/github
```

Set content type to JSON and use a separate high-entropy webhook secret.
Subscribe only to installation, installation repositories, repository, push,
issues, issue comments, pull requests, pull-request reviews, pull-request
review comments, and workflow runs. GitHub's current setup guidance is in its
[App registration](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app),
[permission](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app),
and [webhook](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/using-webhooks-with-github-apps)
documentation.

Keep these three values in files outside the repository:

| Compose secret | Contents |
| --- | --- |
| `github_app_private_key` | GitHub App RSA private-key PEM |
| `github_webhook_secret` | exact secret configured in the App webhook |
| `github_bridge_secret` | 64-character hexadecimal Nostr secret key |

Write the webhook and bridge values without a trailing newline. The App private
key remains a normal PEM file. Prefer the deployment platform's secret-writing
mechanism; for a local fixture, use `printf %s` rather than `echo` for the two
single-line secrets.

The optional [docker-compose.github.yml](../../../docker-compose.github.yml) override
mounts those files read-only below `/run/secrets` and enables the GitHub
adapter. Set the three `LAPIS_GITHUB_*_FILE` variables in `.env`, then start a
fresh local project:

```sh
docker compose -p lapis-github-local \
  -f docker-compose.yml -f docker-compose.github.yml \
  up --build -d
```

For an external worker, set `LAPIS_FORGE_WORKER_MODE=disabled` for the relay
and include both the profile and override:

```sh
docker compose -p lapis-github-local \
  -f docker-compose.yml -f docker-compose.github.yml \
  --profile external-forge-worker up --build -d
```

The override mounts the same secrets into the relay and worker: the relay
validates connection registration and webhooks, while the worker needs them
for provider commands and replication.

## Register and attach the integration

All `/api/forge` administration calls require a fresh relay-admin NIP-98
signature. Use an admin client that signs the exact method, URL, and request
payload; the JSON examples below intentionally omit an `Authorization` header.

1. Publish the owner-signed `kind:30617` repository announcement with its
   `buzz-channel`, GitHub `clone`, and GitHub `web` tags.
2. Create the GitHub connection using the App ID, installation ID, bridge
   public key, and the mounted secret names:

   ```json
   POST /api/forge/connections/github
   {
     "app_id": "12345",
     "installation_id": 67890,
     "credential_ref": "file:github_app_private_key",
     "webhook_secret_ref": "file:github_webhook_secret",
     "bridge_credential_ref": "file:github_bridge_secret",
     "bridge_pubkey": "<public-key-derived-from-bridge-secret>"
   }
   ```

   Repeating registration for the same installation returns the existing
   connection as `409 github_connection_exists`; update it deliberately with
   `PUT /api/forge/connections/github/<connection-id>`. After rotating a
   mounted credential, call `POST
   /api/forge/connections/github/<connection-id>/revalidate` and confirm that
   the connection list reports `status=active` before continuing.

3. List the installation repositories with
   `GET /api/forge/connections/github/<connection-id>/repositories` and retain
   the immutable GitHub repository ID.
4. Attach the `30617` coordinate with the current announcement ID. For a
   private WalGit content replica, use the payload in
   [Git repository replicas](../forge/replicas.md#configuration-and-lifecycle).
5. Read the management-only health endpoint until the replica is `current`:

   ```text
   GET /api/forge/repositories/<owner>/<repo>/health
   ```

6. Verify an authorized NIP-29 member can use `/api/git/.../refs` and a user
   without group access cannot. `/git/...` must return
   `provider_git_required` while GitHub remains Git authority.

Installation access tokens remain in memory. Only credential references,
installation IDs, repository IDs, and the bridge public key are stored in
PostgreSQL.

## Test locally without GitHub credentials

Yes. The deterministic integration lane uses Docker but never talks to live
GitHub or consumes real credentials:

```sh
go test -tags github_integration ./zooid \
  -run '^TestGitHubForgeDockerLane$' -count=1 -v
```

It builds a fake GitHub App/API/Git service and starts real PostgreSQL, WalGit,
and RustFS containers. It then verifies connection registration, GitHub as
clone/ref authority, initial Git transfer into WalGit, authenticated local file
browsing, collaboration echo suppression, exact-SHA CI, and restart
resolution. Testcontainers removes the temporary containers.

This lane intentionally injects the fake provider transport in the test. The
production adapter stays fixed to `github.com`; there is no environment switch
that could redirect production credentials to an arbitrary API host.

Run the independent WalGit recovery lane as well:

```sh
go test -tags walgit_integration ./zooid \
  -run '^TestWalGitObjectStoreRecovery$' -count=1 -v
```

That lane pushes a real repository, destroys the first WalGit cache/container,
starts a fresh sidecar against the same RustFS data, and repeats browser and
clone checks.

To exercise a real GitHub App locally, use a sandbox repository, the GitHub
Compose override, and an HTTPS webhook tunnel that preserves the raw request
body and signature headers. Point the App webhook at the tunnel's
`/webhooks/github` URL. Do not use production repositories, keys, or webhook
secrets for this test.

## Operational checks

- `docker compose ps` shows PostgreSQL, RustFS, WalGit, and the relay healthy.
- The capability endpoint reports `git_authority_backend=github`,
  `content_backend=walgit`, `clone.auth=provider`, and `local_replica=true`.
- The health endpoint reports `replica.state=current` and `behind_refs=0`.
- GitHub push deliveries move the replica through `stale`/`syncing` back to
  `current`; periodic reconciliation repairs missed deliveries.
- Relay and worker logs contain sanitized failure codes, never installation
  tokens, private keys, bridge secrets, or WalGit service credentials.
- Backups cover PostgreSQL and Cloudflare R2 in production, and restore tests
  include WalGit recovery from an empty cache. The local recovery lane uses
  RustFS as its deterministic S3-compatible fixture.

Upgrade checks are listed in [Capability-based forge architecture](../forge/capabilities.md) and
[WalGit integration](../forge/walgit.md#updating-walgit).
