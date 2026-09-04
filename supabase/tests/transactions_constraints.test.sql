begin;

create extension if not exists pgtap with schema extensions;
select plan(23);

insert into auth.users (id, email) values
  ('11111111-1111-1111-1111-111111111111', 'transaction-owner@example.com'),
  ('22222222-2222-2222-2222-222222222222', 'other-transaction-user@example.com');

insert into public.accounts (id, user_id, side, account_type, name, institution_name, account_identifier, metadata, deleted_at)
values
  ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', '11111111-1111-1111-1111-111111111111', 'liability', 'credit_card', 'Main card', 'Amex', '4242', '{"card_last_four":"4242"}', null),
  ('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', '11111111-1111-1111-1111-111111111111', 'asset', 'bank_account', 'Savings', 'DBS', '6789', '{}', null),
  ('cccccccc-cccc-cccc-cccc-cccccccccccc', '11111111-1111-1111-1111-111111111111', 'asset', 'bank_account', 'Closed', 'DBS', '0000', '{}', now()),
  ('dddddddd-dddd-dddd-dddd-dddddddddddd', '22222222-2222-2222-2222-222222222222', 'asset', 'bank_account', 'Other account', 'UOB', '9999', '{}', null);

insert into public.transactions (
  id, user_id, account_id, transaction_kind, title, original_amount_minor, original_currency, occurred_at
) values
  ('01010101-0101-0101-0101-010101010101', '11111111-1111-1111-1111-111111111111', 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'debit', 'Debit transfer leg', 1000, 'SGD', now()),
  ('02020202-0202-0202-0202-020202020202', '11111111-1111-1111-1111-111111111111', 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'credit', 'Credit transfer leg', 1000, 'SGD', now()),
  ('03030303-0303-0303-0303-030303030303', '11111111-1111-1111-1111-111111111111', 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'debit', 'Another debit leg', 1000, 'SGD', now()),
  ('04040404-0404-0404-0404-040404040404', '11111111-1111-1111-1111-111111111111', 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'credit', 'Another credit leg', 1000, 'SGD', now()),
  ('06060606-0606-0606-0606-060606060606', '11111111-1111-1111-1111-111111111111', 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'credit', 'Same-account credit leg', 1000, 'SGD', now());

select throws_ok(
  $$insert into public.transactions (user_id, account_id, transaction_kind, title, original_amount_minor, original_currency, occurred_at)
    values ('11111111-1111-1111-1111-111111111111', 'dddddddd-dddd-dddd-dddd-dddddddddddd', 'debit', 'Cross-owner', 1, 'SGD', now())$$,
  '23514',
  null,
  'a transaction cannot use another user''s account'
);

select throws_ok(
  $$insert into public.transactions (user_id, account_id, transaction_kind, title, original_amount_minor, original_currency, occurred_at)
    values ('11111111-1111-1111-1111-111111111111', 'cccccccc-cccc-cccc-cccc-cccccccccccc', 'debit', 'Soft deleted account', 1, 'SGD', now())$$,
  '23514',
  'transaction account must be active and owned by the transaction user',
  'a transaction cannot be created against a soft-deleted account'
);

select throws_ok(
  $$insert into public.transactions (user_id, account_id, transaction_kind, title, original_amount_minor, original_currency, occurred_at)
    values ('11111111-1111-1111-1111-111111111111', 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'debit', 'Bad currency', 1, 'sgd', now())$$,
  '23514',
  null,
  'currency codes must be three uppercase ISO values'
);

select throws_ok(
  $$insert into public.transactions (user_id, account_id, transaction_kind, title, original_amount_minor, original_currency, occurred_at, line_items)
    values ('11111111-1111-1111-1111-111111111111', 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'debit', 'Bad line items', 1, 'SGD', now(), '{}')$$,
  '23514',
  null,
  'line items must be a JSON array'
);

