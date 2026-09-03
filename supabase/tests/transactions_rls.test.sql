begin;

create extension if not exists pgtap with schema extensions;
select plan(17);

insert into auth.users (id, email) values
  ('11111111-1111-1111-1111-111111111111', 'transaction-owner@example.com'),
  ('22222222-2222-2222-2222-222222222222', 'other-transaction-user@example.com');

insert into public.accounts (id, user_id, side, account_type, name, institution_name, account_identifier, metadata)
values
  ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', '11111111-1111-1111-1111-111111111111', 'liability', 'credit_card', 'Main card', 'Amex', '4242', '{"card_last_four":"4242"}'),
  ('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', '22222222-2222-2222-2222-222222222222', 'asset', 'bank_account', 'Other account', 'DBS', '1234', '{}');

insert into public.transactions (
  id, user_id, account_id, transaction_kind, title, original_amount_minor, original_currency, occurred_at
) values
  ('cccccccc-cccc-cccc-cccc-cccccccccccc', '11111111-1111-1111-1111-111111111111', 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'debit', 'Owner transaction', 648, 'USD', now()),
  ('dddddddd-dddd-dddd-dddd-dddddddddddd', '22222222-2222-2222-2222-222222222222', 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'credit', 'Other transaction', 1000, 'SGD', now());

insert into private.gmail_connections (id, user_id, encrypted_refresh_token)
values ('eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee', '11111111-1111-1111-1111-111111111111', decode('0102', 'hex'));

insert into public.transaction_sync_runs (id, user_id, gmail_connection_id)
values ('ffffffff-ffff-ffff-ffff-ffffffffffff', '11111111-1111-1111-1111-111111111111', 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee');

insert into private.data_sources (
  id, user_id, source_type, provider, provider_message_id, received_at, raw_data
) values (
  '12121212-1212-1212-1212-121212121212',
  '11111111-1111-1111-1111-111111111111',
  'gmail_email',
  'gmail',
  'gmail-message-1',
  now(),
  '{"subject":"Private receipt"}'
);

insert into private.transaction_data_sources (
  user_id, transaction_id, data_source_id, role, matched_by
) values (
  '11111111-1111-1111-1111-111111111111',
  'cccccccc-cccc-cccc-cccc-cccccccccccc',
  '12121212-1212-1212-1212-121212121212',
  'merchant_receipt',
  'automatic'
);

insert into private.transaction_jobs (user_id, sync_run_id, job_type)
values ('11111111-1111-1111-1111-111111111111', 'ffffffff-ffff-ffff-ffff-ffffffffffff', 'gmail_ingestion');

insert into storage.objects (bucket_id, name, owner, metadata)
values (
  'transaction-attachments',
  '11111111-1111-1111-1111-111111111111/12121212-1212-1212-1212-121212121212/receipt.pdf',
  '11111111-1111-1111-1111-111111111111',
  '{"size":1024,"mimetype":"application/pdf"}'
);

set local role authenticated;
set local request.jwt.claim.sub = '11111111-1111-1111-1111-111111111111';

select results_eq(
  'select count(*) from public.transaction_categories where active',
  array[60::bigint],
  'authenticated users can read the complete global category catalogue'
);

select results_eq(
  'select count(*) from public.transactions',
  array[1::bigint],
  'an authenticated user reads only their own transactions'
);

select results_eq(
  'select count(*) from public.transaction_sync_runs',
  array[1::bigint],
  'an authenticated user reads only their own sync runs'
);

select lives_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at, review_status
    ) values (
      '11111111-1111-1111-1111-111111111111',
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
      'debit', 'Direct browser manual transaction', 1, 'SGD', now(), 'confirmed'
    )$$,
  'authenticated users can insert confirmed manual transactions they own'
);

select throws_ok(
  $$select * from private.gmail_connections$$,
  '42501',
  null,
  'authenticated users cannot read Gmail connections'
);

select throws_ok(
  $$select * from private.gmail_oauth_states$$,
  '42501',
  null,
  'authenticated users cannot read OAuth state records'
);

select throws_ok(
  $$select * from private.source_parser_rules$$,
  '42501',
  null,
  'authenticated users cannot read global parser rules'
);

select throws_ok(
  $$select * from private.data_sources$$,
  '42501',
  null,
  'authenticated users cannot read raw data sources'
);

select throws_ok(
  $$select * from private.transaction_data_sources$$,
  '42501',
  null,
  'authenticated users cannot read transaction evidence links'
);

select throws_ok(
  $$select * from private.transaction_links$$,
  '42501',
  null,
  'authenticated users cannot read private transaction links'
);

select throws_ok(
  $$select * from private.source_parse_attempts$$,
  '42501',
  null,
  'authenticated users cannot read parser attempts'
);

select throws_ok(
  $$select * from private.transaction_jobs$$,
  '42501',
  null,
  'authenticated users cannot read durable worker jobs'
);

select is_empty(
  $$select id from storage.objects where bucket_id = 'transaction-attachments'$$,
  'authenticated users cannot access raw transaction attachment objects directly'
);

set local role anon;
set local request.jwt.claim.sub = '';

select throws_ok(
  $$select * from public.transaction_categories$$,
  '42501',
  null,
  'anonymous users cannot read transaction categories'
);

select throws_ok(
  $$select * from public.transactions$$,
  '42501',
  null,
  'anonymous users cannot read transactions'
);

select throws_ok(
  $$select * from public.transaction_sync_runs$$,
  '42501',
  null,
  'anonymous users cannot read sync runs'
);

select throws_ok(
  $$select * from private.data_sources$$,
  '42501',
  null,
  'anonymous users cannot read raw source data'
);

select * from finish();
rollback;
