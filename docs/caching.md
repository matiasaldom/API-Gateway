# Response Caching

The gateway can keep successful `GET` responses in memory and serve repeat requests without calling the backend. Caching is set per route and is off unless a route turns it on.

```
Request ID → Authentication → Rate Limiter → Cache → Router → Proxy
```

Cache hits still count against the key's rate limit, because the limiter runs before the cache.

## Configuration

Set `cache_ttl` on a route in `gateway.yaml`:

```yaml
routes:
  - prefix: /albums
    upstream: http://localhost:8082
    cache_ttl: 30s      # any Go duration: 500ms, 30s, 5m
  - prefix: /users
    upstream: http://localhost:8081
                        # no cache_ttl, or 0: never cached
```

A negative value stops the gateway at startup. Changing `cache_ttl` requires a restart.

## What gets cached

A response is stored only when **all** of these are true:

| Condition | Why |
|---|---|
| Method is `GET` | `POST`, `PUT`, `DELETE`, `HEAD` and others always go to the backend and are never stored |
| The route has `cache_ttl` > 0 | Caching is opt-in per route |
| The request is authenticated | Entries are tied to an API key. A request without one is never cached. |
| Status is `200` | Errors and redirects are never stored, so one failure isn't repeated to later requests |
| No `Set-Cookie` header | The response is specific to one session |
| `Cache-Control` has no `no-store` or `private` | The backend has said not to store it |
| `Vary` is absent or only `Accept-Encoding` | Any other `Vary` means the response depends on headers the cache key doesn't include |
| Body is complete and at most 1 MiB | Truncated or large responses are passed through without being stored |

### Cache key

Each entry is identified by:

- HTTP method
- path
- the query string exactly as sent (`?a=1&b=2` and `?b=2&a=1` are separate entries)
- **authenticated API key ID**
- `Accept-Encoding`, because the same URL can come back gzip-compressed or not

Because the API key ID is part of the key, **one key never receives a response cached for another key**, even for the same URL.

Keys aren't split further by end user. If one API key serves several of your users and a backend personalizes a `GET` response by something other than the URL (for example an `X-User-Id` header), either don't enable `cache_ttl` on that route or have the backend send `Cache-Control: private`.

## Responses

The cache adds these headers when it is consulted:

| Header | Value |
|---|---|
| `X-Cache` | `MISS` when the backend was called, `HIT` when served from memory. Absent when caching didn't apply (non-GET request, or a route without `cache_ttl`). |
| `Age` | On hits: seconds since the response was stored |

A hit replays the stored status, body and backend headers, such as `Content-Type` and `ETag`. Some headers are always generated fresh for each request and never taken from the cache: `X-Request-ID`, `X-RateLimit-*` and `Date`.

## Expiry and memory

- **Expiry:** an entry expires exactly `cache_ttl` after it was stored. An expired entry is never served. It's removed as soon as someone requests it, and a background sweep once a minute removes any other expired entries.
- **Memory cap:** the cache holds at most **256 MiB** in total (`cacheMaxBytes` in `cmd/gateway/main.go`). When it's full, new responses aren't stored until expired entries are freed. Nothing is pushed out early (there is no LRU).
- **Per instance:** each gateway instance has its own cache, and restarting the gateway clears it.
- **Backend load:** if several requests for the same uncached URL arrive at once, each one calls the backend.

## Metrics

`GET /metrics` returns Prometheus text format. Like `/health`, it needs no API key.

```
cache_hits 2
cache_misses 1
cache_entries 1
cache_bytes 171
```

| Metric | Type | Meaning |
|---|---|---|
| `cache_hits` | counter | Responses served from the cache |
| `cache_misses` | counter | Cacheable requests that had to go to the backend |
| `cache_entries` | gauge | Responses currently stored |
| `cache_bytes` | gauge | Approximate memory used by stored responses |

Hit ratio = `cache_hits / (cache_hits + cache_misses)`. The endpoint only exposes these counts, never response data. If you don't want it public, block `/metrics` at your load balancer.

## Tests

| What | Where |
|---|---|
| Storing and reading entries, expiry at the exact instant, sweeping, the memory cap, key generation, concurrent access, metrics output | `internal/cache/cache_test.go` |
| Hits skip the backend, fresh per-request headers, requests that bypass the cache, responses that are never stored, `Vary: Accept-Encoding` | `internal/cache/middleware_test.go` |
| Repeated GET skips the backend, TTL expiry calls the backend again, API keys don't share entries, POST is never cached, a route without a TTL is never cached | `tests/cache_test.go` |

All of these run clean under `go test -race ./...` (see [rate-limiting.md](rate-limiting.md#tests) for running `-race` on Windows).
