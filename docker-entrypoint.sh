#!/bin/sh
set -e

# Lapis NIP-29 Relay Entrypoint
# Generates config from environment variables if not present

CONFIG_DIR="${CONFIG:-/app/config}"
MEDIA_DIR="${MEDIA:-/app/media}"

# Default values
RELAY_HOST="${RELAY_HOST:-localhost}"
RELAY_SCHEMA="${RELAY_SCHEMA:-lapis_relay}"
RELAY_SECRET="${RELAY_SECRET:-}"
RELAY_NAME="${RELAY_NAME:-Lapis Relay}"
RELAY_DESCRIPTION="${RELAY_DESCRIPTION:-NIP-29 community relay for the Lapis registry}"
RELAY_PUBKEY="${RELAY_PUBKEY:-}"
ADMIN_PUBKEYS="${ADMIN_PUBKEYS:-}"
POLICY_OPEN="${POLICY_OPEN:-true}"
POLICY_ANONYMOUS_READ="${POLICY_ANONYMOUS_READ:-true}"
KEYTR_STORAGE_ENABLED="${KEYTR_STORAGE_ENABLED:-false}"
POLICY_PUBLIC_JOIN="${POLICY_PUBLIC_JOIN:-true}"
GROUPS_AUTO_JOIN="${GROUPS_AUTO_JOIN:-true}"
GROUPS_ADMIN_CREATE_ONLY="${GROUPS_ADMIN_CREATE_ONLY:-true}"
GROUPS_PRIVATE_ADMIN_ONLY="${GROUPS_PRIVATE_ADMIN_ONLY:-true}"
GROUPS_PRIVATE_RELAY_ADMIN_ACCESS="${GROUPS_PRIVATE_RELAY_ADMIN_ACCESS:-false}"
AGENTS_ENABLED="${AGENTS_ENABLED:-true}"
AGENTS_ALLOW_OWNER_AUTH="${AGENTS_ALLOW_OWNER_AUTH:-true}"
AGENTS_ENFORCE_CREDENTIAL_KINDS="${AGENTS_ENFORCE_CREDENTIAL_KINDS:-true}"
AGENTS_BUNKER_ENABLED="${AGENTS_BUNKER_ENABLED:-false}"
AGENTS_BUNKER_PUBLIC_URL="${AGENTS_BUNKER_PUBLIC_URL:-https://$RELAY_HOST}"
AGENTS_BUNKER_ALLOWED_BROWSER_ORIGINS="${AGENTS_BUNKER_ALLOWED_BROWSER_ORIGINS:-[]}"
AGENTS_BUNKER_CREDENTIAL_DIR="${AGENTS_BUNKER_CREDENTIAL_DIR:-/run/secrets}"
AGENTS_BUNKER_MASTER_KEY_REF="${AGENTS_BUNKER_MASTER_KEY_REF:-file:lapis-agent-bunker-master-key}"
AGENTS_BUNKER_MASTER_KEY_VERSION="${AGENTS_BUNKER_MASTER_KEY_VERSION:-v1}"
AGENTS_BUNKER_INVITATION_LIFETIME="${AGENTS_BUNKER_INVITATION_LIFETIME:-24h}"
AGENTS_BUNKER_MAX_ACTIVE_PER_OWNER="${AGENTS_BUNKER_MAX_ACTIVE_PER_OWNER:-10}"
AGENTS_BUNKER_MAX_PENDING_PER_OWNER="${AGENTS_BUNKER_MAX_PENDING_PER_OWNER:-3}"
AGENTS_BUNKER_CREATIONS_PER_HOUR="${AGENTS_BUNKER_CREATIONS_PER_HOUR:-5}"
AGENTS_BUNKER_REQUESTS_PER_MINUTE="${AGENTS_BUNKER_REQUESTS_PER_MINUTE:-120}"
BLOSSOM_ENABLED="${BLOSSOM_ENABLED:-true}"
BLOSSOM_PUBLIC_URL="${BLOSSOM_PUBLIC_URL:-https://$RELAY_HOST}"
REGISTRY_POLICY_ENABLED="${REGISTRY_POLICY_ENABLED:-false}"
REGISTRY_RELEASE_GROUP_ID="${REGISTRY_RELEASE_GROUP_ID:-}"
REGISTRY_CURATOR_ROLE="${REGISTRY_CURATOR_ROLE:-registry-curator}"
GIT_ENABLED="${GIT_ENABLED:-false}"
GIT_PUBLIC_URL="${GIT_PUBLIC_URL:-}"
GIT_BACKEND_URL="${GIT_BACKEND_URL:-}"
GIT_RECONCILE_INTERVAL="${GIT_RECONCILE_INTERVAL:-5s}"
GIT_MAX_DIFF_BYTES="${GIT_MAX_DIFF_BYTES:-8388608}"
GIT_MAX_DIFF_FILES="${GIT_MAX_DIFF_FILES:-2000}"
GITHUB_ENABLED="${GITHUB_ENABLED:-false}"
GITHUB_CREDENTIAL_DIR="${GITHUB_CREDENTIAL_DIR:-/run/secrets}"
GITHUB_WORKER_INTERVAL="${GITHUB_WORKER_INTERVAL:-2s}"
GITHUB_RECONCILE_INTERVAL="${GITHUB_RECONCILE_INTERVAL:-5m}"
GITHUB_MAX_WEBHOOK_BYTES="${GITHUB_MAX_WEBHOOK_BYTES:-4194304}"
AUTH_ENABLED="${AUTH_ENABLED:-false}"
AUTH_PUBLIC_URL="${AUTH_PUBLIC_URL:-https://$RELAY_HOST}"
AUTH_ALLOWED_BROWSER_ORIGINS="${AUTH_ALLOWED_BROWSER_ORIGINS:-[]}"
AUTH_WEBAUTHN_RP_ID="${AUTH_WEBAUTHN_RP_ID:-}"
AUTH_WEBAUTHN_ORIGINS="${AUTH_WEBAUTHN_ORIGINS:-[]}"
AUTH_CREDENTIAL_DIR="${AUTH_CREDENTIAL_DIR:-/run/secrets}"
AUTH_MASTER_KEY_REF="${AUTH_MASTER_KEY_REF:-file:lapis-auth-master-key}"
AUTH_MASTER_KEY_VERSION="${AUTH_MASTER_KEY_VERSION:-v1}"
AUTH_SESSION_LIFETIME="${AUTH_SESSION_LIFETIME:-168h}"
AUTH_SESSION_IDLE="${AUTH_SESSION_IDLE:-24h}"
AUTH_CHALLENGE_TTL="${AUTH_CHALLENGE_TTL:-10m}"
AUTH_GITHUB_CLIENT_ID="${AUTH_GITHUB_CLIENT_ID:-}"
AUTH_GITHUB_CLIENT_SECRET_REF="${AUTH_GITHUB_CLIENT_SECRET_REF:-}"
AUTH_GOOGLE_CLIENT_ID="${AUTH_GOOGLE_CLIENT_ID:-}"
AUTH_GOOGLE_CLIENT_SECRET_REF="${AUTH_GOOGLE_CLIENT_SECRET_REF:-}"
AUTH_TEST_GITHUB_AUTHORIZATION_URL="${AUTH_TEST_GITHUB_AUTHORIZATION_URL:-}"
AUTH_TEST_GITHUB_TOKEN_URL="${AUTH_TEST_GITHUB_TOKEN_URL:-}"
AUTH_TEST_GITHUB_USER_URL="${AUTH_TEST_GITHUB_USER_URL:-}"
AUTH_TEST_GOOGLE_ISSUER_URL="${AUTH_TEST_GOOGLE_ISSUER_URL:-}"
FORGE_WORKER_MODE="${FORGE_WORKER_MODE:-embedded}"
LAPIS_PROCESS_BIN="${LAPIS_PROCESS_BIN:-/bin/lapis-relay}"

