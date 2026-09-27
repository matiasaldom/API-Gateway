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