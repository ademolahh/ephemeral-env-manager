CREATE TABLE admins (
    id            uuid PRIMARY KEY,
    username      text NOT NULL UNIQUE,
    password_hash bytea NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT NOW()
);
