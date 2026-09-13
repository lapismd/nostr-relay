# Unified local Community

The relay owns `scripts/local-dev.sh` and `docker-compose.local.yml`. Community
exposes matching `pnpm local:*` aliases. The persistent Compose project is
`lapis-local-dev`; disposable Docker tests use separate projects and volumes.

## Setup

1. Run `scripts/local-dev.sh init`. Missing files are copied independently from
   `~/.lapis/relay` into `~/.lapis/community`. Originals are never changed.
   Repeated initialization preserves existing credentials. Directories use
   0700, configuration and credential files 0600. No normal command reads the
   legacy directory. Keep this directory outside every source checkout.
2. Run `scripts/local-dev.sh capture-github` before retiring the old GitHub
   stack. It captures the single existing connection and repository binding,
   verifies its owner against the copied operator key, and excludes history.
3. Add GitHub OAuth and Google web client IDs to the copied `relay.env`; put
   each client secret in its referenced file. Keep all provider credentials,
   the auth master key, tunnel certificate and credentials here. Runtime
   account tokens and sessions stay encrypted/hashed in PostgreSQL.
   `agent-bunker-master-key` is generated independently with mode 0600 and is
   never reused as the application-auth master key.
4. Install `cloudflared` and run `scripts/local-dev.sh tunnel:setup`. Authenticate
   the intended Cloudflare zone when prompted. The named tunnel has two routes
   and a final 404 rule; conflicting DNS routes are rejected.
5. Run `scripts/local-dev.sh build`, then `scripts/local-dev.sh up`.
   Missing credentials or locally built images fail explicitly.

| Provider setting | URL |
| --- | --- |
| GitHub OAuth callback | https://local-relay.lapis.md/api/auth/callback/github |
| Google OAuth callback | https://local-relay.lapis.md/api/auth/callback/google |
| GitHub App webhook | https://local-relay.lapis.md/webhooks/github |
| OAuth return destination | https://local-community.lapis.md |
| Managed-agent provisioning | https://local-relay.lapis.md/api/agents/managed |
| Managed-agent NIP-46 | wss://local-relay.lapis.md/nip46 |

The tunnel's relay route uses `http://relay:3334` inside Docker. Fixture and
materializer tools resolve the public relay hostname to that service through a
Compose network alias, avoiding a dependency on external DNS during hydration.
Host-side administration connects through the loopback port while signing the
public URL and preserving its virtual host, so local DNS propagation cannot
interrupt GitHub restoration or Keytr recovery.
The Community route uses `http://host.docker.internal:7036`. Relay, Git, Blossom and auth public
URLs use the relay domain. Credentialed auth CORS permits exactly the Community
HTTPS origin. Cookies remain host-only, Secure, HttpOnly and SameSite=Lax.
WebAuthn RP ID and origin belong to the Community domain.

## Lifecycle

| Command | Behavior |
| --- | --- |
| `init` | Copy missing credentials, validate keypairs, generate missing local identities, report missing configuration. |
| `capture-github` | Save the existing test App connection and binding without history. |
| `tunnel:setup` | Create the named tunnel and DNS routes once. |
| `build` | Build local relay, WalGit and fixture images explicitly. |
| `up` | Check prerequisites, stop the old GitHub worker, start the persistent stack, hydrate, attach GitHub, start Vite and open HTTPS Community. |
| `rehydrate` | Restore missing baseline groups, additive roles, blobs and events. |
| `reset --yes` | Quiesce writes, export and verify signed encrypted Keytr records, reset only this project's volumes, restore records and seed. Managed agents are removed and their controller bundles become invalid. |
| `down` | Stop owned Vite and containers, retaining volumes. |
| `doctor` | Validate credentials, images and service readiness. |
| `logs`, `ps` | Inspect the owned stack. |
| `site` | Start the optional static renderer using the same relay. |

Relay configuration is regenerated on container recreation. PostgreSQL,
Blossom, RustFS and WalGit cache are persisted. The forge worker runs inside
the relay; GitHub user actions never fall back to installation credentials.
Group creation stays admin-only. The restored GitHub binding uses GitHub for
content and collaboration, with CI disabled.

`down` followed by `up` retains managed agents in the PostgreSQL volume, and
the same controller client reconnects to the same agent pubkey. The explicitly
destructive `reset --yes` removes managed agents with other non-Keytr user
state; create a new managed agent and controller setup afterward.

Reset backups contain original signed encrypted kind 31777 events, never
plaintext Nostr keys or PRF output. Each verified export is archived under
`backups`; invalid export aborts before volume removal. Credentials, public
configuration and backups are outside Compose volumes. Restore verifies that
every original event is retained.

## Browser configuration and acceptance

Only explicitly allowlisted browser settings from `public.env` enter Vite.
The credential-bearing `relay.env` is never loaded by Vite or served to a
browser. The supervisor restarts when public configuration changes. Explicit
process settings support isolated tests. The unified host uses port 7036,
HTTPS assets, WSS HMR on 443, an explicit allowed host, and direct relay
requests for real CORS coverage.

Exercise Google/GitHub discovery, callbacks, session restoration, signing,
link/disconnect and logout; NIP-07 plus NIP-98/NIP-42; NIP-46 authorization,
reconnect and relay switching; ephemeral/private-key development controls;
Keytr create/import/discover/reconnect/link/sign/logout; and capability-driven
account passkeys and custody transfer with disposable identities. Check
cancellation, unavailable providers/signers and unapproved CORS origins.

