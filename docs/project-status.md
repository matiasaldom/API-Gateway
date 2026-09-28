# API Gateway Project Status

Date: 2026-09-27

Completed

✅ Phase 1 Proxy Foundation
✅ Phase 2 Identity & Persistence
✅ Phase 3 Rate Limiting
✅ Phase 4 Response Caching
✅ Phase 5 Analytics
✅ Phase 6A Management API
✅ Phase 6B Dashboard & Demo
✅ Phase 7 Portfolio Polish: Docker Compose, CI, benchmarks, token bucket, hardening

All planned phases are complete.

## What each phase delivered

| Phase | Delivered | Docs |
|---|---|---|
| 1 Proxy Foundation | Reverse proxy, prefix routing from YAML, request IDs, structured logs, upstream timeout (502/504) | [README](../README.md) |
| 2 Identity & Persistence | PostgreSQL, API keys (SHA-256 hashes only), Bearer auth (401/403), `apikey` CLI | [authentication.md](authentication.md) |
| 3 Rate Limiting | Per-plan fixed-window limits (free 100/min, pro 1000/min), 429 + `X-RateLimit-*` + `Retry-After` | [rate-limiting.md](rate-limiting.md) |
| 4 Response Caching | Per-route TTL, per-API-key GET cache, `X-Cache`, `/metrics` hit/miss counters | [caching.md](caching.md) |
| 5 Analytics | Async batched events → `request_events`; p50/p95/p99, errors, per route/account, plan utilization | [analytics.md](analytics.md) |
| 6A Management API | `/admin/*`: create/list/revoke keys, edit plan limits, live route edits, audit logs | [admin-api.md](admin-api.md) |
| 6B Dashboard & Demo | React + TypeScript dashboard: overview, routes, keys, plans, accounts; charts, sorting | [dashboard/README.md](../dashboard/README.md) |
| 7 Portfolio Polish | `docker compose up` demo stack with seed data and echo backends; `scripts/demo.sh`; GitHub Actions CI (race tests, dashboard build, end-to-end); reproducible benchmarks and the connection-pool fix they found; token bucket option; 10 MiB body limit, blocked link-local upstreams, `/ready`; client cache bypass; expired/rejected cache and analytics counters on `/metrics`; product brief | [benchmarks.md](benchmarks.md), [product.md](product.md) |

Architecture: [architecture.md](architecture.md). Decisions: [decisions.md](decisions.md) (ADR-001 – ADR-010).

## Current State

`docker compose up --build` runs the gateway, dashboard, PostgreSQL (migrated and seeded), and two demo backends.

Verified:
- /health returns 200; /ready returns 200, or 503 when PostgreSQL is unreachable
- Missing key -> 401
- Invalid key -> 401
- Revoked key -> 403
- Valid key -> reaches upstream
- Free plan's request over its per-minute limit -> 429 with `Retry-After`
- Repeated GET on a cached route -> `X-Cache: HIT`, backend not called; `Cache-Control: no-cache` -> `X-Cache: BYPASS`
- Request body over 10 MiB -> 413
- Analytics events written asynchronously and flushed on graceful shutdown
- Admin API: 401 without token, 403 with a wrong one; key, plan, and route edits apply to the next request
- Dashboard: all five pages work against the live admin API, desktop and mobile, via Vite and via the nginx container
- `scripts/demo.sh` passes against the compose stack, and checks every status code and cache header
- `go test -race ./...` clean with PostgreSQL integration tests, and gofmt/vet clean, in a Linux container (as CI runs it)
- Benchmark suite: 0 errors up to concurrency 500 ([benchmarks.md](benchmarks.md))

## Known Limitations

- Rate-limit state and the response cache are per instance and reset on restart (ADR-002, ADR-003, ADR-007).
- Every request, including cache hits, looks up its API key in PostgreSQL; this is where most of the remaining overhead goes under load ([benchmarks.md](benchmarks.md)).
- Route edits apply immediately on the instance that handled them; other instances pick them up on restart (ADR-005).
- `request_events` grows without bound; add a retention job before production (see analytics.md).
- No time-series analytics endpoint; the dashboard's live charts sample in the browser (ADR-006).
- The compose stack's demo keys and admin token are public (ADR-010); migrations there run only on a fresh volume.
- On Windows, Go's coarse clock records very fast requests as 0 µs latency (Linux containers are unaffected).

## Possible Next Steps

- In-memory API key cache with a short TTL (decide how long a revoked key may keep working), then re-run the benchmarks
- `/admin/analytics/timeseries` for historical traffic and latency charts
- Periodic route reload so every instance applies admin edits without a restart
- Shared rate-limit and cache store (e.g. Redis) for multi-instance deployments
- Retention job or monthly partitions for `request_events`
