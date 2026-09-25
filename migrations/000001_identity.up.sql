-- Identity schema: who owns an application, which plan it is on, and the API keys it authenticates with.

create table plans (
  id         bigint generated always as identity primary key,
  name       text not null unique,
  created_at timestamptz not null default now()
);

create table users (
  id         bigint generated always as identity primary key,
  email      text not null,
  created_at timestamptz not null default now()
);

create unique index users_email_key on users (lower(email));

create table applications (
  id         bigint generated always as identity primary key,
  user_id    bigint not null references users (id) on delete cascade,
  plan_id    bigint not null references plans (id),
  name       text not null,
  created_at timestamptz not null default now()
);

create index applications_user_id_idx on applications (user_id);
create index applications_plan_id_idx on applications (plan_id);

-- Only the SHA-256 digest of a key is stored; the raw key is shown once at creation.
create table api_keys (
  id             bigint generated always as identity primary key,
  application_id bigint not null references applications (id) on delete cascade,
  key_hash       bytea not null unique check (octet_length(key_hash) = 32),
  status         text not null default 'active' check (status in ('active', 'revoked')),
  created_at     timestamptz not null default now(),
  revoked_at     timestamptz,
  check ((status = 'revoked') = (revoked_at is not null))
);

create index api_keys_application_id_idx on api_keys (application_id);
