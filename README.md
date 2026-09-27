# API Gateway

A high-performance API Gateway written in Go.

## Features

- Reverse proxy routing
- API key authentication
- Per-plan rate limiting (fixed window)
- Per-route response caching (in-memory, per API key)
- Asynchronous request analytics (PostgreSQL, admin endpoints)
- PostgreSQL-backed identity storage
- Request tracing IDs
- Structured logging
- Configuration-driven routing

## Architecture

Client
→ Gateway
→ Analytics (async event → PostgreSQL)
→ Authentication
→ Rate Limiting
→ Cache
→ Routing
→ Reverse Proxy
→ Backend Services

## Tech Stack

- Go
- PostgreSQL
- Docker
- YAML Configuration

## Running Locally

### Start PostgreSQL

docker run ...

### Configure Environment

DATABASE_URL=...

ADMIN_TOKEN=$(openssl rand -hex 32)   # optional: enables /admin/* and /analytics/*

### Apply Migrations

for f in migrations/*.up.sql; do psql "$DATABASE_URL" -f "$f"; done

### Start Gateway

go run ./cmd/gateway

### Create an API Key

go run ./cmd/apikey create -email dev@example.com -app "My App"

or, with the gateway running and ADMIN_TOKEN set:

curl -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" -d '{"email":"dev@example.com","application":"My App"}' http://localhost:8080/admin/api-keys

curl http://localhost:8080/users/1 -H "Authorization: Bearer <key>"

See [docs/authentication.md](docs/authentication.md) for key management, error responses, and logging,
[docs/rate-limiting.md](docs/rate-limiting.md) for plan limits and rate-limit headers,
[docs/caching.md](docs/caching.md) for per-route response caching,
[docs/analytics.md](docs/analytics.md) for analytics collection and endpoints,
and [docs/admin-api.md](docs/admin-api.md) for managing keys, plans, and routes over HTTP.

## Health Check & Metrics

curl http://localhost:8080/health

curl http://localhost:8080/metrics   # cache_hits, cache_misses, cache_entries, cache_bytes

curl -H "Authorization: Bearer $ADMIN_TOKEN" http://localhost:8080/analytics/summary?window=24h
# also /analytics/routes and /analytics/accounts

## Tests

go test ./...

## Roadmap

- [x] Reverse proxy
- [x] API key authentication
- [x] API key administration CLI
- [x] PostgreSQL persistence
- [x] Rate limiting (fixed window, in-memory)
- [x] Response caching (in-memory, per API key)
- [x] Analytics (async collection, admin endpoints)
- [x] Management API (keys, plans, routes, analytics)
- [ ] Dashboard

## Current Status

Completed:
- Reverse proxy routing
- API key authentication
- PostgreSQL-backed identity storage
- API key creation, revocation, and listing CLI
- Per-plan fixed window rate limiting
- Per-route response caching
- Asynchronous analytics with admin endpoints
- Management API (API keys, plan limits, live route edits)
- Request ID tracing
- Structured logging
- Integration tests

Next:
- Dashboard
