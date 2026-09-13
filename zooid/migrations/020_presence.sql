CREATE TABLE IF NOT EXISTS {{.Name}}__presence_accounts (
  pubkey TEXT PRIMARY KEY,
  state JSONB NOT NULL,
  expires_at BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS {{.Name}}__presence_accounts_expiry
  ON {{.Name}}__presence_accounts(expires_at) WHERE expires_at > 0;
CREATE TABLE IF NOT EXISTS {{.Name}}__presence_leases (
  pubkey TEXT NOT NULL REFERENCES {{.Name}}__presence_accounts(pubkey) ON DELETE CASCADE,
  connection_id UUID NOT NULL,
  lease JSONB NOT NULL,
  retain_until BIGINT NOT NULL,
  PRIMARY KEY (pubkey, connection_id)
);
CREATE INDEX IF NOT EXISTS {{.Name}}__presence_leases_connection
  ON {{.Name}}__presence_leases(connection_id);
CREATE INDEX IF NOT EXISTS {{.Name}}__presence_leases_cleanup
  ON {{.Name}}__presence_leases(retain_until);
