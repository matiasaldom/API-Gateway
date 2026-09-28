# Rate Limiting

Every authenticated request counts against its API key's plan limit. When a key uses up its limit, further requests get `429 Too Many Requests` until the next window starts.

```
Request ID → Authentication → Rate Limiter → Router → Proxy
```

`/health` is not rate limited. Requests rejected by authentication (`401`/`403`) never reach the limiter and don't count.

## Plans

| Plan | Limit |
|---|---|
| `free` | 100 requests / minute |
| `pro` | 1000 requests / minute |

- **Where limits live:** in `plans.requests_per_minute`. Migration `000002_plan_rate_limits` adds the column and seeds these two plans.
- **How they're read:** the limit comes back with the API key lookup that authentication already does. The limiter adds no database queries and never writes to the database.
- **Choosing a plan:** give `apikey create` the `-plan` flag (default `free`). The plan must already exist.
- **Changing a limit:** update the row. The new value applies to the key's next request, with no restart:
  ```sql
  update plans set requests_per_minute = 2000 where name = 'pro';
  ```

## Response headers

Every request that reaches the limiter gets:

| Header | Meaning |
|---|---|
| `X-RateLimit-Limit` | The key's limit per minute |
| `X-RateLimit-Remaining` | Requests left in the current window, after this one |
| `Retry-After` | Seconds until the window resets. **Only sent on `429` responses.** |

Go writes the names on the wire as `X-Ratelimit-Limit` and `X-Ratelimit-Remaining`. HTTP header names are case-insensitive, so clients should look them up without regard to case.

## When the limit is exceeded

```
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Retry-After: 30
X-Ratelimit-Limit: 100
X-Ratelimit-Remaining: 0
X-Request-Id: 5c0f…

{"error":"rate limit exceeded","request_id":"5c0f…"}
```

The body has the same shape as every other gateway error (see [authentication.md](authentication.md#error-responses)). Clients should wait `Retry-After` seconds before retrying.

## How windows work

- **Windows follow the clock:** each window is one wall-clock minute (`12:00:00.000`–`12:00:59.999`), and all keys start a new window at the same instant. A request at exactly `12:01:00.000` belongs to the new window. `Retry-After` is rounded up, so it is never `0`.
- **Bursts at the boundary:** a client can send its full limit at the end of one window and again at the start of the next. That allows up to 2× the limit within a few seconds. This is the accepted cost of fixed windows.
- **Limits are per gateway instance:** counters live in memory. Running N instances allows up to N× the limit, and restarting an instance resets its counters.

See ADR-002 in [decisions.md](decisions.md) for why this design was chosen.

## Token bucket (optional)

Fixed windows are the default. Set this in `gateway.yaml` to use token buckets instead:

```yaml
rate_limit_algorithm: token_bucket   # default: fixed_window
```

Each key gets a bucket holding up to its plan limit in tokens, refilled continuously at `limit / 60` tokens per second. Every request takes one token, and an empty bucket means `429`.

| | Fixed window | Token bucket |
|---|---|---|
| Burst from idle | Full limit | Full limit |
| Most requests in any 2 seconds (limit 60/min) | 120, across a window boundary | 62: the full bucket plus 2 seconds of refill |
| `X-RateLimit-Remaining` | Requests left in this minute | Whole tokens left in the bucket |
| `Retry-After` on 429 | Seconds until the next minute | Seconds until one token refills (rounded up) |
| Memory | One counter per key active this minute | One bucket per key active in the last minute |

`TestTokenBucketHasNoWindowBoundaryBurst` checks the 120-versus-62 row against both algorithms. Buckets idle for a full minute are dropped, since a full bucket and a missing one behave the same. Everything else stays the same: limits come from the plan and apply on the next request, state is per instance, and one rejection per key per minute is logged. See ADR-007.

## Logging

The first rejection for a key in each window is logged once at `WARN`:

```json
{"level":"WARN","msg":"rate limit exceeded","request_id":"5c0f…","api_key_id":7,"application_id":3,"plan_id":1,"limit":100,"window":"1m0s","retry_after_s":30}
```

A client that keeps sending requests after that produces no more of these lines. Each rejected request still appears in the access log with `"status":429`.

## Performance

The limiter holds a single mutex while it looks up and increments a map entry.

Measured on an i7-11700B:

| Benchmark | Result |
|---|---|
| `Allow`, one goroutine | ~35 ns, 0 allocations |
| `Allow`, 8 goroutines with different keys | ~80 ns |
| Middleware added to a request (headers included) | ~275 ns |

The token bucket's `Allow` measures ~57 ns against ~55 ns for the fixed window, both with 0 allocations, when both run in the `golang:1.25` Linux container on the same machine. Container numbers run higher than the native Windows figures above.

For scale, a proxied request costs about 0.5 ms end to end through the gateway (see [benchmarks.md](benchmarks.md)), so the limiter is well under 0.1% of it. To reproduce:

```sh
go test -run '^$' -bench . -benchmem ./internal/limiter/
```

If contention on the mutex ever shows up under real load, the next step is to split the counters by key across several mutexes.

## Tests

| What | Where |
|---|---|
| Counter increments, window reset, exact boundary, limit exceeded | `internal/limiter/limiter_test.go` |
| Token bucket: burst then refill, no boundary burst, lowered limit, idle-bucket sweep, concurrency, middleware headers | `internal/limiter/tokenbucket_test.go` |
| Many concurrent callers never exceed the limit, including across a window change | `internal/limiter/limiter_test.go` |
| Free plan: 101st request → 429; Pro plan: 1001st → 429, with headers | `tests/ratelimit_test.go` |
| Limits are per key; 250 concurrent requests → exactly 100 allowed | `tests/ratelimit_test.go` |

The integration tests fix the limiter's clock partway through a minute, so they can't fail by crossing a window boundary. Run everything under the race detector with:

```sh
go test -race ./...
```

On Windows, `-race` needs cgo and a C compiler. Without one, run it in a container:

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.27 go test -race ./...
```
