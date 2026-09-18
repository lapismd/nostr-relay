-- Relay-owned hosted-repository protection document. Deleting Git files or
-- announcement tags cannot disable the live default branch.
CREATE TABLE IF NOT EXISTS {{.Name}}__repository_protection (
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  protected_patterns JSONB NOT NULL DEFAULT '[]'::jsonb,
  required_approvals INTEGER NOT NULL DEFAULT 1,
  admin_bypass BOOLEAN NOT NULL DEFAULT FALSE,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (owner, repo)
);
