-- Versioned named ruleset documents. Legacy pattern columns remain a compiled
-- projection so older readers can still see include targets.
ALTER TABLE {{.Name}}__repository_protection
  ADD COLUMN IF NOT EXISTS document JSONB NOT NULL DEFAULT '{}'::jsonb;
