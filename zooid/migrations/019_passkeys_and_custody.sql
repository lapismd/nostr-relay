CREATE TABLE IF NOT EXISTS {{.Name}}__auth_passkeys (
  account_id TEXT NOT NULL REFERENCES {{.Name}}__auth_accounts(account_id) ON DELETE CASCADE,
  credential_id_hash BYTEA NOT NULL,
  encrypted_credential TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_used_at TIMESTAMPTZ,
  PRIMARY KEY (account_id, credential_id_hash),
  UNIQUE (credential_id_hash)
);

CREATE TABLE IF NOT EXISTS {{.Name}}__auth_webauthn_flows (
  flow_hash BYTEA PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES {{.Name}}__auth_accounts(account_id) ON DELETE CASCADE,
  flow_kind TEXT NOT NULL CHECK (flow_kind IN ('register', 'custody_transfer')),
  encrypted_session TEXT NOT NULL,
  device_public_key BYTEA,
  expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS {{.Name}}__idx_auth_webauthn_flows_expiry
  ON {{.Name}}__auth_webauthn_flows(expires_at);

CREATE TABLE IF NOT EXISTS {{.Name}}__auth_custody_transfers (
  transfer_id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES {{.Name}}__auth_accounts(account_id) ON DELETE CASCADE,
  challenge_hash BYTEA NOT NULL,
  transfer_state TEXT NOT NULL CHECK (transfer_state IN ('key_received_by_device', 'complete', 'cancelled')),
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  completed_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS {{.Name}}__idx_auth_custody_active_account
  ON {{.Name}}__auth_custody_transfers(account_id)
  WHERE transfer_state = 'key_received_by_device';