# Create directories
mkdir -p "$CONFIG_DIR" "$MEDIA_DIR"

# Validate DATABASE_URL
if [ -z "$DATABASE_URL" ]; then
    echo "ERROR: DATABASE_URL environment variable is required"
    exit 1
fi

# Generate config file if it doesn't exist
CONFIG_FILE="$CONFIG_DIR/$RELAY_HOST"

if [ ! -f "$CONFIG_FILE" ]; then
    echo "Generating config for host: $RELAY_HOST"

    if [ -z "$RELAY_SECRET" ]; then
        echo "ERROR: RELAY_SECRET environment variable is required"
        exit 1
    fi

    cat > "$CONFIG_FILE" << EOF
# Auto-generated Zooid configuration
host = "$RELAY_HOST"
schema = "$RELAY_SCHEMA"
secret = "$RELAY_SECRET"

[info]
name = "$RELAY_NAME"
description = "$RELAY_DESCRIPTION"
EOF

    if [ -n "$RELAY_PUBKEY" ]; then
        echo "pubkey = \"$RELAY_PUBKEY\"" >> "$CONFIG_FILE"
    fi

    cat >> "$CONFIG_FILE" << EOF

[policy]
open = $POLICY_OPEN
anonymous_read = $POLICY_ANONYMOUS_READ
keytr_storage = $KEYTR_STORAGE_ENABLED
public_join = $POLICY_PUBLIC_JOIN
strip_signatures = false

