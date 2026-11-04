-- API keys. Only a SHA-256 hash of each key is stored; the key itself is
-- shown once by `filedrop keys create` and never saved.
CREATE TABLE api_keys (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE,
    key_hash   BLOB    NOT NULL UNIQUE,
    revoked    INTEGER NOT NULL DEFAULT 0 CHECK (revoked IN (0, 1)),
    created_at INTEGER NOT NULL -- Unix seconds
) STRICT;

-- Uploaded files. The id is the random link ID and also the name of the
-- encrypted file on disk. wrapped_key is the per-file data key encrypted with
-- the master key; wrap_nonce is the AES-GCM nonce used for that wrapping.
CREATE TABLE files (
    id                  TEXT    PRIMARY KEY,
    api_key_id          INTEGER NOT NULL REFERENCES api_keys (id),
    status              TEXT    NOT NULL CHECK (status IN ('pending', 'ready', 'deleted')),
    locked              INTEGER NOT NULL DEFAULT 0 CHECK (locked IN (0, 1)),
    remaining_downloads INTEGER NOT NULL CHECK (remaining_downloads >= -1), -- -1 = unlimited
    current_downloads   INTEGER NOT NULL DEFAULT 0 CHECK (current_downloads >= 0),
    wrapped_key         BLOB    NOT NULL,
    wrap_nonce          BLOB    NOT NULL,
    size_bytes          INTEGER NOT NULL CHECK (size_bytes >= 0),
    original_name       TEXT    NOT NULL,
    content_type        TEXT    NOT NULL,
    created_at          INTEGER NOT NULL, -- Unix seconds
    expires_at          INTEGER NOT NULL CHECK (expires_at > created_at) -- Unix seconds
) STRICT;

CREATE INDEX files_expires_at ON files (expires_at);
CREATE INDEX files_status ON files (status);
CREATE INDEX files_api_key_id ON files (api_key_id);
