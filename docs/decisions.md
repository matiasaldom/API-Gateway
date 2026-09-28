ADR-001

Current timeout budget applies to the entire upstream
request/response lifecycle.

Pros:
- Simpler implementation
- Prevents slow upstreams from tying up connections indefinitely

Cons:
- Long-running downloads may be interrupted

Future Consideration:
- Separate:
    Dial timeout
    Header timeout
    Idle timeout
    Response streaming timeout

ADR-002

Rate limiting uses in-memory fixed windows aligned to the
clock minute, keyed by API key. Each plan's limit is stored
on the plans row and read with the API key lookup.

Pros:
- One mutex and a map; ~35 ns per check, no allocations
- No database writes and no extra queries per request
- Windows reset for every key at once, so the whole map is
  cleared at rollover instead of tracking a window per key
- Limit changes take effect on the next request, no restart

Cons:
- Clients can burst up to 2x the limit around a window boundary
- Limits apply per gateway instance; N instances allow N x the limit
- Counters reset when the gateway restarts

Future Consideration:
- Shared store (e.g. Redis) once the gateway runs more than one instance
- Sliding window or token bucket if boundary bursts cause problems
- Shard the mutex by key if contention shows up

ADR-003

Response caching is in-memory, opt-in per route (cache_ttl),
and scoped per API key. Only complete 200 GET responses without
Set-Cookie, no-store/private, or a Vary other than Accept-Encoding
are stored. Headers set by the gateway itself (request ID,
rate-limit counts, Date) are never stored.

Pros:
- One API key can never receive a response cached for another key
- No cache by default: backends opt in route by route
- Cache hits still count against the rate limit
- A total memory cap refuses new entries when full instead of
  running an eviction policy

Cons:
- Entries are not split by end user within one API key; routes
  that personalize by headers must not enable caching, or the
  backend must send Cache-Control: private
- Concurrent misses for the same key all reach the backend
- Per instance; cleared on restart
- When full, new responses go uncached until the janitor frees
  expired entries

Future Consideration:
- Combine concurrent misses for the same key (singleflight)
- LRU eviction if the memory cap is reached in practice
- Shared cache (e.g. Redis) once the gateway runs multiple instances

ADR-004

Analytics stores one raw event per request in PostgreSQL
(request_events). Events are emitted after the response into
a bounded buffer, then batch-written by one background worker
using COPY. Totals are computed when an endpoint is queried,
not stored ahead of time.

Pros:
- Requests never wait for the database; a full buffer drops
  events and counts them instead of adding latency
- Writer errors and panics are contained in the worker
- Raw events answer any breakdown (route, key, plan, status)
  without deciding the questions up front
- Exact percentiles from percentile_cont
- Shutdown writes pending events after in-flight requests finish

Cons:
- Events can be lost when the buffer overflows, a write fails,
  or the process crashes; this is acceptable for analytics,
  not for billing
- Table grows without limit until a retention job is added
- Query cost grows with the window; long windows over large
  tables scan many rows
- 429/504 from backends count the same as the gateway's own

Future Consideration:
- Retention job, or monthly partitions dropped on a schedule
- Per-minute rollup tables if long-window queries become slow
- Retry a failed batch once before dropping it

ADR-005

Routes are stored in PostgreSQL (routes table) so the admin
API can edit them and the edits persist. gateway.yaml still
decides which routes exist: at startup, new prefixes are
inserted with their YAML values, prefixes missing from the
YAML are deleted, and existing rows keep their stored
upstream and cache TTL. The live route table is swapped
atomically after an admin edit, so changes apply without a
restart.

Pros:
- Route edits apply immediately and survive restarts
- gateway.yaml keeps working as the declaration of routes and
  the first set of values; no separate seeding step
- Only one place defines route validation (the routing package)

Cons:
- After an admin edit, gateway.yaml no longer shows the real
  upstream and TTL for that route (the gateway logs a warning
  at startup when they differ)
- Other gateway instances apply a route edit only when they
  restart; plan limits, by contrast, apply everywhere immediately
- Removing a route from gateway.yaml discards its stored edits

Future Consideration:
- Reload routes on a timer (or via LISTEN/NOTIFY) so every
  instance applies edits without a restart
- POST/DELETE /admin/routes if routes should be managed without
  gateway.yaml at all
ADR-006

