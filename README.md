# Lapis Relay

Lapis Relay is the Lapis-maintained NIP-29 relay image. It is based on the
[Unicity Relay](https://github.com/unicitynetwork/unicity-relay) fork of
[Zooid](https://github.com/coracle-social/zooid) and retains the upstream MIT
license and history.

The canonical functional documentation is the linked mdBook under
[`spec/src`](spec/src/index.md). It covers:

- relay architecture, transports, NIP-29 policy, and configuration;
- Google, GitHub, and Nostr application auth, managed keys, passkeys, and NIP-46 signing;
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

## Continuous integration and public image

Pull requests and `main` changes run formatting, unit tests, specification
checks, upstream patch-queue verification, the tagged Docker integration
lanes, a candidate `relay` image build, and a Trivy scan that rejects
fixable high or critical runtime vulnerabilities.

Trusted `main` pushes, `v*.*.*` tags, and manual workflow runs repeat those
gates before publishing:

```text
ghcr.io/<repository-owner>/lapis-relay
```

Each publication includes `linux/amd64` and `linux/arm64` manifests, an
SBOM, BuildKit provenance, and GitHub build provenance. The default branch
produces `edge`; every build produces a `sha-<40-character-commit>` tag;
version releases additionally produce `<major>.<minor>.<patch>`,
`<major>.<minor>`, and `latest`.

The image is the Dockerfile `relay` target. The forge-worker binary is in
the same image; override `LAPIS_PROCESS_BIN=/bin/lapis-forge-worker` when a
separate worker process is required.

Production deployments should select a version and pin the resolved
manifest digest. GitHub keeps container-package visibility separate from
repository visibility. The publish workflow finishes only when the image is
anonymously readable. If a new package inherits private visibility, a
package administrator must make `lapis-relay` public and rerun the failed
workflow.

Deploying a running service remains a manual operator gate.
