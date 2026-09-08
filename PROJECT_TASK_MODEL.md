# Project and task event model

This relay stores ordinary signed Nostr events. Project/task support is a local
policy and indexing extension around NIP-34, NIP-22, NIP-29, NIP-09, and Buzz's
NIP-MP project event. The relay does not create, clone, serve, or modify Git
repositories.

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

The anchor has `d`, `name`, `description`, and `buzz-channel` tags as available.
It deliberately has no `clone`, `web`, or custom synthetic marker. Creating the
anchor is lazy in Lapis clients, but the relay accepts Buzz Desktop's eager
default repository using the same shape. An anchor is only an event; no disk,
GitHub, GitLab, or relay-hosted Git repository is created.

## Wire events

| Meaning | Event shape |
| --- | --- |
| Repository or task anchor | `kind:30617` |
| Buzz project | `kind:30621` (NIP-MP) |
| Task/issue | `kind:1621`, `a` points to `30617` |
| Status | `kind:1630` through `1633`, root `e` points to task |
| Buzz assignment/unassignment | `kind:1`, root `e`, repository `a`, `p`, and `t:assignment` or `t:unassignment` |
| Standard task comment | NIP-22 `kind:1111`, uppercase `E/K/P` for the task and lowercase `e/k/p` for the immediate parent |
| Current Buzz Desktop task comment | `kind:1` with root `e` and repository `a` |
| Subtask | `kind:1621` with matching custom `parent` and `e` tags plus the same repository `a` |
| Project/repository deletion | NIP-09 `kind:5` with an `a` target |

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

Buzz Desktop currently trusts the issue author and repository owner for status,
while the NIP-34 model also permits repository maintainers. The relay retains
all raw events; the Lapis reducer follows NIP-34 and documents this Desktop
display divergence rather than rejecting maintainer-authored status events.

## Channel scope and authorization

`buzz-channel` binds a project or repository to a NIP-29 group. The relay
derives the same channel for repository-scoped issues, status events, notes,
comments, and subtasks. It applies normal NIP-29 read/write policy to an
internal event copy with a derived `h` tag, without modifying the signed event.

Scope resolution fails closed when a required parent cannot be resolved, when
the ancestry is ambiguous, or when project/repository bindings disagree.
Direct-user signing is the first supported authorization mode. A future managed
owner mode is isolated behind the scope/actor policy interface; NIP-OA and
`x-auth-tag` are not accepted as owner authority in this version.

## Replacement and deletion

Projects and repositories use normal parameterized-replaceable event rules.
Project and repository tombstones must be signed by the addressed owner. The
read projection suppresses a replaceable head when an authorized tombstone at
the same or newer timestamp targets its address. Tombstones themselves remain
queryable. This gives WebSocket and HTTP publication the same visible result
even though the upstream WebSocket dispatcher may additionally remove stored
target rows.

## Non-goals

- Git smart-HTTP, repository storage, cloning, or mirroring
- a project/task-specific REST API
- relay-side comment conversion
- relay-side task status or assignment events
- managed-owner/NIP-OA authorization in the first implementation
