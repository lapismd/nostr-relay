# Deferred forge work

This is the follow-up ledger for the capabilities deliberately left out of the
initial WalGit forge slice. It maps back to the original phased plan so a later
change does not mistake an available foundation for a finished workflow.

## Landed foundation

The relay already provides:

- deterministic `30617:<owner>:<d>` mapping and durable, idempotent hosted
  repository provisioning;
- NIP-98/NIP-29 protected Git smart HTTP, LFS, bundles, and browser read APIs;
- Buzz-compatible Nostr project, task, pull-request, review, comment, and
  lifecycle storage and reduction;
- bounded arbitrary base-to-head PR comparison through the private WalGit
  extension;
- authenticated, deduplicated WalGit ref-event storage and a monotonic current
  ref projection; and
- a Compose deployment and recovery lane proving an empty WalGit disk cache can
  reconstruct repository data from the object-store WAL.

The current ref projection is only an integration input. Nothing currently
turns it into activity, a PR revision, CI state, a merge, or a Nostr repository
state event.

## Planned follow-up slices

### Protected refs and policy synchronization

Original phases: 11-12, 38-39, and the protected-ref cases in 43.

Repository writes currently pass the group's coarse `CanWrite` check. The relay
does not yet compile NIP-29 roles into WalGit per-ref policy, protect main or
release branches, or define a merge-coordinator bypass principal.

The follow-up must define a configurable role-to-push policy, reconcile only
hard Git rules into WalGit, and expose any policy mutation through an explicit
admin-authorized Go operation rather than the public proxy. Acceptance must
cover ordinary branch pushes, protected-target rejection, coordinator access,
idempotent policy reconciliation, and restart behavior.

### External repository import and repository retirement

Original phases: 7, 38, 41, and the lifecycle cases in 42.

External `clone` announcements are classified and retained, but there is no
admin import/cutover workflow. A later implementation should ask WalGit to
import, verify refs and object reachability, and only then have the repository
owner publish the replacement `kind:30617` with the canonical relay clone URL.
A temporary upstream-follow mode may be used for cutover, but bidirectional or
permanent mirroring is not part of the design.

Replacing or deleting an announcement currently marks desired state inactive
without destroying Git data. Archive, retention, and destructive deletion need
an explicit operator policy and admin-authorized workflow before any object is
removed.

### Ref-driven activity and pull-request revisions

Original phases: 20-22, 40, and 45-46.

The durable event and current-ref tables are ready for a restart-safe consumer.
The consumer still needs to correlate repository branches with open PRs and,
when desired, publish project activity or a `kind:1619` update. Automatic
publication must preserve the existing actor rules: a ref update is not by
itself authority to sign for a PR author. The design must select an explicitly
trusted repository-owner/integration signer or require the author to sign.

Acceptance must cover branch creation/update/deletion, unrelated and closed
PRs, multiple matching PRs, duplicate delivery, restart checkpoints, and
deterministic processing. `kind:30618` repository-state publication remains a
separate opt-in protocol decision.

### Merge coordinator and execution

Original phases: 23-26 and 48.

There is no `MergePolicy`, merge eligibility service, Git merge executor, or
automatic merged-status publication. The follow-up needs to evaluate the
current immutable PR tip, reviews, changes requested, required approvals,
optional CI, and the current target ref without a time-of-check/time-of-use
gap. Merge/rebase/fast-forward work must use supported Git/WalGit operations,
never direct WAL mutation.

The coordinator should update a protected target with a constrained service
identity, wait for the durable WalGit ref confirmation, and only then publish a
Buzz/NIP-34 `kind:1631` carrying the full `merge-commit`. Duplicate confirmation
must not duplicate lifecycle state. A full merge queue scheduler remains out of
scope.

### Provider-neutral CI

Original phases: 27-33 and the CI cases in 48.

No `CIProvider`, runner credential flow, CI dispatch, or signed result event is
implemented. The future interface should dispatch against the canonical relay
clone URL and an exact immutable commit, grant a short-lived read-only identity,
and bind results to repository, PR, and commit. Results for an older PR tip must
be historical rather than satisfying current merge policy.

The Nostr event convention and signer authority for CI results must be specified
before implementation. A disposable one-way `WalGit -> CI forge` mirror is an
adapter option only when a provider cannot clone WalGit directly; it must never
become Git or collaboration authority. Build runners themselves remain outside
this repository.

## Original definition-of-done delta

The original definition of done has six outcomes that remain open. They are
covered by the slices above:

| Original item | Current status | Owning follow-up |
| --- | --- | --- |
| 6. Protected branches reject normal direct updates | Coarse repository write authorization exists; per-ref protection does not | Protected refs and policy synchronization |
| 11. Branch updates drive PR revision and CI workflows | Ref changes are durable and deduplicated, but have no consumer | Ref-driven revisions and provider-neutral CI |
| 12. A merge coordinator updates a protected target | Not implemented | Merge coordinator and execution |
| 13. Confirmed Git merge publishes `kind:1631` | Manual signed statuses are supported; automatic confirmed publication is not | Merge coordinator and execution |
| 17. CI uses a provider-neutral interface | Not implemented | Provider-neutral CI |
| 18. CI runs against canonical WalGit and an exact SHA | Required by the design, but no CI dispatch exists yet | Provider-neutral CI |

All other original outcomes are either implemented by the landed foundation or
are explicitly categorized below as non-goals rather than unfinished relay
behavior.

## Deliberate non-goals

These items from the original plan are not pending implementation unless the
architecture is explicitly reconsidered:

- GitHub/GitLab issue, pull-request, comment, review, or project-board sync;
- bidirectional Git mirroring or importing Git state back from a CI mirror;
- a custom Git object database, bare-repository cache, upload-pack,
  receive-pack, LFS implementation, or compactor in the Go relay;
- exposing raw WalGit settings, policy, maintenance, diagnostics, or repository
  deletion APIs to public clients;
- per-user external-forge OAuth; and
- a full merge queue scheduler or CI build-runner implementation.

WalGit remains the Git authority, the Nostr relay remains the collaboration
authority, and external CI remains optional execution infrastructure.
