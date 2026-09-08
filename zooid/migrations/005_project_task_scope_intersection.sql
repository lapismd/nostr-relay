-- Preserve every existing single-scope row while allowing a PR graph event to
-- retain both its repository and origin channels.
ALTER TABLE {{.Name}}__project_task_scopes
  DROP CONSTRAINT IF EXISTS {{.Name}}__project_task_scopes_pkey;

ALTER TABLE {{.Name}}__project_task_scopes
  ADD PRIMARY KEY (event_id, channel);
