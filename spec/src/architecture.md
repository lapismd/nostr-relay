# Architecture

A Lapis Relay process can serve multiple virtual relays. Each file in the
configured `CONFIG` directory defines a hostname, immutable PostgreSQL schema,
relay signing key, NIP-11 metadata, policy, groups, management methods, Blossom
support, and static roles for one virtual relay.

```text
Nostr and Buzz clients
        |
WebSocket + HTTP + Git Smart HTTP
        |
Lapis Relay (identity, validation, NIP-29 policy)
        |                    |
PostgreSQL event store       forge capability resolver
                             |                 |
                        WalGit sidecar     GitHub App
                             |
                   S3-compatible object store
                  RustFS local / Cloudflare R2 production
```

## Ownership boundaries

- `internal/khatru` is the upstream-derived generic relay transport and is not
  the home for Lapis-specific policy.
- Zooid owns persistence, virtual-relay configuration, NIP-29 authorization,
  operational migrations, and HTTP orchestration.
- `internal/buzzcompat` adapts Buzz HTTP and Blossom spellings to the normal
  validation, storage, and policy pipeline.
- `internal/projecttask` validates and reduces project, task, pull-request,
  review, status, scope, and deletion event graphs.
- `internal/forge` defines provider-neutral capabilities and content results.
- `internal/gitforge` adapts WalGit; `internal/githubforge` contains GitHub API
  and credential translation. Provider SDK objects never cross those adapters.

PostgreSQL stores signed events and durable operational state. Project and
repository coordinates are stable Nostr identifiers. Forge integration rows,
outbox jobs, webhook deliveries, replicas, and CI runs can be retried or
reconciled without rewriting signed history.

WalGit is never exposed directly. The relay authenticates clients and creates
its internal service identity only after NIP-29 authorization. WalGit's local
disk is a reconstructable cache; its S3-compatible bucket is durable Git
storage. RustFS exists only as the local S3 fixture. Production is designed for
a private Cloudflare R2 bucket through provider-neutral `LAPIS_S3_*`
configuration.

GitHub clone and push remain provider-native and use the user's GitHub
credentials. NIP-29 protects only the relay's application, browser, diff, and
collaboration surfaces; it cannot grant or restrict direct GitHub traffic.
GitHub repository reads pass through a bounded process-local cache after relay
authorization and connection validation. Cache keys use stable provider
repository identity, concurrent misses are coalesced, and provider writes or
verified webhooks invalidate the affected repository without changing signed
Nostr authority.

The optional managed-agent bunker provides narrowly scoped custody for explicit Community-created agents and remains independent of application authentication. See [Relay-managed agent bunker](managed-agent-bunker.md).

See [Relay surfaces](relay/surfaces.md), [NIP-29 groups and
policy](relay/groups-and-policy.md), [Plugin registry
policy](protocol/plugin-registry.md), and the [forge model](forge/model.md) for
the complete contracts.
