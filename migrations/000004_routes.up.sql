-- Routes managed at runtime. gateway.yaml decides which prefixes exist (synced at
-- startup); upstream and cache TTL are edited through the admin API and persist.

create table routes (
  id           bigint generated always as identity primary key,
  prefix       text not null unique,
  upstream     text not null,
  cache_ttl_ms bigint not null default 0 check (cache_ttl_ms >= 0),
  created_at   timestamptz not null default now(),
  updated_at   timestamptz not null default now()
);
