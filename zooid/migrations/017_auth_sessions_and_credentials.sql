CREATE TABLE IF NOT EXISTS {{.Name}}__auth_oauth_flows (
  state_hash BYTEA PRIMARY KEY,
  provider TEXT NOT NULL,
  verifier TEXT NOT NULL,
  nonce TEXT NOT NULL,
  intent TEXT NOT NULL,
  account_id TEXT NOT NULL DEFAULT '',
  return_to TEXT NOT NULL DEFAULT '/',
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS {{.Name}}__auth_nostr_challenges (
  challenge_hash BYTEA PRIMARY KEY,
  nostr_pubkey TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS {{.Name}}__auth_github_credentials (
  account_id TEXT PRIMARY KEY REFERENCES {{.Name}}__auth_accounts(account_id) ON DELETE CASCADE,
  github_user_id TEXT NOT NULL UNIQUE,
  github_login TEXT NOT NULL,
  encrypted_access_token TEXT NOT NULL,
  encrypted_refresh_token TEXT NOT NULL DEFAULT '',
  access_token_expires_at TIMESTAMPTZ,
  refresh_token_expires_at TIMESTAMPTZ,
  app_client_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('active', 'reconnect_required', 'disconnected')),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS {{.Name}}__idx_auth_oauth_expiry
  ON {{.Name}}__auth_oauth_flows(expires_at);

CREATE INDEX IF NOT EXISTS {{.Name}}__idx_auth_challenge_expiry
  ON {{.Name}}__auth_nostr_challenges(expires_at)
