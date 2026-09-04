begin;

create extension if not exists pgtap with schema extensions;
select no_plan();

select has_column('public', 'accounts', 'opening_balances', 'Accounts expose the current balance projection');
select has_column('public', 'accounts', 'opening_balance_as_of', 'Accounts expose the baseline as-of time');
select has_column('public', 'accounts', 'opening_balance_version', 'Accounts expose optimistic baseline version');

select has_table('private', 'account_opening_balance_revisions', 'opening-balance revisions are normalized');
select has_table('private', 'account_opening_balance_revision_amounts', 'revision currencies are normalized');
select has_table('private', 'transaction_calculation_treatments', 'spending treatments are separate from evidence');
select has_table('private', 'credit_card_statements', 'Credit Card bills exist');
select has_table('private', 'credit_card_statement_lines', 'Credit Card bill lines exist');
select has_table('private', 'credit_card_statement_payment_candidates', 'ambiguous payment candidates are retained for selection');
select has_table('private', 'credit_card_statement_events', 'Credit Card bill changes have an event audit');

select has_column(
  'private', 'credit_card_statements', 'unresolved_candidate_count',
  'bill projections retain unresolved or omitted candidate counts'
);
select has_column('private', 'credit_card_statement_lines', 'line_index', 'bill line order is explicit');
select has_column('private', 'credit_card_statement_lines', 'line_kind', 'bill line semantics are explicit');
select has_column('private', 'credit_card_statement_lines', 'line_fingerprint', 'bill line identity is server normalized');
select has_column('private', 'credit_card_statement_lines', 'time_precision', 'bill line matching distinguishes exact and date-only evidence');

select ok(
  (
    select bool_and(relation.relrowsecurity)
    from pg_class relation
    join pg_namespace namespace on namespace.oid = relation.relnamespace
    where namespace.nspname = 'private'
      and relation.relname in (
        'account_opening_balance_revisions',
        'account_opening_balance_revision_amounts',
        'transaction_calculation_treatments',
        'credit_card_statements',
        'credit_card_statement_lines',
        'credit_card_statement_payment_candidates',
        'credit_card_statement_events'
      )
  ),
  'RLS is enabled on every Account Balance and Credit Card private table'
);

select ok(
  not exists (
    select 1
    from (values
      ('private.account_opening_balance_revisions'::regclass),
      ('private.account_opening_balance_revision_amounts'::regclass),
      ('private.transaction_calculation_treatments'::regclass),
      ('private.credit_card_statements'::regclass),
      ('private.credit_card_statement_lines'::regclass),
      ('private.credit_card_statement_payment_candidates'::regclass),
      ('private.credit_card_statement_events'::regclass)
    ) as relation(name)
    cross join (values ('anon'), ('authenticated')) as role_name(name)
    where has_table_privilege(role_name.name, relation.name, 'SELECT')
      or has_table_privilege(role_name.name, relation.name, 'INSERT')
      or has_table_privilege(role_name.name, relation.name, 'UPDATE')
      or has_table_privilege(role_name.name, relation.name, 'DELETE')
  ),
  'browser roles have no privileges on balance history, treatment, or bill workflow rows'
);

select ok(
  has_column_privilege('authenticated', 'public.accounts', 'name', 'UPDATE')
  and has_column_privilege('authenticated', 'public.accounts', 'metadata', 'UPDATE')
  and not has_column_privilege('authenticated', 'public.accounts', 'user_id', 'UPDATE')
  and not has_column_privilege('authenticated', 'public.accounts', 'opening_balances', 'UPDATE')
  and not has_column_privilege('authenticated', 'public.accounts', 'opening_balance_version', 'UPDATE'),
  'browser Account CRUD cannot mutate ownership or opening-balance state'
);

select ok(
  private.opening_balances_are_valid('{"SGD":"-1250"}'::jsonb, 'bank_account')
  and not private.opening_balances_are_valid('{"SGD":"-1250"}'::jsonb, 'credit_card')
  and not private.opening_balances_are_valid('{"SGD":"12.50"}'::jsonb, 'bank_account')
  and not private.opening_balances_are_valid('{"sgd":"1250"}'::jsonb, 'bank_account'),
  'opening-balance validation accepts Bank overdrafts and rejects unsafe representations'
);

