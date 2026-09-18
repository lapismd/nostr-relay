# Projects, tasks, and pull requests

This relay stores ordinary signed Nostr events. Project/task support is a local
policy and indexing extension around NIP-34, NIP-22, NIP-29, NIP-09, and Buzz's
NIP-MP project event. When the optional forge integration is enabled, WalGit
stores Git data behind the relay's NIP-98/NIP-29 boundary. Pull requests remain
Nostr event graphs; WalGit never becomes collaboration authority.

## Coordinates

A project is a parameterized-replaceable `kind:30621` event. Its `a` tags
contain zero or more NIP-34 repository coordinates:

```text
30617:<repository-owner>:<repository-d>
```

A task is a NIP-34 issue (`kind:1621`) with one `a` tag containing such a
coordinate. A project-wide task therefore still has normal NIP-34 semantics.

## Project Task Anchor

A Project Task Anchor is an optional `kind:30617` repository announcement with
no Git backing. It exists only when a task does not naturally belong to an
external repository. It uses the project signer's key and project `d` value, so
its coordinate is deterministic:

```text
30617:<project-signer>:<project-d>
```

The anchor has `d`, `name`, `description`, and `buzz-channel` tags as available,
plus the exact marker `project-task-anchor=true`. It deliberately has no
`clone` or `web`. Creating the anchor is lazy in Lapis clients. An anchor is
only an event; no WalGit repository or external Git resource is created.

The explicit marker removes the ambiguity between a logical anchor and a
clone-less repository that a Buzz-compatible client expects the relay to host.
An unmarked repository with no `clone` is hosted lazily when forge support is
enabled. A repository with an external `clone` remains external unless one of
its clone locators equals the relay's canonical Git URL.

## Wire events

| Meaning | Event shape |
| --- | --- |
| External or WalGit-backed repository | `kind:30617`; class is derived from `clone` locators |
| Project Task Anchor | `kind:30617`, `project-task-anchor=true`, no Git backing |
| Buzz project | `kind:30621` (NIP-MP) |
| Task/issue | `kind:1621`, `a` points to `30617` |
| Pull request | `kind:1618`, `a` points to `30617`; required repository-owner `p`, `subject`, full `c`, and one or more `clone` locators; optional full `base-commit` and `merge-base` preserve the immutable comparison baseline |
| Pull-request update | `kind:1619`; required matching `a`, root `E/P`, new full `c`, and one or more `clone` locators; optional full `base-commit` and `merge-base` replace the comparison baseline for the new tip |
| Status | `kind:1630` through `1633`, root `e` points to a task or pull request |
| Buzz assignment/unassignment | `kind:1`, root `e`, repository `a`, `p`, and `t:assignment` or `t:unassignment` |
| Standard task comment | NIP-22 `kind:1111`, uppercase `E/K/P` for the task and lowercase `e/k/p` for the immediate parent |
| Current Buzz Desktop task comment | `kind:1` with root `e` and repository `a` |
| Buzz pull-request comment or review | `kind:1` with root `e`, repository `a`, and optional review or inline `t` labels |
| Standard pull-request comment | NIP-22 `kind:1111`, with `K=1618` and consistent root/immediate-parent ancestry |
| Work-item comment reaction | NIP-25 `kind:7` with immediate target `e/p/k` and matching repository `a` |
| Subtask | `kind:1621` with matching custom `parent` and `e` tags plus the same repository `a` |
| Project/repository deletion | NIP-09 `kind:5` with an `a` target |
| Pull-request graph deletion | NIP-09 `kind:5` with an `e` target, signed by that event's signer |

The relay accepts both comment encodings and never translates or synthesizes
one from the other. A client that needs comments to appear in both unmodified
Buzz Desktop and strictly NIP-22 clients must dual-publish.

Unknown tags are retained. A signed project/task event is stored and returned
unchanged; in particular, the relay never appends an `h` tag.

## Deterministic read model

The raw event stream remains authoritative. Clients may derive current task
state using `internal/projecttask`:

- status defaults to open (`1630`);
- only a `CanMaintain` signer on the repository `buzz-channel` changes task
  status; the `30617` `maintainers` tag is not authority;
- equal status timestamps use ascending event ID as the tie-breaker;
- only a `CanMaintain` signer can assign or unassign anyone; the issue
  author and `30617` owner are not assignment authority;
- another signer can only operate on itself with exactly one matching `p` tag;
- self operations without `prior` are applied first, authoritative operations
  second, and causal self operations last, preserving chronological order
  inside each category;
- a causal self operation applies only when its `prior` event is the current
  head for that assignee;
- all root `kind:1` notes and NIP-22 task comments remain visible as activity,
  including assignment notes.

Pull requests use the reproducible `PullRequestState` projection. A root is
Draft when any `t` value case-insensitively equals `draft`, otherwise Open.
Trusted `1619` updates still come from the PR author or the `30617` address
owner. Trusted merged status (`1631`) comes from `CanMaintain` on the
repository channel, or from the same signer as a provider-authoritative
pull-request root that carries `external-provider` provenance. That exception
covers GitHub collaboration-bridge observations; it does not restore
author- or `30617`-owner self-merge for native pull requests. Trusted close, reopen, and draft statuses (`1632`, `1630`,
`1633`) come from the PR author or `CanMaintain`. The newest timestamp wins,
with the lexicographically smallest event ID for an equal timestamp. Kinds
`1630`, `1631`, `1632`, and `1633` mean Open, Merged, Closed, and Draft. A
merged `1631` may carry a full `merge-commit` only when a matching `r` is
present. Patch-oriented `q` and `applied-as-commits` tags remain ordinary
preserved event data and do not trigger relay-side patch handling.
`base-commit` is a Lapis extension containing the immutable target-branch tip
observed for that review revision. When an exact `merge-base` is absent, readers
perform the normal three-dot comparison between `base-commit` and `c`; they
never substitute the target branch's current tip for a historical review.

