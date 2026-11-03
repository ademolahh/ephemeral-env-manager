CREATE TABLE callers (
    id                uuid PRIMARY KEY,
    github_login      text,
    github_id         text UNIQUE,
    created_at        timestamptz NOT NULL DEFAULT now(),
    quota_ram_gb      integer NOT NULL CHECK (quota_ram_gb >= 0),
    quota_cpu_cores   integer NOT NULL CHECK (quota_cpu_cores >= 0),
    quota_disk_gb     integer NOT NULL CHECK (quota_disk_gb >= 0),
    cost_budget_total numeric NOT NULL CHECK (cost_budget_total >= 0),
    cost_used         numeric NOT NULL DEFAULT 0 CHECK (cost_used >= 0),
    prod_bug_cost     numeric NOT NULL DEFAULT 0 CHECK (prod_bug_cost >= 0),
    budget_expires_at timestamptz NOT NULL,
    budget_set_at     timestamptz NOT NULL DEFAULT now(),
    role              caller_role NOT NULL,
    permitted_env     env_type NOT NULL DEFAULT 'REGULAR_TESTING'
);