insert into private.data_sources (
  id, user_id, source_type, provider, provider_message_id, received_at, raw_data
) values
(
  '05050505-0505-0505-0505-050505050505',
  '11111111-1111-1111-1111-111111111111',
  'gmail_email', 'gmail', 'message-1', now(), '{}'
),
(
  '07070707-0707-0707-0707-070707070707',
  '11111111-1111-1111-1111-111111111111',
  'gmail_email', 'gmail', 'message-2', now(), '{}'
);

select throws_ok(
  $$insert into private.data_sources (user_id, source_type, provider, provider_message_id, received_at, raw_data)
    values ('11111111-1111-1111-1111-111111111111', 'gmail_email', 'gmail', 'message-1', now(), '{}')$$,
  '23505',
  null,
  'provider message identity is idempotent per user and provider'
);

insert into private.transaction_data_sources (
  user_id, transaction_id, data_source_id, role, matched_by
) values (
  '11111111-1111-1111-1111-111111111111',
  '01010101-0101-0101-0101-010101010101',
  '05050505-0505-0505-0505-050505050505',
  'bank_alert', 'automatic'
);

select throws_ok(
  $$insert into private.transaction_data_sources (user_id, transaction_id, data_source_id, role, matched_by)
    values ('11111111-1111-1111-1111-111111111111', '01010101-0101-0101-0101-010101010101', '05050505-0505-0505-0505-050505050505', 'bank_alert', 'automatic')$$,
  '23505',
  null,
  'an active source cannot be linked to the same transaction twice'
);

set constraints private.transaction_links_assert_integrity immediate;

select lives_ok(
  $$insert into private.transaction_links (user_id, debit_transaction_id, credit_transaction_id)
    values ('11111111-1111-1111-1111-111111111111', '01010101-0101-0101-0101-010101010101', '02020202-0202-0202-0202-020202020202')$$,
  'an internal transfer links one debit leg to one credit leg'
);

set constraints public.transactions_assert_transfer_link_integrity immediate;

select throws_ok(
  $$update public.transactions
    set account_id = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa'
    where id = '02020202-0202-0202-0202-020202020202'$$,
  '23514',
  'an internal transfer link requires two distinct accounts',
  'editing a linked leg cannot collapse both legs onto one account'
);

select throws_ok(
  $$update public.transactions
    set transaction_kind = 'credit'
    where id = '01010101-0101-0101-0101-010101010101'$$,
  '23514',
  'an internal transfer link requires one debit and one credit transaction',
  'editing a linked leg cannot invalidate the debit-credit relationship'
);

select lives_ok(
  $$update public.transactions
    set account_id = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb'
    where id = '03030303-0303-0303-0303-030303030303'$$,
  'an ordinary unlinked transaction remains editable'
);

update public.transactions
set account_id = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa'
where id = '03030303-0303-0303-0303-030303030303';

select throws_ok(
  $$insert into private.transaction_links (user_id, debit_transaction_id, credit_transaction_id)
    values ('11111111-1111-1111-1111-111111111111', '03030303-0303-0303-0303-030303030303', '01010101-0101-0101-0101-010101010101')$$,
  '23514',
  'an internal transfer link requires one debit and one credit transaction',
  'a transfer link cannot use a debit transaction as its credit leg'
);

select throws_ok(
  $$insert into private.transaction_links (user_id, debit_transaction_id, credit_transaction_id)
    values ('11111111-1111-1111-1111-111111111111', '01010101-0101-0101-0101-010101010101', '04040404-0404-0404-0404-040404040404')$$,
  '23505',
  null,
  'a transaction cannot be reused in a second transfer link'
);

select throws_ok(
  $$insert into private.transaction_links (user_id, debit_transaction_id, credit_transaction_id)
    values ('11111111-1111-1111-1111-111111111111', '03030303-0303-0303-0303-030303030303', '06060606-0606-0606-0606-060606060606')$$,
  '23514',
  'an internal transfer link requires two distinct accounts',
  'an internal transfer cannot use the same account for both legs'
);

