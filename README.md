# Lapis Relay

Lapis Relay is the Lapis-maintained NIP-29 relay image. It is based on the
[Unicity Relay](https://github.com/unicitynetwork/unicity-relay) fork of
[Zooid](https://github.com/coracle-social/zooid) and retains the upstream MIT
license and history.

The canonical functional documentation is the linked mdBook under
[`spec/src`](spec/src/index.md). It covers:

- relay architecture, transports, NIP-29 policy, and configuration;
- Buzz-compatible projects, tasks, pull requests, HTTP, and Blossom behavior;
- capability-based WalGit and GitHub forge integrations;
- clean-boot data population, Cloudflare R2 deployment, local RustFS testing,
  observability, verification, and deferred work; and
- upstream maintenance and historical compatibility audits.

Use [the specification summary](spec/src/SUMMARY.md) as the complete index.
The fork provenance, upstream pin, and local patch map remain in
[UPSTREAM.md](UPSTREAM.md).

## Quick start

Create an ignored `.env` from [.env.example](.env.example), replace every
required secret placeholder, and start the local stack:

```sh
cp .env.example .env
docker compose up --build
```

This starts PostgreSQL, the relay, private WalGit, and RustFS. RustFS is the
local S3-compatible object-store fixture; production WalGit storage is designed
for a private Cloudflare R2 bucket through provider-neutral `LAPIS_S3_*`
variables. A clean boot creates infrastructure and relay-owned state only;
application data is published later as signed Nostr events.

Read [deployment and local testing](spec/src/operations/deployment.md) before
configuring WalGit, GitHub, external workers, or R2.

## Development

```sh
just test
just spec-check
```

Build the documentation with `just spec-build` or serve it locally with
`just spec-serve`. Generated HTML under `spec/book` is disposable and is not
committed.

Build the relay and forge-worker images locally with:

```sh
docker build --target relay --tag lapis-relay:local .
docker build --target forge-worker --tag lapis-forge-worker:local .
```

Container publication and deployment are manual gates.