select ok(
  exists (
    select 1 from pg_constraint
    where conrelid = 'public.transactions'::regclass
      and conname = 'transactions_creation_method_check'
      and pg_get_constraintdef(oid) like '%credit_card_statement%'
  ),
  'statement-created canonical transactions have narrow provenance support'
);

select ok(
  exists (
    select 1 from pg_constraint
    where conrelid = 'private.credit_card_statements'::regclass
      and conname = 'credit_card_statements_document_fkey'
      and confdeltype = 'r'
  ),
  'a retained bill restricts deletion of its Bulk document evidence'
);

select ok(
  exists (
    select 1 from pg_indexes
    where schemaname = 'private'
      and tablename = 'credit_card_statement_payment_candidates'
      and indexname = 'statement_payment_candidates_one_choice_key'
  ),
  'only one ambiguous payment candidate can be selected for a bill'
);

select ok(
  exists (
    select 1 from pg_trigger
    where tgrelid = 'private.credit_card_statements'::regclass
      and tgname = 'credit_card_statements_assert_valid'
      and not tgisinternal
  ),
  'bill ownership, period, Account, document, and payoff integrity are deferred and validated'
);

select ok(
  exists (
    select 1 from pg_trigger
    where tgrelid = 'private.credit_card_statement_lines'::regclass
      and tgname = 'credit_card_statement_lines_assert_valid'
      and not tgisinternal
  ),
  'bill line candidate and canonical-transaction integrity is deferred and validated'
);

select ok(
  pg_get_functiondef(
    'private.validate_credit_card_statement(uuid,uuid)'::regprocedure
  ) like '%debit_account_deleted_at is not null%',
  'bill payoff validation rejects archived Bank Accounts'
);

select ok(
  pg_get_functiondef(
    'private.validate_credit_card_statement_line(uuid,uuid)'::regprocedure
  ) like '%debit_account.deleted_at is null%',
  'payment-line validation requires an active Bank Account'
);

select ok(
  pg_get_functiondef(
    'private.validate_statement_payment_candidate(uuid,uuid)'::regprocedure
  ) like '%account_deleted_at is not null%',
  'payment-candidate validation rejects archived Bank Accounts'
);

insert into auth.users (id, email) values
  ('92000000-0000-0000-0000-000000000001', 'balance-owner@example.com');

insert into public.accounts (
  id, user_id, side, account_type, name, institution_name
) values (
  '92000000-0000-0000-0000-000000000010',
  '92000000-0000-0000-0000-000000000001',
  'asset', 'bank_account', 'Test Bank', 'Test Bank'
);

insert into private.account_opening_balance_revisions (
  id, user_id, account_id, version, as_of, reason, changed_by_user_id
) values (
  '92000000-0000-0000-0000-000000000020',
  '92000000-0000-0000-0000-000000000001',
  '92000000-0000-0000-0000-000000000010',
  1, '2026-09-01 00:00:00+00', null,
  '92000000-0000-0000-0000-000000000001'
);

insert into private.account_opening_balance_revision_amounts (
  user_id, revision_id, currency, amount_minor
) values (
  '92000000-0000-0000-0000-000000000001',
  '92000000-0000-0000-0000-000000000020',
  'SGD', -1250
);

update public.accounts
set opening_balances = '{"SGD":"-1250"}'::jsonb,
    opening_balance_as_of = '2026-09-01 00:00:00+00',
    opening_balance_version = 1
where id = '92000000-0000-0000-0000-000000000010';

set constraints public.accounts_assert_opening_balance_state immediate;
set constraints private.account_balance_revisions_assert_state immediate;
set constraints private.account_balance_amounts_assert_state immediate;

select results_eq(
  $$select opening_balances from public.accounts
    where id = '92000000-0000-0000-0000-000000000010'$$,
  array['{"SGD":"-1250"}'::jsonb],
  'the public Account projection exactly matches the normalized latest revision'
);