[groups]
enabled = true
auto_join = $GROUPS_AUTO_JOIN
admin_create_only = $GROUPS_ADMIN_CREATE_ONLY
private_admin_only = $GROUPS_PRIVATE_ADMIN_ONLY
private_relay_admin_access = $GROUPS_PRIVATE_RELAY_ADMIN_ACCESS

[agents]
enabled = $AGENTS_ENABLED
allow_owner_auth = $AGENTS_ALLOW_OWNER_AUTH
enforce_credential_kinds = $AGENTS_ENFORCE_CREDENTIAL_KINDS

[agents.bunker]
enabled = $AGENTS_BUNKER_ENABLED
public_url = "$AGENTS_BUNKER_PUBLIC_URL"
allowed_browser_origins = $AGENTS_BUNKER_ALLOWED_BROWSER_ORIGINS
credential_dir = "$AGENTS_BUNKER_CREDENTIAL_DIR"
master_key_ref = "$AGENTS_BUNKER_MASTER_KEY_REF"
master_key_version = "$AGENTS_BUNKER_MASTER_KEY_VERSION"
invitation_lifetime = "$AGENTS_BUNKER_INVITATION_LIFETIME"
max_active_per_owner = $AGENTS_BUNKER_MAX_ACTIVE_PER_OWNER
max_pending_per_owner = $AGENTS_BUNKER_MAX_PENDING_PER_OWNER
creations_per_hour = $AGENTS_BUNKER_CREATIONS_PER_HOUR
requests_per_minute = $AGENTS_BUNKER_REQUESTS_PER_MINUTE

[blossom]
enabled = $BLOSSOM_ENABLED
public_url = "$BLOSSOM_PUBLIC_URL"

[registry]
enabled = $REGISTRY_POLICY_ENABLED
release_group_id = "$REGISTRY_RELEASE_GROUP_ID"
curator_role = "$REGISTRY_CURATOR_ROLE"

[git]
enabled = $GIT_ENABLED
public_url = "$GIT_PUBLIC_URL"
backend_url = "$GIT_BACKEND_URL"
reconcile_interval = "$GIT_RECONCILE_INTERVAL"
max_diff_bytes = $GIT_MAX_DIFF_BYTES
max_diff_files = $GIT_MAX_DIFF_FILES

