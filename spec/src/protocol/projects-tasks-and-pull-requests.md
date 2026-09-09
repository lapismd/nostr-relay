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
| Pull request | `kind:1618`, `a` points to `30617`; required repository-owner `p`, `subject`, full `c`, and one or more `clone` locators |
| Pull-request update | `kind:1619`; required matching `a`, root `E/P`, new full `c`, and one or more `clone` locators |
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
- only the issue author, repository owner, or a repository `maintainers` key
  changes status;
- equal status timestamps use ascending event ID as the tie-breaker;
- the issue author or repository owner can assign or unassign anyone;
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
Trusted updates and statuses come only from the PR author or repository owner;
the newest timestamp wins, with the lexicographically smallest event ID for an
equal timestamp. Kinds `1630`, `1631`, `1632`, and `1633` mean Open, Merged,
Closed, and Draft. A merged `1631` may carry a full `merge-commit` only when a
matching `r` is present. Patch-oriented `q` and `applied-as-commits` tags remain
ordinary preserved event data and do not trigger relay-side patch handling.

Root `p` recipients and trusted `t:review-request` notes form the reviewer set,
excluding the PR author. A decision has exactly one of `t:approval` or
`t:changes-requested`, and is trusted only from a requested reviewer or the
repository owner, never the author. Missing `c` means the initial tip. Only a
decision for the current tip is effective; equal-timestamp decisions by one
reviewer use the lexicographically largest ID. Older decisions remain
historical. Valid `file`/`side`/`line` anchors are current or outdated by tip;
invalid anchors remain visible comments with no usable anchor.

Buzz Desktop currently trusts the issue author and repository owner for status,
while the NIP-34 model also permits repository maintainers. The relay retains
all raw events; the Lapis reducer follows NIP-34 and documents this Desktop
display divergence rather than rejecting maintainer-authored status events.

## Channel scope and authorization

`buzz-channel` binds a project or repository to a NIP-29 group. The relay
derives the same channel for repository-scoped issues, PRs, status events,
updates, notes, comments, reactions, subtasks, and tombstones. A PR root may additionally
carry an opaque bounded `h` origin channel. Repository and origin channels are
an intersection: a caller must pass normal NIP-29 policy for every channel,
and all descendants inherit both. Each check uses an internal event copy with
only the derived `h`; the signed event is never modified.

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
