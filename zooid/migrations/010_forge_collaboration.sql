-- Durable collaboration processing metadata. Payloads remain provider data
-- and are never treated as signed Nostr authority.
ALTER TABLE {{.Name}}__forge_webhook_deliveries
  ADD COLUMN IF NOT EXISTS connection_id TEXT NOT NULL DEFAULT '';

ALTER TABLE {{.Name}}__forge_webhook_deliveries
  ADD COLUMN IF NOT EXISTS installation_id BIGINT NOT NULL DEFAULT 0;

ALTER TABLE {{.Name}}__forge_webhook_deliveries
  ADD COLUMN IF NOT EXISTS external_repository_id TEXT NOT NULL DEFAULT '';

ALTER TABLE {{.Name}}__forge_webhook_deliveries
  ADD COLUMN IF NOT EXISTS action TEXT NOT NULL DEFAULT '';

ALTER TABLE {{.Name}}__forge_webhook_deliveries
  ADD COLUMN IF NOT EXISTS external_object_id TEXT NOT NULL DEFAULT '';

ALTER TABLE {{.Name}}__forge_outbox
  ADD COLUMN IF NOT EXISTS actor TEXT NOT NULL DEFAULT '';

ALTER TABLE {{.Name}}__forge_outbox
  ADD COLUMN IF NOT EXISTS nostr_root_id TEXT NOT NULL DEFAULT '';

ALTER TABLE {{.Name}}__forge_outbox
  ADD COLUMN IF NOT EXISTS source_event_id TEXT NOT NULL DEFAULT '';

ALTER TABLE {{.Name}}__forge_outbox
  ADD COLUMN IF NOT EXISTS expected_head TEXT NOT NULL DEFAULT '';

ALTER TABLE {{.Name}}__forge_outbox
  ADD COLUMN IF NOT EXISTS provider_result JSONB;

ALTER TABLE {{.Name}}__forge_object_mappings
  ADD COLUMN IF NOT EXISTS correlation_marker TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS {{.Name}}__idx_forge_mapping_correlation
  ON {{.Name}}__forge_object_mappings(provider, connection_id, correlation_marker)
  WHERE correlation_marker <> '';

CREATE TABLE IF NOT EXISTS {{.Name}}__forge_provider_refs (
  provider TEXT NOT NULL,
  connection_id TEXT NOT NULL,
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  ref_name TEXT NOT NULL,
  commit_oid TEXT NOT NULL,
  observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (provider, connection_id, owner, repo, ref_name)
);
