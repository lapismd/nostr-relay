ALTER TABLE {{.Name}}__repository_integrations
  ADD COLUMN IF NOT EXISTS ci_connection_id TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS ci_external_repository_id TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS ci_external_repository TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS ci_workflow TEXT NOT NULL DEFAULT '';

ALTER TABLE {{.Name}}__forge_ci_runs
  ADD COLUMN IF NOT EXISTS inputs JSONB NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN IF NOT EXISTS sanitized_failure TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS activity_event_id TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS provider_updated_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS {{.Name}}__idx_forge_ci_runs_active
  ON {{.Name}}__forge_ci_runs(status, updated_at)
  WHERE status NOT IN ('success', 'failure', 'cancelled', 'timed_out', 'action_required', 'neutral', 'skipped', 'stale', 'error');
