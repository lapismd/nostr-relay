# NIP-29 groups and policy

The relay enforces group visibility and mutation permissions at ingestion and
query time. A client-side control is never the security boundary.

## Lapis defaults

- `policy.open = true` lets authenticated relay users read and use public
  groups without pre-existing group membership.
- `policy.anonymous_read = true` lets unauthenticated clients read public,
  non-hidden group content and public profile or emoji metadata.
- private and hidden groups, direct messages, invitations, and scoped
  application state remain filtered.
- every publish or state-changing action requires NIP-42 authentication.
- `groups.admin_create_only = true` restricts group creation to configured
  relay administrators.
- `groups.private_admin_only = true` restricts private-group creation to relay
  administrators.
- `groups.private_relay_admin_access = false` prevents relay administrators
  from reading or moderating a private group solely because of the relay role.

`groups.auto_join` controls whether relay members can join groups without
approval. `policy.public_join` separately controls joining the relay without an
invite. Deployments should keep those concepts distinct.

## Write-restricted groups

A group whose metadata contains `write-restricted` acts as an announcement
channel. Only the creator, group administrators, and members carrying the
`writer` role may publish messages, topics, or replies. Authenticated readers
may still publish NIP-25 reactions; a closed group still requires membership
before reacting.

Roles are carried by kind `9000` put-user events, for example
`["p", "<pubkey>", "writer"]` or `["p", "<pubkey>", "maintainer"]`, and appear
on the group's kind `39002` member snapshot. Each new member snapshot advances
beyond the previous stored relay-signed snapshot timestamp, with per-group
serialization. This preserves NIP-01 replacement ordering across rapid updates
and backwards clock movement, so clients cannot retain a stale list after a
successful membership change. Only relay administrators may create a
write-restricted group or add that flag later.

The `writer` role is chat and posting policy only. It does not grant repository
create, review, or merge authority.

## Channel maintainers

`CanMaintain(channel, pubkey)` is true when the channel exists and the pubkey
is the group creator, a group administrator listed on kind `39001` (including
relay `can_manage` administrators surfaced there), or a current member whose
`39002` snapshot carries the `maintainer` role. The role is cached from
put-user the same way `writer` is. Only administrators (`manageMembers`) may
assign or remove `maintainer`. Maintainers cannot escalate themselves to
administrator.

`CanMaintain` is the only channel authority for creating or replacing a
`kind:30617` repository or project-task-anchor, for trusted pull-request
review decisions, and for merge or trusted close. The `30617` announcement
signer remains the NIP-01 address owner and can still replace or tombstone
that coordinate, but losing `maintainer` or administrator status removes
create, review, and merge authority. A `30617` `maintainers` tag and a
requested-reviewer `p` tag are not authority.

## Retention

`[groups.retention]` applies bounded message retention to kinds `9` and `10`.
The `default` duration applies to all groups unless
`[groups.retention.groups]` supplies an `h`-tag group ID override. Empty means
unlimited. Durations use `s`, `m`, `h`, or `d` suffixes and are validated when
configuration loads. Membership, metadata, and administrator events are never
removed by chat retention.

## Static relay roles

`[roles.<name>]` associates configured pubkeys with relay capabilities.
`can_invite` controls invite claims. `can_manage` authorizes NIP-86 relay
management and administrative NIP-29 actions. The special `[roles.member]`
section supplies defaults for relay users. Arbitrary role names do not grant
new behavior unless relay policy maps them to a capability.

Project and forge event graphs derive repository and origin group scope without
mutating signed events. Those rules are defined in [Projects, tasks, and pull
requests](../protocol/projects-tasks-and-pull-requests.md).
