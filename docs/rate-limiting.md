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

For scale, a proxied request takes on the order of 100 µs. To reproduce:

```sh
go test -run '^$' -bench . -benchmem ./internal/limiter/
```

If contention on the mutex ever shows up under real load, the next step is to split the counters by key across several mutexes.

## Tests

| What | Where |
|---|---|
| Counter increments, window reset, exact boundary, limit exceeded | `internal/limiter/limiter_test.go` |
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