select throws_ok(
  $$update private.account_opening_balance_revisions
    set as_of = '2026-08-31 00:00:00+00'
    where id = '92000000-0000-0000-0000-000000000020'$$,
  '23514',
  'opening-balance revisions and amounts are immutable',
  'opening-balance revision history is immutable'
);

-- A Card payment commonly posts after the activity period closes. It remains
-- valid when it falls between the statement and due dates and is linked to a
-- real Bank-to-Card transfer.
insert into public.accounts (
  id, user_id, side, account_type, name, institution_name
) values (
  '92000000-0000-0000-0000-000000000011',
  '92000000-0000-0000-0000-000000000001',
  'liability', 'credit_card', 'Test Card', 'Test Bank'
);

insert into private.data_sources (
  id, user_id, source_type, provider, received_at, raw_data
) values (
  '92000000-0000-0000-0000-000000000030',
  '92000000-0000-0000-0000-000000000001',
  'bulk_upload_document', 'user_upload', '2026-09-01 00:00:00+00', '{}'
);

insert into public.bulk_import_batches (
  id, user_id, template_version, title_snapshot,
  document_type_snapshot, parsing_prompt_snapshot
) values (
  '92000000-0000-0000-0000-000000000031',
  '92000000-0000-0000-0000-000000000001',
  1, 'Test Card Bill', 'credit_card_bill', 'Read the printed bill values.'
);

insert into private.bulk_import_batch_accounts (
  user_id, batch_id, account_id, account_ref, sort_order,
  account_name, institution_name, account_type
) values (
  '92000000-0000-0000-0000-000000000001',
  '92000000-0000-0000-0000-000000000031',
  '92000000-0000-0000-0000-000000000011',
  'account_1', 0, 'Test Card', 'Test Bank', 'credit_card'
);

insert into private.bulk_import_documents (
  id, user_id, batch_id, source_scope_id, data_source_id, sort_order
) values (
  '92000000-0000-0000-0000-000000000032',
  '92000000-0000-0000-0000-000000000001',
  '92000000-0000-0000-0000-000000000031',
  '92000000-0000-0000-0000-000000000030',
  '92000000-0000-0000-0000-000000000030', 0
);

insert into private.bulk_import_chunks (
  id, user_id, batch_id, document_id, attempt_generation,
  chunk_index, page_manifest, page_count
) values (
  '92000000-0000-0000-0000-000000000033',
  '92000000-0000-0000-0000-000000000001',
  '92000000-0000-0000-0000-000000000031',
  '92000000-0000-0000-0000-000000000032',
  1, 0, '{}', 1
);

insert into private.source_parse_attempts (
  id, user_id, data_source_id, bulk_import_chunk_id, attempt_ordinal
) values (
  '92000000-0000-0000-0000-000000000034',
  '92000000-0000-0000-0000-000000000001',
  '92000000-0000-0000-0000-000000000030',
  '92000000-0000-0000-0000-000000000033', 1
);

insert into private.bulk_import_candidates (
  id, user_id, batch_id, document_id, data_source_id,
  source_parse_attempt_id, attempt_generation, output_ordinal,
  fingerprint, parsed_candidate, account_id
) values (
  '92000000-0000-0000-0000-000000000035',
  '92000000-0000-0000-0000-000000000001',
  '92000000-0000-0000-0000-000000000031',
  '92000000-0000-0000-0000-000000000032',
  '92000000-0000-0000-0000-000000000030',
  '92000000-0000-0000-0000-000000000034',
  1, 0, decode(repeat('a1', 32), 'hex'),
  '{"bill_line_index":1,"bill_line_kind":"payment"}',
  '92000000-0000-0000-0000-000000000011'
);

insert into private.credit_card_statements (
  id, user_id, account_id, bulk_document_id, bulk_attempt_generation,
  period_start, period_end, statement_date, due_date,
  settlement_currency, amount_due_minor
) values (
  '92000000-0000-0000-0000-000000000040',
  '92000000-0000-0000-0000-000000000001',
  '92000000-0000-0000-0000-000000000011',
  '92000000-0000-0000-0000-000000000032', 1,
  '2026-08-01', '2026-08-31', '2026-09-01', '2026-09-25',
  'SGD', 1000
);

