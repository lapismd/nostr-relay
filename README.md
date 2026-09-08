# Lapis Relay

Lapis Relay is the Lapis-maintained NIP-29 relay image. It is based on the
[Unicity Relay](https://github.com/unicitynetwork/unicity-relay) fork of
[Zooid](https://github.com/coracle-social/zooid) and retains the complete
upstream history and MIT license. See [UPSTREAM.md](UPSTREAM.md) for the pinned
import and update procedure.

The first Lapis slice deliberately keeps the upstream protocol implementation
intact. Lapis defaults restrict group creation to configured relay admins while
anonymous readers can browse public groups and authenticated users can use
them.

## Buzz client compatibility

The Lapis image supports the core HTTP bridge used by Buzz desktop and mobile
clients alongside the standard Nostr WebSocket transport:

- `POST /query` queries one or more Nostr filters.
- `POST /events` submits a signed event through the normal relay policy and
  storage pipeline.
- `POST /count` counts unique events matching one or more filters.
- `GET /info` returns the same NIP-11 document as `GET /` with an
  `Accept: application/nostr+json` header.
- `GET|HEAD /media/{sha256_ext}` is an alias for the existing Blossom blob
  route. Uploads use the standard `PUT /upload` endpoint; the legacy Buzz
  spelling `PUT /media/upload` is an internal alias for the same handler.

The three POST routes require a fresh NIP-98 event whose `u`, `method`, and
`payload` tags match the request. Auth events are accepted within a 60-second
window and may be used only once per running relay process. Query results and
event writes go through the same membership and NIP-29 authorization hooks as
WebSocket traffic.

Buzz's Blossom tokens use unpadded Base64URL and authority-scoped BUD-11
`server` tags. The compatibility adapter verifies those tokens and passes their
authenticated identity into the existing Blossom read/write policy; upstream
Khatru remains unmodified.

The adapter lives in `internal/buzzcompat`; `zooid/buzz_compat.go` is its only
integration point with the upstream-derived relay. Keep Buzz-specific behavior
behind that boundary when updating the fork.

## Projects, tasks, and pull requests

The relay accepts Buzz NIP-MP projects (`kind:30621`) and repository-scoped
NIP-34 tasks and pull requests without hosting Git repositories. Project Task
Anchors are optional `kind:30617` logical namespaces with no `clone` or `web`
tag. Project/task group scope is derived from `buzz-channel` and enforced
through the normal NIP-29 policy without adding an `h` tag to signed events. A
pull request may add an origin `h`; both repository and origin scopes are
required for its full event graph.

See [PROJECT_TASK_MODEL.md](PROJECT_TASK_MODEL.md) for event shapes, status and
assignment/review reduction, dual comment compatibility, deletion behavior, and
non-goals. The current Buzz Desktop HTTP/event audit and upstream refresh
checklist are in
[docs/BUZZ_PROJECT_COMPATIBILITY.md](docs/BUZZ_PROJECT_COMPATIBILITY.md).

## Lapis container

Build the local image:

```sh
docker build --tag lapis-relay:local .
```

The image runs `/bin/lapis-relay`. `docker compose up --build` starts the relay
and PostgreSQL after the required relay secret, owner pubkey, and TOML-formatted
admin pubkey list are provided through the environment. Never commit those
values.

## Upstream description

This is a fork of [Zooid](https://github.com/coracle-social/zooid), a multi-tenant relay based on [Khatru](https://gitworkshop.dev/fiatjaf.com/nostrlib/tree/master/khatru) which implements a range of access controls. This fork is customized for use with [Unicity Sphere](https://github.com/unicitylabs/sphere) for NIP-29 group chat functionality.

## Unicity Fork Modifications

This fork includes the following changes from upstream Zooid:

### Open Policy for Public Groups

Modified `groups.go` to allow authenticated users to read messages from public groups with open policy, without requiring explicit group membership:

```go
// In CanRead function - allows public group access
if g.Config.Policy.Open && !HasTag(meta.Tags, "private") {
    return true
}
```

This enables the "browse and join" workflow where users can discover public groups, view their messages, and then join if interested.

Lapis also defaults `anonymous_read = true`. Unauthenticated clients may read
public, non-hidden group content plus public profile and emoji metadata. Private
groups, hidden groups, direct messages, invites, and application-specific state
remain filtered. Publishing and every state-changing action still require
NIP-42 authentication.

### Group Creation and Privacy Controls

Added configurable group creation policies and private group access control:

- `admin_create_only` — only relay admins can create groups
- `private_admin_only` — only relay admins can create private groups (public groups open to all)
- `private_relay_admin_access` — when `false`, relay admins cannot see or moderate private groups; only the group creator can moderate their own private group

### Per-Group Write Permissions (Write-Restricted Groups)

Added support for write-restricted groups (announcement channels). A group with the `write-restricted` metadata flag only allows designated writers, admins, and the group creator to post. Regular authenticated readers can read and publish NIP-25 reactions; they cannot publish messages, topics, or replies. Closed groups still require membership before reacting.

- Uses NIP-29 roles on put-user events (kind 9000) — the `writer` role designates who can post
- Only relay admins can create or set `write-restricted` on groups
- Roles are tracked in a separate in-memory cache alongside the membership cache
- The group members list (kind 39002) includes role information in p-tags
- Combine with `closed` for public announcement channels (anyone can join and read, only writers can post)

Create a write-restricted group via the [groupchat CLI](https://github.com/unicity-sphere/sphere-infra/tree/main/groupchat-cli):

```bash
node create-group.js "Announcements" "Official updates" --write-restricted --writer <pubkey>
node manage-writers.js add announcements <pubkey>
node manage-writers.js remove announcements <pubkey>
```

### Configuration for Sphere

The relay is configured with:
- `public_join = true` — allows anyone to join without invite
- `groups.enabled = true` — enables NIP-29 support
- `groups.auto_join = true` — members can join groups without approval
- Open policy for public group message access

## Original Zooid Documentation

---

## Architecture

A single zooid instance can run any number of "virtual" relays. The `config` directory can contain any number of configuration files, each of which represents a single virtual relay.

## Environment

Zooid supports a few environment variables, which configure shared resources like the web server or PostgreSQL database.

- `DATABASE_URL` - **required**. PostgreSQL connection string (e.g., `postgres://user:pass@host:5432/dbname?sslmode=verify-full`).
- `PORT` - the port the server will listen on for all requests. Defaults to `3334`.
- `CONFIG` - where to store relay configuration files. Defaults to `./config`.
- `MEDIA` - where to store blossom media files. Defaults to `./media`.
- `DB_MAX_OPEN_CONNS` - maximum open database connections. Defaults to `20`.
- `DB_MAX_IDLE_CONNS` - maximum idle database connections. Defaults to `5`.
- `DB_CONN_MAX_LIFETIME_SECS` - connection max lifetime in seconds. Defaults to `300`.
- `PPROF_ADDR` - if set (e.g. `127.0.0.1:6060`), serves `net/http/pprof` on a separate listener. **Bind to localhost** and reach it via SSH/port-forward — never expose pprof publicly.

## Configuration

Configuration files are written using [toml](https://toml.io). Top level configuration options are required:

- `host` - a hostname to serve this relay on.
- `schema` - a string that identifies this relay. This cannot be changed, and must be usable as a SQL identifier (alphanumeric and underscores only).
- `secret` - the nostr secret key of the relay. Will be used to populate the relay's NIP 11 `self` field and sign generated events.

### `[info]`

Contains information for populating the relay's `nip11` document.

- `name` - the name of your relay.
- `icon` - an icon for your relay.
- `pubkey` - the public key of the relay owner. Does not affect access controls.
- `description` - your relay's description.

### `[policy]`

Contains policy and access related configuration.

- `open` - whether authenticated users can use public groups without relay membership. Defaults to `false` for upstream config files; the Lapis container defaults it to `true`.
- `anonymous_read` - whether unauthenticated clients can read public, non-hidden group content and public profile/emoji metadata. Defaults to `true`; set it to `false` to require NIP-42 for reads.
- `public_join` - whether to allow non-members to join the relay without an invite code. Defaults to `false`.
- `strip_signatures` - whether to remove signatures when serving events to non-admins. This requires clients/users to trust the relay to properly authenticate signatures. Be cautious about using this; a malicious relay will be able to execute all kinds of attacks, including potentially serving events unrelated to a community use case.

### `[groups]`

Configures NIP 29 support.

- `enabled` - whether NIP 29 is enabled.
- `auto_join` - whether relay members can join groups without approval. Defaults to `false`.
- `admin_create_only` - only relay admins can create groups. Defaults to `true`.
- `private_admin_only` - only relay admins can create private groups. Defaults to `true`.
- `private_relay_admin_access` - relay admins can see and moderate private groups. When `false`, only the group creator can moderate their private group. Defaults to `false`.

Groups also support a `write-restricted` metadata flag (set in the group creation content JSON). When set, only members with the `writer` role, relay admins, and the group creator can post messages, topics, or replies. Authenticated readers may still publish NIP-25 kind 7 reactions; closed groups continue to require membership. The `writer` role is assigned via kind 9000 (put-user) events with `["p", "<pubkey>", "writer"]` tags. Only relay admins can create write-restricted groups or add the flag to existing groups.

#### `[groups.retention]`

Configures automatic deletion of old chat messages (kinds 9, 10) on a per-group basis. Membership, metadata, and admin events are never deleted. A background goroutine checks every minute and deletes expired messages in batches.

- `default` — default retention duration for all groups. Empty or omitted means unlimited (no deletion).
- `[groups.retention.groups]` — per-group overrides keyed by group ID (the NIP-29 `h` tag value). Takes precedence over `default`.

Duration format: integer + unit suffix — `s` (seconds), `m` (minutes), `h` (hours), `d` (days). Examples: `"30s"`, `"24h"`, `"7d"`. Validated at config load time.

```toml
[groups.retention]
default = ""              # unlimited (no deletion)

[groups.retention.groups]
"ephemeral-chat" = "1h"       # group ID → retention duration
"daily-standup" = "7d"
```

### `[management]`

Configures NIP 86 support.

- `enabled` - whether NIP 86 is enabled.
- `methods` - a list of [NIP 86](https://github.com/nostr-protocol/nips/blob/master/86.md) relay management methods enabled for this relay.

### `[blossom]`

Configures blossom support.

- `enabled` - whether blossom is enabled.

### `[roles]`

Defines roles that can be assigned to different users and attendant privileges. Each role is defined by a `[roles.{role_name}]` header and has the following options:

- `pubkeys` - a list of nostr pubkeys this role is assigned to.
- `can_invite` - a boolean indicating whether this role can invite new members to the relay by requesting a `kind 28935` claim. Defaults to `false`. See [access requests](https://github.com/nostr-protocol/nips/pull/1079) for more details.
- `can_manage` - a boolean indicating whether this role can use NIP 86 relay management and administer NIP 29 groups. Defaults to `false`.

A special `[roles.member]` heading may be used to configure policies for all relay users (that is, pubkeys assigned to other roles, or who have redeemed an invite code).

### Example

The below config file might be saved as `./config/my-relay.example.com` in order to route requests from `wss://my-relay.example.com` to this virtual relay.

```toml
host = "my-relay.example.com"
schema = "my_relay"
secret = "<hex private key>"

[info]
name = "My relay"
icon = "https://example.com/icon.png"
pubkey = "<hex public key>"
description = "A community relay for my friends"

[policy]
open = true
anonymous_read = true
public_join = true
strip_signatures = false

[groups]
enabled = true
auto_join = false

[groups.retention]
default = ""

[management]
enabled = true
methods = ["supportedmethods", "banpubkey", "allowpubkey"]

[blossom]
enabled = false

[roles.member]
can_invite = true

[roles.admin]
pubkeys = ["d9254d9898fd4728f7e2b32b87520221a50f6b8b97d935d7da2de8923988aa6d"]
can_manage = true
```

## Development

See `justfile` for defined commands.

## Deploying

Zooid requires a PostgreSQL 16+ database. It can be run using an OCI container:

```sh
podman run -it \
  -p 3334:3334 \
  -e DATABASE_URL="postgres://zooid:password@db-host:5432/zooid?sslmode=verify-full" \
  -v ./config:/app/config \
  -v ./media:/app/media \
  ghcr.io/coracle-social/zooid
```

## Running with Unicity Sphere

For local development with Sphere, start the PostgreSQL database and the relay:

```bash
# Start PostgreSQL
docker compose up -d postgres

# Run the relay (requires DATABASE_URL)
DATABASE_URL="postgres://zooid:dev@localhost:5432/zooid?sslmode=disable" just run
```

Or use Docker Compose with a full containerized setup:

```bash
cd /path/to/groupchat  # Contains docker-compose.yml and config/
docker compose up -d
```

This starts Zooid on `ws://localhost:3334` with the pre-configured `localhost` relay.

### Default Configuration

The `config/localhost` file provides a development configuration:

```toml
host = "localhost"
schema = "localhost"
secret = "<relay-private-key>"

[info]
name = "Localhost Relay"
description = "Local development NIP-29 relay for Sphere"

[policy]
public_join = true

[groups]
enabled = true
auto_join = true

[roles.member]
can_invite = true
```

### Verifying the Relay

Check relay status:
```bash
curl http://localhost:3334
```

View logs:
```bash
docker compose logs -f zooid
```

## Metrics

The relay exposes Prometheus metrics at the `/metrics` endpoint on the same port as the relay (default `3334`). No additional configuration is needed — metrics are always available.

```bash
curl http://localhost:3334/metrics
```

A background goroutine updates all metrics every 30 seconds. Cache-derived metrics (group counts, membership) are read from in-memory caches. `zooid_events_total` uses a Postgres `reltuples` estimate (instant, no table scan). `zooid_messages_total` uses an index-scanned COUNT query. Per-group member metrics exclude private and hidden groups.

### Available metrics

All metrics carry an `instance` label derived from the relay's `schema` config value.

| Metric | Type | Description |
|--------|------|-------------|
| `zooid_groups_total` | Gauge | Total number of groups |
| `zooid_groups_private` | Gauge | Number of private groups |
| `zooid_groups_hidden` | Gauge | Number of hidden groups |
| `zooid_groups_closed` | Gauge | Number of closed groups |
| `zooid_group_members` | Gauge | Members per group (labels: `instance`, `group`; capped at 1000 public groups) |
| `zooid_group_messages` | Gauge | Chat messages per group (labels: `instance`, `group`; public groups only) |
| `zooid_group_members_total` | Gauge | Distinct members across all groups |
| `zooid_groups_tracked` | Gauge | Number of groups reported in per-group metrics |
| `zooid_relay_members_total` | Gauge | Total relay members |
| `zooid_banned_pubkeys_total` | Gauge | Total banned pubkeys |
| `zooid_banned_events_total` | Gauge | Total banned events |
| `zooid_events_total` | Gauge | Estimated total events in database (via `reltuples`) |
| `zooid_messages_total` | Gauge | Total chat messages (kinds 9, 10) in database |
| `zooid_query_duration_seconds` | Histogram | Duration of database query execution and row scanning |
| `zooid_retention_deleted_total` | Counter | Total chat messages deleted by retention policy |
| `zooid_retention_run_duration_seconds` | Histogram | Duration of each retention cleanup run |

### Forwarding to Grafana Cloud with Alloy

The repo includes a [Grafana Alloy](https://grafana.com/docs/alloy/) config that scrapes the relay and forwards metrics to Grafana Cloud via `remote_write`. The setup lives in `docker-compose.metrics.yml` and `alloy/config.alloy`.

**1. Get your Grafana Cloud credentials** from your stack's Prometheus connection details (remote write URL, username, API token).

**2. Start the relay and Alloy together:**

```bash
export RELAY_SECRET="$(openssl rand -hex 32)"
export GRAFANA_REMOTE_WRITE_URL="https://prometheus-prod-XX-prod.grafana.net/api/prom/push"
export GRAFANA_USERNAME="123456"
export GRAFANA_API_TOKEN="glc_..."

docker compose -f docker-compose.yml -f docker-compose.metrics.yml up --build
```

This starts postgres, builds/runs the relay, and starts Alloy — all on the same Docker network. Alloy scrapes `relay:3334/metrics` every 15 seconds.

**3. To run Alloy standalone** (e.g. while running the relay on the host via `just run`), edit `alloy/config.alloy` to change `relay:3334` to `host.docker.internal:3334`, then:

```bash
docker compose -f docker-compose.yml -f docker-compose.metrics.yml up alloy
```

**4. Collecting metrics from the performance test:**

The integration perf test can expose a metrics HTTP server for Alloy to scrape during the run:

```bash
# Terminal 1: start Alloy
docker compose -f docker-compose.yml -f docker-compose.metrics.yml up alloy

# Terminal 2: run perf test with metrics server on :9090, hold open 90s after completion
METRICS_PORT=9090 METRICS_WAIT=90s go test -v -tags=integration -run TestIntegration_QueryPerformance -timeout 30m ./zooid/
```

Alloy's `perftest` scrape target collects from `host.docker.internal:9090`.

### Prometheus scrape config

If using Prometheus directly instead of Alloy:

```yaml
scrape_configs:
  - job_name: zooid
    scrape_interval: 30s
    static_configs:
      - targets: ['localhost:3334']
```

If you don't configure a Prometheus scraper, the relay runs normally with negligible overhead — metrics are stored in fixed-size in-memory structs and overwritten each cycle.

## CI/CD

### GitHub Actions Workflows

This fork includes automated CI/CD:

**Build and Push (`docker-build.yml`):**
- Triggers on push to main/master or tags
- Builds Docker image
- Pushes to GitHub Container Registry (`ghcr.io/unicitylabs/zooid`)
- Tags: `latest`, `sha-<commit>`, semver tags

**Deploy to AWS (`deploy-aws.yml`):**
- Triggers after successful build or manual dispatch
- Forces new ECS deployment
- Waits for service stability

### Required Secrets

Add these to GitHub repository secrets for AWS deployment:
- `AWS_ACCESS_KEY_ID`
- `AWS_SECRET_ACCESS_KEY`

### Manual Deployment

```bash
# Force new deployment
aws ecs update-service \
    --cluster sphere-zooid-relay-cluster \
    --service sphere-zooid-relay-zooid-relay \
    --force-new-deployment \
    --region me-central-1
```

## AWS Infrastructure

See `/Users/pavelg/work/unicity/sphere-infra/aws/` for CloudFormation templates and deployment scripts.

### Environment Variables

When running in AWS ECS, these environment variables configure the relay:

| Variable | Description |
|----------|-------------|
| `DATABASE_URL` | **Required.** PostgreSQL connection string (e.g., `postgres://user:pass@host:5432/zooid?sslmode=verify-full`) |
| `RELAY_HOST` | Domain name (e.g., `sphere-relay.unicity.network`) |
| `RELAY_SECRET` | Nostr private key (64-char hex) |
| `RELAY_NAME` | Display name |
| `RELAY_DESCRIPTION` | Description |
| `ADMIN_PUBKEYS` | Admin pubkeys (quoted, comma-separated) |
| `POLICY_ANONYMOUS_READ` | Allow unauthenticated read-only access to public community data (default: `true`) |
| `GROUPS_ADMIN_CREATE_ONLY` | Only admins can create groups (default: `true`) |
| `GROUPS_PRIVATE_ADMIN_ONLY` | Only admins can create private groups (default: `true`) |
| `GROUPS_PRIVATE_RELAY_ADMIN_ACCESS` | Relay admins can see/moderate private groups (default: `false`) |
| `DB_MAX_OPEN_CONNS` | Max open DB connections (default: `20`) |
| `DB_MAX_IDLE_CONNS` | Max idle DB connections (default: `5`) |
| `DB_CONN_MAX_LIFETIME_SECS` | Connection max lifetime in seconds (default: `300`) |
| `PPROF_ADDR` | If set (e.g. `127.0.0.1:6060`), serves `net/http/pprof` on a separate listener. Bind to localhost only — never expose publicly. |
