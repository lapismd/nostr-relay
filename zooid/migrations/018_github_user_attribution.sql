-- Preserve the selected GitHub credential for durable retries. Existing and
-- system operations continue to use the GitHub App installation identity.
ALTER TABLE {{.Name}}__forge_outbox
  ADD COLUMN IF NOT EXISTS credential_mode TEXT NOT NULL DEFAULT 'system';

ALTER TABLE {{.Name}}__forge_outbox
  DROP CONSTRAINT IF EXISTS {{.Name}}__forge_outbox_credential_mode_check;

ALTER TABLE {{.Name}}__forge_outbox
  ADD CONSTRAINT {{.Name}}__forge_outbox_credential_mode_check
  CHECK (credential_mode IN ('system', 'user'));
