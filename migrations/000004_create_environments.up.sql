CREATE TABLE environments (
    id               uuid PRIMARY KEY,
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    type             env_type NOT NULL,
    status           env_status NOT NULL DEFAULT 'PROVISIONING',
    deleted_at       timestamptz,
    provider         provider NOT NULL,
    endpoint         text,
    ram_gb           integer NOT NULL CHECK (ram_gb >= 0),
    cpu_cores        integer NOT NULL CHECK (cpu_cores >= 0),
    disk_gb          integer NOT NULL CHECK (disk_gb >= 0),
    hourly_rate      numeric NOT NULL CHECK (hourly_rate >= 0),
    caller_id        uuid NOT NULL REFERENCES callers (id),
    failure_reason   text,
    template_name    text NOT NULL,
    template_version text NOT NULL
);

CREATE INDEX environments_caller_id_idx ON environments (caller_id);
