# API Gateway

A high-performance API Gateway written in Go.

## Features

- Reverse proxy routing
- API key authentication
- Per-plan rate limiting (fixed window)
- PostgreSQL-backed identity storage
- Request tracing IDs
- Structured logging
- Configuration-driven routing

## Architecture

Client
→ Gateway
→ Authentication
→ Rate Limiting
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

### Apply Migrations

for f in migrations/*.up.sql; do psql "$DATABASE_URL" -f "$f"; done

### Start Gateway

go run ./cmd/gateway

### Create an API Key

go run ./cmd/apikey create -email dev@example.com -app "My App"

curl http://localhost:8080/users/1 -H "Authorization: Bearer <key>"

See [docs/authentication.md](docs/authentication.md) for key management, error responses, and logging,
and [docs/rate-limiting.md](docs/rate-limiting.md) for plan limits and rate-limit headers.

## Health Check

curl http://localhost:8080/health

## Tests

go test ./...

## Roadmap

- [x] Reverse proxy
- [x] API key authentication
- [x] API key administration CLI
- [x] PostgreSQL persistence
- [x] Rate limiting (fixed window, in-memory)
- [ ] Response caching
- [ ] Analytics
- [ ] Management API

## Current Status

Completed:
- Reverse proxy routing
- API key authentication
- PostgreSQL-backed identity storage
- API key creation, revocation, and listing CLI
- Per-plan fixed window rate limiting
- Request ID tracing
- Structured logging
- Integration tests

In Progress:
- Response caching
- Analytics