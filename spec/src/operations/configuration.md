# Operations and configuration

## Process environment

| Variable | Meaning | Default |
| --- | --- | --- |
| `DATABASE_URL` | PostgreSQL connection string. Required. | none |
| `PORT` | Shared HTTP and WebSocket listener port. | `3334` |
| `CONFIG` | Directory containing virtual-relay TOML files. | `./config` |
| `MEDIA` | Blossom media directory. | `./media` |
| `DB_MAX_OPEN_CONNS` | Maximum open database connections. | `20` |
| `DB_MAX_IDLE_CONNS` | Maximum idle database connections. | `5` |
| `DB_CONN_MAX_LIFETIME_SECS` | Connection maximum lifetime. | `300` |
| `PPROF_ADDR` | Optional private profiling listener. | disabled |
| `FORGE_WORKER_MODE` | `embedded`, `disabled`, or worker-only `only`. | `embedded` |
| `WALGIT_RELAY_TOKEN` | Runtime-only private WalGit service token. | none |
| `WALGIT_WEBHOOK_SECRET` | Runtime-only WalGit ref-event HMAC secret. | none |
| `LAPIS_AGENTS_ENABLED` | Enable Buzz agent protocol policy. | `true` |
| `LAPIS_AGENTS_ALLOW_OWNER_AUTH` | Let a relay member's signed NIP-OA credential grant connection-local NIP-AA admission to an agent. | `true` |
| `LAPIS_AGENTS_ENFORCE_CREDENTIAL_KINDS` | Enforce NIP-OA `kind=` conditions on owner-derived agent writes. | `true` |

The container entrypoint maps deployment-facing `LAPIS_*` variables into the
relay configuration and private sidecar environment. The complete WalGit,
GitHub App, Cloudflare R2, and local RustFS variable sets are in [Deployment and
local testing](deployment.md).

## Virtual-relay files

Each TOML file in `CONFIG` defines one virtual relay. The top-level fields are:

- `host`: hostname routed to this relay;
- `schema`: immutable PostgreSQL schema name using SQL-safe alphanumeric or
  underscore characters; and
- `secret`: Nostr private key used for the relay's NIP-11 identity and generated
  relay events.

Never commit or log real values for `secret`, database passwords, service
tokens, webhook secrets, provider credentials, or bridge keys.

The major sections are:

- `[info]`: NIP-11 name, icon, owner pubkey, and description;
- `[policy]`: `open`, `anonymous_read`, `public_join`, and optional signature
  stripping;
- `[groups]`: NIP-29 enablement, auto-join, creation, and private-group policy;
- `[groups.retention]`: default and per-group chat retention;
- `[agents]`: Buzz agent policy, owner-derived admission, and credential-kind
  enforcement;
- `[management]`: NIP-86 enablement and method allowlist;
- `[blossom]`: Blossom enablement;
- `[registry]`: optional Lapis plugin-release group and curator-role policy;
- `[roles.<name>]`: static pubkeys plus `can_invite` and `can_manage`; and
- `[git]`: public clone base, internal WalGit URL, reconcile interval, and diff
  limits;
- `[auth]`: optional application sessions, encrypted managed identities,
  passkeys, NIP-46, and provider configuration described in
  [Application authentication and signing](../auth.md).

The checked-in [.env.example](../../../.env.example) and entrypoint are the
canonical inventory of container variables. Operator-managed TOML files are
not silently rewritten. An older entrypoint-generated file may receive
backward-compatible additive sections during an upgrade.

## Minimal virtual relay

```toml
host = "relay.example"
schema = "lapis_relay"
secret = "<runtime-secret>"

[info]
name = "Lapis Relay"
pubkey = "<owner-pubkey>"

[policy]
open = true
anonymous_read = true
public_join = true

[groups]
enabled = true
auto_join = true
admin_create_only = true
private_admin_only = true
private_relay_admin_access = false

[agents]
enabled = true
allow_owner_auth = true
enforce_credential_kinds = true

[management]
enabled = true
methods = ["supportedmethods", "banpubkey", "allowpubkey"]

[blossom]
enabled = true

[registry]
enabled = false
release_group_id = ""
curator_role = "registry-curator"

[roles.admin]
pubkeys = ["<admin-pubkey>"]
can_manage = true
```

The placeholders are documentation only. Generate and inject real secrets at
deployment time.
