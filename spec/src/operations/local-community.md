# Unified local Community

`community-local` owns the persistent `lapis-local-dev` cluster: credentials,
Compose, tunnel routes, image builds, and the `nostr-acp` rebuild. This
repository keeps the relay image, `scripts/local-container-entrypoint.sh`,
`docker-entrypoint.sh`, and `cmd/local-admin`. Disposable Docker tests use
separate Compose projects and volumes.

Operators run `community-local init`, `community-local build`, and
`community-local up` from the sibling checkout. The runbook lives in
`community-local/spec/src/operations.md`.
