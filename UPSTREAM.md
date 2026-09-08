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

Project/task compatibility follows the same rule. No project/task changes live
under `internal/khatru`. The local patch map is:

- `internal/projecttask`: wire validation, Project Task Anchor templates,
  deterministic task reduction, and storage-independent scope resolution.
- `zooid/project_tasks.go`: the only relay/store adapter for derived NIP-29
  scope, durable scope sidecars, and NIP-09 address projection.
- `zooid/instance.go`: callback wiring only for write checks, query/broadcast
  visibility, anonymous visibility, and post-save scope persistence.
- `zooid/events.go` plus migrations `003` and `004`: the exact
  `buzz-channel` index extension and durable derived-scope table.
- `internal/buzzcompat`: remains generic HTTP transport. It contains no project
  or task routes.

The audited Buzz contract and refresh checklist live in
`docs/BUZZ_PROJECT_COMPATIBILITY.md`. Update its pinned Buzz commit and contract
fixtures before adapting behavior to a newer Desktop release.

When updating, fetch the upstream remote with Jujutsu, review the complete
upstream range, rebase or merge it into a dedicated update change, and rerun
the unit, integration, and container acceptance lanes before committing.
