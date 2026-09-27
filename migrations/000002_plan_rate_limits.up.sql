-- Per-plan rate limits, read alongside the API key on each request (no extra query).

alter table plans
  add column requests_per_minute integer not null default 100 check (requests_per_minute > 0);

insert into plans (name, requests_per_minute)
values ('free', 100), ('pro', 1000)
on conflict (name) do update set requests_per_minute = excluded.requests_per_minute;

-- Existing plans were backfilled with 100 above; new plans must state their limit.
alter table plans alter column requests_per_minute drop default;
