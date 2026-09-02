begin;

create extension if not exists pgtap with schema extensions;
select plan(12);

insert into auth.users (id, email) values
  ('11111111-1111-1111-1111-111111111111', 'oauth-owner@example.com');

select lives_ok(
  $$insert into private.gmail_oauth_states (user_id, state_digest, encrypted_pkce_verifier, expires_at)
    values (
      '11111111-1111-1111-1111-111111111111',
      decode(repeat('01', 32), 'hex'),
      decode('c0ffee', 'hex'),
      now() + interval '10 minutes'
    )$$,
  'a state digest and encrypted PKCE verifier can be stored without storing the raw state'
);

select throws_ok(
  $$insert into private.gmail_oauth_states (user_id, state_digest, encrypted_pkce_verifier, expires_at)
    values (
      '11111111-1111-1111-1111-111111111111',
      decode(repeat('01', 32), 'hex'),
      decode('beef', 'hex'),
      now() + interval '10 minutes'
    )$$,
  '23505',
  null,
  'a state digest can be used only once'
);

select throws_ok(
  $$insert into private.gmail_oauth_states (user_id, state_digest, encrypted_pkce_verifier, expires_at)
    values (
      '11111111-1111-1111-1111-111111111111',
      decode('01', 'hex'),
      decode('c0ffee', 'hex'),
      now() + interval '10 minutes'
    )$$,
  '23514',
  null,
  'an OAuth state digest must be a SHA-256-sized value'
);

select throws_ok(
  $$insert into private.gmail_oauth_states (user_id, state_digest, encrypted_pkce_verifier, expires_at)
    values (
      '11111111-1111-1111-1111-111111111111',
      decode(repeat('02', 32), 'hex'),
      decode('c0ffee', 'hex'),
      now() - interval '1 minute'
    )$$,
  '23514',
  null,
  'an OAuth state must expire after it is created'
);

select throws_ok(
  $$update private.gmail_oauth_states
      set encrypted_pkce_verifier = decode('beef', 'hex')
    where state_digest = decode(repeat('01', 32), 'hex')$$,
  '23514',
  'OAuth state fields are immutable',
  'the PKCE verifier cannot be changed after the state is created'
);

select lives_ok(
  $$update private.gmail_oauth_states
      set consumed_at = now()
    where state_digest = decode(repeat('01', 32), 'hex')$$,
  'a valid OAuth state can be consumed once'
);

select throws_ok(
  $$update private.gmail_oauth_states
      set consumed_at = now()
    where state_digest = decode(repeat('01', 32), 'hex')$$,
  '23514',
  'an OAuth state cannot be consumed more than once',
  'a consumed OAuth state cannot be consumed again'
);

insert into private.transaction_jobs (user_id, job_type)
values ('11111111-1111-1111-1111-111111111111', 'gmail_ingestion');

select lives_ok(
  $$update private.transaction_jobs
      set status = 'running',
          leased_at = now(),
          lease_expires_at = now() + interval '1 minute',
          leased_by = 'worker-local-1'
    where user_id = '11111111-1111-1111-1111-111111111111'$$,
  'a running job records the worker that holds its lease'
);

select throws_ok(
  $$insert into private.transaction_jobs (
      user_id, job_type, status, leased_at, lease_expires_at
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'source_parsing', 'running', now(), now() + interval '1 minute'
    )$$,
  '23514',
  null,
  'a job lease requires a worker identifier'
);

select throws_ok(
  $$insert into private.transaction_jobs (
      user_id, job_type, status, leased_at, lease_expires_at, leased_by
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'source_parsing', 'queued', now(), now() + interval '1 minute', 'worker-local-1'
    )$$,
  '23514',
  null,
  'only a running job can retain an active lease'
);

select results_eq(
  $$select count(*) from pg_indexes where schemaname = 'private' and tablename = 'gmail_oauth_states'
      and indexname = 'gmail_oauth_states_unconsumed_expiry_idx'$$,
  array[1::bigint],
  'an unconsumed-state expiry index exists for safe cleanup'
);

select results_eq(
  $$select count(*) from pg_indexes where schemaname = 'private' and tablename = 'transaction_jobs'
      and indexname = 'transaction_jobs_leased_by_expiry_idx'$$,
  array[1::bigint],
  'a worker lease index exists for lease recovery'
);

select * from finish();
rollback;
