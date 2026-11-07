CREATE TABLE templates (
    id         uuid PRIMARY KEY,
    name       text NOT NULL,
    version    text NOT NULL,
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    UNIQUE (name, version)
);
