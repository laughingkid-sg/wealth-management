begin;

create extension if not exists pgtap with schema extensions;
select no_plan();

select has_table('private', 'api_idempotency_records', 'server mutation idempotency is persisted');
select has_table('private', 'bulk_import_templates', 'bulk templates exist');
select has_table('private', 'bulk_import_template_accounts', 'template Account selections exist');
select has_table('public', 'bulk_import_batches', 'owner-readable batch progress exists');
select has_table('private', 'bulk_import_batch_accounts', 'immutable batch Account snapshots exist');
select has_table('private', 'bulk_import_documents', 'logical bulk documents exist');
select has_table('private', 'bulk_import_files', 'signed-upload reservations exist');
select has_table('private', 'bulk_import_chunks', 'bounded parse chunks exist');
select has_table('private', 'source_candidates', 'multi-transaction candidates exist');

select has_column('public', 'transactions', 'time_precision', 'canonical transactions record exact or date-only precision');
select has_column('private', 'transaction_jobs', 'attempt_generation', 'bulk jobs use a typed generation');
select has_column('private', 'transaction_data_sources', 'bulk_import_candidate_id', 'bulk evidence links are candidate scoped');
select has_column('private', 'source_parse_attempts', 'attempt_ordinal', 'each provider call has an immutable ordinal');

select ok(
  (
    select bool_and(relation.relrowsecurity)
    from pg_class relation
    join pg_namespace namespace on namespace.oid = relation.relnamespace
    where (namespace.nspname, relation.relname) in (
      ('private', 'api_idempotency_records'),
      ('private', 'bulk_import_templates'),
      ('private', 'bulk_import_template_accounts'),
      ('public', 'bulk_import_batches'),
      ('private', 'bulk_import_batch_accounts'),
      ('private', 'bulk_import_documents'),
      ('private', 'bulk_import_files'),
      ('private', 'bulk_import_chunks'),
      ('private', 'source_candidates')
    )
  ),
  'RLS is enabled on every new Bulk Import table'
);

select ok(
  not exists (
    select 1
    from (values
      ('private.api_idempotency_records'::regclass),
      ('private.bulk_import_templates'::regclass),
      ('private.bulk_import_template_accounts'::regclass),
      ('private.bulk_import_batch_accounts'::regclass),
      ('private.bulk_import_documents'::regclass),
      ('private.bulk_import_files'::regclass),
      ('private.bulk_import_chunks'::regclass),
      ('private.source_candidates'::regclass)
    ) as relation(name)
    cross join (values ('anon'), ('authenticated')) as role_name(name)
    where has_table_privilege(role_name.name, relation.name, 'SELECT')
      or has_table_privilege(role_name.name, relation.name, 'INSERT')
      or has_table_privilege(role_name.name, relation.name, 'UPDATE')
      or has_table_privilege(role_name.name, relation.name, 'DELETE')
  ),
  'browser roles have no privileges on Bulk Import operational/configuration tables'
);

select ok(
  has_table_privilege('authenticated', 'public.bulk_import_batches', 'SELECT')
  and not has_table_privilege('authenticated', 'public.bulk_import_batches', 'INSERT')
  and not has_table_privilege('authenticated', 'public.bulk_import_batches', 'UPDATE')
  and not has_table_privilege('authenticated', 'public.bulk_import_batches', 'DELETE'),
  'authenticated clients can read but cannot mutate batch progress directly'
);

select ok(
  exists (
    select 1 from pg_policies
    where schemaname = 'public' and tablename = 'bulk_import_batches'
      and policyname = 'Users can read their own bulk import batches'
      and roles = array['authenticated'::name]
  ),
  'batch progress has an authenticated owner-read policy'
);

select ok(
  exists (
    select 1 from pg_indexes
    where schemaname = 'private' and tablename = 'source_candidates'
      and indexname = 'bulk_candidates_document_output_key'
  ),
  'candidate output ordinal is idempotent per document generation'
);

select ok(
  exists (
    select 1 from pg_indexes
    where schemaname = 'private' and tablename = 'source_parse_attempts'
      and indexname = 'source_parse_attempts_bulk_chunk_ordinal_key'
  ),
  'provider-call attempts are unique by chunk and ordinal'
);

