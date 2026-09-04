# Upstream provenance

Lapis Relay is maintained from:

- Repository: `https://github.com/unicitynetwork/unicity-relay.git`
- Imported commit: `6c89860dbb1fd2d5cb628cf3978c78cb2dbb7999`
- Imported on: 2026-09-01
- License: MIT, retained in `LICENSE`

The full upstream Git history is retained. The upstream source still uses the
historical `zooid` Go module and package names; Lapis-specific naming is kept at
the repository, executable, image, configuration, and documentation boundary
so upstream updates remain reviewable.

Lapis's Buzz HTTP transport is isolated in `internal/buzzcompat` and connected
through `zooid/buzz_compat.go`. The upstream-derived `zooid/instance.go` should
retain only the adapter registration and calls to the shared authenticated
pubkey accessor. During an upstream update, preserve that narrow seam and avoid
moving Buzz route behavior into the core relay handlers.

When updating, fetch the upstream remote with Jujutsu, review the complete
upstream range, rebase or merge it into a dedicated update change, and rerun
the unit, integration, and container acceptance lanes before committing.
