create table private.gmail_oauth_states (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  state_digest bytea not null,
  encrypted_pkce_verifier bytea not null,
  expires_at timestamptz not null,
  consumed_at timestamptz,
  created_at timestamptz not null default now(),
  constraint gmail_oauth_states_state_digest_check check (octet_length(state_digest) = 32),
  constraint gmail_oauth_states_encrypted_pkce_verifier_check
    check (octet_length(encrypted_pkce_verifier) > 0),
  constraint gmail_oauth_states_expiry_check check (expires_at > created_at),
  constraint gmail_oauth_states_consumed_at_check
    check (consumed_at is null or (consumed_at >= created_at and consumed_at <= expires_at)),
  constraint gmail_oauth_states_state_digest_key unique (state_digest)
);

comment on table private.gmail_oauth_states is
  'Single-use, expiring Gmail OAuth state records. Raw state values are never persisted.';

create or replace function private.protect_gmail_oauth_state()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if old.user_id is distinct from new.user_id
    or old.state_digest is distinct from new.state_digest
    or old.encrypted_pkce_verifier is distinct from new.encrypted_pkce_verifier
    or old.expires_at is distinct from new.expires_at
    or old.created_at is distinct from new.created_at then
    raise exception using
      errcode = '23514',
      message = 'OAuth state fields are immutable';
  end if;

  if old.consumed_at is not null then
    raise exception using
      errcode = '23514',
      message = 'an OAuth state cannot be consumed more than once';
  end if;

  return new;
end;
$$;

revoke execute on function private.protect_gmail_oauth_state() from public, anon, authenticated;

create trigger gmail_oauth_states_protect_immutable_fields
before update on private.gmail_oauth_states
for each row execute function private.protect_gmail_oauth_state();

create index gmail_oauth_states_unconsumed_expiry_idx
  on private.gmail_oauth_states (expires_at)
  where consumed_at is null;

create index gmail_oauth_states_user_created_at_idx
  on private.gmail_oauth_states (user_id, created_at desc);

alter table private.gmail_oauth_states enable row level security;
revoke all privileges on table private.gmail_oauth_states from public, anon, authenticated;

alter table private.transaction_jobs
  add column leased_by text;

alter table private.transaction_jobs
  drop constraint transaction_jobs_lease_check;

alter table private.transaction_jobs
  add constraint transaction_jobs_lease_check check (
    (leased_at is null and lease_expires_at is null and leased_by is null)
    or (
      leased_at is not null
      and lease_expires_at is not null
      and lease_expires_at > leased_at
      and leased_by is not null
      and char_length(btrim(leased_by)) between 1 and 128
      and status = 'running'
    )
  );

create index transaction_jobs_leased_by_expiry_idx
  on private.transaction_jobs (leased_by, lease_expires_at)
  where leased_by is not null;