[github]
enabled = $GITHUB_ENABLED
credential_dir = "$GITHUB_CREDENTIAL_DIR"
worker_interval = "$GITHUB_WORKER_INTERVAL"
reconcile_interval = "$GITHUB_RECONCILE_INTERVAL"
max_webhook_bytes = $GITHUB_MAX_WEBHOOK_BYTES

[auth]
enabled = $AUTH_ENABLED
public_url = "$AUTH_PUBLIC_URL"
allowed_browser_origins = $AUTH_ALLOWED_BROWSER_ORIGINS
webauthn_rp_id = "$AUTH_WEBAUTHN_RP_ID"
webauthn_origins = $AUTH_WEBAUTHN_ORIGINS
credential_dir = "$AUTH_CREDENTIAL_DIR"
master_key_ref = "$AUTH_MASTER_KEY_REF"
master_key_version = "$AUTH_MASTER_KEY_VERSION"
session_lifetime = "$AUTH_SESSION_LIFETIME"
session_idle = "$AUTH_SESSION_IDLE"
challenge_ttl = "$AUTH_CHALLENGE_TTL"

[auth.github]
client_id = "$AUTH_GITHUB_CLIENT_ID"
client_secret_ref = "$AUTH_GITHUB_CLIENT_SECRET_REF"

[auth.google]
client_id = "$AUTH_GOOGLE_CLIENT_ID"
client_secret_ref = "$AUTH_GOOGLE_CLIENT_SECRET_REF"

[auth.testing]
github_authorization_url = "$AUTH_TEST_GITHUB_AUTHORIZATION_URL"
github_token_url = "$AUTH_TEST_GITHUB_TOKEN_URL"
github_user_url = "$AUTH_TEST_GITHUB_USER_URL"
google_issuer_url = "$AUTH_TEST_GOOGLE_ISSUER_URL"
EOF

    # Add admin role if pubkeys provided
    if [ -n "$ADMIN_PUBKEYS" ]; then
        cat >> "$CONFIG_FILE" << EOF

[roles.admin]
pubkeys = [$ADMIN_PUBKEYS]
can_invite = true
can_manage = true
EOF
    fi

    cat >> "$CONFIG_FILE" << EOF

[roles.member]
can_invite = true
EOF

    echo "Config generated at: $CONFIG_FILE"
else
    echo "Using existing config: $CONFIG_FILE"

    # Compose volumes commonly retain an older auto-generated config across
    # image upgrades. Add the optional section once without rewriting operator-
    # managed files or persisting either WalGit runtime secret.
    if [ "$GIT_ENABLED" = "true" ] \
        && grep -q '^# Auto-generated Zooid configuration$' "$CONFIG_FILE" \
        && ! grep -q '^\[git\]$' "$CONFIG_FILE"; then
        cat >> "$CONFIG_FILE" << EOF

[git]
enabled = true
public_url = "$GIT_PUBLIC_URL"
backend_url = "$GIT_BACKEND_URL"
reconcile_interval = "$GIT_RECONCILE_INTERVAL"
max_diff_bytes = $GIT_MAX_DIFF_BYTES
max_diff_files = $GIT_MAX_DIFF_FILES

[github]
enabled = $GITHUB_ENABLED
credential_dir = "$GITHUB_CREDENTIAL_DIR"
worker_interval = "$GITHUB_WORKER_INTERVAL"
reconcile_interval = "$GITHUB_RECONCILE_INTERVAL"
max_webhook_bytes = $GITHUB_MAX_WEBHOOK_BYTES
EOF
        echo "Added Git configuration to existing auto-generated config"
    fi

	if [ "$GITHUB_ENABLED" = "true" ] \
		&& grep -q '^# Auto-generated Zooid configuration$' "$CONFIG_FILE" \
		&& ! grep -q '^\[github\]$' "$CONFIG_FILE"; then
		cat >> "$CONFIG_FILE" << EOF

