# Architecture

## Request path

Every proxied request passes through the same chain. `/health`, `/metrics`, and the admin API branch off before it.

```mermaid
flowchart LR
    client([Client])

    subgraph gw[Gateway process]
        direction LR
        rid[Request ID]
        log[Access log]
        an[Analytics<br/>timer + event]
        auth[Auth<br/>API key]
        ident[Identify]
        rl[Rate limiter<br/>fixed window]
        cache[Response cache<br/>per key, per route]
        router[Router<br/>live route table]
        proxy[Reverse proxy]
    end

    upstream([Upstream services])
    pg[(PostgreSQL)]
    worker[[Analytics worker<br/>batch COPY]]

    client --> rid --> log --> an --> auth --> ident --> rl --> cache --> router --> proxy --> upstream
    auth -.->|key + plan lookup| pg
    an -.->|event, non-blocking| worker -.->|batched COPY| pg
    cache -.->|hit: skip upstream| client
```

| Stage | Package | Rejects with | State |
|---|---|---|---|
| Request ID | `internal/requestid` | — | — |
| Access log | `internal/proxy` | — | — |
| Analytics | `internal/metrics` | — | Buffered channel → worker → `request_events` |
| Auth | `internal/auth` | 401 missing/invalid, 403 revoked, 503 DB down | `api_keys` + `plans` (one query per request) |
| Identify | `internal/metrics` | — | Attaches key/app/plan to the analytics event |
| Rate limiter | `internal/limiter` | 429 + `Retry-After` | In-memory counters, clock-aligned minutes |
| Cache | `internal/cache` | — | In-memory, 256 MiB cap, TTL per route |
| Router | `internal/routing` | 404 no route | Atomic route table (swapped by admin edits) |
| Proxy | `internal/proxy` | 502 upstream error, 504 timeout | — |

## Whole system

```mermaid
flowchart TB
    subgraph clients[Clients]
        apps([API consumers<br/>Bearer API key])
        ops([Operators])
    end

    subgraph dash[dashboard/ — React + Vite]
        ui[Dashboard UI]
        vproxy[Vite proxy<br/>/admin → gateway]
    end

    subgraph gateway[Gateway — cmd/gateway]
        pipeline[Request pipeline<br/>auth · limit · cache · route · proxy]
        admin[Admin API<br/>/admin/* · ADMIN_TOKEN]
        metricsEp["/metrics · /health"]
        table[(Live route table)]
        mem[(In-memory<br/>rate counters · cache)]
        collector[[Analytics collector]]
    end

    cli[[apikey CLI<br/>cmd/apikey]]
    yaml[/gateway.yaml<br/>declares routes/]
    pg[(PostgreSQL<br/>users · applications · plans<br/>api_keys · routes · request_events)]
    upstreams([Upstream services])

    apps --> pipeline --> upstreams
    ops --> ui --> vproxy --> admin
    ops --> cli --> pg
    pipeline --- table
    pipeline --- mem
    pipeline --> collector --> pg
    pipeline -- key + plan lookup --> pg
    admin -- keys · plans · routes · analytics --> pg
    admin -- live route swap --> table
    yaml -- synced at startup --> pg
```

## Where state lives

| Data | Stored in | Shared across instances | Survives restart |
|---|---|---|---|
| Users, applications, API key hashes | PostgreSQL | yes | yes |
| Plan limits | PostgreSQL (read on every request) | yes, immediately | yes |
| Routes (upstream, cache TTL) | PostgreSQL + in-memory live table | on restart (ADR-005) | yes |
| Rate-limit counters | memory | no (per instance) | no |
| Cached responses | memory | no (per instance) | no |
| Analytics events | PostgreSQL (`request_events`) | yes | yes (unflushed events are lost on crash) |

## Design decisions

See [decisions.md](decisions.md):

| ADR | Decision |
|---|---|
| 001 | One upstream timeout for the whole request/response |
| 002 | In-memory fixed-window rate limiting; limits stored on plans |
| 003 | Opt-in per-route response cache, scoped per API key |
| 004 | Raw analytics events, async batched writes, aggregated at query time |
| 005 | Routes in PostgreSQL, declared by gateway.yaml, edited via admin API |
| 006 | Dashboard as a separate Vite app, same-origin via proxy |
