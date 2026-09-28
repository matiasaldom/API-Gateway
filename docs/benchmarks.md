# Benchmarks

How much latency the gateway adds, how much the cache saves, and what the first benchmark run found and fixed. Every number here comes from [`scripts/bench.sh`](../scripts/bench.sh) and can be reproduced with one command.

**Machine:** Intel Core i7-11700B (8 cores / 16 threads), 32 GB RAM, Windows 11 with Docker Desktop 29.8 (16 CPUs and 15.4 GiB given to its Linux VM). Load generator: oha 1.16.0. Date: 2026-09-27.

## Summary

| Question | Answer (measured) |
|---|---|
| Latency the gateway adds to one request | **+0.50 ms at p50** (0.12 ms direct → 0.62 ms through the gateway, concurrency 1). This is the full pipeline: auth lookup in PostgreSQL, rate limit, analytics, proxy. |
| Throughput through the gateway | **~16,700 req/s** uncached and **~24,000 req/s** from the cache, at concurrency 500 with p99 under 45 ms. There were no errors in any scenario. |
| What the cache saves with a 20 ms backend | **p50 21.6 ms → 1.9 ms (11× lower)**, with throughput from 2,300 to 24,300 req/s. Cache hits never reach the backend. |
| Analytics under sustained saturation | 2.2 million events stored, and 2.5% dropped when the buffer filled. Requests never waited on it (ADR-004). |

## What benchmarking found: upstream connection churn

The first run showed the uncached path falling over under concurrency. The cached path was fine, which pointed at the proxy's connection handling:

| Gateway, no cache | Before | After | Change |
|---|---:|---:|---|
| Concurrency 100: req/s | 964 | 14,657 | **15× more** |
| Concurrency 100: p99 | 653 ms | 18.4 ms | **35× lower** |
| Concurrency 100: failed requests | 3,441 | 0 | |
| Concurrency 500: req/s / p99 | 4,604 / 2,132 ms | 16,650 / 44.7 ms | 3.6× more / 48× lower |
| POST 1 KB, concurrency 50: req/s | 2,401 | 13,154 | 5.5× more |
| POST 1 KB, concurrency 50: failed | 1,256 | 0 | |
| Gateway CPU at concurrency 100 | 1,507% | 577% | 2.6× less |
| Upstream connect errors, whole suite | 10,324 × `connect: cannot assign requested address` | 0 | |

Both runs also log a few hundred `context canceled` upstream errors. Those come from oha cancelling in-flight requests when each timed run ends.

**Cause.** The proxy used Go's `http.DefaultTransport`, which keeps only **2 idle connections per upstream host**. With 100 concurrent requests, 98 connections were closed after every response and new ones opened for the next. Each closed connection waits in `TIME_WAIT`, so the container ran out of ephemeral ports. New connections then failed and became 502s, and the gateway spent its CPU on TCP handshakes.

**Fix.** A transport with up to 512 idle connections per host ([`internal/proxy/gateway.go`](../internal/proxy/gateway.go), ADR-009). `TestUpstreamConnectionsAreReused` counts new connections at the upstream and fails if the pool regresses: without the fix it sees 353 connections for 1,000 requests, and with it at most 100.

## Where the remaining time goes

- **PostgreSQL on every request.** PostgreSQL CPU sits at 300–500% during gateway runs, against ~5% when calling the backend directly. Every request, including cache hits, looks up its API key in PostgreSQL, and analytics events are written in batches on top of that. Cache hits show the highest PostgreSQL CPU only because they run at the highest request rate. Caching key lookups in memory for a few seconds is the obvious next step. The cost is that a revoked key would keep working until its entry expires.
- **The cache does not beat a trivial backend.** The echo backend answers in ~0.1 ms, which is less than the gateway's own work. A cache hit (0.38 ms) is faster than an uncached request through the gateway (0.62 ms), but not faster than calling the echo service directly. The cache pays off when the backend does real work: with a 20 ms backend it is 11× faster.
- **Large responses.** At 500 KB the gateway reaches 53% of direct throughput uncached, since it copies every byte, and 196% of direct throughput from the cache.

## Method

