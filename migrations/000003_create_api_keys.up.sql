CREATE TABLE api_keys (
    id           uuid PRIMARY KEY,
    api_key_hash bytea NOT NULL UNIQUE,
    caller_id    uuid NOT NULL REFERENCES callers (id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    valid        boolean NOT NULL DEFAULT true
);

CREATE INDEX api_keys_caller_id_idx ON api_keys (caller_id);
