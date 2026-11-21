# API reference

The API is served by `eem` on port 8080. Request and response bodies are JSON. Unknown fields are rejected and bodies are capped at 1 MiB.

## Authentication

| Credential | How to send it | Who uses it |
| --- | --- | --- |
| API key | `Authorization: Bearer <key>` | Scripts and agents. A key belongs to a caller and has the `user` or `agent` role. |
| Caller session | `eem_session` cookie, set by GitHub sign-in | The dashboard. Lasts 24 hours. |
| Admin session | `Authorization: Bearer <token>` from `POST /admin/login` | Admins. Lasts 12 hours. |

An **agent key** shares its caller's quota and budget. It can create only `PR_REVIEW` and, with permission, `PROD_BUG` environments. It can be limited to a lower type and a spend cap, and it sees only the environments it created.

## Errors

Every error has the same shape:

```json
{ "error": "create environment: using 2 cpu, 4096 MiB ram, 20 GB disk, requesting 4 cpu, ...: quota exceeded", "code": "quota_exceeded" }
```

Branch on `code`, since the message can change. Codes:

| Status | Codes |
| --- | --- |
| 400 | `bad_request`, `invalid_env_type`, `no_provider`, `provider_not_allowed`, `provider_unavailable`, `empty_command` |
| 401 | `unauthorized` |
| 403 | `forbidden`, `not_permitted`, `quota_exceeded`, `budget_exceeded`, `budget_expired`, `prod_bug_cap_exceeded`, `key_cost_cap_exceeded`, `api_key_inactive` |
| 404 | `not_found`, `env_not_found`, `caller_not_found`, `template_not_found`, `api_key_not_found` |
| 409 | `conflict`, `invalid_transition`, `env_not_running`, `capacity_below_usage`, `github_id_taken`, `template_exists`, `idempotency_key_in_use` |
| 413 | `body_too_large` |
| 503 | `capacity_exceeded`, `capacity_pool_not_found`, `prices_not_found` |
| 504 | `timeout` |
| 500 | `internal` |

## Units

- **Resources:** `cpu_millis` (1000 is one core), `ram_mb` (MiB) and `disk_gb`. Each container needs at least 10 millicores, 16 MiB and 1 GB.
- **Prices:** micro-USD per hour for each CPU core, GB of RAM and GB of disk. Partial cores and GB are charged pro rata.
- **Budgets and costs:** US cents.

## Environments

### `POST /envs`: create an environment

Caller key or session. Returns `202 Accepted` with the environment in `PROVISIONING`.

```json
{
  "type": "PR_REVIEW",
  "workload": "ghcr.io/acme/api:3f2c1a9",
  "template": "api@v3",
  "ttl": "2h",
  "resources": { "workload": { "cpu_millis": 1500 }, "services": { "postgres": { "ram_mb": 2048 } } }
}
```

