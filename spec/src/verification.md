# Verification

Validation starts with the narrowest test for the changed behavior and expands
in proportion to the affected boundary.

| Area | Evidence |
| --- | --- |
| Specification | `just spec-check` builds the linked mdBook and validates local Markdown targets; the Go CI workflow installs mdBook 0.5.4 and rebuilds it. |
| Go unit and package behavior | Focused package tests, followed by `go test ./...`. |
| Buzz project and pull-request behavior | Relevant tagged project/PR Docker integration lane. |
| WalGit storage and recovery | Tagged WalGit object-store recovery lane and local WalGit image build. |
| GitHub integration | Focused HTTP coverage proves admin-only connection create, explicit update, revalidation, duplicate-installation conflict, and sanitized discovery; pull-request conversation and inline-review comment reaction snapshots map only GitHub's eight values, and relay reactions/deletions call the mapped GitHub endpoints. The deterministic fake GitHub App/API/Git/webhook Docker lane never uses live credentials. |
| Deployment topology | Compose configuration rendering and local relay, WalGit, and forge-worker image builds. |

Documentation-only changes do not alter a runtime contract, but must still run
`just spec-check` and the full Go suite. Tagged integrations and image builds
are required when their runtime, fixture, Compose, or documented executable
contract changes.

No validation command may print relay signing keys, database passwords,
provider credentials, service tokens, webhook secrets, or production
hostnames. Publishing an image and deploying a service remain explicit manual
gates after local verification.
