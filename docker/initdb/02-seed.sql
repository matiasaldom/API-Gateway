-- Demo data for docker compose. LOCAL USE ONLY: these API keys are published
-- in this repository, so anyone can use them. Never load this file into a
-- database that is reachable from the internet.
--
--   gw_demo_free_plan_local_docker_key_0000000000A   free plan (100/min)
--   gw_demo_pro_plan_local_docker_key_00000000000A   pro plan (1000/min)
--   gw_demo_revoked_local_docker_key_000000000000A   revoked (403)
--   gw_benchmark_only_local_docker_key_0000000000A   benchmark plan (effectively unlimited)

insert into plans (name, requests_per_minute) values ('benchmark', 1000000000);

insert into users (email) values ('demo@example.com');

insert into applications (user_id, plan_id, name)
select u.id, p.id, a.name
from users u
cross join (values ('Demo Free App', 'free'), ('Demo Pro App', 'pro'), ('Benchmark Client', 'benchmark')) as a(name, plan)
join plans p on p.name = a.plan
where u.email = 'demo@example.com';

insert into api_keys (application_id, key_hash, status, revoked_at)
select a.id, sha256(convert_to(k.raw, 'UTF8')), k.status, case when k.status = 'revoked' then now() end
from (values
  ('Demo Free App',    'gw_demo_free_plan_local_docker_key_0000000000A', 'active'),
  ('Demo Pro App',     'gw_demo_pro_plan_local_docker_key_00000000000A', 'active'),
  ('Demo Free App',    'gw_demo_revoked_local_docker_key_000000000000A', 'revoked'),
  ('Benchmark Client', 'gw_benchmark_only_local_docker_key_0000000000A', 'active')
) as k(app, raw, status)
join applications a on a.name = k.app;
