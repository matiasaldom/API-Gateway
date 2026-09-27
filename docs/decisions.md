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