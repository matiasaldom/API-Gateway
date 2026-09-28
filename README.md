# API Gateway

[![CI](https://github.com/matiasaldom/API-Gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/matiasaldom/API-Gateway/actions/workflows/ci.yml)

An API gateway written in Go. It authenticates clients with API keys, enforces per-plan rate limits, caches responses, and records analytics for every request. It also has a management API and a React dashboard for running it.

![Dashboard overview: request totals, error rate, cache hit ratio, latency percentiles, and live traffic and latency charts](docs/images/overview.png)

<!-- Demo recording: add docs/images/demo.gif here once recorded (see "Demo" below). -->

## Why

A small team opening its API to outside developers has to know **who is calling**, **how much they may call**, and **what is happening**, before the first customer arrives. Managed gateways charge per request and are heavy to run for a two-person team. Writing auth, limits, and metrics into every backend means repeating the same code in each service, and the copies drift apart.

This gateway sits in front of the backend services and handles all three in one place. The services themselves don't change. It is built for the **platform engineer** who runs the API, the **third-party developer** who calls it, and the **operator** on call. See [docs/product.md](docs/product.md) for personas, prioritization, and success metrics.

## Features

- **Reverse proxy.** Routes by path prefix and forwards method, path, query, headers and body over pooled keep-alive connections. Returns 502 on upstream error and 504 on timeout.
- **API key authentication.** Clients send `Authorization: Bearer <key>`. Only SHA-256 hashes are stored, and raw keys are never logged.
- **Rate limiting.** Per-key limits set by the plan (free 100/min, pro 1000/min), using fixed windows (the default) or token buckets. Over-limit requests get 429 with `X-RateLimit-*` and `Retry-After`.
- **Response caching.** GET responses are cached in memory per route and per API key, with `X-Cache: HIT`/`MISS`. Clients can skip the cache with `Cache-Control: no-cache`.
- **Analytics.** One event per request is written to PostgreSQL in batches, off the request path. Reports cover p50/p95/p99 latency, errors, traffic per route and account, and plan utilization.
- **Management API.** Create and revoke keys, change plan limits, and edit routes without restarting. Every change is recorded in an audit log.
- **Dashboard.** React + TypeScript: overview, routes, API keys, plans and accounts, with charts and sortable tables.
- **Operations.** Request IDs across logs and responses, structured JSON logs, `/health`, `/ready` (checks the database), `/metrics`, a 10 MiB request body limit, and graceful shutdown that writes out pending analytics.

## Quick start

You need Docker.

```sh
docker compose up --build
```

This starts PostgreSQL (migrated and seeded with demo keys), two demo backends, the gateway on **:8080**, and the dashboard on **:3000**.

```sh
curl -i http://localhost:8080/users/1 \
  -H "Authorization: Bearer gw_demo_free_plan_local_docker_key_0000000000A"
```

Open http://localhost:3000 and sign in with the admin token `local-demo-admin-token-do-not-use-in-production`.

| Seeded key | Plan |
|---|---|
| `gw_demo_free_plan_local_docker_key_0000000000A` | free, 100/min |
| `gw_demo_pro_plan_local_docker_key_00000000000A` | pro, 1000/min |
| `gw_demo_revoked_local_docker_key_000000000000A` | revoked (403) |

These keys and the admin token are published in this repository, so use them only on your own machine (ADR-010). Set `ADMIN_TOKEN` to override the token. `docker compose down -v` stops everything and deletes the database.

## Demo

[`scripts/demo.sh`](scripts/demo.sh) walks through every feature against the running stack, pausing between steps:

1. A request routed through the gateway, with request ID and rate-limit headers
2. An unknown key gets 401, and a revoked key gets 403
3. The free plan's limit is exceeded, giving 429 with `Retry-After`
4. The same request twice gives `X-Cache: MISS` then `HIT`, with the backend's timestamp unchanged
5. The dashboard shows the traffic that was just generated
6. Raising the plan limit through the admin API unblocks the key on its next request, with no restart

```sh
scripts/demo.sh              # press Enter between steps
NO_PAUSE=1 scripts/demo.sh   # run straight through; CI runs it this way as an end-to-end test
```

## Architecture

```mermaid
flowchart LR
    apps([API clients<br/>Bearer API key]) --> gw
    ops([Operators]) --> dash[Dashboard<br/>React + Vite] --> admin

    subgraph gw[Gateway]
        direction LR
        a[Analytics] --> l[Body limit] --> b[Auth] --> c[Rate limit] --> d[Cache] --> e[Router] --> f[Proxy]
    end
    admin[Admin API<br/>/admin/*]

    f --> up([Upstream services])
    b -- key + plan --> pg[(PostgreSQL)]
    a -. batched events .-> pg
    admin -- keys · plans · routes · analytics --> pg
    admin -. live route swap .-> e
```

A request passes through analytics timing, the body limit, API-key authentication, the rate limiter, the response cache, and the router, then goes to the proxy. Auth reads the key and its plan limit in a single query. Analytics events are written in the background. Rate-limit state and cached responses live in memory.

[docs/architecture.md](docs/architecture.md) has the full request pipeline, the system diagram, and where each kind of state lives.

## Performance

Measured with [`scripts/bench.sh`](scripts/bench.sh) on an i7-11700B under Docker Desktop, with the load generator on the same machine. The method, caveats, and full tables are in [docs/benchmarks.md](docs/benchmarks.md).

| | Result |
|---|---|
| Latency added by the full pipeline (auth, limit, analytics, proxy) | +0.50 ms at p50 |
| Throughput at concurrency 500 | ~16,700 req/s uncached, ~24,000 req/s from cache, p99 under 45 ms, 0 errors |
| Cache hit vs a 20 ms backend | p50 21.6 ms → 1.9 ms (11× lower) |
| Upstream connection-pool fix found by benchmarking (ADR-009) | 15× throughput and 35× lower p99 at concurrency 100; 3,441 failed requests → 0 |

## Security decisions

| Decision | Detail |
|---|---|
| Keys are never stored in plain text | Only SHA-256 hashes are stored. A key is shown once, at creation. Keys are 256 random bits, so a fast hash is enough; a slow hash like bcrypt would only add latency. |
| Clear, distinct auth errors | Missing or unknown key gives 401, and a revoked key gives 403. Malformed keys are rejected before any database query. |
| Admin API is separate | `/admin/*` needs its own `ADMIN_TOKEN`, compared in constant time. Every change and every refused attempt is audit-logged. |
| No cache leaks between customers | The API key ID is part of every cache key. `Set-Cookie`, `private`, `no-store` and `Vary` responses are never cached (ADR-003). |
| No pivoting to cloud metadata | Routes can't point at link-local addresses such as 169.254.169.254. This is checked on config load, on admin edits, and again after DNS resolution (ADR-008). |
| Resource limits | 10 MiB request bodies, an upstream timeout, a header read timeout, a 256 MiB cache cap, and a bounded analytics buffer that drops events instead of blocking requests. |
| Safe proxying | Hop-by-hop headers are stripped. All SQL is parameterized. Database URL parse errors are not echoed, because the URL may contain a password. |

## Screenshots

| Routes | Accounts |
|---|---|
| [![Routes page with per-route traffic and latency](docs/images/routes.png)](docs/images/routes.png) | [![Accounts page with plan utilization](docs/images/accounts.png)](docs/images/accounts.png) |
| Per-route traffic, 5xx rate, p95 and cache hits. Upstream and cache TTL can be edited in place. | Top applications, and plan utilization per key (busiest minute ÷ limit), with over-limit keys flagged. |

| API keys | Mobile |
|---|---|
| [![API keys page](docs/images/api-keys.png)](docs/images/api-keys.png) | [![Dashboard on a phone](docs/images/mobile.png)](docs/images/mobile.png) |
| Create keys (shown once), revoke, filter and page through them. | On phones the navigation becomes a top bar and tables become cards. |

## Running without Docker

You need Go 1.25+, PostgreSQL, and Node 22+ for the dashboard.

```sh
export DATABASE_URL="postgres://postgres:postgres@localhost:5432/apigateway?sslmode=disable"
export ADMIN_TOKEN=$(openssl rand -hex 32)                              # optional; enables /admin/* and the dashboard
for f in migrations/*.up.sql; do psql "$DATABASE_URL" -f "$f"; done   # apply migrations

go run ./cmd/echo -addr :8081 -service users &                          # demo backends for gateway.yaml
go run ./cmd/echo -addr :8082 -service albums &
go run ./cmd/gateway                                                    # routes come from gateway.yaml

go run ./cmd/apikey create -email dev@example.com -app "My App"         # prints a new key once
curl -i http://localhost:8080/users/1 -H "Authorization: Bearer <key>"

cd dashboard && npm install && npm run dev                              # http://localhost:5173
```

## Configuration

```yaml
# gateway.yaml
listen_addr: ":8080"
upstream_timeout: 30s                 # whole upstream request/response (ADR-001)
rate_limit_algorithm: fixed_window    # or token_bucket (ADR-007)

routes:
  - prefix: /users                    # matches /users and /users/..., longest prefix wins
    upstream: http://localhost:8081
  - prefix: /albums
    upstream: http://localhost:8082
    cache_ttl: 30s                    # cache GET responses per API key; omit or 0 to disable
```

`DATABASE_URL` and `ADMIN_TOKEN` come from the environment. Plan limits, and route upstreams and TTLs, can be changed at runtime through the admin API ([docs/admin-api.md](docs/admin-api.md)).

## Documentation

| Topic | Doc |
|---|---|
| Problem, users, prioritization, success metrics, roadmap | [docs/product.md](docs/product.md) |
| System architecture and request pipeline | [docs/architecture.md](docs/architecture.md) |
| Benchmark method and results | [docs/benchmarks.md](docs/benchmarks.md) |
| API keys, authentication, error responses | [docs/authentication.md](docs/authentication.md) |
| Plan limits, fixed window vs token bucket, rate-limit headers | [docs/rate-limiting.md](docs/rate-limiting.md) |
| Per-route response caching | [docs/caching.md](docs/caching.md) |
| Analytics collection and endpoints | [docs/analytics.md](docs/analytics.md) |
| Management API reference | [docs/admin-api.md](docs/admin-api.md) |
| Dashboard | [dashboard/README.md](dashboard/README.md) |
| Design decisions (ADR-001 – 010) | [docs/decisions.md](docs/decisions.md) |
| Project status and known limitations | [docs/project-status.md](docs/project-status.md) |

## Project layout

```
cmd/gateway/        gateway server
cmd/apikey/         CLI: create, revoke, and list API keys
cmd/echo/           demo upstream service (used by docker compose and benchmarks)
internal/
  admin/            management API (/admin/*)
  auth/             API key authentication middleware
  cache/            per-route, per-key response cache
  config/           YAML + environment configuration
  limiter/          fixed-window and token-bucket rate limiters
  metrics/          analytics collector, middleware, and endpoints
  proxy/            request pipeline and reverse proxy
  routing/          prefix router and live route table
  storage/          PostgreSQL access
migrations/         SQL migrations (000001 – 000004)
tests/              integration tests (PostgreSQL-backed)
dashboard/          React + TypeScript + Vite dashboard (Dockerfile: nginx)
docker/             compose config, database init and seed, benchmark image
scripts/            demo.sh (walkthrough / e2e test), bench.sh (load tests)
docs/               documentation
```

## Tests

```sh
go test ./...                                               # unit tests; PostgreSQL tests are skipped
TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable" go test ./...   # with the compose database
go test -race ./...                                         # needs cgo; on Windows run in a Linux container
cd dashboard && npm run build                               # strict TypeScript check + production build
```

Each integration test runs in its own temporary PostgreSQL schema and drops it afterwards. [CI](.github/workflows/ci.yml) runs on every push. It runs gofmt, vet, and the full suite under the race detector against PostgreSQL, builds the dashboard, and runs `scripts/demo.sh` against the Docker Compose stack as an end-to-end test.

## Tech stack

Go · PostgreSQL (pgx) · YAML configuration · React 19 · TypeScript (strict) · Vite · Recharts · Docker Compose · nginx · GitHub Actions · oha (load testing)

## Roadmap

- [x] Reverse proxy
- [x] API key authentication
- [x] API key administration CLI
- [x] PostgreSQL persistence
- [x] Rate limiting (fixed window and token bucket, in-memory)
- [x] Response caching (in-memory, per API key)
- [x] Analytics (async collection, admin endpoints)
- [x] Management API (keys, plans, routes, analytics)
- [x] Dashboard (React + TypeScript)
- [x] Docker Compose demo, CI, reproducible benchmarks
- [ ] In-memory API key cache (every request, even a cache hit, queries PostgreSQL today; see benchmarks)
- [ ] Historical time-series analytics endpoint
- [ ] Shared rate-limit and cache store for multiple gateway instances

See [docs/project-status.md](docs/project-status.md) for current status, known limitations, and next steps.