[github]
enabled = true
credential_dir = "$GITHUB_CREDENTIAL_DIR"
worker_interval = "$GITHUB_WORKER_INTERVAL"
reconcile_interval = "$GITHUB_RECONCILE_INTERVAL"
max_webhook_bytes = $GITHUB_MAX_WEBHOOK_BYTES
EOF
		echo "Added GitHub configuration to existing auto-generated config"
	fi

	if [ "$AUTH_ENABLED" = "true" ] \
		&& grep -q '^# Auto-generated Zooid configuration$' "$CONFIG_FILE" \
		&& ! grep -q '^\[auth\]$' "$CONFIG_FILE"; then
		cat >> "$CONFIG_FILE" << EOF

[auth]
enabled = true
public_url = "$AUTH_PUBLIC_URL"
allowed_browser_origins = $AUTH_ALLOWED_BROWSER_ORIGINS
webauthn_rp_id = "$AUTH_WEBAUTHN_RP_ID"
webauthn_origins = $AUTH_WEBAUTHN_ORIGINS
credential_dir = "$AUTH_CREDENTIAL_DIR"
master_key_ref = "$AUTH_MASTER_KEY_REF"
master_key_version = "$AUTH_MASTER_KEY_VERSION"
session_lifetime = "$AUTH_SESSION_LIFETIME"
session_idle = "$AUTH_SESSION_IDLE"
challenge_ttl = "$AUTH_CHALLENGE_TTL"

[auth.github]
client_id = "$AUTH_GITHUB_CLIENT_ID"
client_secret_ref = "$AUTH_GITHUB_CLIENT_SECRET_REF"

[auth.google]
client_id = "$AUTH_GOOGLE_CLIENT_ID"
client_secret_ref = "$AUTH_GOOGLE_CLIENT_SECRET_REF"

[auth.testing]
github_authorization_url = "$AUTH_TEST_GITHUB_AUTHORIZATION_URL"
github_token_url = "$AUTH_TEST_GITHUB_TOKEN_URL"
github_user_url = "$AUTH_TEST_GITHUB_USER_URL"
google_issuer_url = "$AUTH_TEST_GOOGLE_ISSUER_URL"
EOF
		echo "Added authentication configuration to existing auto-generated config"
	fi

	if [ "$AGENTS_BUNKER_ENABLED" = "true" ] \
		&& grep -q '^# Auto-generated Zooid configuration$' "$CONFIG_FILE" \
		&& ! grep -q '^\[agents.bunker\]$' "$CONFIG_FILE"; then
		cat >> "$CONFIG_FILE" << EOF

[agents.bunker]
enabled = true
public_url = "$AGENTS_BUNKER_PUBLIC_URL"
allowed_browser_origins = $AGENTS_BUNKER_ALLOWED_BROWSER_ORIGINS
credential_dir = "$AGENTS_BUNKER_CREDENTIAL_DIR"
master_key_ref = "$AGENTS_BUNKER_MASTER_KEY_REF"
master_key_version = "$AGENTS_BUNKER_MASTER_KEY_VERSION"
invitation_lifetime = "$AGENTS_BUNKER_INVITATION_LIFETIME"
max_active_per_owner = $AGENTS_BUNKER_MAX_ACTIVE_PER_OWNER
max_pending_per_owner = $AGENTS_BUNKER_MAX_PENDING_PER_OWNER
creations_per_hour = $AGENTS_BUNKER_CREATIONS_PER_HOUR
requests_per_minute = $AGENTS_BUNKER_REQUESTS_PER_MINUTE
EOF
		echo "Added managed agent bunker configuration to existing auto-generated config"
	fi
fi

echo ""
echo "Starting Lapis process..."
export FORGE_WORKER_MODE
exec "$LAPIS_PROCESS_BIN" "$@"
