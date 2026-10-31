CREATE TABLE callers (
    id                uuid PRIMARY KEY,
    github_login      text,
    github_id         text UNIQUE,
    created_at        timestamptz NOT NULL DEFAULT now(),
    quota_ram_gb      integer NOT NULL CHECK (quota_ram_gb >= 0),
    quota_cpu_cores   integer NOT NULL CHECK (quota_cpu_cores >= 0),
    quota_disk_gb     integer NOT NULL CHECK (quota_disk_gb >= 0),
    cost_budget       numeric NOT NULL CHECK (cost_budget >= 0),
    budget_expires_at timestamptz NOT NULL,
    budget_set_at     timestamptz NOT NULL DEFAULT now(),
    role              caller_role NOT NULL,
    permitted_env     env_type NOT NULL DEFAULT 'REGULAR_TESTING'
);
