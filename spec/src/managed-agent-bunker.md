# Relay-managed agent bunker

The managed-agent bunker is an optional relay capability for creating a Nostr
agent identity and connecting an independently operated agent controller. It is
separate from application authentication: enabling `[agents.bunker]` neither
enables `nostr-auth` nor makes an application account authoritative for agent
custody.

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
Active clients may sign only kinds `22242`, `9`, `7`, `20001`, `20002`, and
`24200`; group-scoped events require current NIP-29 membership. NIP-44 encrypt
and decrypt are restricted to the recorded owner. NIP-04, arbitrary profile
changes, Git and NIP-98 signing, unknown methods or kinds, malformed tags, and
stale timestamps are rejected.

## Managed ownership admission

A managed profile carries
`["lapis-managed", "<owner-pubkey>", "<relay-pubkey>"]`. Main-relay NIP-42
accepts it only for an active custodied agent when the current agent kind `0`
and the same owner's current kind `30177` reciprocally link that agent, the
owner still passes normal relay admission, and neither owner nor agent is
banned. Existing NIP-OA credentials remain the authority for external agents.

## Deferred scope

The targeted controller bunker does not expose an arbitrary-client signing
service. Git workspace bootstrap, forge credentials and tools, controller
process management, runtime and model selection, and additional signer
backends remain deferred.
