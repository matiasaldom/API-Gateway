-- One row per proxied request, written asynchronously in batches by the gateway.
-- No foreign keys: analytics writes must never fail because a key, application,
-- or plan was deleted, and events outlive the rows they reference.

create table request_events (
  id             bigint generated always as identity primary key,
  occurred_at    timestamptz not null,
  request_id     text not null,
  method         text not null,
  route          text,             -- matched route prefix; null when no route matched
  status         smallint not null,
  latency_us     integer not null check (latency_us >= 0),
  api_key_id     bigint,           -- null for requests rejected before authentication succeeded
  application_id bigint,
  plan_id        bigint,
  cache_status   text check (cache_status in ('HIT', 'MISS'))
);

-- Append-only and time-ordered: BRIN keeps the time-range index tiny.
create index request_events_occurred_at_brin on request_events using brin (occurred_at);
