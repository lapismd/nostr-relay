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
POLICY_PUBLIC_JOIN="${POLICY_PUBLIC_JOIN:-true}"
GROUPS_AUTO_JOIN="${GROUPS_AUTO_JOIN:-true}"
GROUPS_ADMIN_CREATE_ONLY="${GROUPS_ADMIN_CREATE_ONLY:-true}"
GROUPS_PRIVATE_ADMIN_ONLY="${GROUPS_PRIVATE_ADMIN_ONLY:-true}"
GROUPS_PRIVATE_RELAY_ADMIN_ACCESS="${GROUPS_PRIVATE_RELAY_ADMIN_ACCESS:-false}"
BLOSSOM_ENABLED="${BLOSSOM_ENABLED:-true}"
BLOSSOM_PUBLIC_URL="${BLOSSOM_PUBLIC_URL:-https://$RELAY_HOST}"

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
public_join = $POLICY_PUBLIC_JOIN
strip_signatures = false

[groups]
enabled = true
auto_join = $GROUPS_AUTO_JOIN
admin_create_only = $GROUPS_ADMIN_CREATE_ONLY
private_admin_only = $GROUPS_PRIVATE_ADMIN_ONLY
private_relay_admin_access = $GROUPS_PRIVATE_RELAY_ADMIN_ACCESS

[blossom]
enabled = $BLOSSOM_ENABLED
public_url = "$BLOSSOM_PUBLIC_URL"
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
fi

echo ""
echo "Starting Lapis relay..."
exec /bin/lapis-relay "$@"
