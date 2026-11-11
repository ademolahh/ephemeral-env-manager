CREATE TABLE api_keys (
    id            uuid PRIMARY KEY,
    caller_id     uuid NOT NULL REFERENCES callers (id),
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    api_key_hash  bytea NOT NULL UNIQUE,
    role          caller_role NOT NULL DEFAULT 'user',
    permitted_env env_type,
    cost_cap      bigint CHECK (cost_cap >= 0),
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    last_used_at  timestamptz,
    revoked_at    timestamptz
);

CREATE INDEX api_keys_caller_id_idx ON api_keys (caller_id, created_at DESC);
