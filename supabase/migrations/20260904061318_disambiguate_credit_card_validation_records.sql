-- Avoid PL/pgSQL record-variable/SQL-alias ambiguity reported by plpgsql_check.
-- These definitions preserve the validation behaviour introduced in
-- 20260904043721_create_account_balances_and_credit_card_bills.sql.

create or replace function private.validate_transaction_treatment(
  checked_transaction_id uuid,
  checked_user_id uuid
)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  treatment_row record;
  transaction_record record;
  item jsonb;
  total numeric := 0;
begin
  select treatment.* into treatment_row
  from private.transaction_calculation_treatments treatment
  where treatment.transaction_id = checked_transaction_id
    and treatment.user_id = checked_user_id;
  if not found then return; end if;

  select txn.original_amount_minor, txn.original_currency, txn.line_items
  into transaction_record
  from public.transactions txn
  where txn.id = checked_transaction_id and txn.user_id = checked_user_id
  for update;
  if not found then
    raise exception using errcode = '23503', message = 'treatment transaction not found';
  end if;

  if treatment_row.spending_basis = 'line_items' then
    if jsonb_array_length(transaction_record.line_items) = 0 then
      raise exception using errcode = '23514',
        message = 'line-item treatment requires a complete non-empty item list';
    end if;
    for item in select value from jsonb_array_elements(transaction_record.line_items)
    loop
      if not (item ? 'line_total_minor')
        or item -> 'line_total_minor' = 'null'::jsonb
        or item ->> 'currency' <> transaction_record.original_currency then
        raise exception using errcode = '23514',
          message = 'line-item treatment requires complete same-currency totals';
      end if;
      total := total + (item ->> 'line_total_minor')::numeric;
    end loop;
    if total <> transaction_record.original_amount_minor::numeric then
      raise exception using errcode = '23514',
        message = 'line-item totals must equal the canonical transaction amount';
    end if;
  end if;
end;
$$;

create or replace function private.validate_credit_card_statement_line(
  checked_line_id uuid,
  checked_user_id uuid
)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  line_row record;
  statement_row record;
  candidate_row record;
  transaction_record record;
  amount_or_date_differs boolean := false;
begin
  select line.* into line_row
  from private.credit_card_statement_lines line
  where line.id = checked_line_id and line.user_id = checked_user_id;
  if not found then return; end if;

  select statement.* into statement_row
  from private.credit_card_statements statement
  where statement.id = line_row.statement_id and statement.user_id = checked_user_id
  for update;

  if line_row.bulk_candidate_id is not null then
    select candidate.* into candidate_row
    from private.bulk_import_candidates candidate
    where candidate.id = line_row.bulk_candidate_id and candidate.user_id = checked_user_id;
    if candidate_row.document_id is distinct from statement_row.bulk_document_id
      or candidate_row.attempt_generation is distinct from statement_row.bulk_attempt_generation
      or candidate_row.parsed_candidate ->> 'bill_line_index' is distinct from line_row.line_index::text
      or candidate_row.parsed_candidate ->> 'bill_line_kind' is distinct from line_row.line_kind then
      raise exception using errcode = '23514',
        message = 'statement lines must preserve the pinned candidate generation, line index, and line kind';
    end if;
  end if;

  if line_row.transaction_id is null then return; end if;

  select txn.*, account.account_type
  into transaction_record
  from public.transactions txn
  join public.accounts account
    on account.id = txn.account_id and account.user_id = txn.user_id
  where txn.id = line_row.transaction_id and txn.user_id = checked_user_id
  for update of txn;

  if transaction_record.account_id <> statement_row.account_id
    or transaction_record.account_type <> 'credit_card'
    or (line_row.line_kind in ('activity', 'fee', 'interest') and transaction_record.transaction_kind <> 'debit')
    or (line_row.line_kind in ('refund', 'payment') and transaction_record.transaction_kind <> 'credit') then
    raise exception using errcode = '23514',
      message = 'a linked statement transaction must use the bill Card Account and line direction';
  end if;

  amount_or_date_differs :=
    (line_row.amount_minor is not null and transaction_record.original_amount_minor <> line_row.amount_minor)
    or (line_row.currency is not null and transaction_record.original_currency <> line_row.currency)
    or (
      line_row.occurred_at is not null
      and (
        (line_row.time_precision = 'date'
          and (transaction_record.occurred_at at time zone 'UTC')::date <> line_row.occurred_on)
        or (line_row.time_precision = 'exact'
          and abs(extract(epoch from transaction_record.occurred_at - line_row.occurred_at)) > 600)
      )
    )
    or (
      line_row.line_kind = 'payment'
      and statement_row.statement_date is not null and statement_row.due_date is not null
      and (transaction_record.occurred_at at time zone 'UTC')::date
        not between statement_row.statement_date and statement_row.due_date
    )
    or (
      line_row.line_kind <> 'payment'
      and statement_row.period_start is not null and statement_row.period_end is not null
      and (transaction_record.occurred_at at time zone 'UTC')::date
        not between statement_row.period_start and statement_row.period_end
    );

  if amount_or_date_differs and line_row.link_exception_reason is null then
    raise exception using errcode = '23514',
      message = 'a non-exact statement link requires an exception reason';
  end if;
  if not amount_or_date_differs and line_row.link_exception_reason is not null then
    raise exception using errcode = '23514',
      message = 'an exact statement link cannot carry an exception reason';
  end if;

  if line_row.line_kind = 'payment' and not exists (
    select 1 from private.transaction_links link
    join public.transactions debit
      on debit.id = link.debit_transaction_id and debit.user_id = link.user_id
    join public.accounts debit_account
      on debit_account.id = debit.account_id and debit_account.user_id = debit.user_id
    where link.user_id = checked_user_id
      and link.credit_transaction_id = transaction_record.id
      and debit.transaction_kind = 'debit'
      and debit_account.account_type = 'bank_account'
      and debit_account.deleted_at is null
  ) then
    raise exception using errcode = '23514',
      message = 'a payment line may link only to a Bank-to-Card internal transfer';
  end if;