set constraints private.transaction_data_sources_assert_cardinality immediate;
set constraints private.transaction_links_assert_source_cardinality immediate;

select lives_ok(
  $$insert into private.transaction_data_sources (user_id, transaction_id, data_source_id, role, matched_by)
    values ('11111111-1111-1111-1111-111111111111', '02020202-0202-0202-0202-020202020202', '05050505-0505-0505-0505-050505050505', 'bank_alert', 'automatic')$$,
  'one source can support both exact legs of an internal transfer'
);

select throws_ok(
  $$insert into private.transaction_data_sources (user_id, transaction_id, data_source_id, role, matched_by)
    values ('11111111-1111-1111-1111-111111111111', '03030303-0303-0303-0303-030303030303', '05050505-0505-0505-0505-050505050505', 'other', 'user')$$,
  '23514',
  'a source may have at most two active transaction links',
  'a transfer source cannot gain a third active transaction link'
);

insert into private.transaction_data_sources (user_id, transaction_id, data_source_id, role, matched_by)
values ('11111111-1111-1111-1111-111111111111', '03030303-0303-0303-0303-030303030303', '07070707-0707-0707-0707-070707070707', 'other', 'user');

select throws_ok(
  $$insert into private.transaction_data_sources (user_id, transaction_id, data_source_id, role, matched_by)
    values ('11111111-1111-1111-1111-111111111111', '04040404-0404-0404-0404-040404040404', '07070707-0707-0707-0707-070707070707', 'other', 'user')$$,
  '23514',
  'two active source links must be the legs of one internal transfer',
  'ordinary transactions cannot share one active source'
);

update private.transaction_data_sources
set detached_at = now(), detached_by_user = true
where user_id = '11111111-1111-1111-1111-111111111111'
  and transaction_id = '03030303-0303-0303-0303-030303030303'
  and data_source_id = '07070707-0707-0707-0707-070707070707'
  and detached_at is null;

select lives_ok(
  $$insert into private.transaction_data_sources (user_id, transaction_id, data_source_id, role, matched_by)
    values ('11111111-1111-1111-1111-111111111111', '03030303-0303-0303-0303-030303030303', '07070707-0707-0707-0707-070707070707', 'other', 'user')$$,
  'a soft-detached source can be reattached to the same transaction'
);

select results_eq(
  $$select count(*) from pg_indexes where schemaname = 'public' and tablename = 'transactions'
      and indexname = 'transactions_user_occurred_at_idx'$$,
  array[1::bigint],
  'the primary transaction list index exists'
);

select results_eq(
  $$select count(*) from pg_indexes where schemaname = 'private' and tablename = 'data_sources'
      and indexname = 'data_sources_user_parse_received_idx'$$,
  array[1::bigint],
  'the source processing index exists'
);

select results_eq(
  $$select public from storage.buckets where id = 'transaction-attachments'$$,
  array[false],
  'the transaction attachment bucket is private'
);

select results_eq(
  $$select file_size_limit from storage.buckets where id = 'transaction-attachments'$$,
  array[5242880::bigint],
  'the transaction attachment bucket limits each file to five MiB'
);

select results_eq(
  $$select array_to_string(allowed_mime_types, ',') from storage.buckets where id = 'transaction-attachments'$$,
  array['application/pdf,image/bmp,image/jpeg,image/png,image/tiff,image/webp,image/heic'],
  'the transaction attachment bucket permits only PDFs and supported images'
);

select results_eq(
  $$select count(*) from pg_policies where schemaname = 'storage' and tablename = 'objects'
      and policyname in (
        'Transaction attachments block browser reads',
        'Transaction attachments block browser updates',
        'Transaction attachments block browser deletes',
        'Transaction attachments gate browser inserts'
      ) and permissive = 'RESTRICTIVE'$$,
  array[4::bigint],
  'attachment objects retain restrictive operation-specific browser policies'
);

select * from finish();
rollback;
