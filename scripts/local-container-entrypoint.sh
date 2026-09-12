#!/bin/sh
# Only the local Compose project uses this adapter. Files are mounted read-only.
set -eu
read_secret() {
  if [ ! -s "/run/secrets/$1" ]; then
    echo "Missing local credential file: $1" >&2
    exit 1
  fi
  cat "/run/secrets/$1"
}
case "$1" in
  relay)
    RELAY_SECRET="$(read_secret relay_secret)"
    export RELAY_SECRET
    DATABASE_URL="$(read_secret database_url)"
    export DATABASE_URL
    WALGIT_RELAY_TOKEN="$(read_secret walgit_relay_token)"
    export WALGIT_RELAY_TOKEN
    WALGIT_WEBHOOK_SECRET="$(read_secret walgit_webhook_secret)"
    export WALGIT_WEBHOOK_SECRET
    exec /docker-entrypoint.sh
    ;;
  walgit)
    WALGIT_RELAY_TOKEN="$(read_secret walgit_relay_token)"
    export WALGIT_RELAY_TOKEN
    WALGIT__EVENTS__WEBHOOK_SECRET="$(read_secret walgit_webhook_secret)"
    export WALGIT__EVENTS__WEBHOOK_SECRET
    S3_ACCESS_KEY_ID="$(read_secret s3_access_key_id)"
    export S3_ACCESS_KEY_ID
    S3_SECRET_ACCESS_KEY="$(read_secret s3_secret_access_key)"
    export S3_SECRET_ACCESS_KEY
    exec tini -- walgit serve
    ;;
  rustfs)
    RUSTFS_ACCESS_KEY="$(read_secret s3_access_key_id)"
    export RUSTFS_ACCESS_KEY
    RUSTFS_SECRET_KEY="$(read_secret s3_secret_access_key)"
    export RUSTFS_SECRET_KEY
    exec /entrypoint.sh rustfs
    ;;
  bucket)
    s3_access="$(read_secret s3_access_key_id)"
    s3_secret="$(read_secret s3_secret_access_key)"
    rc alias set --quiet --region us-east-1 --bucket-lookup path local http://rustfs:9000 "$s3_access" "$s3_secret"
    exec rc bucket create --quiet --ignore-existing local/lapis-walgit
    ;;
  *) echo "Unknown local service" >&2; exit 2 ;;
esac
