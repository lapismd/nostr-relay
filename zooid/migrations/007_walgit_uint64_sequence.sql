-- WalGit serializes WAL sequence numbers as decimal uint64 strings. PostgreSQL
-- BIGINT is signed, so widen the projection before accepting webhooks whose
-- sequence exceeds MaxInt64.
ALTER TABLE {{.Name}}__walgit_ref_events
  ALTER COLUMN sequence TYPE NUMERIC(20, 0) USING sequence::numeric;

ALTER TABLE {{.Name}}__walgit_refs
  ALTER COLUMN sequence TYPE NUMERIC(20, 0) USING sequence::numeric;
