CREATE TABLE IF NOT EXISTS {{.Name}}__forge_migrations (
  operation_id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  source_announcement_id TEXT NOT NULL,
  source_connection_id TEXT NOT NULL,
  source_repository_id TEXT NOT NULL,
  source_refs JSONB,
  keep_github_collaboration BOOLEAN NOT NULL DEFAULT FALSE,
  retain_github_mirror BOOLEAN NOT NULL DEFAULT FALSE,
  state TEXT NOT NULL,
  sanitized_failure TEXT NOT NULL DEFAULT '',
  target_announcement_id TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  completed_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS {{.Name}}__idx_forge_active_migration
  ON {{.Name}}__forge_migrations(owner, repo)
  WHERE state IN ('pending', 'importing', 'awaiting-owner-announcement');