select ok(
  exists (
    select 1 from pg_indexes
    where schemaname = 'private' and tablename = 'transaction_jobs'
      and indexname = 'transaction_jobs_bulk_document_active_key'
  ),
  'document-generation jobs have an active idempotency index'
);

select ok(
  not exists (select 1 from pg_publication where pubname = 'supabase_realtime')
  or exists (
    select 1 from pg_publication_tables
    where pubname = 'supabase_realtime'
      and schemaname = 'public' and tablename = 'bulk_import_batches'
  ),
  'batch progress is published to Realtime when the publication exists'
);

select results_eq(
  $$select count(*) from pg_policies
    where schemaname = 'storage' and tablename = 'objects'
      and policyname in (
        'Transaction attachments block browser reads',
        'Transaction attachments block browser updates',
        'Transaction attachments block browser deletes',
        'Transaction attachments gate browser inserts'
      ) and permissive = 'RESTRICTIVE'$$,
  array[4::bigint],
  'private transaction attachments use operation-specific restrictive policies'
);

select ok(
  exists (
    select 1 from pg_policies
    where schemaname = 'storage' and tablename = 'objects'
      and policyname = 'Users can upload reserved transaction attachments'
      and cmd = 'INSERT'
  ),
  'the only browser upload policy is reservation gated'
);

insert into auth.users (id, email) values
  ('91000000-0000-0000-0000-000000000001', 'bulk-owner@example.com');

insert into public.accounts (
  id, user_id, side, account_type, name, institution_name
) values (
  '91000000-0000-0000-0000-000000000010',
  '91000000-0000-0000-0000-000000000001',
  'liability', 'credit_card', 'Test Card', 'Test Bank'
);

insert into private.bulk_import_templates (
  id, user_id, title, document_type, parsing_prompt
) values (
  '91000000-0000-0000-0000-000000000020',
  '91000000-0000-0000-0000-000000000001',
  'Test Card Bill', 'credit_card_bill', 'Read only values printed in this bill.'
);

insert into private.bulk_import_template_accounts (
  user_id, template_id, account_id, sort_order
) values (
  '91000000-0000-0000-0000-000000000001',
  '91000000-0000-0000-0000-000000000020',
  '91000000-0000-0000-0000-000000000010', 0
);

set constraints private.bulk_templates_assert_accounts immediate;
set constraints private.bulk_template_accounts_assert_accounts immediate;

select pass('a Credit Card bill template accepts exactly one active owned Credit Card Account');

select lives_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at, time_precision
    ) values (
      '91000000-0000-0000-0000-000000000001',
      '91000000-0000-0000-0000-000000000010',
      'debit', 'Date-only statement line', 100, 'SGD',
      '2026-09-04 12:00:00+00', 'date'
    )$$,
  'date-only canonical transactions use the noon-UTC placeholder'
);

select throws_ok(
  $$insert into public.transactions (
      user_id, account_id, transaction_kind, title,
      original_amount_minor, original_currency, occurred_at, time_precision
    ) values (
      '91000000-0000-0000-0000-000000000001',
      '91000000-0000-0000-0000-000000000010',
      'debit', 'Invalid date placeholder', 100, 'SGD',
      '2026-09-04 00:00:00+00', 'date'
    )$$,
  '23514',
  null,
  'date precision rejects non-noon placeholder timestamps'
);

select lives_ok(
  $$insert into private.data_sources (
      user_id, source_type, provider, received_at, raw_data
    ) values (
      '91000000-0000-0000-0000-000000000001',
      'bulk_upload_document', 'user_upload', now(), '{}'
    )$$,
  'bulk uploaded documents are accepted as raw data sources'
);

select throws_ok(
  $$insert into private.data_sources (
      user_id, source_type, provider, received_at, raw_data
    ) values (
      '91000000-0000-0000-0000-000000000001',
      'bulk_upload_document', 'gmail', now(), '{}'
    )$$,
  '23514',
  null,
  'bulk uploaded sources require the user-upload provider contract'
);

select * from finish();
rollback;
