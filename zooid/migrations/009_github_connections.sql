ALTER TABLE {{.Name}}__forge_connections
  ADD COLUMN IF NOT EXISTS app_id TEXT NOT NULL DEFAULT '';

ALTER TABLE {{.Name}}__forge_connections
  ADD COLUMN IF NOT EXISTS webhook_secret_ref TEXT NOT NULL DEFAULT '';

ALTER TABLE {{.Name}}__forge_connections
  ADD COLUMN IF NOT EXISTS bridge_credential_ref TEXT NOT NULL DEFAULT '';
