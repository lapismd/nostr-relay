-- Desired Git backing is a durable projection of current kind-30617 events.
-- It is intentionally separate from signed event authority and has no FK to
-- events, so deletion of a replaceable announcement cannot erase recovery
-- state before the reconciler observes the tombstone.
CREATE TABLE IF NOT EXISTS {{.Name}}__git_repository_intents (
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  announcement_id TEXT NOT NULL DEFAULT '',
  channel TEXT NOT NULL DEFAULT '',
  desired_class TEXT NOT NULL,
  generation BIGINT NOT NULL DEFAULT 1,
  reconcile_state TEXT NOT NULL,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  reconcile_after TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_error TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (owner, repo)
);

CREATE INDEX IF NOT EXISTS {{.Name}}__idx_git_repository_intents_due
  ON {{.Name}}__git_repository_intents(reconcile_state, reconcile_after);

-- WalGit's webhook is at-least-once. Keep its normative identity as the
-- primary key and maintain a separate current-ref projection for cheap future
-- activity, PR-revision, and CI consumers.
CREATE TABLE IF NOT EXISTS {{.Name}}__walgit_ref_events (
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  sequence BIGINT NOT NULL,
  ref_name TEXT NOT NULL,
  action TEXT NOT NULL,
  old_oid TEXT NOT NULL,
  new_oid TEXT NOT NULL,
  pusher TEXT NOT NULL DEFAULT '',
  correlation_id TEXT NOT NULL DEFAULT '',
  received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (owner, repo, sequence, ref_name)
);

CREATE TABLE IF NOT EXISTS {{.Name}}__walgit_refs (
  owner TEXT NOT NULL,
  repo TEXT NOT NULL,
  ref_name TEXT NOT NULL,
  oid TEXT NOT NULL,
  sequence BIGINT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (owner, repo, ref_name)
);
