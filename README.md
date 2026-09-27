# API Gateway

An API gateway written in Go. It authenticates clients with API keys, enforces per-plan rate limits, caches responses, and records analytics for every request. It also has a management API and a React dashboard for running it.

![Dashboard overview: request totals, error rate, cache hit ratio, latency percentiles, and live traffic and latency charts](docs/images/overview.png)

## Features

- **Reverse proxy.** Routes by path prefix, forwards method, path, query, headers and body, and applies an upstream timeout (502 on upstream error, 504 on timeout).
- **API key authentication.** Clients send `Authorization: Bearer <key>`. Only SHA-256 hashes are stored, and raw keys are never logged.
- **Rate limiting.** Fixed one-minute windows per API key, with the limit set by the plan (free 100/min, pro 1000/min). Over-limit requests get 429 with `X-RateLimit-*` and `Retry-After`.
- **Response caching.** GET responses are cached in memory per route and per API key, with `X-Cache: HIT`/`MISS`.
- **Analytics.** One event per request is written to PostgreSQL in batches, off the request path. Reports cover p50/p95/p99 latency, errors, traffic per route and account, and plan utilization.
- **Management API.** Create and revoke keys, change plan limits, and edit routes without restarting. Every change is recorded in an audit log.
- **Dashboard.** React + TypeScript: overview, routes, API keys, plans and accounts, with charts and sortable tables.
- **Operations.** Request IDs across logs and responses, structured JSON logs, `/health`, `/metrics`, and graceful shutdown that writes out pending analytics.

## Architecture

```mermaid
flowchart LR
    apps([API clients<br/>Bearer API key]) --> gw
    ops([Operators]) --> dash[Dashboard<br/>React + Vite] --> admin

    subgraph gw[Gateway]
        direction LR
        a[Analytics] --> b[Auth] --> c[Rate limit] --> d[Cache] --> e[Router] --> f[Proxy]
    end
    admin[Admin API<br/>/admin/*]

    f --> up([Upstream services])
    b -- key + plan --> pg[(PostgreSQL)]
    a -. batched events .-> pg
    admin -- keys · plans · routes · analytics --> pg
    admin -. live route swap .-> e
```

A request passes through analytics timing, API-key authentication, the rate limiter, the response cache, and the router, then goes to the proxy. Auth reads the key and its plan limit in a single query. Analytics events are written in the background, and rate-limit counters and cached responses live in memory.

[docs/architecture.md](docs/architecture.md) has the full request pipeline, the system diagram, and where each kind of state lives.

## Screenshots

| Routes | Accounts |
|---|---|
| [![Routes page with per-route traffic and latency](docs/images/routes.png)](docs/images/routes.png) | [![Accounts page with plan utilization](docs/images/accounts.png)](docs/images/accounts.png) |
| Per-route traffic, 5xx rate, p95 and cache hits. Upstream and cache TTL can be edited in place. | Top applications, and plan utilization per key (busiest minute ÷ limit), with over-limit keys flagged. |

| API keys | Mobile |
|---|---|
| [![API keys page](docs/images/api-keys.png)](docs/images/api-keys.png) | [![Dashboard on a phone](docs/images/mobile.png)](docs/images/mobile.png) |
| Create keys (shown once), revoke, filter and page through them. | On phones the navigation becomes a top bar and tables become cards. |

## Quick start

You need Go 1.25+, PostgreSQL, and Node 22+ for the dashboard.

**1. Start PostgreSQL**

```sh
docker run -d --name api-gateway-postgres -p 5432:5432 \
  -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=apigateway postgres:latest
```

**2. Configure the environment**

```sh
export DATABASE_URL="postgres://postgres:postgres@localhost:5432/apigateway?sslmode=disable"
export ADMIN_TOKEN=$(openssl rand -hex 32)   # optional; enables /admin/* and the dashboard
```

**3. Apply migrations**

```sh
for f in migrations/*.up.sql; do psql "$DATABASE_URL" -f "$f"; done
```

**4. Start the gateway.** Routes are declared in [`gateway.yaml`](gateway.yaml).

```sh
go run ./cmd/gateway
```

**5. Create an API key and call a route**

```sh
go run ./cmd/apikey create -email dev@example.com -app "My App"
# or: curl -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
#       -d '{"email":"dev@example.com","application":"My App"}' http://localhost:8080/admin/api-keys

curl -i http://localhost:8080/users/1 -H "Authorization: Bearer <key>"
```

**6. Open the dashboard**

```sh
cd dashboard && npm install && npm run dev   # http://localhost:5173, sign in with ADMIN_TOKEN
```

**Health and metrics**

```sh
curl http://localhost:8080/health
curl http://localhost:8080/metrics                                                  # cache counters
curl -H "Authorization: Bearer $ADMIN_TOKEN" "http://localhost:8080/admin/analytics/summary?window=24h"
```

## Documentation

| Topic | Doc |
|---|---|
| System architecture and request pipeline | [docs/architecture.md](docs/architecture.md) |
| API keys, authentication, error responses | [docs/authentication.md](docs/authentication.md) |
| Plan limits and rate-limit headers | [docs/rate-limiting.md](docs/rate-limiting.md) |
| Per-route response caching | [docs/caching.md](docs/caching.md) |
| Analytics collection and endpoints | [docs/analytics.md](docs/analytics.md) |
| Management API reference | [docs/admin-api.md](docs/admin-api.md) |
| Dashboard | [dashboard/README.md](dashboard/README.md) |
| Design decisions (ADR-001 – 006) | [docs/decisions.md](docs/decisions.md) |
| Project status and known limitations | [docs/project-status.md](docs/project-status.md) |

## Project layout

```
cmd/gateway/        gateway server
cmd/apikey/         CLI: create, revoke, and list API keys
internal/
  admin/            management API (/admin/*)
  auth/             API key authentication middleware
  cache/            per-route, per-key response cache
  config/           YAML + environment configuration
  limiter/          fixed-window rate limiter
  metrics/          analytics collector, middleware, and endpoints
  proxy/            request pipeline and reverse proxy
  routing/          prefix router and live route table
  storage/          PostgreSQL access
migrations/         SQL migrations (000001 – 000004)
tests/              integration tests (PostgreSQL-backed)
dashboard/          React + TypeScript + Vite dashboard
docs/               documentation
```

## Tests

```sh
go test ./...                                               # unit tests; PostgreSQL tests are skipped
TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable" go test ./...
go test -race ./...                                         # needs cgo; on Windows run in a Linux container
cd dashboard && npm run build                               # strict TypeScript check + production build
```

Each integration test runs in its own temporary PostgreSQL schema and drops it afterwards.

## Tech stack

Go · PostgreSQL (pgx) · YAML configuration · React 19 · TypeScript (strict) · Vite · Recharts · Docker

## Roadmap

- [x] Reverse proxy
- [x] API key authentication
- [x] API key administration CLI
- [x] PostgreSQL persistence
- [x] Rate limiting (fixed window, in-memory)
- [x] Response caching (in-memory, per API key)
- [x] Analytics (async collection, admin endpoints)
- [x] Management API (keys, plans, routes, analytics)
- [x] Dashboard (React + TypeScript)
- [ ] Historical time-series analytics endpoint
- [ ] Shared rate-limit and cache store for multiple gateway instances

See [docs/project-status.md](docs/project-status.md) for current status, known limitations, and next steps.