insert into public.transactions (
  id, user_id, account_id, transaction_kind, title,
  original_amount_minor, original_currency, occurred_at, review_status
) values
  (
    '92000000-0000-0000-0000-000000000041',
    '92000000-0000-0000-0000-000000000001',
    '92000000-0000-0000-0000-000000000010',
    'debit', 'Card payment Bank debit', 1000, 'SGD',
    '2026-09-10 09:00:00+00', 'confirmed'
  ),
  (
    '92000000-0000-0000-0000-000000000042',
    '92000000-0000-0000-0000-000000000001',
    '92000000-0000-0000-0000-000000000011',
    'credit', 'Card payment credit', 1000, 'SGD',
    '2026-09-10 09:00:00+00', 'confirmed'
  );

insert into private.transaction_links (
  id, user_id, debit_transaction_id, credit_transaction_id
) values (
  '92000000-0000-0000-0000-000000000043',
  '92000000-0000-0000-0000-000000000001',
  '92000000-0000-0000-0000-000000000041',
  '92000000-0000-0000-0000-000000000042'
);

insert into private.credit_card_statement_lines (
  user_id, statement_id, bulk_candidate_id, line_index, line_kind,
  line_fingerprint, description, occurred_on, occurred_at, time_precision,
  amount_minor, currency, resolution_status, transaction_id
) values (
  '92000000-0000-0000-0000-000000000001',
  '92000000-0000-0000-0000-000000000040',
  '92000000-0000-0000-0000-000000000035',
  1, 'payment', decode(repeat('b2', 32), 'hex'), 'Payment received',
  '2026-09-10', '2026-09-10 09:00:00+00', 'exact',
  1000, 'SGD', 'linked',
  '92000000-0000-0000-0000-000000000042'
);

select lives_ok(
  $$set constraints all immediate$$,
  'a post-period, pre-due payment line validates against the payment window'
);

update private.credit_card_statements
set unresolved_candidate_count = 1
where id = '92000000-0000-0000-0000-000000000040';

select is(
  (
    select unresolved_candidate_count
    from private.credit_card_statements
    where id = '92000000-0000-0000-0000-000000000040'
  ),
  1,
  'an unresolved candidate count remains persisted on the Review bill'
);

select throws_ok(
  $$update private.credit_card_statements
    set status = 'unpaid'
    where id = '92000000-0000-0000-0000-000000000040'$$,
  '23514',
  null,
  'a bill with unresolved candidate evidence cannot leave Review'
);

insert into public.accounts (
  id, user_id, side, account_type, name, institution_name
) values (
  '92000000-0000-0000-0000-000000000012',
  '92000000-0000-0000-0000-000000000001',
  'asset', 'bank_account', 'Archived Bank', 'Test Bank'
);

insert into public.transactions (
  id, user_id, account_id, transaction_kind, title,
  original_amount_minor, original_currency, occurred_at, review_status
) values (
  '92000000-0000-0000-0000-000000000044',
  '92000000-0000-0000-0000-000000000001',
  '92000000-0000-0000-0000-000000000012',
  'debit', 'Archived Bank candidate', 1000, 'SGD',
  '2026-09-10 09:00:00+00', 'confirmed'
);

update public.accounts
set deleted_at = now()
where id = '92000000-0000-0000-0000-000000000012';

select throws_ok(
  $$insert into private.credit_card_statement_payment_candidates (
      user_id, statement_id, bank_transaction_id, reason
    ) values (
      '92000000-0000-0000-0000-000000000001',
      '92000000-0000-0000-0000-000000000040',
      '92000000-0000-0000-0000-000000000044',
      'Same amount and date window'
    )$$,
  '23514',
  'a payment suggestion must be an exact in-window Bank debit',
  'payment candidates reject a debit from an archived Bank Account'
);

select * from finish();
rollback;