Root `p` recipients and trusted `t:review-request` notes form the reviewer
set, excluding the PR author. A decision has exactly one of `t:approval` or
`t:changes-requested`. The relay rejects those decisions at ingest unless the
signer `CanMaintain` the repository `buzz-channel` and is not the PR author.
The Lapis reducer trusts the same `CanMaintain` set, never the author, and
never a requested-reviewer or `30617` owner who lacks the channel role. Missing
`c` means the initial tip. Only a decision for the current tip is effective;
equal-timestamp decisions by one reviewer use the lexicographically largest ID.
Older decisions remain historical. Valid `file`/`side`/`line` anchors are
current or outdated by tip; invalid anchors remain visible comments with no
usable anchor. Origin-channel membership alone is not enough to review or
merge; the repository channel is required.

Buzz Desktop may still display author- or owner-trusted reviews and statuses.
Lapis does not. The relay retains all raw events; the Lapis reducer follows
channel `CanMaintain` and documents this Desktop display divergence.

## Repository publication

Creating or replacing a hosted or external `kind:30617`, including a
project-task-anchor, requires an existing `buzz-channel` and `CanMaintain` on
that channel. Channel-less hosted announcements remain rejected. Rebinding
`buzz-channel` additionally requires `CanMaintain` on the new channel and
either `CanMaintain` or administrator status on the previous channel, so a
maintainer of another channel cannot steal the coordinate. The address owner
who loses `maintainer` keeps the parameterized-replaceable address but cannot
create, review, or merge. Channel administrators cannot replace another
pubkey's `30617`; they bind a new repository instead.

GitHub `/api/forge/.../merge` requires `CanMaintain` on the repository
channel, not merely the `30617` owner or a relay administrator.

Hosted WalGit repositories advertise `capabilities.merge=true` and accept the
same NIP-98 merge route. Eligibility requires an open pull request whose
target is the live protected default branch or a compiled extra
`protected-ref` pattern, a current tip matching `expected_head`, no
current-tip `changes-requested` decision, at least the document's
`required_approvals` count of current-tip `CanMaintain` approvals that are
not the PR author (default one), and an actor who `CanMaintain`. When
`admin_bypass` is enabled, a channel creator or relay administrator may merge
without that extra approval count.
When `OWNERS.md` exists on the merge-target tip, every changed-path owner
group also needs a current-tip approval from a listed owner or a channel
administrator. The relay updates that protected ref as the constrained
`merge-coordinator:<owner>/<repo>` principal, waits for the confirmed WalGit
ref, and returns the merge commit. Community then publishes the merging
maintainer's signed `1631` with `merge-commit` and `r`. The relay never signs
that status as a user. A WalGit outage after the Git update is retried as a
durable operation; `1631` is not expected until the ref is confirmed.

## Channel scope and authorization

`buzz-channel` binds a project or repository to a NIP-29 group. The relay
derives the same channel for repository-scoped issues, PRs, status events,
updates, notes, comments, reactions, subtasks, and tombstones. A PR root may additionally
carry an opaque bounded `h` origin channel. Repository and origin channels are
an intersection: a caller must pass normal NIP-29 write policy for every
channel, and all descendants inherit both. Repository create, trusted review,
and merge additionally require `CanMaintain` on the repository channel. Each
check uses an internal event copy with only the derived `h`; the signed event
is never modified.

Anonymous reads require every resolved channel to exist and be public and
non-hidden. Scope resolution fails closed when a required parent or group
cannot be resolved, when ancestry is ambiguous, cyclic, or conflicting, or
when repository/root bindings disagree. The durable sidecar stores one row per
`(event_id, channel)`, retaining both scopes across restarts and deletions.
Direct-user signing is the first supported authorization mode. A future managed
owner mode is isolated behind the scope/actor policy interface; NIP-OA and
`x-auth-tag` are not accepted as owner authority in this version.

## Replacement and deletion

Projects and repositories use normal parameterized-replaceable event rules.
Project and repository tombstones must be signed by the addressed owner. A PR
root, descendant, or reaction can be deleted only by that event's signer. The read
projection suppresses an event when an authorized tombstone at the same or
newer timestamp targets its address or ID. Tombstones remain queryable and an
event-ID deletion does not cascade through the PR graph. This gives WebSocket
and HTTP publication the same visible result even though the upstream
WebSocket dispatcher may additionally remove stored target rows.

## Non-goals

- a project/task/PR-specific REST API
- relay-side comment conversion
- relay-side task status or assignment events
- managed-owner/NIP-OA authorization in the first implementation
- protocol-level `kind:1617` patch application or `kind:30618` repository-state
  projections;
- Git ref verification, merge execution, or automatic PR revisions inside the
  event reducer; and
- automatic group creation or conversion of provider observations into native
  user-signed authority.

Git smart HTTP, browser reads, and PR diffs are documented separately in
the [forge model](../forge/model.md) and [WalGit integration](../forge/walgit.md).
Provider import, mirroring, and CI are optional operational forge capabilities;
they do not change this signed-event model.
