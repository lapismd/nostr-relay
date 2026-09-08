-- Provider-neutral repository composition. Signed Nostr announcements remain
-- authoritative. These rows are restart-safe operational configuration only.
CREATE TABLE IF NOT EXISTS {{.Name}}__forge_connections (
  id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  host TEXT NOT NULL,
  credential_ref TEXT NOT NULL,
  installation_id TEXT NOT NULL DEFAULT '',
  account_id TEXT NOT NULL DEFAULT '',
  account_login TEXT NOT NULL DEFAULT '',
  bridge_pubkey TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',
  last_error TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (provider, host, installation_id)
);

CREATE TABLE IF NOT EXISTS {{.Name}}__repository_integrations (
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  announcement_id TEXT NOT NULL DEFAULT '',
  configuration_source TEXT NOT NULL,
  git_backend TEXT NOT NULL,
  collaboration_backend TEXT NOT NULL DEFAULT 'relay',
  mirror_backend TEXT NOT NULL DEFAULT '',
  ci_backend TEXT NOT NULL DEFAULT '',
  connection_id TEXT NOT NULL DEFAULT '',
  external_repository_id TEXT NOT NULL DEFAULT '',
  external_repository TEXT NOT NULL DEFAULT '',
  generation BIGINT NOT NULL DEFAULT 1,
  health_state TEXT NOT NULL DEFAULT 'unknown',
  health_detail TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (owner, repo)
);

CREATE INDEX IF NOT EXISTS {{.Name}}__idx_repository_integrations_connection
  ON {{.Name}}__repository_integrations(connection_id, external_repository_id);

-- Backfill the landed classification without rewriting or resigning events.
INSERT INTO {{.Name}}__repository_integrations
  (owner, repo, announcement_id, configuration_source, git_backend,
   collaboration_backend, generation, health_state)
SELECT owner, repo, announcement_id, 'legacy-default',
       CASE desired_class
         WHEN 'hosted' THEN 'walgit'
         WHEN 'external' THEN 'unmanaged'
         ELSE 'none'
       END,
       'relay', generation,
       CASE
         WHEN desired_class = 'hosted' AND reconcile_state = 'ready' THEN 'ready'
         WHEN desired_class = 'hosted' THEN 'provisioning'
         WHEN desired_class IN ('external', 'anchor') THEN 'ready'
         ELSE 'inactive'
       END
FROM {{.Name}}__git_repository_intents
ON CONFLICT (owner, repo) DO NOTHING;

CREATE TABLE IF NOT EXISTS {{.Name}}__forge_object_mappings (
  provider TEXT NOT NULL,
  connection_id TEXT NOT NULL,
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  object_type TEXT NOT NULL,
  nostr_event_id TEXT NOT NULL DEFAULT '',
  nostr_root_id TEXT NOT NULL DEFAULT '',
  external_id TEXT NOT NULL,
  external_number BIGINT,
  origin TEXT NOT NULL,
  authority TEXT NOT NULL,
  external_url TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (provider, connection_id, external_id, object_type)
);

CREATE UNIQUE INDEX IF NOT EXISTS {{.Name}}__idx_forge_mapping_nostr
  ON {{.Name}}__forge_object_mappings(provider, connection_id, nostr_event_id, object_type)
  WHERE nostr_event_id <> '';

CREATE TABLE IF NOT EXISTS {{.Name}}__forge_webhook_deliveries (
  provider TEXT NOT NULL,
  delivery_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  payload BYTEA NOT NULL,
  payload_sha256 TEXT NOT NULL,
  processing_state TEXT NOT NULL DEFAULT 'pending',
  attempt_count INTEGER NOT NULL DEFAULT 0,
  process_after TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  lease_until TIMESTAMPTZ,
  last_error TEXT NOT NULL DEFAULT '',
  received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  processed_at TIMESTAMPTZ,
  PRIMARY KEY (provider, delivery_id)
);

CREATE INDEX IF NOT EXISTS {{.Name}}__idx_forge_webhooks_due
  ON {{.Name}}__forge_webhook_deliveries(processing_state, process_after);

CREATE TABLE IF NOT EXISTS {{.Name}}__forge_outbox (
  operation_id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  connection_id TEXT NOT NULL DEFAULT '',
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  command TEXT NOT NULL,
  object_type TEXT NOT NULL,
  payload JSONB NOT NULL,
  idempotency_key TEXT NOT NULL UNIQUE,
  correlation_marker TEXT NOT NULL DEFAULT '',
  processing_state TEXT NOT NULL DEFAULT 'pending',
  attempt_count INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  lease_until TIMESTAMPTZ,
  provider_object_id TEXT NOT NULL DEFAULT '',
  sanitized_failure TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS {{.Name}}__idx_forge_outbox_due
  ON {{.Name}}__forge_outbox(processing_state, next_attempt_at);

CREATE TABLE IF NOT EXISTS {{.Name}}__forge_reconciliation_cursors (
  provider TEXT NOT NULL,
  connection_id TEXT NOT NULL,
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  object_family TEXT NOT NULL,
  cursor TEXT NOT NULL DEFAULT '',
  overlap_from TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (provider, connection_id, owner, repo, object_family)
);

CREATE TABLE IF NOT EXISTS {{.Name}}__forge_ci_runs (
  operation_id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  connection_id TEXT NOT NULL,
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  pull_request_event_id TEXT NOT NULL DEFAULT '',
  commit_oid TEXT NOT NULL,
  workflow TEXT NOT NULL,
  provider_run_id TEXT NOT NULL DEFAULT '',
  provider_url TEXT NOT NULL DEFAULT '',
  mirror_ref TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  cleanup_state TEXT NOT NULL DEFAULT 'not-required',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS {{.Name}}__forge_mirror_refs (
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  provider TEXT NOT NULL,
  connection_id TEXT NOT NULL,
  external_repository_id TEXT NOT NULL,
  source_oid TEXT NOT NULL,
  mirror_ref TEXT NOT NULL,
  provider_oid TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  cleanup_state TEXT NOT NULL DEFAULT 'pending',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (owner, repo, provider, connection_id, mirror_ref)
);
