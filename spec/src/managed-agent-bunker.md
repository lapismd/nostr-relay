# Relay-managed agent bunker

The managed-agent bunker is an optional relay capability for creating a Nostr
agent identity and connecting an independently operated agent controller. It is
separate from application authentication: enabling `[agents.bunker]` neither
enables `nostr-auth` nor makes an application account authoritative for agent
custody.

The targeted `lapis-managed-v1` bunker, provisioning API, relay admission, and
controller policy are implemented. Arbitrary-client bunkers and the deferred
execution and forge features listed below remain outside this capability.

A signed-in human owner creates a pending agent with a supplied NIP-46 client
public key. The relay generates a distinct agent key, encrypts it with a
bunker-specific wrapping key, and returns a one-time `bunker://` invitation.
The owner publishes a persona and kind `30177` instance, the pending signer
signs one exact kind `0`, and activation verifies both reciprocal records. The
agent secret never leaves the relay. Community keeps the NIP-46 client secret
only in memory and gives it to the operator in a one-time controller bundle.

## Configuration and discovery

`[agents.bunker]` contains independent enablement, public URL, exact browser
origins, credential directory, master-key reference and version, invitation
lifetime, owner quotas, and client rate limit. The default limits are ten
active and three pending agents per owner, five creations per owner per hour,
a 24-hour invitation, and 120 requests per client per minute.

When enabled, NIP-11 advertises NIP-46 and `lapis_managed_agents` version `1`
with the provisioning URL, `wss` bunker URL, relay public key, and
`lapis-managed-v1` authorization type. When disabled, the extension is absent.
Provisioning is available to an authenticated, unbanned human accepted by the
relay's normal open or closed admission policy. A managed agent cannot create
another managed agent.

## Persistence and custody

PostgreSQL stores managed-agent state, controller client public keys, encrypted
and hashed invitation state until pairing, exact request idempotency records,
and sanitized audit outcomes. The wrapping associated data binds agent, owner,
relay, and wrapping-key version. Decryption is scoped to one signing operation,
and plaintext key material is cleared immediately afterward.

One controller client is active at a time. A replacement invitation leaves the
old client active until the replacement pairs. The current client may reconnect
after restart without an invitation. Revocation disables signing; deletion
requires an owner-signed unlink, revokes all clients, and destroys ciphertext.

## HTTP and NIP-46 surfaces

The body-bound, replay-protected NIP-98 API exposes:

- `GET /api/agents/managed`
- `POST /api/agents/managed`
- `POST /api/agents/managed/{pubkey}/activate`
- `POST /api/agents/managed/{pubkey}/connections`
- `DELETE /api/agents/managed/{pubkey}/connections/current`
- `DELETE /api/agents/managed/{pubkey}`

The dedicated `/nip46` WebSocket endpoint accepts only fresh, valid kind
`24133` events addressed to a managed agent. It is a transport for encrypted
NIP-46 requests and responses, never a general relay storage endpoint. Request
IDs are durable: an exact retry returns the cached response, while different
content under the same ID is rejected.

Bootstrap permits `connect`, `get_public_key`, and one exact pending kind `0`.
Active clients may sign only kinds `13`, `22242`, `9`, `7`, `10050`, `20001`,
`20002`, `24200`, and the bounded relay-tool kinds described below;
group-scoped events require current NIP-29 membership.
Kind `10050` must advertise only the bunker relay. Kind `13` is limited to an
empty-tag NIP-17 seal and may use the protocol-defined randomized timestamp up
to two days in the past. Standard NIP-44 encrypt and decrypt are restricted to
the recorded owner or the agent itself.

The `lapis_nip17_unwrap` NIP-46 extension accepts one complete signed kind
`1059` event. It decrypts only when the wrap has exactly one `p` tag naming the
managed agent, leaving seal, rumor, author, recipient, and policy validation to
the controller. This gives the controller the outer ephemeral-key operation
needed by NIP-17 without exposing general NIP-44 decryption for arbitrary peer
keys. NIP-04, arbitrary profile changes, Git and NIP-98 signing, unknown
methods or kinds, malformed tags, and timestamps outside the method-specific
bounds are rejected.

## Relay tool signing

The controller may expose relay reads without asking the bunker to sign. A
write still requires an owner permission decision in the controller, after
which the managed signer accepts only these event templates:

- channel-scoped long-form notes (`30023`) with one `d`, one `title`, and the
  authorized `h`;
- projects (`30621`) and repositories (`30617`) whose `buzz-channel` and `h`
  identify the same authorized channel;
- issues (`1621`) anchored to a current repository in that channel;
- issue statuses (`1630` through `1633`) anchored to both the current issue and
  its repository in that channel; and
- NIP-22 pull-request comments (`1111`) whose root and parent are the same
  current `1618` event and whose repository anchor resolves to that channel.

Project repository coordinates must resolve to current repository
announcements in the same channel. Repository, issue, status, and work-item
comment tags are validated against the relay's NIP-34 model before signing.
Unknown tags, missing anchors, cross-channel references, stale targets, and
non-member channels fail closed. This permission does not expose Git object,
GitHub provider, NIP-98, or arbitrary event signing.

## Managed ownership admission

A managed profile carries
`["lapis-managed", "<owner-pubkey>", "<relay-pubkey>"]`. Main-relay NIP-42
accepts it only for an active custodied agent when the current agent kind `0`
and the same owner's current kind `30177` reciprocally link that agent, the
owner still passes normal relay admission, and neither owner nor agent is
banned. Existing NIP-OA credentials remain the authority for external agents.

## Deferred scope

The targeted controller bunker does not expose an arbitrary-client signing
service. Git workspace bootstrap, forge credentials and provider mutations,
controller process management, and additional signer backends remain deferred.

## Conversation reactions and forum replies

Active managed clients may additionally sign kind 1111 comments only for a
validated same-group kind 11 forum root and kind 11/1111 parent, with matching
E/K/P and e/k/p references and current membership. Kind 5 is allowed only to
delete the same agent's stored kind 7 reactions in the same group. Unknown,
foreign or unrelated targets fail closed; arbitrary event deletion and creation
of forum roots remain forbidden. Approved-participant encrypted DM seals carry
private reaction and deletion rumors without public message references.

Accepted group reaction deletions retain the validated target group before the
reaction is removed. Read and broadcast policy must still deliver those
tombstones to authorized group readers after deletion and relay restart.

Relay history queries release each bounded storage page before evaluating
read authorization. Reciprocal ownership and room checks may query PostgreSQL
and must not exhaust the pool while outer history cursors remain open.

Managed signing permits kind `40100` only as an exact single-h canvas snapshot
within the 64 KiB UTF-8 limit. The signer MUST check current channel membership,
management permission and archive state before signing; publication independently
checks the same channel write policy. Owner approval and a fresh-head check are
controller responsibilities.

## Direct message participant policy

The latest verified owner-authored kind `30177` instance record MAY contain
`dm_allowlist`, up to 128 canonical peer public keys. Missing or malformed lists
grant owner-only access. The managed signer MUST enforce this policy for every
NIP-44 peer and every participant in a DM rumor. Control-channel encryption
remains owner-only. Other peers may receive only validated direct-message
rumors; arbitrary private payloads and unapproved recipient sets are rejected.

| Surface | Verification |
| --- | --- |
| Managed DM policy | Signed-instance, participant-set, owner-control and revocation tests in the agent bunker suite. |
