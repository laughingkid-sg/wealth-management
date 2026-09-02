begin;

create extension if not exists pgtap with schema extensions;
select plan(10);

insert into auth.users (id, email) values
  ('11111111-1111-1111-1111-111111111111', 'account-owner@example.com'),
  ('22222222-2222-2222-2222-222222222222', 'another-user@example.com');

insert into public.accounts (user_id, side, account_type, name, institution_name, metadata) values
  ('11111111-1111-1111-1111-111111111111', 'asset', 'bank_account', 'Everyday', 'DBS', '{"country":"SG"}'),
  ('22222222-2222-2222-2222-222222222222', 'liability', 'credit_card', 'Main card', 'UOB', '{}');

set local role authenticated;
set local request.jwt.claim.sub = '11111111-1111-1111-1111-111111111111';

select results_eq(
  'select count(*) from public.accounts',
  array[1::bigint],
  'an authenticated user reads only their own rows'
);

select lives_ok(
  $$insert into public.accounts (user_id, side, account_type, name, institution_name)
    values ('11111111-1111-1111-1111-111111111111', 'asset', 'brokerage', 'Investments', 'Interactive Brokers')$$,
  'an authenticated user can insert their own account'
);

select throws_ok(
  $$insert into public.accounts (user_id, side, account_type, name, institution_name)
    values ('22222222-2222-2222-2222-222222222222', 'asset', 'brokerage', 'Not mine', 'Interactive Brokers')$$,
  '42501',
  null,
  'an authenticated user cannot insert an account for another user'
);

select is_empty(
  $$update public.accounts set name = 'Hacked' where user_id = '22222222-2222-2222-2222-222222222222' returning name$$,
  'an authenticated user cannot update another user account'
);

select throws_ok(
  $$update public.accounts set user_id = '22222222-2222-2222-2222-222222222222'
    where user_id = '11111111-1111-1111-1111-111111111111'$$,
  '42501',
  null,
  'an authenticated user cannot reassign ownership'
);

select throws_ok(
  $$delete from public.accounts where user_id = '11111111-1111-1111-1111-111111111111' returning id$$,
  '42501',
  null,
  'authenticated users have no delete access; soft delete is required'
);

set local role anon;
set local request.jwt.claim.sub = '';

select throws_ok(
  $$select * from public.accounts$$,
  '42501',
  null,
  'anonymous users cannot read accounts'
);
select throws_ok(
  $$insert into public.accounts (user_id, side, account_type, name, institution_name)
    values ('11111111-1111-1111-1111-111111111111', 'asset', 'bank_account', 'Blocked', 'DBS')$$,
  '42501',
  null,
  'anonymous users cannot insert accounts'
);

set local role postgres;

select throws_ok(
  $$insert into public.accounts (user_id, side, account_type, name, institution_name)
    values ('11111111-1111-1111-1111-111111111111', 'liability', 'bank_account', 'Invalid', 'DBS')$$,
  '23514',
  null,
  'the type must be compatible with the side'
);

select throws_ok(
  $$insert into public.accounts (user_id, side, account_type, name, institution_name, metadata)
    values ('11111111-1111-1111-1111-111111111111', 'asset', 'bank_account', 'Invalid', 'DBS', '[]')$$,
  '23514',
  null,
  'metadata must be a JSON object'
);

select * from finish();
rollback;