Keytr remains the pinned host-injected implementation. Kind 31777 bootstrap
storage is opt-in and enabled only in this stack and isolated auth tests.
Signature validation, addressable replacement, bans and limits still apply.
Other kinds retain normal authentication. Browser-memory-only secret and PRF
handling is unchanged; unsupported authenticators fail without a weaker
fallback. Existing `community.localhost` passkeys require new enrollment or
explicit import for this domain.

Do not retire old registry/auth stacks or the temporary tunnel until public
HTTPS acceptance passes. Then stop their containers while retaining their
volumes and original credentials for rollback. Do not reactivate the old
GitHub worker while this stack processes the same installation.

Live acceptance requires real Google/GitHub credentials, Cloudflare zone
access, and a supported PRF authenticator. Record those outcomes separately
from disposable mock-provider and virtual-authenticator checks. Historical
account migration, plugin releases, GitHub Actions and image publication are
outside this setup.

Registry freshness heads retain the protocol's 30-day validity bound. Their
complete compare-and-swap revision history is persisted in `fixture-heads.json` and
renewed during hydration after 27 days. Rehydration replays the same signed
head chain, including after a fresh database reset. Release, discussion and project event
IDs stay stable; only the necessary curator freshness heads advance. Resets
preserve this state. Repeated hydration within a freshness period is identical.

Local container entrypoints read relay, PostgreSQL, WalGit, object-store and
provider credentials from read-only `/run/secrets` mounts. Secret values are
not interpolated into the Compose service environment. The original provider
secret references remain unchanged inside the relay.

`doctor` verifies that NIP-11 advertises NIP-46 and
`lapis_managed_agents`, the advertised API and bunker URLs resolve to the local
relay, the Community origin receives provisioning CORS while an unapproved
origin does not, `/nip46` upgrades to WebSocket, and the relay sees the enabled
feature and mounted master-key file without reading or printing its contents.

Run `pnpm test:local:e2e` from the sibling `nostr-agent-controller` checkout for
the disposable agent acceptance lane. It builds the controller, provisions and
activates a new managed agent in an application-auth-disabled local stack,
restarts the relay and reconnects the same signer, adds the agent to a fixture
channel with its required writer role, and then verifies online presence, its
NIP-17 inbox, a DM response, encrypted started/completed activity, a
channel-mention response, and a reply-mention response. The runtime is
deterministic and in-process, so this lane does not require an AI host, model,
operator controller configuration, or live credentials.

After downloading `lapis.agent-controller-bootstrap/1`, copy each generated
environment entry into the controller's private environment and add its agent
and signer objects to the controller configuration. Choose `hostId` and
`runtimeAgent` locally; the bundle deliberately leaves them unset. For example:

```sh
export LAPIS_AGENT_0123456789AB_BUNKER_URL='bunker://...'
export LAPIS_AGENT_0123456789AB_NIP46_CLIENT_SECRET='<private-client-secret>'
pnpm start
```


## Implementation verification, 2026-09-12

Credential initialization and GitHub binding capture were run locally. Original
credential bytes, permissions and modification times were preserved. Repeated
initialization copied no existing files. Canonical keypairs and the captured
repository owner's signature were validated without printing credentials.

| Check | Result |
| --- | --- |
| Credential/idempotence, fixture clock and signed backup tests | Passed; backup verification includes 1001 records and rejects tampering before replacing recovery data. |
| Canonical topology/reset | Passed: 13 plugins, identical repeat hydration, preservation of an extra role, restart persistence, freshness-head history replay, verified Keytr export and restoration after volume reset. |
| Local managed-agent end-to-end | Passed 2026-09-13: managed onboarding, real NIP-46 restart reconnect, NIP-17 inbox and DM reply, online presence, six encrypted turn activity frames, channel mention reply, and reply mention reply. |
| Relay Go suite | `go test ./...` passed. |
| GitHub and WalGit tagged Docker lanes | Passed. |
| Local relay, WalGit and fixture image builds | Passed; no images published. |
| Community format, specification, type checks and unit tests | Passed; 448 unit tests passed, 2 skipped. |
| Cold public Vite configuration | Passed allowed-host, asset/HMR settings, isolation headers and credential-directory denial checks. |
| Docker auth contract | Both tests passed, including PRF-backed Keytr create/reconnect, signing and GitHub linking against the actual relay image with disposable fake providers. |
| Projects Docker story | Passed: 1 targeted contract, 42 other stories skipped by this lane. |
| Packaged UI consumer | Passed all six viewport/theme cases. |
| Storybook and package builds | Passed. |
| Relay-to-site smoke | Passed with all 13 plugins and browser asset/thread checks. |

The full `pnpm checks` sequence is **not green**. Its Storybook stage stopped
with a browser connection/RPC `createTesters` failure after 320 assertions;
the separate Docker Storybook lane passed 53 assertions but exited with an
unhandled `relay connection closed by us` rejection. These failures remain
unresolved; assertions and error handling were not weakened to hide them.

Public-domain activation and live Google/GitHub login remain pending OAuth
client IDs/secrets and Cloudflare account access. A real supported-authenticator
Keytr smoke also remains pending. Old persistent stacks and the temporary
tunnel have not been retired. Virtual-authenticator and fake-provider results
do not substitute for these live acceptance checks.
