CREATE TABLE IF NOT EXISTS {{.Name}}__blossom_tags (
    pubkey TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (pubkey, sha256, key, value)
);
CREATE INDEX IF NOT EXISTS {{.Name}}__idx_blossom_tags_owner_key_value ON {{.Name}}__blossom_tags(pubkey, key, value, sha256);
