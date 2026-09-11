# Verification

Validation starts with the narrowest test for the changed behavior and expands
in proportion to the affected boundary.

| Area | Evidence |
| --- | --- |
| Specification | `just spec-check` builds the linked mdBook and validates local Markdown targets; the Go CI workflow installs mdBook 0.5.4 and rebuilds it. |
| Member snapshot ordering | `TestGroupStore_MembersSnapshotAdvancesTimestamp` verifies same-second and backwards-clock replacement; the Community Docker agent contract covers discovery, Follow, channel membership and encrypted owner-only observer capture. |
| Agent personas | Protocol and Zooid tests cover slug/content validation, deterministic replacement ordering, owner and exact-linked-agent reads, sibling and stranger denial, safe counts, search exclusion, link/profile revocation, deletion, and restart reconstruction. Community LC-NOSTR-023 covers explicit name inheritance, persona-first presentation, durable owner writes, prompt-only edits, and non-destructive unlinking. |
| Go unit and package behavior | Focused package tests, followed by `go test ./...`. |
| Buzz project and pull-request behavior | Relevant tagged project/PR Docker integration lane. |
| WalGit storage and recovery | Tagged WalGit object-store recovery lane and local WalGit image build. |
| GitHub integration | Focused HTTP coverage proves admin-only connection create, explicit update, revalidation, duplicate-installation conflict, and sanitized discovery; signed review decisions and inline anchors call native GitHub review endpoints with visible signer attribution, durable correlation, mapping, and echo suppression; pull-request lifecycle and review-request webhooks retain merged state and timeline provenance; provider roots persist an immutable base SHA and merged diffs use the provider-neutral Git comparison path, with GitHub pull files limited to legacy fallback; pull-request conversation and inline-review comment reaction snapshots map only GitHub's eight values, and relay reactions/deletions call the mapped GitHub endpoints. The deterministic fake GitHub App/API/Git/webhook Docker lane never uses live credentials. |
| Deployment topology | Compose configuration rendering and local relay, WalGit, and forge-worker image builds. |

Documentation-only changes do not alter a runtime contract, but must still run
`just spec-check` and the full Go suite. Tagged integrations and image builds
are required when their runtime, fixture, Compose, or documented executable
contract changes.

No validation command may print relay signing keys, database passwords,
provider credentials, service tokens, webhook secrets, or production
hostnames. Publishing an image and deploying a service remain explicit manual
gates after local verification.