| Field | Notes |
| --- | --- |
| `type` | `REGULAR_TESTING`, `PR_REVIEW` or `PROD_BUG`, at most the caller's `permitted_env`. |
| `workload` | The image to test. It runs as the template's workload container. |
| `template` | A stored template as `name@version`, or an inline template object (see [Templates](#templates)). Inline templates are recorded as `inline:<name>` with a `sha256:` version, and admins can turn them off with `INLINE_TEMPLATES=false`. |
| `provider` | Optional. Defaults to the template's `default_provider`. |
| `ttl`, `resources` | Optional overrides of the template. |

Send an `Idempotency-Key` header to make retries safe: the same key returns the same environment.

Before anything starts, one transaction checks the type permission, the caller's quota, the estimated cost against the budget (or against the `PROD_BUG` cap for agents) and the provider's capacity pool. A higher-priority type may preempt lower-priority environments to make room.

### `GET /envs`: list environments

Caller key or session. Query parameters are `status`, `page` (from 1) and `limit` (1–100, default 10). The response holds the page, `next_page` when there is one, the `total`, and `counts` by status.

### `GET /envs/{id}`: get an environment

The owner or an admin. It includes `instance_status`, the live provider state (`running`, `starting`, `degraded`, `unhealthy`, `stopped`, `missing` or `unknown`).

While an environment with ports is active, its owner also gets:

| Field | Use |
| --- | --- |
| `endpoint` | The environment's host name on the proxy. |
| `access_url` | Open it in a browser. The proxy swaps the token for a cookie and redirects. |
| `access_token` | Send it as `X-EEM-Access-Token` from scripts and agents. |

### `DELETE /envs/{id}`: delete an environment

The owner or an admin. Returns `202` while `DELETING`, or `200` if the environment was already gone.

### `POST /envs/{id}/exec`: run a command in the workload

The owner or an admin. The environment must be `RUNNING`.

```json
{ "cmd": ["sh", "-c", "go test ./..."] }
```

The response is `{"output": "...", "exit_code": 0, "truncated": false}`. A non-zero exit is still `200`. Output stops at 1 MiB, with `truncated` set, and the command times out after 5 minutes (`504`).

## Templates

### `POST /templates`: store a template

Admin. Templates are immutable: storing an existing `name` and `version` returns `409`.

```json
{
  "name": "api",
  "version": "v3",
  "providers": ["docker"],
  "default_provider": "docker",
  "ttl": "4h",
  "workload": {
    "ports": [8080],
    "resources": { "cpu_millis": 1000, "ram_mb": 1024, "disk_gb": 5 },
    "healthcheck": "wget -qO- http://127.0.0.1:8080/healthz",
    "env": { "DATABASE_URL": "postgres://app:app@postgres:5432/app" }
  },
  "services": [
    {
      "name": "postgres",
      "image": "postgres:18",
      "resources": { "cpu_millis": 500, "ram_mb": 512, "disk_gb": 5 },
      "env": { "POSTGRES_USER": "app", "POSTGRES_PASSWORD": "app" },
      "healthcheck": "pg_isready -U app"
    }
  ]
}
```

Services reach each other by name. The proxy forwards to the workload's first port. Workload and service `aliases` that name the same node let a cluster share one address. The proxy routes to that alias, and losing a minority of the nodes reports `degraded`.

### `GET /templates`, `GET /templates/{name}/{version}`

Any signed-in caller or admin.

## Callers, keys and budgets

| Method and path | Who | Body or notes |
| --- | --- | --- |
| `GET /me` | anyone signed in | Who the credential belongs to. |
| `POST /auth/logout` | a session | Revokes the session. |
| `POST /keys` | caller session | `{"name", "role": "user"\|"agent", "expires_in": "720h", "permitted_env", "cost_cap"}`. The key is shown once. |
| `GET /keys` | caller session | The caller's keys, without secrets. |
| `DELETE /keys/{id}` | the key's owner or an admin | Revokes the key. |
| `POST /callers` | admin | `{"github_login", "github_id"}` creates a caller with default quota and budget. |
| `POST /callers/{id}/keys`, `GET /callers/{id}/keys` | admin | Same as `/keys`, for any caller. |
| `GET /callers/{id}/budget` | the caller (`me`) or an admin | Quota, resources in use, budget, spend and expiry. |
| `PUT /callers/{id}/budget` | admin | `{"quota": {...}, "cost_budget_total": 10000, "budget_expires_at": "2026-12-31T00:00:00Z"}` |
| `PUT /callers/{id}/permission` | admin | `{"permitted_env": "PROD_BUG", "prod_bug_cost_cap": 5000}` |

## Capacity and prices

| Method and path | Who | Body or notes |
| --- | --- | --- |
| `GET /capacity` | anyone signed in | Each pool's total, used, the 90% normal limit, and what's free for normal types and for `PROD_BUG`. |
| `PUT /capacity/{provider}` | admin | `{"total": {"cpu_millis": 16000, "ram_mb": 32768, "disk_gb": 500}}`. Refused below current usage. |
| `PUT /prices/{provider}` | admin | `{"per_cpu_core": 4048, "per_ram_gb": 4445, "per_disk_gb": 110}` in micro-USD per hour. |

## Sign-in

| Method and path | Notes |
| --- | --- |
| `POST /admin/login` | `{"username", "password"}` returns `{"token", "expires_at"}`. |
| `GET /auth/providers` | Whether GitHub sign-in is configured. |
| `GET /auth/github/login`, `GET /auth/github/callback` | OAuth with PKCE and state. `GITHUB_ALLOWED_USERS` limits who can sign up. |

## MCP

`/mcp` is a stateless streamable-HTTP MCP server. Authenticate with an API key as a bearer token. The tools are `create_environment`, `get_environment`, `list_environments`, `delete_environment`, `exec`, `list_templates`, `get_template`, `get_budget` and `get_capacity`. They take the same inputs and follow the same rules as the endpoints above.

## Operational endpoints

Each service serves these on `ADMIN_ADDR` (default `:9090`):

| Path | Use |
| --- | --- |
| `/healthz` | Liveness. Always `200` while the process runs. |
| `/readyz` | `200` when every dependency check passes, otherwise `503`, with one result per check: Postgres, Redis and Docker for eem, Postgres and Docker for the worker, Postgres for the proxy. |
| `/metrics` | Prometheus metrics; see the README. |
