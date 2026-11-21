# Ephemeral Environment Manager

[![CI](https://github.com/ademolahh/ephemeral-env-manager/actions/workflows/ci.yml/badge.svg)](https://github.com/ademolahh/ephemeral-env-manager/actions/workflows/ci.yml)

EEM creates short-lived, isolated environments on Docker for testing, reviewing pull requests and reproducing production bugs. You give it a workload image and a template (the services the workload needs, such as Postgres or Redis). EEM starts them on a private network, puts them behind an authenticated URL and deletes them when their time is up.

Humans use it through a REST API and a dashboard. AI agents use it through MCP: an agent can create a `PR_REVIEW` environment, run the test suite inside it with `exec` and tear it down, all within a budget a human set.

What it enforces:

- **Priorities:** `PROD_BUG` > `PR_REVIEW` > `REGULAR_TESTING`. When the capacity pool is full, a higher type preempts lower ones.
- **Limits:** every caller has a resource quota and a cost budget. Every provider has a capacity pool; normal types can use 90% of it and `PROD_BUG` all of it.
- **Cost:** the estimate is reserved at creation and the actual runtime is charged on delete.
- **Readiness:** an environment is `RUNNING` only once every container is running and healthy.
- **Recovery:** all state lives in Postgres. A worker expires environments, reclaims stuck ones and removes containers that no record owns.

## Architecture

```mermaid
flowchart LR
    dev["Developer<br/>dashboard or REST"] -->|"GitHub session or API key"| eem
    agent["AI agent<br/>MCP"] -->|"agent API key"| eem
    client["Browser or script"] -->|"env-&lt;id&gt;.domain<br/>+ access token"| proxy

    subgraph control["Control plane"]
        eem["eem<br/>API, MCP, dashboard"]
        worker["worker<br/>reconcile loops"]
        proxy["proxy"]
        pg[("Postgres<br/>all state")]
        redis[("Redis<br/>sessions")]
    end

    eem --> pg
    eem --> redis
    worker --> pg
    proxy -->|"route lookup"| pg

    subgraph host["Docker host"]
        subgraph netA["env A: internal network"]
            wa["workload"] --- sa["services"]
        end
        subgraph netB["env B: internal network"]
            wb["workload"] --- sb["services"]
        end
    end

    eem -->|"create, exec, status"| host
    worker -->|"status, teardown, orphans"| host
    proxy -->|"joins each env network"| wa
    proxy --> wb
```

| Service | Role |
| --- | --- |
| `eem` | API, MCP server and dashboard. It checks permissions, quota, budget and capacity in one transaction, then provisions in the background and watches readiness. |
| `worker` | Runs independent loops: expire environments past their TTL, stop ones whose caller is over budget, catch containers that vanished, resolve environments stuck in `PROVISIONING`, retry teardowns and remove orphaned containers. |
| `proxy` | Routes `env-<id>.<domain>` to that environment's workload after checking its access token. It joins each environment's network and listens only on its edge address, so workloads can't reach it or each other. |

## Lifecycle

```mermaid
stateDiagram-v2
    [*] --> PROVISIONING: POST /envs reserves quota, budget and capacity
    PROVISIONING --> RUNNING: every container running and healthy
    PROVISIONING --> FAILED: create error, never healthy, or stuck
    PROVISIONING --> DELETING: deleted, expired, over budget or preempted
    RUNNING --> DELETING: deleted, expired, over budget, preempted or missing
    DELETING --> DELETED: containers and network removed, cost settled
    FAILED --> [*]
    DELETED --> [*]
```

Capacity is released when an environment leaves the active states. The reserved cost is settled to the actual runtime once it reaches `DELETED` or `FAILED`.

## Getting started

You need Docker with Compose and Go 1.27.

```sh
cp .env.example .env
# set POSTGRES_PASSWORD, REDIS_PASSWORD and PROXY_SECRET (openssl rand -hex 32)
# on Linux, also set DOCKER_GID=$(getent group docker | cut -d: -f3)

docker compose up -d --build --wait               # postgres, redis, migrations, eem, worker, proxy
make seed-docker ARGS="-cpu 8 -ram 16 -disk 100"  # capacity pool and hourly prices
make admin ARGS="-username admin"                 # prompts for a password
make api-key                                      # a caller and key; ARGS="-role agent" for an agent key
```

The `make` helpers run in a one-off `tools` container on the compose network, so they need no Go toolchain or database port. The dashboard is at http://localhost:8080. GitHub sign-in works once `GITHUB_CLIENT_ID` and `GITHUB_CLIENT_SECRET` are set.

## Walkthrough

An admin stores a template:

```sh
ADMIN=$(curl -s -X POST localhost:8080/admin/login \
  -d '{"username":"admin","password":"…"}' | jq -r .token)

curl -X POST localhost:8080/templates -H "Authorization: Bearer $ADMIN" -d '{
  "name": "web", "version": "v1", "providers": ["docker"], "default_provider": "docker", "ttl": "30m",
  "workload": {
    "ports": [80],
    "resources": {"cpu_millis": 250, "ram_mb": 128, "disk_gb": 1},
    "healthcheck": "wget -qO- http://127.0.0.1/ >/dev/null"
  },
  "services": [{
    "name": "cache", "image": "redis:7-alpine",
    "resources": {"cpu_millis": 250, "ram_mb": 64, "disk_gb": 1},
    "healthcheck": "redis-cli ping"
  }]
}'
```

A caller creates an environment from it:

```sh
curl -X POST localhost:8080/envs -H "Authorization: Bearer $KEY" -H "Idempotency-Key: pr-482" \
  -d '{"type": "REGULAR_TESTING", "workload": "nginx:alpine", "template": "web@v1"}'
```

```json
{
  "id": "bd10525e-…",
  "status": "PROVISIONING",
  "resources": {"cpu_millis": 500, "ram_mb": 192, "disk_gb": 2},
  "hourly_rate": 3078,
  "endpoint": "http://env-bd10525e-….env.localhost:8000",
  "access_url": "http://env-bd10525e-….env.localhost:8000/?eem_token=…",
  "access_token": "…"
}
```

Poll `GET /envs/{id}` until it's `RUNNING`, then reach it. Open `access_url` in a browser, or send the token from a script:

```sh
curl -H "X-EEM-Access-Token: $TOKEN" http://env-$ID.env.localhost:8000/
```

Run commands in the workload, then delete the environment:

```sh
curl -X POST localhost:8080/envs/$ID/exec -H "Authorization: Bearer $KEY" \
  -d '{"cmd": ["sh", "-c", "wget -qO- http://127.0.0.1/ | head -1"]}'
# {"output": "<!DOCTYPE html>\n", "exit_code": 0}

curl -X DELETE localhost:8080/envs/$ID -H "Authorization: Bearer $KEY"
```

When a check fails, the error has a stable code:

```json
{"error": "create environment: PROD_BUG with permission REGULAR_TESTING: environment type not permitted for caller", "code": "not_permitted"}
```

Every endpoint, body and error code is in the [API reference](docs/api.md).

### Agents over MCP

Create an agent key (`make api-key ARGS="-role agent"`, or from the dashboard), then register the server with your MCP client, for example Claude Code:

```sh
claude mcp add --transport http eem http://localhost:8080/mcp --header "Authorization: Bearer $AGENT_KEY"
```

The agent gets `create_environment`, `get_environment`, `list_environments`, `exec`, `delete_environment`, `list_templates`, `get_template`, `get_budget` and `get_capacity`. An agent key shares its owner's quota and budget, can be capped lower, and only sees the environments it created.

## Design decisions

- **One transaction decides admission.** `POST /envs` locks the caller row, the API key row and the capacity pool row (`SELECT … FOR UPDATE`, always in that order), then checks permission, quota, budget and capacity and inserts the environment. Concurrent requests can't both take the last slot or overspend a budget, and the fixed lock order rules out deadlocks. Tests drive these races against a real Postgres.
- **Preemption is part of admission.** When a pool is full, the same transaction picks victims from lower-priority types, lowest priority first and newest first within a type, and marks them `DELETING`. It skips victims that wouldn't free the resource that is short. The new environment is only admitted if it then fits.
- **Headroom for incidents.** Normal types can fill 90% of a pool; `PROD_BUG` can use 100% and preempt the rest. A production bug can always get a reproduction environment, even during a busy review day.
- **Money is integers.** Prices are micro-USD per hour and budgets are cents, so there's no floating point in billing. Partial cores are charged pro rata and rounded up. The estimate is reserved when an environment is created, and the actual runtime is charged when it's deleted, in the same statement that releases its capacity.
- **Status changes are guarded updates.** Every transition is `UPDATE … WHERE status = ANY(allowed)`, so the API, the background provisioner and the worker can race on one environment safely. The loser sees `invalid_transition` and moves on.
- **Postgres is the source of truth; the worker reconciles.** If eem dies after committing but before Docker finishes, the worker resolves the stuck environment. If containers exist with no live record, the worker removes them. On startup eem resumes readiness checks for environments it was provisioning.
- **Access tokens need no storage.** An environment's token is an HMAC of its id under `PROXY_SECRET`, so eem and the proxy agree on it without sharing a table. The proxy swaps it for a host-only cookie and strips it before the request reaches the workload.
- **Each environment gets its own internal network.** Workloads can't reach the internet, other environments or the control plane. The proxy is the only way in, and it listens only on its edge address.

## Security model

EEM runs images that callers choose and lets them run commands inside, so isolation and limits matter:

- **Containers** run with `no-new-privileges`, a process limit (`DOCKER_PIDS_LIMIT`, default 1024), CPU and memory limits with no extra swap, capped logs, and without `NET_RAW`, `MKNOD`, `AUDIT_WRITE` and `SETFCAP`. Disk is reserved and priced, and enforced per container when `DOCKER_DISK_QUOTA=true` (this needs overlay2 on xfs with `pquota`).
- **Control-plane services** run as the distroless nonroot user with a read-only root filesystem and no capabilities. Postgres and Redis publish no host ports, the admin ports bind to localhost, and Redis needs a password.
- **Credentials:** API keys and session tokens are random 256-bit values stored only as SHA-256 hashes. Admin passwords use bcrypt. GitHub sign-in uses OAuth with state and PKCE. Cross-origin form posts are rejected.
- **Templates:** admins own the catalog. Inline templates are labeled `inline:<name>` with a hash of their spec, and `INLINE_TEMPLATES=false` turns them off.

What it doesn't protect against: eem and the worker can use the Docker socket, so they are as trusted as root on the host. Environments share the host kernel, so treat them as test isolation, not a sandbox for hostile code. A gVisor or Kata runtime would be the next step.

## Observability

Each service serves `/healthz`, `/readyz` and `/metrics` on `ADMIN_ADDR`. In compose these are `localhost:9090` for eem, `9091` for the worker and `9092` for the proxy. Logs are structured, through `slog`; set `LOG_FORMAT=json` for JSON.

| Metric | What it shows |
| --- | --- |
| `eem_environments{type,status}` | Environments by state, read from Postgres at scrape time |
| `eem_capacity_total`, `eem_capacity_used{provider,resource}` | Pool size and reservations |
| `eem_environments_created_total{type}`, `eem_preemptions_total{type}` | Admission and preemption |
| `eem_environment_ready_seconds` | Time from creation to healthy |
| `eem_provision_failures_total` | Environments that never became ready |
| `eem_worker_actions_total{action}`, `eem_worker_task_errors_total{task}` | What reconciliation is doing |
| `eem_http_requests_total{route,method,code}`, `eem_http_request_duration_seconds{route}` | API traffic |
| `eem_proxy_requests_total{code}` | Proxy traffic, including rejected tokens (401) |

## Configuration

Everything is set through environment variables; [.env.example](.env.example) lists them with defaults.

| Variable | Used by | Purpose |
| --- | --- | --- |
| `DATABASE_URL` | all | Postgres connection |
| `REDIS_URL` | eem | Session store |
| `PROXY_DOMAIN`, `PROXY_PUBLIC_PORT`, `PROXY_CONTAINER` | eem, worker, proxy | Environment host names and the proxy container to attach |
| `PROXY_SECRET` | eem, worker, proxy | Signs access tokens; at least 32 bytes |
| `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`, `GITHUB_ALLOWED_USERS`, `PUBLIC_URL` | eem | GitHub sign-in |
| `INLINE_TEMPLATES` | eem | `false` allows stored templates only |
| `DOCKER_PIDS_LIMIT`, `DOCKER_DISK_QUOTA` | eem, worker | Per-container limits |
| `WORKER_INTERVAL` | worker | How often each reconcile loop runs (default `30s`) |
| `ADMIN_ADDR`, `LOG_FORMAT`, `LOG_LEVEL` | all | Operational endpoints and logging |

## Development

```sh
make test           # starts a throwaway Postgres and Redis (compose.test.yaml), then runs every test with -race
make test-db-down   # stops them
make lint           # golangci-lint
```

Integration tests run against real Postgres, Redis and Docker. The test databases are separate from the dev stack, keep their data in tmpfs and listen on loopback only. Each test gets its own schema or a rolled-back transaction, so they run in parallel. CI starts the same services and sets `TEST_REQUIRE_ALL=1`, so a test that would skip for a missing dependency fails instead.

Schema changes go in a new numbered migration in [migrations/](migrations/), applied by `migrate` in compose. `make migrate-new name=…` creates one.

## Limitations and roadmap

- **Docker on one host only.** Providers sit behind an interface (`Create`, `Delete`, `Status`, `Address`, `Exec`, `List`), but Docker is the only implementation.
- **One eem and one worker.** Provisioning and readiness checks run in eem's memory, and eem resumes them on restart. A second eem would see the first one's in-flight environments as missing. Worker loops are idempotent but have no leader election.
- **Plain HTTP.** Put TLS termination in front of the proxy and eem.
- **Exec isn't streaming.** It returns output when the command ends, capped at 1 MiB and 5 minutes.
- **Template env values** are stored as plain JSON. Don't put production secrets in templates.
- **Access tokens can't be revoked one at a time.** They end with the environment, or for all environments when `PROXY_SECRET` rotates.

## License

[MIT](LICENSE)
