CREATE TABLE IF NOT EXISTS {{.Name}}__managed_agents (
  pubkey TEXT PRIMARY KEY,
  owner_pubkey TEXT NOT NULL,
  relay_pubkey TEXT NOT NULL,
  status TEXT NOT NULL,
  profile_content TEXT NOT NULL,
  profile_created_at BIGINT NOT NULL,
  encrypted_secret_key TEXT NOT NULL,
  encrypted_data_key TEXT NOT NULL,
  encryption_version TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  activated_at TIMESTAMPTZ,
  owner_event JSONB,
  profile_event JSONB
);
CREATE INDEX IF NOT EXISTS {{.Name}}__managed_agents_owner_status
  ON {{.Name}}__managed_agents(owner_pubkey, status);

CREATE TABLE IF NOT EXISTS {{.Name}}__managed_agent_clients (
  agent_pubkey TEXT NOT NULL REFERENCES {{.Name}}__managed_agents(pubkey) ON DELETE CASCADE,
  client_pubkey TEXT NOT NULL,
  status TEXT NOT NULL,
  invitation_hash TEXT NOT NULL DEFAULT '',
  invitation_ciphertext TEXT NOT NULL DEFAULT '',
  invitation_data_key TEXT NOT NULL DEFAULT '',
  invitation_key_version TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL,
  paired_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  PRIMARY KEY (agent_pubkey, client_pubkey)
);
CREATE UNIQUE INDEX IF NOT EXISTS {{.Name}}__managed_agent_active_client
  ON {{.Name}}__managed_agent_clients(agent_pubkey) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS {{.Name}}__managed_agent_requests (
  agent_pubkey TEXT NOT NULL,
  client_pubkey TEXT NOT NULL,
  request_id TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  response_event JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (agent_pubkey, client_pubkey, request_id)
);

CREATE TABLE IF NOT EXISTS {{.Name}}__managed_agent_http_idempotency (
  owner_pubkey TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  response_json JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (owner_pubkey, idempotency_key)
);

CREATE TABLE IF NOT EXISTS {{.Name}}__managed_agent_auth_events (
  event_id TEXT PRIMARY KEY,
  used_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS {{.Name}}__managed_agent_audit (
  id BIGSERIAL PRIMARY KEY,
  owner_pubkey TEXT NOT NULL DEFAULT '',
  agent_pubkey TEXT NOT NULL DEFAULT '',
  client_pubkey TEXT NOT NULL DEFAULT '',
  operation TEXT NOT NULL,
  result TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL
);
