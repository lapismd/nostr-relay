-- Durable derived NIP-29 scope for project/task events. The signed event stays
-- unchanged. This sidecar survives restarts and keeps kind-5 tombstones scoped
-- after the WebSocket deletion path removes their addressed target.
CREATE TABLE IF NOT EXISTS {{.Name}}__project_task_scopes (
  event_id TEXT PRIMARY KEY REFERENCES {{.Name}}__events(id) ON DELETE CASCADE,
  channel TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS {{.Name}}__idx_project_task_scopes_channel
  ON {{.Name}}__project_task_scopes(channel);

-- Direct bindings can be backfilled in SQL. Derived issue/comment/status scope
-- is populated lazily by the resolver on first read.
INSERT INTO {{.Name}}__project_task_scopes (event_id, channel)
SELECT events.id, tag.value ->> 1
FROM {{.Name}}__events AS events
CROSS JOIN LATERAL jsonb_array_elements(events.tags::jsonb) AS tag(value)
WHERE jsonb_typeof(tag.value) = 'array'
  AND jsonb_array_length(tag.value) >= 2
  AND tag.value ->> 0 = 'buzz-channel'
  AND tag.value ->> 1 <> ''
ON CONFLICT (event_id) DO NOTHING;
