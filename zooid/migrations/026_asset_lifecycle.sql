CREATE TABLE IF NOT EXISTS {{.Name}}__asset_references (
    owner_pubkey TEXT NOT NULL,
    reference_type TEXT NOT NULL,
    reference_key TEXT NOT NULL,
    slot TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    source_event_id TEXT NOT NULL,
    source_created_at BIGINT NOT NULL,
    PRIMARY KEY (reference_type, reference_key, slot)
);
CREATE INDEX IF NOT EXISTS {{.Name}}__idx_asset_references_owner_hash
    ON {{.Name}}__asset_references(owner_pubkey, sha256);

CREATE TABLE IF NOT EXISTS {{.Name}}__asset_history (
    id BIGSERIAL PRIMARY KEY,
    owner_pubkey TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    action TEXT NOT NULL,
    reference_type TEXT,
    reference_key TEXT,
    slot TEXT,
    source_event_id TEXT,
    occurred_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS {{.Name}}__idx_asset_history_owner_order
    ON {{.Name}}__asset_history(owner_pubkey, occurred_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS {{.Name}}__idx_asset_history_hash_order
    ON {{.Name}}__asset_history(sha256, occurred_at DESC, id DESC);

INSERT INTO {{.Name}}__asset_history (
    owner_pubkey, sha256, action, occurred_at
)
SELECT b.pubkey, b.sha256, 'uploaded', b.uploaded_at
FROM {{.Name}}__blossom_blobs b
WHERE NOT EXISTS (
    SELECT 1 FROM {{.Name}}__asset_history h
    WHERE h.owner_pubkey = b.pubkey AND h.sha256 = b.sha256
        AND h.action = 'uploaded' AND h.occurred_at = b.uploaded_at
);
