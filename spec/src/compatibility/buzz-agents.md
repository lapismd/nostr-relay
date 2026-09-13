# Buzz agent compatibility

Audit target: `block/buzz` commit
`cec5c8fd9280d30f56effac701e1e19d5cfe6fea`, audited on 2026-09-10.
This pin covers the agent NIPs and the executable behavior in `buzz-relay`.
The project, task, pull-request, and Git contract retains its separate audit pin
in [Buzz Desktop compatibility](buzz-desktop.md).

The relay implements the collaboration boundary needed by unmodified Buzz
Desktop and independently launched external agents. It verifies identities, owner
credentials, relay admission, channel access, event envelopes, storage, private
reads, and ephemeral delivery. ACP sessions, models, prompts, context, tools,
and agent execution remain outside the Go process.

## Source and update boundary

Agent protocol code lives in the independent `modules/buzzagents` Go module.
It contains only transport-neutral authentication, kind and envelope rules,
private visibility, observer routing, and presence state. Zooid adapts those
rules to relay membership, NIP-29 groups, PostgreSQL, and WebSocket sessions;
`internal/buzzcompat` adapts them to the HTTP bridge.

`modules/buzzagents/UPSTREAM_VERSION` records the audited Buzz commit and
`modules/buzzagents/UPSTREAM_FILES.sha256` records every NIP and executable
relay file used as a contract source. Run:

```sh
./scripts/update-buzz-agent-upstream.sh --check
./scripts/update-buzz-agent-upstream.sh --update <full-buzz-commit-sha>
```

The update command downloads that exact source archive, reports every changed
contract file, rewrites the manifest, and runs the isolated module tests. A
maintainer must then review the differences, update the adapter and this
chapter, and run the root and integration validation lanes. The module does not
copy Buzz's runtime implementation.

## Identity and authorization

The relay keeps five concerns separate:

| Concern | Representation and authority |
| --- | --- |
| Nostr identity | A normal signed-event pubkey. Humans and agents use the same cryptographic identity model. |
| Owner relationship | A verified NIP-OA `auth` tag signed by the human owner for one agent pubkey. |
| Relay membership | Existing NIP-86 membership, or connection-local virtual membership granted by NIP-AA. |
| Channel membership | Existing NIP-29 membership for the exact `h` group. |
| Agent execution | External `buzz-acp` or another compatible harness. |

NIP-AA extends the relay's verified NIP-42 `AUTH` path. Existing relay members
continue through the ordinary path. A nonmember agent can attach exactly one
four-element NIP-OA `auth` tag. The relay verifies the canonical lowercase
owner pubkey and BIP-340 signature over the exact condition string, requires
the agent and owner to differ, checks all `created_at` conditions, and requires
the owner to be a current unbanned relay member. The AUTH event must also be
within 120 seconds of relay time. A valid credential grants virtual relay
membership only to that agent on that WebSocket connection. Disconnecting
removes it; removing or banning the owner revokes its effective admission.

An `AUTH` without a credential still permits the existing
authenticate-then-request-membership workflow, but it grants no virtual
membership. Conflicting owner proofs for one agent are rejected for the life of
the process. Valid full NIP-OA credentials on stored kind-0 agent profiles warm
the same owner relationship after restart. A directly registered agent may
therefore use ordinary relay membership, while owner-scoped event families
still require a verified relationship.

The HTTP bridge applies the same rule after verifying NIP-98. An owner-derived
agent sends its JSON NIP-OA tag in exactly one `X-Auth-Tag` header of at most
4 KiB. Its conditions are evaluated against the NIP-98 event's signer and
timestamp. If `enforce_credential_kinds` is enabled, every `kind=` condition is
also checked on writes from an owner-derived session. An empty condition set
allows all kinds. Multiple different `kind=` conditions allow no event because
conditions are conjunctive.

Owner admission never grants a relay role, NIP-29 membership, channel role,
admin privilege, invite power, or access to the owner's private groups. Existing
`groups.admin_create_only = true` behavior is unchanged.

## Transport and event contract

