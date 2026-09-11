# Application authentication and signing

Application auth is optional and disabled by default. When enabled, it maps
Google, GitHub, and existing Nostr login methods onto an account whose Nostr
pubkey remains the canonical relay identity. Authentication providers, signer
custody, and GitHub credentials are stored as separate records and may change
without changing that pubkey.

## Configuration and secrets

```toml
[auth]
enabled = true
public_url = "https://relay.example"
credential_dir = "/run/secrets"
master_key_ref = "file:lapis_auth_master_key"
master_key_version = "v1"
session_lifetime = "168h"
session_idle = "24h"
challenge_ttl = "10m"

[auth.github]
client_id = "<github-app-oauth-client-id>"
client_secret_ref = "file:lapis_auth_github_client_secret"

[auth.google]
client_id = "<google-oidc-client-id>"
client_secret_ref = "file:lapis_auth_google_client_secret"
```

`auth.public_url` must be HTTPS except on a loopback development host. The
master key file contains exactly 32 bytes encoded as hexadecimal or base64.
Provider secrets and the master key are loaded from the configured credential
directory and are never stored in PostgreSQL.

The Compose overlays are deliberately composable. Start with
`docker-compose.auth.yml`, then add `docker-compose.auth-github.yml`,
`docker-compose.auth-google.yml`, or both. The GitHub App installation overlay
remains separate because installation credentials and user OAuth credentials
serve different actors.

## Accounts, sessions, and managed keys

The account table has one immutable Nostr pubkey. External identities use the
provider's immutable subject: GitHub numeric user ID or Google `sub`. A new
OAuth account gets a random Nostr keypair and a managed signer binding. An
existing Nostr login creates an external binding and stores no private key.

Managed private keys use envelope encryption. A random per-account data key
encrypts the 32-byte Nostr secret with AES-GCM; the deployment master key wraps
that data key. Associated data binds both ciphertexts to the account, pubkey,
and wrapping-key version. Database backups alone cannot decrypt a managed key.
The `auth` package exposes signing operations and never exposes a raw-key
getter.

Sessions use an opaque random cookie whose SHA-256 hash is stored in the
database. Cookies are HttpOnly, SameSite=Lax, and Secure for HTTPS deployments.
OAuth state, PKCE verifier, OIDC nonce, Nostr login challenges, WebAuthn
challenges, and transfer proofs are single-use and expire.

## HTTP surface

All paths are below `/api/auth/`:

| Method and path | Purpose |
| --- | --- |
| `GET login/github` | Start GitHub login; `intent=link` connects GitHub to the current account. |
| `GET login/google` | Start Google OIDC login. |
| `GET callback/<provider>` | Provider callback used by the OAuth client. |
| `POST nostr/challenge` | Issue a one-time challenge for an existing Nostr pubkey. |
| `POST nostr/login` | Consume the challenge with a body-bound NIP-98 signature. |
| `GET session` | Return account, identities, signer state, passkey count, and GitHub link state. |
| `POST logout` | Revoke the current application session. |
| `DELETE github` | Remove forge credentials without deleting the account or GitHub login identity. |
| `POST relay/sign-auth` | Sign a NIP-42 response through the active managed or NIP-46 signer. |
| `POST nip46/connect` | Connect an externally controlled account to a `bunker://` signer with the same pubkey. |
| `POST nip46/switch-relays` | Ask the connected signer for its current relay set. |
| `POST nip46/logout` | Remove the local NIP-46 binding and return to an external signer binding. |
| `POST passkeys/register/begin` | Start first-passkey enrollment after a login no older than ten minutes. |
| `POST passkeys/register/finish` | Validate and persist the attested credential. |
| `POST custody/transfer/begin` | Start a user-verified passkey assertion for device transfer. |
| `POST custody/transfer/authorize` | Consume the assertion and return the encrypted key envelope. |
| `POST custody/transfer/complete` | Verify same-key possession and NIP-46 availability, then complete transfer. |

State-changing session routes require an exact `Origin` match with
`auth.public_url`. OAuth callbacks use one-time state; Nostr login uses a
one-time challenge and a NIP-98 signature over the exact request body.

## NIP-46 signer boundary

The relay uses the maintained `fiatjaf.com/nostr/nip46` implementation for
NIP-44-encrypted kind `24133` RPC. `NIP46Signer` supports connection,
`get_public_key`, `sign_event`, and `switch_relays`; logout removes the relay's
client binding. The relay applies its signing policy before sending a request,
then verifies that the response kept every unsigned event field, has the bound
pubkey, and has a valid ID and signature.

The saved bunker URL contains signer pubkey and relay locators. Its one-time
connection secret is removed before persistence. The relay's NIP-46 client key
is encrypted with the auth master-key wrapper.

## Managed-to-device custody transfer

Custody transfer requires an enrolled passkey and a WebAuthn assertion with
user verification. The device supplies a raw X25519 public key encoded with
unpadded base64url. After the assertion succeeds, the relay returns:

```json
{
  "transfer_id": "<uuid>",
  "server_public_key": "<base64url X25519 public key>",
  "encrypted_private_key": "<base64url nonce plus AES-GCM ciphertext>",
  "possession_challenge": "<random challenge>",
  "expires_at": "<RFC3339>"
}
```

Both sides derive 32 bytes with HKDF-SHA256 from the X25519 shared secret and
the following `info`, which is also the AES-GCM associated data:

```text
lapis-custody-transfer-v1\0<account-id>\0<transfer-id>\0<hex-pubkey>
```

The ciphertext contains the raw 32-byte Nostr secret. The device proves
receipt by signing a kind `22242` event with `relay`, `challenge`, and
`purpose=custody-transfer` tags, and supplies a reachable `bunker://` URL. The
relay verifies the signature, the unchanged account pubkey, and the remote
signer's `get_public_key` result. One database transaction then changes the
binding from `managed/migration_pending` to `nip46/active`, deletes the managed
key, and completes the transfer. Failure before that transaction keeps the
managed key and the explicit pending state.

## GitHub attribution

GitHub OAuth uses a GitHub App user access token with `repo` scope. Access and
refresh tokens are encrypted and bound to the account and credential purpose.
A user-authored outbox operation records `credential_mode=user` when the
author has a linked GitHub credential. Its recovery reads and mutation use
that same user token. A revoked, expired-without-refresh, or permission-denied
credential returns `github_user_reconnect_required`; it never falls back to
the installation token.

Unlinked users use `credential_mode=system`, preserving the existing GitHub
App installation path. Mirrors, reconciliation, CI, imports, discovery, cache
refresh, and other background work always use installation credentials.

Auth, signing, custody, and GitHub credential events write sanitized audit
records. Audit rows contain account and pubkey identity, authentication and
signer type, operation, GitHub actor and credential type where applicable,
correlation fields, result, and time. They never contain tokens, private keys,
OAuth state, WebAuthn responses, decrypted NIP-46 content, or transfer
ciphertext.
