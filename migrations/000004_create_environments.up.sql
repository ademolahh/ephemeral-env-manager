CREATE TABLE environments (
    id               uuid PRIMARY KEY,
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    type             env_type NOT NULL,
    status           env_status NOT NULL DEFAULT 'PROVISIONING',
    deleted_at       timestamptz,
    provider         provider NOT NULL,
    endpoint         text,
    backend          text,
    ram_gb           integer NOT NULL CHECK (ram_gb >= 0),
    cpu_cores        integer NOT NULL CHECK (cpu_cores >= 0),
    disk_gb          integer NOT NULL CHECK (disk_gb >= 0),
    hourly_rate      bigint NOT NULL CHECK (hourly_rate >= 0),
    caller_id        uuid NOT NULL REFERENCES callers (id),
    api_key_id       uuid REFERENCES api_keys (id),
    failure_reason   text,
    template_name    text NOT NULL,
    template_version text NOT NULL,
    idempotency_key  text,
    estimated_cost   bigint NOT NULL CHECK (estimated_cost >= 0),
    reserved_cost    bigint NOT NULL CHECK (reserved_cost BETWEEN 0 AND estimated_cost),
    charged_cost     bigint CHECK (charged_cost BETWEEN 0 AND estimated_cost)
);

CREATE INDEX environments_caller_created_idx ON environments (caller_id, created_at DESC, id DESC);
CREATE INDEX environments_status_idx on environments (status);
CREATE UNIQUE INDEX environments_caller_idempotency_key_idx ON environments (caller_id, idempotency_key);
CREATE INDEX environments_api_key_idx ON environments (api_key_id) WHERE api_key_id IS NOT NULL;