**Setup.** Everything runs from `docker compose` on one machine: PostgreSQL, two echo backends (`cmd/echo`), the gateway, and the load generator ([oha](https://github.com/hatoo/oha)) in its own container on the same Docker network. Every target, including "direct to backend", goes through the same Docker bridge networking, so the comparisons are fair to each other.

**What is compared.**

| Target | Path | What it measures |
|---|---|---|
| Direct to backend | load generator → echo service | The baseline: no gateway |
| Gateway, no cache | load generator → gateway → echo service (`/users`) | Full pipeline: auth lookup, rate limit, analytics, proxy |
| Gateway, cache hit | load generator → gateway (`/albums`, 30 s TTL) | Full pipeline, answered from memory; the backend is not called |

**Workloads.** Concurrency 1, 10, 100 and 500 with a small JSON response; payloads of 100 B, 10 KB and 500 KB at concurrency 50; a backend that takes 20 ms per response; and `POST` requests with a 1 KB body. Each run lasts 10 seconds with keep-alive connections. Requests use a seeded API key on a plan with a very high limit, so no request is rate limited.

**Resources.** CPU and memory are the highest `docker stats` sample taken during each run. 100% CPU means one core.

**Caveats.**
- The load generator, gateway, backends and database share one machine and compete for its CPUs. The numbers compare targets against each other on this machine; they are not a capacity figure for a dedicated server.
- Docker Desktop runs containers in a Linux VM, which adds networking overhead to every target equally.
- The gateway writes one JSON access-log line per request to stdout, as it does in production. Docker's log handling is part of the measured cost.
- oha cancels requests still in flight when each run ends. Those are excluded from the error counts.

**Reproduce.**

```sh
docker compose up -d --build
scripts/bench.sh | tee bench-results.md     # about 8 minutes
```

Micro-benchmarks for single components (no network):

```sh
go test -run '^$' -bench . -benchmem ./internal/limiter/ ./internal/metrics/
```

## Full results (after the fix)

- Docker: Docker Desktop, 16 CPUs, 16550359040 bytes memory
- Load generator: oha 1.16.0
- Rate-limit algorithm: fixed_window
- CPU and memory are the peak of samples taken during each run; 100% CPU = one core.

### 1. Concurrency sweep (small JSON, ~180 B)

| Target | Concurrency | Req/s | p50 ms | p95 ms | p99 ms | Non-2xx | Errors | Gateway CPU | Gateway memory | PostgreSQL CPU |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| Direct to backend | 1 | 8135 | 0.12 | 0.17 | 0.21 | 0 | 0 | 0% | 5 MiB | 3% |
| Gateway, no cache | 1 | 1561 | 0.62 | 0.81 | 0.99 | 0 | 0 | 79% | 10 MiB | 24% |
| Gateway, cache hit | 1 | 2539 | 0.38 | 0.47 | 0.58 | 0 | 0 | 81% | 11 MiB | 33% |
| Direct to backend | 10 | 50439 | 0.16 | 0.44 | 0.64 | 0 | 0 | 3% | 10 MiB | 0% |
| Gateway, no cache | 10 | 8417 | 1.11 | 1.86 | 2.41 | 0 | 0 | 500% | 18 MiB | 196% |
| Gateway, cache hit | 10 | 14428 | 0.61 | 1.14 | 1.79 | 0 | 0 | 471% | 18 MiB | 340% |
| Direct to backend | 100 | 81007 | 0.28 | 5.6 | 8.39 | 0 | 0 | 0% | 16 MiB | 4% |
| Gateway, no cache | 100 | 14657 | 6.21 | 13.65 | 18.44 | 0 | 0 | 577% | 32 MiB | 328% |
| Gateway, cache hit | 100 | 19325 | 4.73 | 8.75 | 12.51 | 0 | 0 | 519% | 33 MiB | 479% |
| Direct to backend | 500 | 160072 | 0.61 | 19.06 | 29.43 | 0 | 0 | 0% | 28 MiB | 5% |
| Gateway, no cache | 500 | 16650 | 29.14 | 37.91 | 44.72 | 0 | 0 | 616% | 64 MiB | 364% |
| Gateway, cache hit | 500 | 23995 | 20.2 | 24.77 | 28.57 | 0 | 0 | 551% | 62 MiB | 502% |

### 2. Payload size (concurrency 50)

| Target | Concurrency | Req/s | p50 ms | p95 ms | p99 ms | Non-2xx | Errors | Gateway CPU | Gateway memory | PostgreSQL CPU |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| Direct, 100 B | 50 | 118082 | 0.17 | 1.71 | 2.48 | 0 | 0 | 0% | 56 MiB | 4% |
| Gateway no cache, 100 B | 50 | 14998 | 3.02 | 6.67 | 8.87 | 0 | 0 | 569% | 37 MiB | 313% |
| Gateway cache hit, 100 B | 50 | 22090 | 2.04 | 4.24 | 5.98 | 0 | 0 | 545% | 34 MiB | 491% |
| Direct, 10000 B | 50 | 51788 | 0.45 | 3.44 | 4.61 | 0 | 0 | 0% | 31 MiB | 42% |
| Gateway no cache, 10000 B | 50 | 11073 | 4.04 | 9.24 | 12.48 | 0 | 0 | 552% | 35 MiB | 248% |
| Gateway cache hit, 10000 B | 50 | 21018 | 2.19 | 4.32 | 5.79 | 0 | 0 | 586% | 32 MiB | 445% |
| Direct, 500000 B | 50 | 6319 | 3.63 | 22.61 | 31.99 | 0 | 0 | 0% | 30 MiB | 6% |
| Gateway no cache, 500000 B | 50 | 3353 | 12.77 | 33.5 | 45.91 | 0 | 0 | 476% | 37 MiB | 102% |
| Gateway cache hit, 500000 B | 50 | 12398 | 3.69 | 7.6 | 10.26 | 0 | 0 | 610% | 33 MiB | 331% |

### 3. Slow backend: 20 ms per response (concurrency 50)

| Target | Concurrency | Req/s | p50 ms | p95 ms | p99 ms | Non-2xx | Errors | Gateway CPU | Gateway memory | PostgreSQL CPU |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| Direct to backend | 50 | 2396 | 20.84 | 21.59 | 22.21 | 0 | 0 | 2% | 29 MiB | 0% |
| Gateway, no cache | 50 | 2300 | 21.57 | 22.99 | 24.13 | 0 | 0 | 101% | 30 MiB | 51% |
| Gateway, cache hit | 50 | 24322 | 1.88 | 3.77 | 5.06 | 0 | 0 | 543% | 28 MiB | 495% |

### 4. Writes: POST with a 1 KB JSON body (concurrency 50)

| Target | Concurrency | Req/s | p50 ms | p95 ms | p99 ms | Non-2xx | Errors | Gateway CPU | Gateway memory | PostgreSQL CPU |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| Direct to backend | 50 | 55828 | 0.3 | 3.62 | 5.29 | 0 | 0 | 3% | 27 MiB | 4% |
| Gateway | 50 | 13154 | 3.35 | 8.07 | 10.89 | 0 | 0 | 558% | 35 MiB | 288% |

### Analytics under load

Analytics events stored: 2206731. Dropped because the buffer was full: 57171 (by design; see ADR-004).

## Before the connection-pool fix

The same suite on the same machine, before ADR-009. Only the rows through the gateway without the cache changed materially.

- Docker: Docker Desktop, 16 CPUs, 16550359040 bytes memory
- Load generator: oha 1.16.0
- Rate-limit algorithm: fixed_window
- CPU and memory are the peak of samples taken during each run; 100% CPU = one core.

### 1. Concurrency sweep (small JSON, ~180 B)

| Target | Concurrency | Req/s | p50 ms | p95 ms | p99 ms | Non-2xx | Errors | Gateway CPU | Gateway memory | PostgreSQL CPU |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| Direct to backend | 1 | 8107 | 0.12 | 0.17 | 0.21 | 0 | 0 | 0% | 6 MiB | 3% |
| Gateway, no cache | 1 | 1555 | 0.61 | 0.84 | 1.05 | 0 | 0 | 79% | 10 MiB | 22% |
| Gateway, cache hit | 1 | 2575 | 0.38 | 0.47 | 0.56 | 0 | 0 | 85% | 11 MiB | 34% |
| Direct to backend | 10 | 49628 | 0.16 | 0.44 | 0.65 | 0 | 0 | 0% | 11 MiB | 4% |
| Gateway, no cache | 10 | 5272 | 1.49 | 4.41 | 6.53 | 0 | 0 | 946% | 27 MiB | 166% |
| Gateway, cache hit | 10 | 14165 | 0.62 | 1.16 | 1.78 | 0 | 0 | 476% | 26 MiB | 317% |
| Direct to backend | 100 | 72398 | 0.32 | 6.13 | 9.3 | 0 | 0 | 0% | 24 MiB | 7% |
| Gateway, no cache | 100 | 964 | 83.46 | 269.88 | 653.37 | 3441 | 0 | 1507% | 54 MiB | 54% |
| Gateway, cache hit | 100 | 21292 | 4.45 | 7.16 | 9.28 | 0 | 0 | 528% | 41 MiB | 490% |
| Direct to backend | 500 | 145142 | 0.59 | 21.18 | 33.19 | 0 | 0 | 0% | 37 MiB | 5% |
| Gateway, no cache | 500 | 4604 | 66.37 | 169.05 | 2131.78 | 75 | 0 | 1593% | 120 MiB | 213% |
| Gateway, cache hit | 500 | 21463 | 22.58 | 28.16 | 35.12 | 0 | 0 | 549% | 72 MiB | 494% |

### 2. Payload size (concurrency 50)

| Target | Concurrency | Req/s | p50 ms | p95 ms | p99 ms | Non-2xx | Errors | Gateway CPU | Gateway memory | PostgreSQL CPU |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| Direct, 100 B | 50 | 59373 | 0.32 | 3.24 | 4.76 | 0 | 0 | 0% | 55 MiB | 4% |
| Gateway no cache, 100 B | 50 | 1000 | 49.37 | 91.36 | 131.98 | 3746 | 0 | 1487% | 49 MiB | 31% |
| Gateway cache hit, 100 B | 50 | 21401 | 2.13 | 4.32 | 5.85 | 0 | 0 | 542% | 44 MiB | 491% |
| Direct, 10000 B | 50 | 33848 | 0.57 | 5.09 | 7.49 | 0 | 0 | 3% | 39 MiB | 1% |
| Gateway no cache, 10000 B | 50 | 4581 | 6.93 | 32.38 | 73.71 | 575 | 0 | 1458% | 50 MiB | 255% |
| Gateway cache hit, 10000 B | 50 | 18894 | 2.41 | 4.87 | 6.7 | 0 | 0 | 599% | 41 MiB | 438% |
| Direct, 500000 B | 50 | 5117 | 4.82 | 28.3 | 41.31 | 0 | 0 | 3% | 36 MiB | 0% |
| Gateway no cache, 500000 B | 50 | 782 | 59.92 | 122.97 | 215.78 | 1053 | 0 | 1336% | 51 MiB | 58% |
| Gateway cache hit, 500000 B | 50 | 10101 | 4.46 | 9.61 | 13.53 | 0 | 0 | 595% | 48 MiB | 339% |

### 3. Slow backend: 20 ms per response (concurrency 50)

| Target | Concurrency | Req/s | p50 ms | p95 ms | p99 ms | Non-2xx | Errors | Gateway CPU | Gateway memory | PostgreSQL CPU |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| Direct to backend | 50 | 2393 | 20.83 | 21.51 | 21.87 | 0 | 0 | 2% | 42 MiB | 21% |
| Gateway, no cache | 50 | 2258 | 21.95 | 23.88 | 25.32 | 0 | 0 | 185% | 41 MiB | 61% |
| Gateway, cache hit | 50 | 18801 | 2.37 | 5.06 | 7.26 | 0 | 0 | 526% | 39 MiB | 486% |

### 4. Writes: POST with a 1 KB JSON body (concurrency 50)

| Target | Concurrency | Req/s | p50 ms | p95 ms | p99 ms | Non-2xx | Errors | Gateway CPU | Gateway memory | PostgreSQL CPU |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| Direct to backend | 50 | 43923 | 0.42 | 4.43 | 6.8 | 0 | 0 | 0% | 36 MiB | 6% |
| Gateway | 50 | 2401 | 10.41 | 80.01 | 128.93 | 1256 | 0 | 1464% | 51 MiB | 122% |

### Analytics under load

Analytics events stored: 1484515. Dropped because the buffer was full: 37362 (by design; see ADR-004).
