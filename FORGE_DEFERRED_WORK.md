# Deferred forge work

This ledger distinguishes unavailable workflow from landed foundations. It
supersedes the pre-abstraction ledger: provider-neutral Git content results,
independent Git authority/content backends, GitHub-to-WalGit private replicas,
provider collaboration, durable webhooks/outbox, verified authority cutover,
separate one-way outbound Git and collaboration projection, provider-neutral
CI, and exact-SHA GitHub Actions dispatch are now implemented.

## Landed foundations

The relay provides:

- Buzz-compatible signed Nostr projects, tasks, pull requests, updates,
  reviews, comments, and lifecycle reduction;
- capability-based WalGit/GitHub composition with provider-neutral content
  results and stable browser/PR-diff serialization;
- WalGit provisioning, Smart HTTP, LFS, bundles, browser reads, arbitrary
  comparison, durable ref events, object-store recovery, and repository import;
- GitHub App repository reads, provider-first issues/PRs/comments/state/merge,
  bridge-signed provider roots, strict relay-root authority, webhook recovery,
  reconciliation, and bounded initial import;
- continuous or on-demand private GitHub-to-WalGit replication, explicit
  verified authority cutover, and independently optional one-way outbound
  GitHub Git and collaboration projection; and
- exact-commit GitHub Actions, including a disposable private GitHub CI mirror
  for WalGit repositories and bridge-signed historical CI activity.

Operational support does not install a GitHub App, create CI mirror
repositories, publish a deployment, or grant production credentials. Those
remain manual operator gates.

## Pending native WalGit workflows

### Protected refs and policy synchronization

Repository writes still use the group's repository-level `CanWrite` decision.
The relay does not compile NIP-29 roles into WalGit per-ref rules, protect main
or release branches, or define a constrained merge-coordinator bypass.

A follow-up must define an explicit role-to-ref policy, reconcile only hard Git
rules to WalGit, and keep policy mutation behind an admin-authorized internal
operation. Acceptance needs ordinary branch pushes, protected target rejection,
coordinator access, idempotent reconciliation, replacement generations, and
restart recovery.

### Ref-driven activity and automatic PR revisions

WalGit ref deliveries and the monotonic current-ref projection are durable,
but no consumer publishes project activity or `kind:1619` automatically. A
push does not authorize the relay to sign as a PR author.

A follow-up must select an explicit repository-owner/integration signer or
require author signing. It must handle branch creation/update/deletion,
multiple matching PRs, closed/unrelated PRs, duplicate delivery, restart
checkpoints, and deterministic tie behavior. Optional `kind:30618`
repository-state publication remains a separate protocol decision.

### WalGit merge coordinator and execution

GitHub provider-first merge commands exist, but there is no native WalGit
`MergePolicy`, eligibility service, Git merge executor, or automatic confirmed
`kind:1631` publication.

A native coordinator must evaluate the current immutable PR tip, trusted
reviews, changes requested, required approvals, optional current-tip CI, and
the current target ref without a time-of-check/time-of-use gap. It must update
a protected target with a constrained service identity, wait for durable
WalGit ref confirmation, then publish one Buzz/NIP-34 merged status carrying
the full merge commit. A merge queue scheduler remains out of scope.

### Repository retirement

Replacement and tombstone state never destroys Git objects. Archive,
retention, and destructive deletion require a separate operator policy,
explicit admin workflow, recoverability rules, and object-store validation.

## Deferred provider coverage

The following are intentionally outside this release rather than partially
implemented:

- GitLab, Forgejo, GitHub Enterprise Server, or arbitrary GitHub API hosts;
- per-user provider OAuth or relayed GitHub clone credentials;
- title/body edit synchronization, external comment edit/deletion, assignee
  identity sync, provider subtasks, Projects, or milestones;
- simultaneous/bidirectional Git replication, reverse reconciliation while
  GitHub is authoritative, or importing a CI mirror back into WalGit;
- automatic GitHub repository/CI-mirror creation;
- new Nostr event kinds or a trusted-bridge reducer extension;
- a custom Git object database, upload-pack, receive-pack, LFS server, or
  compactor in the Go relay; and
- a CI runner implementation or full merge queue scheduler.

## Original definition-of-done delta

Only the original native WalGit automation outcomes remain open:

| Original item | Current status | Follow-up |
| --- | --- | --- |
| Protected branches reject ordinary direct updates | repository-level write ACL only | protected refs |
| Branch updates drive PR revision | refs durable; no automatic `1619` signer | ref-driven revisions |
| A coordinator updates a protected WalGit target | not implemented | WalGit merge coordinator |
| Confirmed WalGit merge publishes `1631` | manual signed state only | WalGit merge coordinator |

The earlier CI outcomes are complete: `CIBackend` is provider-neutral,
GitHub Actions runs against an exact immutable commit, and WalGit uses an
isolated one-way CI mirror when GitHub cannot fetch it directly. CI remains an
input only; merge policy is still deferred.
