-- Git authority and repository-content reads are independently selectable.
-- git_backend remains a compatibility shorthand for pre-replica clients.
ALTER TABLE {{.Name}}__repository_integrations
  ADD COLUMN IF NOT EXISTS git_authority_backend TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS content_backend TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS git_replication_target_backend TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS replica_enabled BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS replica_direction TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS replica_sync_branches BOOLEAN NOT NULL DEFAULT TRUE,
  ADD COLUMN IF NOT EXISTS replica_sync_tags BOOLEAN NOT NULL DEFAULT TRUE,
  ADD COLUMN IF NOT EXISTS replica_mode TEXT NOT NULL DEFAULT 'off',
  ADD COLUMN IF NOT EXISTS replica_read_policy TEXT NOT NULL DEFAULT 'fallback',
  ADD COLUMN IF NOT EXISTS replica_wait_milliseconds INTEGER NOT NULL DEFAULT 1500;

UPDATE {{.Name}}__repository_integrations
SET git_authority_backend = CASE WHEN git_authority_backend = '' THEN git_backend ELSE git_authority_backend END,
    content_backend = CASE WHEN content_backend = '' THEN git_backend ELSE content_backend END;

CREATE TABLE IF NOT EXISTS {{.Name}}__git_replicas (
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  authority_backend TEXT NOT NULL,
  replica_backend TEXT NOT NULL,
  connection_id TEXT NOT NULL,
  external_repository_id TEXT NOT NULL,
  default_branch TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  generation BIGINT NOT NULL DEFAULT 1,
  last_sync_at TIMESTAMPTZ,
  last_success_at TIMESTAMPTZ,
  last_authority_check_at TIMESTAMPTZ,
  last_reconcile_at TIMESTAMPTZ,
  last_error TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (owner, repo)
);

CREATE TABLE IF NOT EXISTS {{.Name}}__git_replica_refs (
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  ref_name TEXT NOT NULL,
  authority_oid TEXT NOT NULL,
  replica_oid TEXT NOT NULL DEFAULT '',
  source_delivery_id TEXT NOT NULL DEFAULT '',
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  verified_at TIMESTAMPTZ,
  PRIMARY KEY (owner, repo, ref_name)
);

CREATE TABLE IF NOT EXISTS {{.Name}}__git_replica_jobs (
  operation_id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  generation BIGINT NOT NULL,
  source_delivery_id TEXT NOT NULL DEFAULT '',
  requested_ref TEXT NOT NULL DEFAULT '',
  target_oid TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL,
  deduplication_key TEXT NOT NULL UNIQUE,
  processing_state TEXT NOT NULL DEFAULT 'pending',
  attempt_count INTEGER NOT NULL DEFAULT 0,
  process_after TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  lease_until TIMESTAMPTZ,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS {{.Name}}__idx_git_replica_jobs_due
  ON {{.Name}}__git_replica_jobs(processing_state, process_after);

CREATE TABLE IF NOT EXISTS {{.Name}}__git_replica_objects (
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  object_oid TEXT NOT NULL,
  reason TEXT NOT NULL,
  state TEXT NOT NULL,
  last_error TEXT NOT NULL DEFAULT '',
  requested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  verified_at TIMESTAMPTZ,
  PRIMARY KEY (owner, repo, object_oid)
);

-- WalGit emits the same durable WAL webhook for native pushes and internal
-- replica writes. Persist the distinction so later PR/CI/activity consumers
-- can fail closed without relying on a pusher-name convention at read time.
ALTER TABLE {{.Name}}__walgit_ref_events
  ADD COLUMN IF NOT EXISTS replica_generated BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS {{.Name}}__git_replication_jobs (
  operation_id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  generation BIGINT NOT NULL,
  source_sequence TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL,
  deduplication_key TEXT NOT NULL UNIQUE,
  processing_state TEXT NOT NULL DEFAULT 'pending',
  attempt_count INTEGER NOT NULL DEFAULT 0,
  process_after TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  lease_until TIMESTAMPTZ,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS {{.Name}}__idx_git_replication_jobs_due
  ON {{.Name}}__git_replication_jobs(processing_state, process_after);

CREATE TABLE IF NOT EXISTS {{.Name}}__git_replication_state (
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  target_backend TEXT NOT NULL,
  generation BIGINT NOT NULL,
  state TEXT NOT NULL DEFAULT 'pending',
  last_success_at TIMESTAMPTZ,
  last_reconcile_at TIMESTAMPTZ,
  last_error TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (owner, repo)
);
