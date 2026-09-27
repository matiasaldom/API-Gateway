# Analytics

The gateway records one event for every request on an API route and saves events to PostgreSQL in the background. Three admin endpoints report on that data.

```
Request ID → Analytics → Authentication → Identify → Rate Limiter → Cache → Router → Proxy
                 │
                 └─ after the response: Emit(event) ──► buffered channel ──► worker ──► batch ──► COPY into request_events
```

- **Nothing on the request path waits for the database.** `Emit` hands the event to a buffer and returns immediately. If the buffer is full, the event is dropped and counted.
- **Analytics failures can't affect traffic.** A failed or panicking database write is logged, counted and dropped, and the worker carries on. If the database is unreachable, requests are still served normally.
- **Events are batched.** A batch is written once it holds 500 events, or after 1 second, whichever comes first, as a single `COPY`.
- **Shutdown writes everything pending.** On SIGINT or SIGTERM the gateway finishes in-flight requests, then writes all queued events, then closes the database. It waits up to 10 s for that final write.

`/health`, `/metrics` and `/analytics/*` aren't recorded.

## What is recorded

Each request becomes one row in `request_events` (migration `000003_analytics`):

| Column | Value |
|---|---|
| `occurred_at` | When the request started |
| `request_id` | Same as the `X-Request-ID` header and the log lines |
| `method`, `status` | HTTP method and final status |
| `route` | The matched route prefix (`/users`), or null if no route matched. Full paths are not stored, because they can contain IDs and personal data. |
| `latency_us` | Total gateway time in microseconds, including authentication, rate limiting and cache |
| `api_key_id`, `application_id`, `plan_id` | From the authenticated key. Null for requests rejected before authentication succeeded (401/403). |
| `cache_status` | `HIT`, `MISS`, or null when the cache wasn't used |

The table has no foreign keys, so deleting an API key or application doesn't affect past events, and an analytics write can never fail because of such a deletion.

**How results are classified:**
- **errors:** status ≥ 500.
- **timeouts:** status 504.
- **rate limit violations:** status 429.

A backend that itself returns 429 or 504 is counted the same way as the gateway's own.

## Endpoints

All three need the **admin token**, not an API key, because they show data for every account:

```sh
export ADMIN_TOKEN=$(openssl rand -hex 32)   # at least 32 characters; set it before starting the gateway
curl -H "Authorization: Bearer $ADMIN_TOKEN" "http://localhost:8080/analytics/summary?window=24h"
```

- **No token set:** if `ADMIN_TOKEN` is unset, `/analytics/*` isn't served at all, and the gateway logs a warning at startup.
- **Token too short:** a token under 32 characters stops the gateway at startup.
- **Wrong or missing token:** a missing token gets `401` and a wrong one gets `403`.
- **Also under `/admin/analytics/*`:** the same endpoints are served there. See [admin-api.md](admin-api.md).

Every endpoint accepts `?window=` with a Go duration (`15m`, `24h`, `168h`). The default is `1h` and the maximum `744h` (31 days). Every response includes the `window`, `from` and `to` it covers.

### `GET /analytics/summary`

Totals for the gateway, plus the health of the collector since the last restart:

```json
{
  "window": "24h0m0s", "from": "2026-09-26T07:42:51Z", "to": "2026-09-27T07:42:51Z",
  "requests": 51, "errors": 50, "timeouts": 0, "rate_limited": 0,
  "cache_hits": 0, "cache_misses": 0,
  "latency_ms": {"p50": 0.576, "p95": 0.8855, "p99": 2.0475},
  "collector": {"written": 51, "failed": 0, "dropped": 0}
}
```

A `collector.dropped` or `collector.failed` value that keeps growing means analytics is losing events: the buffer is overflowing or the database is failing. Gateway traffic is unaffected either way.

### `GET /analytics/routes`

The same numbers broken down per route, busiest first. `"route": null` covers requests that matched no route.

