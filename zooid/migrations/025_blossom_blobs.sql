CREATE TABLE IF NOT EXISTS {{.Name}}__blossom_blobs (
    pubkey TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    uploaded_at BIGINT NOT NULL,
    mime_type TEXT NOT NULL,
    size BIGINT NOT NULL,
    PRIMARY KEY (pubkey, sha256)
);
CREATE INDEX IF NOT EXISTS {{.Name}}__idx_blossom_blobs_owner_order
    ON {{.Name}}__blossom_blobs(pubkey, uploaded_at DESC, sha256 ASC);

INSERT INTO {{.Name}}__blossom_blobs (pubkey, sha256, uploaded_at, mime_type, size)
SELECT DISTINCT ON (e.pubkey, hash.value)
    e.pubkey, hash.value, e.created_at, mime.value, bytes.value::bigint
FROM {{.Name}}__events e
JOIN LATERAL (
    SELECT tag->>1 AS value FROM jsonb_array_elements(e.tags::jsonb) AS tag
    WHERE tag->>0 = 'x' LIMIT 1
) AS hash ON true
JOIN LATERAL (
    SELECT tag->>1 AS value FROM jsonb_array_elements(e.tags::jsonb) AS tag
    WHERE tag->>0 = 'type' LIMIT 1
) AS mime ON true
JOIN LATERAL (
    SELECT tag->>1 AS value FROM jsonb_array_elements(e.tags::jsonb) AS tag
    WHERE tag->>0 = 'size' LIMIT 1
) AS bytes ON true
WHERE e.kind = 24242 AND e.content = '' AND e.sig = repeat('0', 128)
    AND hash.value ~ '^[0-9a-f]{64}$'
    AND bytes.value ~ '^[0-9]+$' AND length(bytes.value) <= 18
ORDER BY e.pubkey, hash.value, e.created_at DESC, e.id DESC
ON CONFLICT (pubkey, sha256) DO NOTHING;
