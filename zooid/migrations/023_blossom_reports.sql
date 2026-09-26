CREATE TABLE IF NOT EXISTS {{.Name}}__blossom_reports (
    event_id TEXT PRIMARY KEY,
    pubkey TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    event_json JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'open',
    reviewed_by TEXT,
    reviewed_at BIGINT
);
CREATE INDEX IF NOT EXISTS {{.Name}}__idx_blossom_reports_status_created ON {{.Name}}__blossom_reports(status, created_at DESC, event_id DESC);
