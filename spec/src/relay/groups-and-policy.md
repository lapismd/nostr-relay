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
`["p", "<pubkey>", "writer"]`, and appear on the group's kind `39002` member
snapshot. Only relay administrators may create a write-restricted group or
add that flag later.

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