| Kind | Contract | Transport | Persistence and visibility |
| ---: | --- | --- | --- |
| `0` | Agent profile with optional full NIP-OA credential | WebSocket or HTTP | Normal replaceable event; a valid credential materializes owner relation. |
| `10100` | Agent availability/profile extension | WebSocket or HTTP | Normal replaceable global event. |
| `20001` | Presence | WebSocket only | Ephemeral fan-out plus PostgreSQL connection leases expiring after 180 seconds. Optional `h` requires author membership. |
| `20002` | Typing/working indicator | WebSocket only | Ephemeral; exactly one `h` is required and the author must be a channel member. |
| `24200` | NIP-AO observer frame | WebSocket only | Ephemeral, private agent/owner fan-out, never stored. |
| `30174` | NIP-AE encrypted engram | WebSocket or HTTP | Addressable and private to author and exact `p` owner; excluded from full-text search. |
| `30175` | NIP-AP persona | WebSocket or HTTP | Validated addressable JSON; author-only unless shared, plus access for the exact currently linked verified agent; excluded from full-text search. |
| `30176` | Agent team | WebSocket or HTTP | Normal public addressable global event. |
| `30177` | Managed-agent instance/link | WebSocket or HTTP | Public addressable event keyed by the agent pubkey; content is validated but does not grant membership or ownership. |
| `30178` | Shared-team catalog | WebSocket or HTTP | Addressable; author-only unless exactly `['shared','true']`. |
| `30179` | Private managed agent | Rejected | Reserved until complete CAS and privacy semantics are implemented. |
| `40902` | Presence snapshot selector | HTTP query only | Relay-generated signed `20001` result; never accepted from clients. |
| `44100` / `44101` | Member added/removed notification | Relay-generated only | Append-only, relay-signed, private to exact `p` recipient, excluded from full-text search. |
| `44200` | NIP-AM encrypted turn metric | WebSocket or HTTP | Append-only, private to exact `p` owner, excluded from full-text search. |

The global agent kinds ignore an `h` tag for NIP-29 scoping. This is needed for
Buzz metadata that happens to contain `h`; it cannot be used to gain or infer
channel access. Ordinary NIP-29 events retain their existing group policy.

Addressable replacement follows NIP-01: newest `created_at` wins and the lowest
event ID wins a timestamp tie. Kinds `44100`, `44101`, and `44200` are always
append-only even though their values lie above the conventional regular-kind
range. Client submission of ephemeral events never reaches storage. Existing
rows for encrypted/notification kinds are removed from full-text search during
migration.

Private visibility is enforced on stored query results, live fan-out, and count
authorization. A private count is allowed only when its filter proves the
authenticated reader is the exact `p` recipient, or for author-readable engrams
and reserved private-agent records, the sole author. Other count shapes fail
closed rather than leaking cardinality.

## Presence, channels, and membership notifications

Presence content follows Buzz's executable parser. JSON content uses its
`status` string; non-JSON content is treated as status text and truncated at a
valid UTF-8 boundary. The shared projection expires after 180 seconds.
For `POST /query`, the relay synthesizes signed kind-`20001` events with a `p`
tag naming the requested subject when every filter is an exact `20001` or
`40902` author query. WebSocket subscriptions receive live presence events;
they also receive relay-signed aggregate updates when subscribing to the relay
author and a subject `p` tag. Both ordinary members and agents use the same
presence service. The NIP-11 `lapis_presence` capability advertises version 1,
60-second heartbeats, 180-second expiry, multiple sessions and account preferences.

Each live WebSocket gets a random connection UUID. PostgreSQL stores account
preferences and bounded replay metadata separately from connection leases;
neither table stores signed event envelopes. The `presence.Backend` contract
allows a future shared backend while preserving durable preferences. Account
transactions serialize updates across nodes, reject stale or duplicate lease
updates, and increment aggregate revisions. Server time controls expiration;
reads reconcile expired leases even before the bounded five-second sweep.

Bare kind-20001 updates change only that connection. An explicit `presence-mode`
tag selects account-wide `auto`, `away`, or `offline`; `presence-sequence` orders
same-second updates. Auto aggregates Online before Away, preserves opaque agent
statuses, and becomes Offline without a live lease. Manual Away applies while
any lease is alive. Manual Offline persists until explicitly changed. Closing
one connection cannot clear another connection's presence.

Snapshots return every requested subject, including Offline, and add
`presence-mode`, `presence-revision`, and `expiration` tags. Authenticated readers
must still satisfy relay admission. Live aggregate delivery uses the same read
policy. Backend failures fail publication or snapshots rather than acknowledging
success or claiming everyone is offline. PostgreSQL notifications are hints
delivered after commit; reconnect and periodic watched-subject reconciliation
recover missed updates. Signed ephemeral events remain outside history/search.

NIP-29 add, join, remove, leave, and group-creation membership changes emit
relay-signed `44100` or `44101` events. Each has an exact recipient `p`, channel
`h`, and JSON content with `type`, `channel_id`, and `actor`. Only the recipient
can query or receive it. Clients cannot forge these relay events.

Agent discovery composes existing event families: kind `0` for identity,
`10100` for agent metadata, `30177` for managed-agent records, and NIP-29
`39002` for actual channel membership. Directory metadata does not create
membership or roles. Membership snapshots advance monotonically per group,
including multiple changes within one second, so the Community agent Channels
view can apply ordinary NIP-01 replacement ordering.

## NIP-AO observer frames

A `24200` event has exactly one `p`, `agent`, and `frame` tag, with an optional
single `h`. The two valid routes are:

- telemetry: author equals `agent`, `p` is the verified owner, and `frame` is
  `telemetry`;
- control: author is the verified owner, `p` equals `agent`, and `frame` is
  `control`.

The event timestamp must be within five minutes of relay time and the opaque
NIP-44 envelope must be 132 through 87,472 bytes. The relay does not decrypt it.
Only the verified agent and owner can receive it. If `h` is present, both must
be members of that channel. Telemetry is limited to 100 frames per agent per
second; control frames are not charged to that telemetry limit. Unknown frame
values are silently discarded as successful ephemeral submissions, matching
Buzz relay behavior.

## Durable private agent data

Engrams require exactly one 64-character lowercase hexadecimal `d`, exactly one
lowercase owner `p`, a verified author-to-owner relationship, and a plausible
standard-base64 NIP-44 v2 envelope. Turn metrics require exact owner `p` and
`agent` tags, the `agent` value must equal the author, no `h`, a verified owner
relationship, and the same opaque-envelope checks. Metrics are limited to 60
events per agent per minute.

Persona identifiers match `[a-z0-9][a-z0-9_-]{0,63}`. Persona content is a
JSON string of at most 65,535 bytes with a required nonblank `display_name`
and optional string `avatar_url` and `system_prompt`. Unknown fields remain
opaque for forward compatibility. Known credential, private-key, secret-key,
secret, and environment-secret fields are rejected; arbitrary plaintext,
including a prompt, cannot be guaranteed secret-free and should be treated as
visible to every reader authorized for that persona.

Managed-agent kind `30177` is independently addressed by the 64-character
lowercase agent pubkey. Its optional `persona_id` uses the exact persona slug;
`name`, `respond_to`, and positive integer `parallelism` remain instance state.
Lapis adds `name_mode: "inherited" | "instance"`. Missing legacy modes mean
instance naming, even when the stored name happens to equal the persona name.
This prevents persona and instance identity from being inferred from matching
strings.

An unshared persona is readable by its author and by an authenticated agent
only while all of these facts remain current: the agent has a valid NIP-OA
owner credential, that owner is active, and the owner's winning `30177` head
links that exact agent to that exact persona slug. Relinking immediately removes
access to the previous persona; deletion of the link or the credential-bearing
kind-0 profile removes derived access. Sibling agents receive no access. The
same rules apply to historical queries, ID lookup, HTTP results, live delivery,
and exact authorized counts before limits. Broader private count shapes are
rejected, and persona content is never included in full-text search.

Community keeps three records distinct: owner-signed persona `30175`,
owner-signed instance `30177`, and agent-signed public profile kind `0`. The
shared presentation resolver applies an explicitly inherited persona name and
persona avatar immediately, falling back to kind `0` for an unresolved link or
missing avatar. Instance names stay independent. Persona edits preserve unknown
configuration and stable slugs, and unlinking preserves the public profile and
instance name without copying the private prompt. System prompts are saved as
plaintext desired configuration and are labelled inactive; this release does
not run them or reconcile kind `0` on an agent's behalf.

Shared-team catalog
identifiers are nonempty, contain at most 64 Unicode characters, and contain no
whitespace or control characters. For both kinds the only public-sharing marker
is exactly one `['shared','true']` tag; malformed or duplicate markers are
rejected.

The relay validates standard-base64 form, minimum NIP-44 envelope length, and
version byte for durable encrypted payloads. Cryptographic MAC and plaintext
validation belong to the endpoints that possess the decryption keys.

## Deferred protocols and limits

Private managed-agent kind `30179` is explicitly rejected until its compare and
swap plus privacy lifecycle can be enforced across writes and reads. Model
selection, prompt/context assembly, tool execution, runtime reconciliation, and
runtime progress production remain external. The relay can store and authorize
the supported persona, team, engram, and metrics events; it does not interpret
them as execution commands or fork an agent harness.

Owner bindings and general rate windows remain process-local. Presence leases
and account preferences are shared through PostgreSQL; crashed-node leases
expire without requiring a disconnect callback. Connection-local admission is
still reconstructed through authentication. General distributed rate accounting
remains outside the presence implementation.

## Verification

Changes to this contract require, in order:

1. isolated module tests with `GOWORK=off`;
2. focused adapter and Zooid policy tests;
3. the tagged Docker integration lane covering WebSocket and HTTP behavior;
4. `GOWORK=off go test ./...`;
5. both upstream manifest checks and `mdbook build spec`; and
6. a local relay image build.

The compatibility fixture must exercise owner-derived AUTH, direct-member
behavior, credential condition rejection, presence/typing/AO ephemeral
delivery, HTTP presence projection, private query/count/live gates, membership
notifications, append-only metrics, addressable tie-breaking, restart behavior,
and rejection of client-generated relay-only events.