end;
$$;

create or replace function private.validate_statement_payment_candidate(
  checked_candidate_id uuid,
  checked_user_id uuid
)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  candidate_row record;
  statement_row record;
  transaction_record record;
begin
  select candidate.* into candidate_row
  from private.credit_card_statement_payment_candidates candidate
  where candidate.id = checked_candidate_id and candidate.user_id = checked_user_id;
  if not found then return; end if;

  select statement.* into statement_row
  from private.credit_card_statements statement
  where statement.id = candidate_row.statement_id and statement.user_id = checked_user_id
  for update;

  select txn.*, account.account_type,
    account.deleted_at as account_deleted_at
  into transaction_record
  from public.transactions txn
  join public.accounts account
    on account.id = txn.account_id and account.user_id = txn.user_id
  where txn.id = candidate_row.bank_transaction_id
    and txn.user_id = checked_user_id
  for update of txn;

  if transaction_record.transaction_kind is distinct from 'debit'
    or statement_row.amount_due_minor is null
    or statement_row.settlement_currency is null
    or statement_row.statement_date is null
    or statement_row.due_date is null
    or transaction_record.account_type is distinct from 'bank_account'
    or transaction_record.account_deleted_at is not null
    or transaction_record.original_amount_minor is distinct from statement_row.amount_due_minor
    or transaction_record.original_currency is distinct from statement_row.settlement_currency
    or (transaction_record.occurred_at at time zone 'UTC')::date
      not between statement_row.statement_date and statement_row.due_date then
    raise exception using errcode = '23514',
      message = 'a payment suggestion must be an exact in-window Bank debit';
  end if;

  if candidate_row.status = 'confirmed' and not exists (
    select 1 from private.transaction_links link
    where link.id = statement_row.payoff_transaction_link_id
      and link.user_id = checked_user_id
      and link.debit_transaction_id = candidate_row.bank_transaction_id
  ) then
    raise exception using errcode = '23514',
      message = 'a confirmed payment candidate must be the bill payoff Bank leg';
  end if;
end;
$$;

revoke execute on function private.validate_transaction_treatment(uuid, uuid)
  from public, anon, authenticated;
revoke execute on function private.validate_credit_card_statement_line(uuid, uuid)
  from public, anon, authenticated;
revoke execute on function private.validate_statement_payment_candidate(uuid, uuid)
  from public, anon, authenticated;
