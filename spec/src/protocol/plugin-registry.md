# Plugin registry policy

The optional registry policy protects the Lapis plugin release graph at the
relay boundary. It does not store a separate catalog projection: signed Nostr
events remain the source of truth and clients materialize the catalog from
those events.

Enable it with:

```toml
[registry]
enabled = true
release_group_id = "plugin-releases"
curator_role = "registry-curator"
```

Both `release_group_id` and `curator_role` are required when the policy is
enabled. The named NIP-29 group should use the ordinary group and membership
policy described in [NIP-29 groups and policy](../relay/groups-and-policy.md).

## Release and decision events

A release is a kind `9` group message containing exactly one matching `h` tag
and exactly one `t=lapis-plugin-release` tag. The release publisher remains the
event signer.

A registry decision is a kind `7` reaction in the release group with exactly
one `h` and one `e` tag, both two-field tags. Its content is one of:

| Content | Meaning | Required signer |
| --- | --- | --- |
| `✅` | approved | member with the configured curator role |
| `❌` | rejected | member with the configured curator role |
| `🚫` | revoked | member with the configured curator role |
| `withdrawn` | withdrawn | signer of the referenced release |

The referenced event must exist and must itself be a release in the configured
group. This stricter policy is evaluated before the generic
write-restricted-group rule, so a valid curator decision or publisher
withdrawal does not acquire authority from UI state or an unrelated role.

## Signed registry heads

A registry head is a curator-signed kind `9` event in the release group with
exactly these three tags:

- `h=<release_group_id>`;
- `t=lapis-plugin-registry-head`; and
- `t=plugin:<pluginId>`.

Its JSON content uses schema `lapis.registry.plugin-head/1` and contains the
plugin ID, authority epoch, positive canonical decimal head revision, optional
previous head event ID, optional active release event ID, canonically sorted
release evidence, and RFC 3339 issue/expiry timestamps.

The issue timestamp must equal the event's `created_at`. Expiry must be later
than issue and no more than 30 days away. Each release evidence item contains a
64-character lowercase hexadecimal release event ID, manifest SHA-256,
decision event ID, unique version, optional canonical release sequence, and a
state of `approved`, `rejected`, `revoked`, or `withdrawn`. An active release
must refer to an approved item.

Heads use compare-and-swap semantics. The first revision is `1`; each later
revision increments by exactly one and names the immediately preceding head
event. The relay reserves accepted revisions while concurrent writes are in
flight so two events cannot claim the same next revision.

## Query behavior

Registry reads use ordinary Nostr filters and NIP-29 visibility. The relay's
generic count implementation recognizes the bounded registry discussion
filter shape and returns an exact count rather than an estimate. Registry
policy does not grant public access to a private group, synthesize releases or
decisions, download plugin archives, or decide whether a client should install
a plugin.
