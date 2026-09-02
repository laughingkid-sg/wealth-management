begin;

create extension if not exists pgtap with schema extensions;
select plan(25);

select has_column(
  'public', 'transaction_sync_runs', 'ingestion_completed_at',
  'sync runs record when ingestion has finished independently of downstream work'
);
select has_column(
  'public', 'transaction_sync_runs', 'sources_parsed_count',
  'sync runs expose parsed source progress'
);
select has_column(
  'public', 'transaction_sync_runs', 'sources_failed_count',
  'sync runs expose failed source progress'
);
select has_column(
  'private', 'data_sources', 'reconciliation_reason',
  'source review and dangling reasons are persisted'
);
select has_column(
  'private', 'data_sources', 'suggested_transaction_id',
  'source reconciliation suggestions can identify a transaction'
);

insert into auth.users (id, email) values
  ('31313131-3131-3131-3131-313131313131', 'operations-owner@example.com'),
  ('32323232-3232-3232-3232-323232323232', 'operations-other@example.com');

insert into public.transaction_sync_runs (
  id, user_id, status, sources_saved_count
) values (
  '41414141-4141-4141-4141-414141414141',
  '31313131-3131-3131-3131-313131313131',
  'running',
  1
);

select throws_ok(
  $$insert into public.transaction_sync_runs (user_id, status)
    values ('31313131-3131-3131-3131-313131313131', 'queued')$$,
  '23505',
  null,
  'one user cannot have two concurrent Gmail sync runs'
);

update public.transaction_sync_runs
set ingestion_completed_at = now(),
    messages_found_count = 1,
    sources_saved_count = greatest(sources_saved_count, 0)
where id = '41414141-4141-4141-4141-414141414141';

select results_eq(
  $$select sources_saved_count
    from public.transaction_sync_runs
    where id = '41414141-4141-4141-4141-414141414141'$$,
  array[1],
  'a successful retry with zero new inserts retains the durable saved-source count'
);

select throws_ok(
  $$update public.transaction_sync_runs
    set sources_parsed_count = -1
    where id = '41414141-4141-4141-4141-414141414141'$$,
  '23514',
  null,
  'parsed source progress cannot be negative'
);

insert into public.accounts (
  id, user_id, side, account_type, name, institution_name
) values (
  '51515151-5151-5151-5151-515151515151',
  '32323232-3232-3232-3232-323232323232',
  'asset', 'bank_account', 'Other savings', 'Bank'
);

insert into public.transactions (
  id, user_id, account_id, transaction_kind, title,
  original_amount_minor, original_currency, occurred_at
) values (
  '61616161-6161-6161-6161-616161616161',
  '32323232-3232-3232-3232-323232323232',
  '51515151-5151-5151-5151-515151515151',
  'credit', 'Other transaction', 100, 'SGD', now()
);

insert into private.data_sources (
  id, user_id, source_type, provider, provider_message_id, received_at, raw_data
) values (
  '71717171-7171-7171-7171-717171717171',
  '31313131-3131-3131-3131-313131313131',
  'gmail_email', 'gmail', 'operations-message', now(), '{}'
);

select throws_ok(
  $$update private.data_sources
    set suggested_transaction_id = '61616161-6161-6161-6161-616161616161'
    where id = '71717171-7171-7171-7171-717171717171'$$,
  '23503',
  null,
  'a source cannot suggest another user''s transaction'
);

select has_index(
  'private', 'data_sources', 'data_sources_user_status_keyset_idx',
  'source queues have an owner-scoped keyset index'
);
select has_index(
  'public', 'transactions', 'transactions_user_keyset_idx',
  'transaction listing has an owner-scoped keyset index'
);
select has_index(
  'private', 'transaction_jobs', 'transaction_jobs_expired_lease_idx',
  'expired worker leases have a recovery index'
);
select has_index(
  'public', 'transaction_sync_runs', 'transaction_sync_runs_one_active_per_user_idx',
  'concurrent sync prevention is backed by a unique partial index'
);
select has_index(
  'public', 'transactions', 'transactions_user_account_id_idx',
  'transaction ownership foreign keys have a composite covering index'
);
select has_index(
  'private', 'transaction_links', 'transaction_links_user_debit_transaction_id_idx',
  'debit-leg ownership foreign keys have a composite covering index'
);
select has_index(
  'private', 'transaction_links', 'transaction_links_user_credit_transaction_id_idx',
  'credit-leg ownership foreign keys have a composite covering index'
);
select has_index(
  'public', 'transaction_sync_runs', 'transaction_sync_runs_user_gmail_connection_id_idx',
  'sync-run connection ownership foreign keys have a composite covering index'
);
select has_index(
  'private', 'data_sources', 'data_sources_user_suggested_account_id_idx',
  'source Account suggestions have a composite ownership index'
);
select has_index(
  'private', 'data_sources', 'data_sources_user_suggested_transaction_id_idx',
  'source transaction suggestions have a composite ownership index'
);

select ok(
  exists (
    select 1
    from pg_trigger
    where tgrelid = 'private.transaction_data_sources'::regclass
      and tgname = 'transaction_data_sources_assert_cardinality'
      and tgdeferrable
      and tginitdeferred
  ),
  'source cardinality is enforced by an initially deferred constraint trigger'
);
select ok(
  exists (
    select 1
    from pg_trigger
    where tgrelid = 'private.transaction_links'::regclass
      and tgname = 'transaction_links_assert_source_cardinality'
      and tgdeferrable
      and tginitdeferred
  ),
  'transfer-link changes revalidate shared source cardinality at commit'
);

select ok(
  exists (
    select 1
    from pg_trigger
    where tgrelid = 'public.transactions'::regclass
      and tgname = 'transactions_assert_transfer_link_integrity'
      and tgdeferrable
      and tginitdeferred
  ),
  'linked transaction account and kind edits are revalidated at commit'
);

select ok(
  exists (
    select 1
    from private.source_parser_rules
    where provider = 'gmail'
      and active
      and extraction_config @> '{"constants":{"transaction_kind":"debit","original_currency":"SGD"}}'::jsonb
  ),
  'a conservative active Gmail OCBC parser rule is seeded'
);
select ok(
  exists (
    select 1
    from private.source_parser_rules
    where provider = 'gmail'
      and active
      and extraction_config -> 'extractors' ? 'card_last_four'
      and not (extraction_config -> 'extractors' ? 'original_amount_minor')
  ),
  'the seed extracts only deterministic minor-unit-safe fields'
);
select ok(
  not exists (select 1 from pg_publication where pubname = 'supabase_realtime')
  or exists (
    select 1
    from pg_publication_tables
    where pubname = 'supabase_realtime'
      and schemaname = 'public'
      and tablename = 'transaction_sync_runs'
  ),
  'transaction sync progress is published to Realtime when the publication exists'
);

select * from finish();
rollback;