```json
{"window": "24h0m0s", "from": "…", "to": "…",
 "routes": [{"route": "/users", "requests": 51, "errors": 50, "timeouts": 0, "rate_limited": 0,
             "cache_hits": 0, "cache_misses": 0, "latency_ms": {"p50": 0.576, "p95": 0.8855, "p99": 2.0475}}]}
```

### `GET /analytics/accounts`

Requests per application and per API key, including plan utilization:

```json
{"window": "24h0m0s", "from": "…", "to": "…",
 "applications": [{"application_id": 1, "application": "e2e", "owner_email": "e2e@example.com",
                   "plan": "free", "requests": 50, "errors": 50, "rate_limited": 0}],
 "api_keys": [{"api_key_id": 1, "application_id": 1, "requests": 50, "errors": 50, "rate_limited": 0,
               "peak_requests_per_minute": 50, "limit_requests_per_minute": 100, "plan_utilization": 0.5}]}
```

**Plan utilization** is the key's busiest minute in the window divided by its plan's current per-minute limit. Minutes follow the clock, the same windows the rate limiter uses. Rejected requests are counted too, so a value above `1.0` means the key asked for more than its plan allows. That's a useful signal for plan upgrades.

## Engineering vs product metrics

| Wanted | Where |
|---|---|
| Request count, error count, timeout count | `summary`, `routes` |
| p50 / p95 / p99 latency | `summary.latency_ms`, `routes[].latency_ms` (computed in Postgres with `percentile_cont`) |
| Cache hits / misses | `summary`, `routes`. Live counters since the last restart are also on `GET /metrics`. |
| Requests by API key / application | `accounts.api_keys`, `accounts.applications` |
| Requests by route | `routes` |
| Rate limit violations | `rate_limited` in all three |
| Plan utilization | `accounts.api_keys[].plan_utilization` |

## Performance

On an i7-11700B, the analytics middleware adds **about 490 ns and 4 allocations per request** (`BenchmarkMiddlewareOverhead`). For scale, a proxied request takes on the order of 100 µs. Writing to Postgres happens entirely on the background worker.

```sh
go test -run '^$' -bench MiddlewareOverhead -benchmem ./internal/metrics/
```

On Windows, Go's clock advances in coarse steps, so very fast requests (cache hits, 429s) can be recorded as 0 µs. Linux measures with nanosecond precision.

## Operations

- **Migration:** apply `migrations/000003_analytics.up.sql` before deploying this version.
- **Retention:** events are kept forever. Delete old rows on a schedule, for example `delete from request_events where occurred_at < now() - interval '90 days'`, or partition the table by month once it grows.
- **Tuning:** the defaults are a buffer of 10,000 events, batches of 500, a 1 s flush interval and a 5 s write timeout. They're `metrics.Options` in `cmd/gateway/main.go`.
- **Multiple instances:** every instance writes to the same table, so the endpoints show totals across all instances. The `collector` numbers are for the instance that answered.

## Tests

| What | Where |
|---|---|
| Batching (full batches, interval flush of partial batches), event order | `internal/metrics/collector_test.go` |
| Shutdown writes pending events; `Close` is safe to call twice and respects its deadline; `Emit` after close is refused | `internal/metrics/collector_test.go` |
| Failure isolation: writer errors and panics are contained; `Emit` never blocks on a stuck writer | `internal/metrics/collector_test.go`, `middleware_test.go` |
| Concurrent events: 50 goroutines, closing while events are still arriving, no duplicates or losses | `internal/metrics/collector_test.go` |
| Event contents: status, route, identity, latency, cache status; aborted requests still counted | `internal/metrics/middleware_test.go` |
| Admin token required, window validation, response shapes, no internal error details in responses | `internal/metrics/analytics_test.go` |
| Real gateway traffic reaches the endpoints with correct counts | `tests/analytics_test.go` |
| Exact percentiles, 5xx/504/429 counts, and utilization from events with fixed timestamps | `tests/analytics_test.go` |
| Traffic unaffected while the analytics database is down | `tests/analytics_test.go` |
