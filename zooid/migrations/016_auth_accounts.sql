CREATE TABLE IF NOT EXISTS {{.Name}}__auth_accounts (
  account_id TEXT PRIMARY KEY,
  nostr_pubkey TEXT NOT NULL UNIQUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS {{.Name}}__auth_external_identities (
  account_id TEXT NOT NULL REFERENCES {{.Name}}__auth_accounts(account_id) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  provider_user_id TEXT NOT NULL,
  provider_login TEXT NOT NULL DEFAULT '',
  display_name TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (provider, provider_user_id),
  UNIQUE (account_id, provider)
);

CREATE TABLE IF NOT EXISTS {{.Name}}__auth_signer_bindings (
  account_id TEXT PRIMARY KEY REFERENCES {{.Name}}__auth_accounts(account_id) ON DELETE CASCADE,
  signer_type TEXT NOT NULL CHECK (signer_type IN ('managed', 'nip46', 'external')),
  public_key TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('active', 'migration_pending', 'disabled')),
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (account_id, public_key)
);

CREATE TABLE IF NOT EXISTS {{.Name}}__auth_managed_keys (
  account_id TEXT PRIMARY KEY REFERENCES {{.Name}}__auth_accounts(account_id) ON DELETE CASCADE,
  public_key TEXT NOT NULL,
  encrypted_private_key TEXT NOT NULL,
  encrypted_data_key TEXT NOT NULL,
  encryption_version TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  rotated_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS {{.Name}}__auth_sessions (
  session_id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES {{.Name}}__auth_accounts(account_id) ON DELETE CASCADE,
  token_hash BYTEA NOT NULL UNIQUE,
  auth_provider TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  idle_expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  revoked_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS {{.Name}}__idx_auth_sessions_account
  ON {{.Name}}__auth_sessions(account_id, expires_at)
  WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS {{.Name}}__auth_audit (
  audit_id TEXT PRIMARY KEY,
  account_id TEXT,
  nostr_pubkey TEXT NOT NULL DEFAULT '',
  auth_provider TEXT NOT NULL DEFAULT '',
  signer_type TEXT NOT NULL DEFAULT '',
  operation TEXT NOT NULL,
  github_actor_id TEXT NOT NULL DEFAULT '',
  github_credential_type TEXT NOT NULL DEFAULT '',
  source_event_id TEXT NOT NULL DEFAULT '',
  correlation_id TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)
