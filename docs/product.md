# Product Brief

The product side of the gateway: who it is for, what problem it solves, what was built first and why, and how success is measured. Engineering decisions are in [decisions.md](decisions.md), and measured performance is in [benchmarks.md](benchmarks.md).

## Problem

A small team that exposes an API to outside developers needs to answer three questions before its first customer arrives:

1. **Who is calling?** Every request should carry an identity that can be revoked without redeploying anything.
2. **How much can they call?** One customer's traffic spike shouldn't take the service down for everyone, and heavier users should be able to pay for more.
3. **What is happening?** When a customer says "your API is slow", the team needs latency, errors, and usage per customer and per endpoint.

Managed gateways (AWS API Gateway, Kong, Apigee) answer these questions, but they cost money per request, tie the team to one vendor, and are heavy to set up for a two-person team. Building auth, limits, and analytics into each backend service repeats the same work in every service, and each copy drifts from the others.

**The gateway puts those three answers in one place, in front of every backend service, with no change to the services themselves.**

## Target users

### The API provider: platform or backend engineer

- **Context:** at a startup or small SaaS company. Runs a handful of backend services and is starting to open some of them to partners or customers.
- **Needs:** issue and revoke API keys, set plan limits, and see who is using what, without writing that code into every service.
- **Frustrations:** managed gateways are expensive at scale and hard to debug. Home-grown middleware in each service drifts apart.
- **What success looks like:** a new endpoint is protected, limited, and measured by adding one route to `gateway.yaml`.

### The API consumer: third-party developer

- **Context:** integrating the provider's API into their own app.
- **Needs:** clear errors they can act on (401 vs 403 vs 429), limits they can see before they hit them, and fast responses.
- **Frustrations:** opaque 500s, limits that aren't documented, and no way to tell when to retry.
- **What success looks like:** the rate-limit headers and `Retry-After` let their client back off correctly without talking to support.

### The operator: whoever is on call

- **Needs:** one screen showing traffic, error rate, latency, and which customer is causing a spike, plus the ability to change a limit or revoke a key immediately.
- **What success looks like:** the problem customer is found and throttled from the dashboard in under five minutes, with no deploy.

## Prioritization

| | Feature | Why here |
|---|---|---|
| **MVP** | Reverse proxy with prefix routing, timeouts, request IDs | Nothing else works without it |
| **MVP** | API key auth (hashed keys, revocation) | Answers "who is calling"; everything after depends on identity |
| **MVP** | Per-plan fixed-window rate limits with headers | Protects the service and creates the free/pro distinction |
| **MVP** | Per-route, per-key response cache | Cheapest latency and backend-load win, and safe because it is per key |
| **MVP** | Async analytics and summary endpoints | Answers "what is happening" without slowing requests down |
| **MVP** | Management API and dashboard | Operators act without SQL or restarts |
| **Shipped after MVP** | Token bucket limiter (option) | Removes the 2× boundary burst; only matters once limits are tight |
| **Shipped after MVP** | Docker Compose demo, CI, benchmarks | Makes the project easy to run, keeps it working, and backs up the performance claims |
| **Later** | Redis-backed limits and cache | Needed only when running more than one gateway instance |
| **Later** | Cache API key lookups in memory | The benchmarks show PostgreSQL at 300–500% CPU under gateway load, and every request, even a cache hit, does a key lookup. A short in-memory cache would remove most of those queries. Trade-off: a revoked key would keep working for a few seconds |
| **Later** | Time-series analytics endpoint | Dashboard charts currently sample in the browser |
| **Later** | JWT validation | For providers whose users already have identity tokens |
| **Excluded** | Billing and invoicing | Analytics events can be lost by design (ADR-004); billing needs exact counts and a different pipeline |
| **Excluded** | Request/response transformation, GraphQL, gRPC | Would turn a focused gateway into a general integration platform |
| **Excluded** | Multi-region deployment | Out of scope for the target team size |

## Success metrics

| Metric | Target | Measured |
|---|---|---|
| Time from `git clone` to first proxied request | Under 5 minutes | One command (`docker compose up --build`), then the curl in the README |
| Gateway latency added to a request (p50) | Under 1 ms at low concurrency | +0.50 ms at concurrency 1 ([benchmarks.md](benchmarks.md)) |
| Cache hit vs uncached request through the gateway | Faster at every concurrency | Faster in every scenario. With a 20 ms backend: 1.9 ms vs 21.6 ms p50 |
| Errors caused by the gateway under load | 0 | 0 across the benchmark suite, up to concurrency 500 (after ADR-009) |
| Time for an operator to throttle a customer | Under 1 minute, no restart | The next request uses the new limit (`scripts/demo.sh` step 6) |

## Guardrail metrics

These must not get worse while the metrics above improve:

| Guardrail | Limit | Enforced by |
|---|---|---|
| Wrong customer's data served from the cache | Never | Cache key includes the API key ID; `TestMiddlewareScopesEntriesToAPIKey` and `tests/cache_test.go` |
| Requests allowed over a plan limit | Never more than the limit per window (fixed window) or limit + refill (token bucket) | Concurrency tests under the race detector |
| Analytics slowing requests down | The request path never waits on the database | Non-blocking emit; events are dropped (and counted) instead |
| Raw API keys in storage or logs | Never | Only SHA-256 hashes are stored; auth logs never include the key |

## Launch plan

This is a portfolio project, so "launch" means a public repository that a reviewer can run and evaluate in minutes.

1. **Run:** `docker compose up` works from a clean clone. CI runs the same stack and the demo script on every push.
2. **Understand:** the README states the problem, the architecture, and the design decisions, and shows screenshots and a demo recording.
3. **Trust:** the benchmark numbers include the hardware, command, and method, and anyone can reproduce them with `scripts/bench.sh`.
4. **Talk about it:** every trade-off has an ADR, so each design choice can be explained in an interview.

## Roadmap

| Version | Scope | Status |
|---|---|---|
| v0.1 | Reverse proxy, routing, timeouts, request IDs | Done |
| v0.2 | API keys, PostgreSQL, `apikey` CLI | Done |
| v0.3 | Fixed-window rate limits per plan | Done |
| v0.4 | Per-route, per-key response cache | Done |
| v0.5 | Async analytics and summary endpoints | Done |
| v0.6 | Management API and React dashboard | Done |
| v0.7 | Docker Compose demo, CI, benchmarks, token bucket, hardening (body limit, upstream checks, `/ready`) | Done |
| v0.8 | In-memory API key cache with a short TTL; time-series analytics endpoint | Next |
| v1.0 | Redis-backed limits and cache, routes reloaded on every instance, multi-instance deployment | Planned |