The dashboard (dashboard/) is a separate Vite + React app that
calls the existing /admin/* endpoints with relative paths.
The Vite dev and preview servers proxy /admin to the gateway,
so the browser sees one origin. The admin token is entered at
sign-in and kept in sessionStorage.

Pros:
- No backend changes: no CORS, no static-file serving, no new
  endpoints in the gateway
- Admin API stays same-origin, so no cross-site request surface
- Token disappears when the tab closes

Cons:
- Hosting dist/ outside Vite needs a same-origin reverse proxy
  in front of the gateway
- No time-series endpoint: live charts are sampled in the
  browser and show only what was sampled since the page opened
- A token in sessionStorage is readable by any script on the
  page (an XSS on the dashboard origin would expose it)

Future Consideration:
- /admin/analytics/timeseries (per-minute buckets) for history charts
- Serve dist/ from the gateway itself behind the admin token

ADR-007

Token bucket is available as a second rate-limiting algorithm,
chosen for the whole gateway with rate_limit_algorithm in
gateway.yaml. Fixed window stays the default. Both implement
one small interface (Allow(key, limit) → Decision), so the
middleware, headers, logging, and plan limits are shared.

Pros:
- Removes the 2x burst at window boundaries (ADR-002): a key
  never gets more than limit + rate*t requests in any span t
- Clients still get their full limit as an initial burst, so
  well-behaved clients see no difference
- Retry-After is exact: time until one token refills
- Same memory bound as fixed windows: buckets idle for a minute
  are dropped, because a full bucket and no bucket are the same

Cons:
- Float arithmetic per request, though measured cost is the same
  as the fixed-window counter (~57 ns vs ~55 ns, no allocations)
- Remaining is "tokens now", which clients find less intuitive
  than "requests left this minute"
- The idle-bucket sweep runs inline once a minute under the lock,
  scanning every bucket
- One algorithm for all plans; not selectable per plan

Future Consideration:
- Per-plan algorithm (a plans column) if tiers need different burst rules
- Move the sweep to a background goroutine at very high key counts
- Shared buckets in Redis for multiple instances (same limit as ADR-002)

ADR-008

The gateway refuses requests and upstreams that could hurt it
or the network it runs in:
- Request bodies over 10 MiB get 413 (checked against
  Content-Length first, and enforced while streaming for
  chunked bodies)
- Upstreams may not be link-local (169.254.0.0/16, fe80::/10)
  or unspecified addresses. Checked when a route is loaded or
  edited, and again by the proxy's dialer after DNS resolution
- /ready checks PostgreSQL; /health only checks the process

Pros:
- A stolen admin token can't point a route at the cloud
  metadata service (169.254.169.254) to read instance
  credentials, even through a hostname that resolves there
- One oversized upload can't tie up an upstream or gateway memory
- Load balancers can tell "process up" from "able to serve"

Cons:
- Private (10/8, 192.168/16) and loopback upstreams stay
  allowed, because internal services are the normal case; the
  gateway does not stop routes to other internal services
- AWS's IPv6 metadata address (fd00:ec2::254) is a unique-local
  address, not link-local, and is not blocked
- One body limit for all routes

Future Consideration:
- Per-route body limits and an upstream allowlist (hosts or CIDRs)
  in gateway.yaml

ADR-009

The proxy's upstream connection pool keeps up to 512 idle
connections per upstream host (4096 in total), instead of Go's
default of 2 per host.

Found by the first benchmark run (docs/benchmarks.md): with the
default, a gateway serving 50 concurrent clients kept 2
connections and closed the other 48 after every request. Each
closed connection sits in TIME_WAIT, so the container ran out of
ephemeral ports: 10,324 requests failed with "connect: cannot
assign requested address" (502), throughput without the cache fell
to ~1,000 req/s, and the gateway spent most of its CPU opening
TCP connections.

Pros:
- Connections are reused under concurrency; the fix removed every
  upstream error in the benchmark suite
- TestUpstreamConnectionsAreReused fails if the pool regresses
  (it counts new connections at the upstream)

Cons:
- Up to 512 idle connections per upstream held open for 90s after
  a burst (IdleConnTimeout), on both sides
- One fixed size for all upstreams

Future Consideration:
- Pool size per route in gateway.yaml if upstreams differ a lot
- MaxConnsPerHost to cap connections to a fragile upstream

ADR-010

docker compose runs a complete demo: PostgreSQL migrated and
seeded on first start, two echo backends (cmd/echo), the
gateway, and the dashboard behind nginx. The seed creates
demo API keys with fixed, published values, and compose has
a default ADMIN_TOKEN.

Pros:
- One command from clone to working demo; the demo script,
  the benchmarks, and the CI end-to-end job all use the same keys
- nginx serves the dashboard and proxies /admin, which is the
  same-origin setup ADR-006 needs outside Vite

Cons:
- Anyone who reads the repository knows the demo credentials.
  The compose file binds PostgreSQL to 127.0.0.1, but the
  gateway (8080) and dashboard (3000) listen on all interfaces
- Migrations run only when the database volume is created;
  new migrations need `docker compose down -v` or a manual apply

Future Consideration:
- A migration tool with version tracking (golang-migrate) if the
  schema changes often
