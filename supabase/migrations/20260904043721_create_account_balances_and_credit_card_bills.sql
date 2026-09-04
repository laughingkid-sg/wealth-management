-- Account opening balances, calculation treatments, and Credit Card bills.
-- Bulk Import is the only statement upload/parser and must be migrated first.

create or replace function private.opening_balances_are_valid(
  checked_balances jsonb,
  checked_account_type text
)
returns boolean
language plpgsql
immutable
set search_path = ''
as $$
declare
  entry record;
  normalized_amount text;
begin
  if checked_balances is null
    or jsonb_typeof(checked_balances) <> 'object'
    or octet_length(checked_balances::text) > 16384
    or (select count(*) from jsonb_object_keys(checked_balances)) > 20 then
    return false;
  end if;

  for entry in select key, value from jsonb_each(checked_balances)
  loop
    if entry.key !~ '^[A-Z]{3}$' or jsonb_typeof(entry.value) <> 'string' then
      return false;
    end if;
    normalized_amount := entry.value #>> '{}';
    if normalized_amount !~ '^(0|-?[1-9][0-9]*)$'
      or octet_length(normalized_amount) > 20 then
      return false;
    end if;
    begin
      if normalized_amount::numeric < -9223372036854775808::numeric
        or normalized_amount::numeric > 9223372036854775807::numeric then
        return false;
      end if;
    exception when numeric_value_out_of_range then
      return false;
    end;
    if checked_account_type <> 'bank_account' and normalized_amount::numeric < 0 then
      return false;
    end if;
  end loop;

  return true;
end;
$$;

revoke execute on function private.opening_balances_are_valid(jsonb, text)
  from public, anon, authenticated;
grant execute on function private.opening_balances_are_valid(jsonb, text)
  to authenticated, service_role;

alter table public.accounts
  add column opening_balances jsonb not null default '{}'::jsonb,
  add column opening_balance_as_of timestamptz,
  add column opening_balance_version integer not null default 0,
  add constraint accounts_opening_balances_shape_check
    check (private.opening_balances_are_valid(opening_balances, account_type)) not valid,
  add constraint accounts_opening_balance_state_check check (
    (
      opening_balance_version = 0
      and opening_balances = '{}'::jsonb
      and opening_balance_as_of is null
    )
    or (
      opening_balance_version > 0
      and opening_balances <> '{}'::jsonb
      and opening_balance_as_of is not null
    )
  ) not valid;

alter table public.accounts validate constraint accounts_opening_balances_shape_check;
alter table public.accounts validate constraint accounts_opening_balance_state_check;

create table private.account_opening_balance_revisions (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  account_id uuid not null,
  version integer not null,
  as_of timestamptz not null,
  reason text,
  changed_by_user_id uuid not null references auth.users(id) on delete cascade,
  created_at timestamptz not null default now(),
  constraint account_balance_revisions_account_fkey
    foreign key (account_id, user_id)
    references public.accounts (id, user_id)
    on delete cascade,
  constraint account_balance_revisions_version_check check (version > 0),
  constraint account_balance_revisions_reason_check check (
    (version = 1 and reason is null)
    or (
      version > 1 and reason is not null
      and char_length(btrim(reason)) between 1 and 500
    )
  ),
  constraint account_balance_revisions_actor_check
    check (changed_by_user_id = user_id),
  constraint account_balance_revisions_account_version_key
    unique (account_id, version),
  constraint account_balance_revisions_id_user_id_key unique (id, user_id)
);

comment on table private.account_opening_balance_revisions is
  'Immutable, normalized Account opening-balance correction history.';

create index account_balance_revisions_user_account_idx
  on private.account_opening_balance_revisions (user_id, account_id, version desc);

create index account_balance_revisions_actor_idx
  on private.account_opening_balance_revisions (changed_by_user_id);

create table private.account_opening_balance_revision_amounts (
  user_id uuid not null references auth.users(id) on delete cascade,
  revision_id uuid not null,
  currency text not null,
  amount_minor bigint not null,
  created_at timestamptz not null default now(),
  primary key (revision_id, currency),
  constraint account_balance_amounts_revision_fkey
    foreign key (revision_id, user_id)
    references private.account_opening_balance_revisions (id, user_id)
    on delete cascade,
  constraint account_balance_amounts_currency_check
    check (currency ~ '^[A-Z]{3}$')
);

create index account_balance_amounts_user_revision_idx
  on private.account_opening_balance_revision_amounts (user_id, revision_id);

create or replace function private.validate_account_opening_balance_state(
  checked_account_id uuid,
  checked_user_id uuid
)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  account_row record;
  revision_row record;
  prior_balances jsonb;
  current_balances jsonb;
  expected_version integer := 0;
  revision_count integer;
begin
  select account.id, account.user_id, account.account_type,
    account.opening_balances, account.opening_balance_as_of,
    account.opening_balance_version
  into account_row
  from public.accounts account
  where account.id = checked_account_id and account.user_id = checked_user_id
  for update;

  if not found then
    return;
  end if;

  select count(*)::integer
  into revision_count
  from private.account_opening_balance_revisions revision
  where revision.account_id = checked_account_id and revision.user_id = checked_user_id;

  if account_row.opening_balance_version = 0 then
    if revision_count <> 0
      or account_row.opening_balances <> '{}'::jsonb
      or account_row.opening_balance_as_of is not null then
      raise exception using errcode = '23514',
        message = 'an unconfigured Account cannot have opening-balance revisions';
    end if;
    return;
  end if;

  prior_balances := null;
  for revision_row in
    select revision.id, revision.version, revision.as_of, revision.reason,
      revision.changed_by_user_id,
      coalesce(
        (
          select jsonb_object_agg(amount.currency, amount.amount_minor::text order by amount.currency)
          from private.account_opening_balance_revision_amounts amount
          where amount.revision_id = revision.id and amount.user_id = revision.user_id
        ),
        '{}'::jsonb
      ) as balances,
      (
        select count(*)::integer
        from private.account_opening_balance_revision_amounts amount
        where amount.revision_id = revision.id and amount.user_id = revision.user_id
      ) as amount_count
    from private.account_opening_balance_revisions revision
    where revision.account_id = checked_account_id and revision.user_id = checked_user_id
    order by revision.version
  loop
    expected_version := expected_version + 1;
    if revision_row.version <> expected_version
      or revision_row.amount_count not between 1 and 20
      or revision_row.as_of > now()
      or revision_row.changed_by_user_id <> checked_user_id
      or not private.opening_balances_are_valid(
        revision_row.balances, account_row.account_type
      ) then
      raise exception using errcode = '23514',
        message = 'opening-balance revisions must be contiguous, complete, owned, valid, and not future dated';
    end if;

    if expected_version > 1
      and revision_row.balances = prior_balances
      and revision_row.as_of = (
        select prior.as_of
        from private.account_opening_balance_revisions prior
        where prior.account_id = checked_account_id
          and prior.user_id = checked_user_id
          and prior.version = expected_version - 1
      ) then
      raise exception using errcode = '23514',
        message = 'an opening-balance correction must change the balances or as-of time';
    end if;
    prior_balances := revision_row.balances;
    current_balances := revision_row.balances;
  end loop;

  select revision.as_of
  into revision_row
  from private.account_opening_balance_revisions revision
  where revision.account_id = checked_account_id
    and revision.user_id = checked_user_id
    and revision.version = account_row.opening_balance_version;

  if revision_count <> account_row.opening_balance_version
    or expected_version <> account_row.opening_balance_version
    or current_balances is distinct from account_row.opening_balances
    or revision_row.as_of is distinct from account_row.opening_balance_as_of then
    raise exception using errcode = '23514',
      message = 'the Account opening-balance projection must equal its latest revision';
  end if;
end;
$$;

create or replace function private.assert_account_opening_balance_state()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  checked_account_id uuid;
begin
  if tg_table_name = 'accounts' then
    perform private.validate_account_opening_balance_state(
      coalesce(new.id, old.id), coalesce(new.user_id, old.user_id)
    );
  elsif tg_table_name = 'account_opening_balance_revisions' then
    if tg_op <> 'INSERT' then
      perform private.validate_account_opening_balance_state(old.account_id, old.user_id);
    end if;
    if tg_op <> 'DELETE' then
      perform private.validate_account_opening_balance_state(new.account_id, new.user_id);
    end if;
  else
    if tg_op <> 'INSERT' then
      select revision.account_id into checked_account_id
      from private.account_opening_balance_revisions revision
      where revision.id = old.revision_id and revision.user_id = old.user_id;
      if checked_account_id is not null then
        perform private.validate_account_opening_balance_state(
          checked_account_id, old.user_id
        );
      end if;
    end if;
    if tg_op <> 'DELETE' then
      select revision.account_id into checked_account_id
      from private.account_opening_balance_revisions revision
      where revision.id = new.revision_id and revision.user_id = new.user_id;
      if checked_account_id is not null then
        perform private.validate_account_opening_balance_state(
          checked_account_id, new.user_id
        );
      end if;
    end if;
  end if;
  if tg_op = 'DELETE' then return old; end if;
  return new;
end;
$$;

-- Revision rows and currency amounts are append-only. Corrections insert a new
-- revision; they never mutate an earlier one.
create or replace function private.protect_opening_balance_revision()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if tg_op = 'UPDATE' then
    raise exception using errcode = '23514',
      message = 'opening-balance revisions and amounts are immutable';
  end if;
  if tg_table_name = 'account_opening_balance_revisions'
    and exists (
      select 1 from public.accounts account
      where account.id = old.account_id and account.user_id = old.user_id
    ) then
    raise exception using errcode = '23514',
      message = 'opening-balance revisions cannot be deleted independently';
  end if;
  if tg_table_name = 'account_opening_balance_revision_amounts'
    and exists (
      select 1 from private.account_opening_balance_revisions revision
      where revision.id = old.revision_id and revision.user_id = old.user_id
    ) then
    raise exception using errcode = '23514',
      message = 'opening-balance revision amounts cannot be deleted independently';
  end if;
  return old;
end;
$$;

revoke execute on function private.validate_account_opening_balance_state(uuid, uuid)
  from public, anon, authenticated;
revoke execute on function private.assert_account_opening_balance_state()
  from public, anon, authenticated;
revoke execute on function private.protect_opening_balance_revision()
  from public, anon, authenticated;

create trigger account_balance_revisions_immutable
before update or delete on private.account_opening_balance_revisions
for each row execute function private.protect_opening_balance_revision();

create trigger account_balance_amounts_immutable
before update or delete on private.account_opening_balance_revision_amounts
for each row execute function private.protect_opening_balance_revision();

create constraint trigger accounts_assert_opening_balance_state
after insert or update on public.accounts
deferrable initially deferred
for each row execute function private.assert_account_opening_balance_state();

create constraint trigger account_balance_revisions_assert_state
after insert on private.account_opening_balance_revisions
deferrable initially deferred
for each row execute function private.assert_account_opening_balance_state();

create constraint trigger account_balance_amounts_assert_state
after insert on private.account_opening_balance_revision_amounts
deferrable initially deferred
for each row execute function private.assert_account_opening_balance_state();

-- Replace table-wide Account writes with the exact browser directory surface.
revoke all privileges on table public.accounts from public, anon, authenticated;
grant select on table public.accounts to authenticated;
grant insert (
  user_id, side, account_type, name, institution_name,
  account_identifier, notes, metadata, sort_order
) on table public.accounts to authenticated;
grant update (
  side, account_type, name, institution_name,
  account_identifier, notes, metadata, sort_order, deleted_at
) on table public.accounts to authenticated;

alter table public.transactions
  drop constraint transactions_creation_method_check,
  add constraint transactions_creation_method_check check (
    creation_method in (
      'automatic_source', 'user_source', 'manual',
      'internal_transfer', 'credit_card_statement'
    )
  );

create table private.transaction_calculation_treatments (
  transaction_id uuid primary key,
  user_id uuid not null references auth.users(id) on delete cascade,
  spending_basis text not null,
  source text not null,
  reason text not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint transaction_treatments_transaction_fkey
    foreign key (transaction_id, user_id)
    references public.transactions (id, user_id)
    on delete cascade,
  constraint transaction_treatments_basis_check
    check (spending_basis in ('transaction_total', 'line_items', 'exclude')),
  constraint transaction_treatments_source_check
    check (source in ('system', 'user')),
  constraint transaction_treatments_reason_check
    check (char_length(btrim(reason)) between 1 and 500),
  constraint transaction_treatments_system_check check (
    source <> 'system'
    or (spending_basis = 'exclude' and reason = 'credit_card_payoff')
  ),
  constraint transaction_treatments_id_user_key unique (transaction_id, user_id)
);

create index transaction_treatments_user_basis_idx
  on private.transaction_calculation_treatments (user_id, spending_basis, transaction_id);

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
  transaction_row record;
  item jsonb;
  total numeric := 0;
begin
  select treatment.* into treatment_row
  from private.transaction_calculation_treatments treatment
  where treatment.transaction_id = checked_transaction_id
    and treatment.user_id = checked_user_id;
  if not found then return; end if;

  select transaction_row.original_amount_minor, transaction_row.original_currency,
    transaction_row.line_items
  into transaction_row
  from public.transactions transaction_row
  where transaction_row.id = checked_transaction_id and transaction_row.user_id = checked_user_id
  for update;
  if not found then
    raise exception using errcode = '23503', message = 'treatment transaction not found';
  end if;

  if treatment_row.spending_basis = 'line_items' then
    if jsonb_array_length(transaction_row.line_items) = 0 then
      raise exception using errcode = '23514',
        message = 'line-item treatment requires a complete non-empty item list';
    end if;
    for item in select value from jsonb_array_elements(transaction_row.line_items)
    loop
      if not (item ? 'line_total_minor')
        or item -> 'line_total_minor' = 'null'::jsonb
        or item ->> 'currency' <> transaction_row.original_currency then
        raise exception using errcode = '23514',
          message = 'line-item treatment requires complete same-currency totals';
      end if;
      total := total + (item ->> 'line_total_minor')::numeric;
    end loop;
    if total <> transaction_row.original_amount_minor::numeric then
      raise exception using errcode = '23514',
        message = 'line-item totals must equal the canonical transaction amount';
    end if;
  end if;
end;
$$;

create or replace function private.assert_transaction_treatment()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if tg_table_name = 'transactions' then
    perform private.validate_transaction_treatment(new.id, new.user_id);
  else
    if tg_op <> 'INSERT' then
      perform private.validate_transaction_treatment(old.transaction_id, old.user_id);
    end if;
    if tg_op <> 'DELETE' then
      perform private.validate_transaction_treatment(new.transaction_id, new.user_id);
    end if;
  end if;
  if tg_op = 'DELETE' then return old; end if;
  return new;
end;
$$;

create or replace function private.protect_system_transaction_treatment()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if old.source = 'system' then
    raise exception using errcode = '23514',
      message = 'system transaction treatments are immutable';
  end if;
  if tg_op = 'DELETE' then return old; end if;
  return new;
end;
$$;

revoke execute on function private.validate_transaction_treatment(uuid, uuid)
  from public, anon, authenticated;
revoke execute on function private.assert_transaction_treatment()
  from public, anon, authenticated;
revoke execute on function private.protect_system_transaction_treatment()
  from public, anon, authenticated;

create trigger transaction_treatments_protect_system
before update or delete on private.transaction_calculation_treatments
for each row execute function private.protect_system_transaction_treatment();

create constraint trigger transaction_treatments_assert_valid
after insert or update or delete on private.transaction_calculation_treatments
deferrable initially deferred
for each row execute function private.assert_transaction_treatment();

create constraint trigger transactions_assert_treatment_valid
after update of original_amount_minor, original_currency, line_items on public.transactions
deferrable initially deferred
for each row execute function private.assert_transaction_treatment();

create trigger transaction_treatments_set_updated_at
before update on private.transaction_calculation_treatments
for each row execute function public.set_updated_at();

alter table private.transaction_links
  add constraint transaction_links_id_user_id_key unique (id, user_id);

create table private.credit_card_statements (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  account_id uuid not null,
  bulk_document_id uuid not null,
  bulk_attempt_generation integer not null,
  period_start date,
  period_end date,
  statement_date date,
  due_date date,
  settlement_currency text,
  amount_due_minor bigint,
  minimum_payment_minor bigint,
  previous_balance_minor bigint,
  unresolved_candidate_count integer not null default 0,
  status text not null default 'review',
  payoff_transaction_link_id uuid,
  version integer not null default 1,
  void_reason text,
  paid_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint credit_card_statements_account_fkey
    foreign key (account_id, user_id)
    references public.accounts (id, user_id)
    on delete restrict,
  constraint credit_card_statements_document_fkey
    foreign key (bulk_document_id, user_id)
    references private.bulk_import_documents (id, user_id)
    on delete restrict,
  constraint credit_card_statements_payoff_fkey
    foreign key (payoff_transaction_link_id, user_id)
    references private.transaction_links (id, user_id)
    on delete restrict,
  constraint credit_card_statements_generation_check
    check (bulk_attempt_generation > 0),
  constraint credit_card_statements_period_check
    check (period_start is null or period_end is null or period_start <= period_end),
  constraint credit_card_statements_dates_check
    check (statement_date is null or due_date is null or statement_date <= due_date),
  constraint credit_card_statements_currency_check
    check (settlement_currency is null or settlement_currency ~ '^[A-Z]{3}$'),
  constraint credit_card_statements_amount_due_check
    check (amount_due_minor is null or amount_due_minor > 0),
  constraint credit_card_statements_minimum_check
    check (minimum_payment_minor is null or minimum_payment_minor >= 0),
  constraint credit_card_statements_previous_check
    check (previous_balance_minor is null or previous_balance_minor >= 0),
  constraint credit_card_statements_unresolved_count_check
    check (unresolved_candidate_count >= 0),
  constraint credit_card_statements_status_check
    check (status in ('review', 'unpaid', 'paid', 'void')),
  constraint credit_card_statements_complete_header_check check (
    status in ('review', 'void')
    or (
      period_start is not null and period_end is not null
      and statement_date is not null and due_date is not null
      and settlement_currency is not null and amount_due_minor is not null
    )
  ),
  constraint credit_card_statements_payment_state_check check (
    (status = 'paid' and payoff_transaction_link_id is not null and paid_at is not null)
    or (status <> 'paid' and payoff_transaction_link_id is null and paid_at is null)
  ),
  constraint credit_card_statements_unresolved_state_check check (
    unresolved_candidate_count = 0
    or (
      status = 'review'
      and payoff_transaction_link_id is null
      and paid_at is null
    )
  ),
  constraint credit_card_statements_void_reason_check check (
    (
      status = 'void' and void_reason is not null
      and char_length(btrim(void_reason)) between 1 and 500
    )
    or (status <> 'void' and void_reason is null)
  ),
  constraint credit_card_statements_version_check check (version > 0),
  constraint credit_card_statements_document_key unique (bulk_document_id),
  constraint credit_card_statements_payoff_key unique (payoff_transaction_link_id),
  constraint credit_card_statements_id_user_id_key unique (id, user_id)
);

create index credit_card_statements_user_account_period_idx
  on private.credit_card_statements
  (user_id, account_id, period_end desc, id desc);

create index credit_card_statements_account_idx
  on private.credit_card_statements (account_id, user_id);

create index credit_card_statements_document_idx
  on private.credit_card_statements (bulk_document_id, user_id);

create or replace function private.validate_credit_card_statement(
  checked_statement_id uuid,
  checked_user_id uuid
)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  statement_row record;
  document_row record;
  account_row record;
  payoff_row record;
begin
  select statement.* into statement_row
  from private.credit_card_statements statement
  where statement.id = checked_statement_id and statement.user_id = checked_user_id;
  if not found then return; end if;

  select account.account_type, account.deleted_at
  into account_row
  from public.accounts account
  where account.id = statement_row.account_id and account.user_id = checked_user_id
  for update;
  if account_row.account_type is distinct from 'credit_card' then
    raise exception using errcode = '23514',
      message = 'a Credit Card bill requires a Credit Card Account';
  end if;

  select document.id, document.data_source_id, document.attempt_generation,
    batch.document_type_snapshot
  into document_row
  from private.bulk_import_documents document
  join public.bulk_import_batches batch
    on batch.id = document.batch_id and batch.user_id = document.user_id
  where document.id = statement_row.bulk_document_id
    and document.user_id = checked_user_id;

  if document_row.document_type_snapshot is distinct from 'credit_card_bill'
    or document_row.data_source_id is null
    or document_row.attempt_generation <> statement_row.bulk_attempt_generation
    or not exists (
      select 1
      from private.bulk_import_batch_accounts selected
      join private.bulk_import_documents document
        on document.batch_id = selected.batch_id and document.user_id = selected.user_id
      where document.id = statement_row.bulk_document_id
        and document.user_id = checked_user_id
        and selected.account_id = statement_row.account_id
        and selected.account_type = 'credit_card'
    ) then
    raise exception using errcode = '23514',
      message = 'the bill must use the pinned Credit Card bulk document generation and Account';
  end if;

  if statement_row.period_start is not null and statement_row.period_end is not null
    and exists (
      select 1 from private.credit_card_statements other
      where other.user_id = checked_user_id
        and other.account_id = statement_row.account_id
        and other.id <> statement_row.id
        and other.period_start is not null and other.period_end is not null
        and daterange(other.period_start, other.period_end, '[]')
          && daterange(statement_row.period_start, statement_row.period_end, '[]')
    ) then
    raise exception using errcode = '23514',
      message = 'Credit Card bill periods for one Account cannot overlap';
  end if;

  if statement_row.payoff_transaction_link_id is not null then
    select link.id,
      debit.account_id as debit_account_id,
      debit.transaction_kind as debit_kind,
      debit.original_amount_minor as debit_amount,
      debit.original_currency as debit_currency,
      debit.occurred_at as debit_occurred_at,
      credit.account_id as credit_account_id,
      credit.transaction_kind as credit_kind,
      credit.original_amount_minor as credit_amount,
      credit.original_currency as credit_currency,
      credit.occurred_at as credit_occurred_at,
      debit_account.account_type as debit_account_type,
      debit_account.deleted_at as debit_account_deleted_at
    into payoff_row
    from private.transaction_links link
    join public.transactions debit
      on debit.id = link.debit_transaction_id and debit.user_id = link.user_id
    join public.transactions credit
      on credit.id = link.credit_transaction_id and credit.user_id = link.user_id
    join public.accounts debit_account
      on debit_account.id = debit.account_id and debit_account.user_id = debit.user_id
    where link.id = statement_row.payoff_transaction_link_id
      and link.user_id = checked_user_id
      and link.link_type = 'internal_transfer';

    if payoff_row.id is null
      or payoff_row.debit_account_type <> 'bank_account'
      or payoff_row.debit_account_deleted_at is not null
      or payoff_row.debit_kind <> 'debit'
      or payoff_row.credit_kind <> 'credit'
      or payoff_row.credit_account_id <> statement_row.account_id
      or payoff_row.debit_amount <> statement_row.amount_due_minor
      or payoff_row.credit_amount <> statement_row.amount_due_minor
      or payoff_row.debit_currency <> statement_row.settlement_currency
      or payoff_row.credit_currency <> statement_row.settlement_currency
      or abs(extract(epoch from payoff_row.credit_occurred_at - payoff_row.debit_occurred_at)) > 600 then
      raise exception using errcode = '23514',
        message = 'the bill payoff must be one exact Bank-to-Card transfer with compatible leg times';
    end if;
  end if;
end;
$$;

create or replace function private.assert_credit_card_statement()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if tg_table_name = 'credit_card_statements' then
    if tg_op <> 'INSERT' then
      perform private.validate_credit_card_statement(old.id, old.user_id);
    end if;
    if tg_op <> 'DELETE' then
      perform private.validate_credit_card_statement(new.id, new.user_id);
    end if;
  elsif tg_table_name = 'transaction_links' then
    perform private.validate_credit_card_statement(
      statement.id, statement.user_id
    )
    from private.credit_card_statements statement
    where statement.payoff_transaction_link_id = coalesce(new.id, old.id);
  else
    perform private.validate_credit_card_statement(
      statement.id, statement.user_id
    )
    from private.credit_card_statements statement
    join private.transaction_links link
      on link.id = statement.payoff_transaction_link_id and link.user_id = statement.user_id
    where coalesce(new.id, old.id) in (link.debit_transaction_id, link.credit_transaction_id);
  end if;
  if tg_op = 'DELETE' then return old; end if;
  return new;
end;
$$;

create or replace function private.assert_new_statement_account_active()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if not exists (
    select 1 from public.accounts account
    where account.id = new.account_id and account.user_id = new.user_id
      and account.account_type = 'credit_card' and account.deleted_at is null
  ) then
    raise exception using errcode = '23514',
      message = 'a new Credit Card bill requires an active Credit Card Account';
  end if;
  return new;
end;
$$;

revoke execute on function private.validate_credit_card_statement(uuid, uuid)
  from public, anon, authenticated;
revoke execute on function private.assert_credit_card_statement()
  from public, anon, authenticated;
revoke execute on function private.assert_new_statement_account_active()
  from public, anon, authenticated;

create trigger credit_card_statements_active_account
before insert on private.credit_card_statements
for each row execute function private.assert_new_statement_account_active();

create constraint trigger credit_card_statements_assert_valid
after insert or update or delete on private.credit_card_statements
deferrable initially deferred
for each row execute function private.assert_credit_card_statement();

create constraint trigger transaction_links_assert_statement_valid
after update or delete on private.transaction_links
deferrable initially deferred
for each row execute function private.assert_credit_card_statement();

create constraint trigger transactions_assert_statement_payoff_valid
after update on public.transactions
deferrable initially deferred
for each row execute function private.assert_credit_card_statement();

create trigger credit_card_statements_set_updated_at
before update on private.credit_card_statements
for each row execute function public.set_updated_at();

create table private.credit_card_statement_lines (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  statement_id uuid not null,
  bulk_candidate_id uuid,
  line_index integer not null,
  line_kind text not null,
  line_fingerprint bytea not null,
  description text not null,
  occurred_on date,
  occurred_at timestamptz,
  time_precision text,
  amount_minor bigint,
  currency text,
  resolution_status text not null default 'pending',
  resolution_reason text,
  link_exception_reason text,
  transaction_id uuid,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint credit_card_statement_lines_statement_fkey
    foreign key (statement_id, user_id)
    references private.credit_card_statements (id, user_id)
    on delete cascade,
  constraint credit_card_statement_lines_candidate_fkey
    foreign key (bulk_candidate_id, user_id)
    references private.bulk_import_candidates (id, user_id)
    on delete restrict,
  constraint credit_card_statement_lines_transaction_fkey
    foreign key (transaction_id, user_id)
    references public.transactions (id, user_id)
    on delete restrict,
  constraint credit_card_statement_lines_index_check check (line_index > 0),
  constraint credit_card_statement_lines_kind_check
    check (line_kind in ('activity', 'refund', 'fee', 'interest', 'payment', 'summary')),
  constraint credit_card_statement_lines_fingerprint_check
    check (octet_length(line_fingerprint) = 32),
  constraint credit_card_statement_lines_description_check
    check (char_length(btrim(description)) between 1 and 500),
  constraint credit_card_statement_lines_amount_check check (
    (amount_minor is null and currency is null)
    or (amount_minor > 0 and currency ~ '^[A-Z]{3}$')
  ),
  constraint credit_card_statement_lines_time_check check (
    (occurred_at is null and occurred_on is null and time_precision is null)
    or (
      occurred_at is not null and occurred_on is not null
      and time_precision in ('exact', 'date')
      and (occurred_at at time zone 'UTC')::date = occurred_on
      and (
        time_precision <> 'date'
        or occurred_at = (
          occurred_on::timestamp + interval '12 hours'
        ) at time zone 'UTC'
      )
    )
  ),
  constraint credit_card_statement_lines_resolution_check
    check (resolution_status in ('pending', 'linked', 'ignored')),
  constraint credit_card_statement_lines_resolution_state_check check (
    (resolution_status = 'pending' and transaction_id is null and resolution_reason is null)
    or (resolution_status = 'linked' and transaction_id is not null and resolution_reason is null)
    or (
      resolution_status = 'ignored' and transaction_id is null
      and resolution_reason is not null
      and char_length(btrim(resolution_reason)) between 1 and 500
    )
  ),
  constraint credit_card_statement_lines_exception_check
    check (link_exception_reason is null or char_length(btrim(link_exception_reason)) between 1 and 500),
  constraint credit_card_statement_lines_summary_check check (
    (line_kind = 'summary' and bulk_candidate_id is null and resolution_status = 'ignored'
      and resolution_reason is not null
      and resolution_reason = 'statement_summary' and transaction_id is null)
    or (line_kind <> 'summary' and bulk_candidate_id is not null)
  ),
  constraint credit_card_statement_lines_statement_index_key
    unique (statement_id, line_index),
  constraint credit_card_statement_lines_statement_fingerprint_key
    unique (statement_id, line_fingerprint),
  constraint credit_card_statement_lines_candidate_key unique (bulk_candidate_id),
  constraint credit_card_statement_lines_transaction_key unique (transaction_id),
  constraint credit_card_statement_lines_id_user_id_key unique (id, user_id)
);

create index credit_card_statement_lines_user_statement_idx
  on private.credit_card_statement_lines (user_id, statement_id, line_index);

create index credit_card_statement_lines_candidate_idx
  on private.credit_card_statement_lines (bulk_candidate_id, user_id)
  where bulk_candidate_id is not null;

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
  transaction_row record;
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

  select transaction_row.*, account.account_type
  into transaction_row
  from public.transactions transaction_row
  join public.accounts account
    on account.id = transaction_row.account_id and account.user_id = transaction_row.user_id
  where transaction_row.id = line_row.transaction_id and transaction_row.user_id = checked_user_id
  for update of transaction_row;

  if transaction_row.account_id <> statement_row.account_id
    or transaction_row.account_type <> 'credit_card'
    or (line_row.line_kind in ('activity', 'fee', 'interest') and transaction_row.transaction_kind <> 'debit')
    or (line_row.line_kind in ('refund', 'payment') and transaction_row.transaction_kind <> 'credit') then
    raise exception using errcode = '23514',
      message = 'a linked statement transaction must use the bill Card Account and line direction';
  end if;

  amount_or_date_differs :=
    (line_row.amount_minor is not null and transaction_row.original_amount_minor <> line_row.amount_minor)
    or (line_row.currency is not null and transaction_row.original_currency <> line_row.currency)
    or (
      line_row.occurred_at is not null
      and (
        (line_row.time_precision = 'date'
          and (transaction_row.occurred_at at time zone 'UTC')::date <> line_row.occurred_on)
        or (line_row.time_precision = 'exact'
          and abs(extract(epoch from transaction_row.occurred_at - line_row.occurred_at)) > 600)
      )
    )
    or (
      line_row.line_kind = 'payment'
      and statement_row.statement_date is not null and statement_row.due_date is not null
      and (transaction_row.occurred_at at time zone 'UTC')::date
        not between statement_row.statement_date and statement_row.due_date
    )
    or (
      line_row.line_kind <> 'payment'
      and statement_row.period_start is not null and statement_row.period_end is not null
      and (transaction_row.occurred_at at time zone 'UTC')::date
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
      and link.credit_transaction_id = transaction_row.id
      and debit.transaction_kind = 'debit'
      and debit_account.account_type = 'bank_account'
      and debit_account.deleted_at is null
  ) then
    raise exception using errcode = '23514',
      message = 'a payment line may link only to a Bank-to-Card internal transfer';
  end if;
end;
$$;

create or replace function private.assert_credit_card_statement_line()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if tg_table_name = 'credit_card_statement_lines' then
    if tg_op <> 'INSERT' then
      perform private.validate_credit_card_statement_line(old.id, old.user_id);
    end if;
    if tg_op <> 'DELETE' then
      perform private.validate_credit_card_statement_line(new.id, new.user_id);
    end if;
  else
    perform private.validate_credit_card_statement_line(line.id, line.user_id)
    from private.credit_card_statement_lines line
    where line.transaction_id = coalesce(new.id, old.id);
  end if;
  if tg_op = 'DELETE' then return old; end if;
  return new;
end;
$$;

revoke execute on function private.validate_credit_card_statement_line(uuid, uuid)
  from public, anon, authenticated;
revoke execute on function private.assert_credit_card_statement_line()
  from public, anon, authenticated;

create constraint trigger credit_card_statement_lines_assert_valid
after insert or update or delete on private.credit_card_statement_lines
deferrable initially deferred
for each row execute function private.assert_credit_card_statement_line();

create constraint trigger transactions_assert_statement_line_valid
after update on public.transactions
deferrable initially deferred
for each row execute function private.assert_credit_card_statement_line();

create trigger credit_card_statement_lines_set_updated_at
before update on private.credit_card_statement_lines
for each row execute function public.set_updated_at();

create table private.credit_card_statement_payment_candidates (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  statement_id uuid not null,
  bank_transaction_id uuid not null,
  status text not null default 'suggested',
  score smallint,
  reason text not null,
  selected_at timestamptz,
  confirmed_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint statement_payment_candidates_statement_fkey
    foreign key (statement_id, user_id)
    references private.credit_card_statements (id, user_id)
    on delete cascade,
  constraint statement_payment_candidates_transaction_fkey
    foreign key (bank_transaction_id, user_id)
    references public.transactions (id, user_id)
    on delete restrict,
  constraint statement_payment_candidates_status_check
    check (status in ('suggested', 'selected', 'confirmed', 'dismissed')),
  constraint statement_payment_candidates_score_check
    check (score is null or score between 0 and 100),
  constraint statement_payment_candidates_reason_check
    check (char_length(btrim(reason)) between 1 and 500),
  constraint statement_payment_candidates_times_check check (
    (status in ('suggested', 'dismissed') and selected_at is null and confirmed_at is null)
    or (status = 'selected' and selected_at is not null and confirmed_at is null)
    or (status = 'confirmed' and selected_at is not null and confirmed_at is not null)
  ),
  constraint statement_payment_candidates_statement_transaction_key
    unique (statement_id, bank_transaction_id),
  constraint statement_payment_candidates_id_user_id_key unique (id, user_id)
);

create unique index statement_payment_candidates_one_choice_key
  on private.credit_card_statement_payment_candidates (statement_id)
  where status in ('selected', 'confirmed');

create unique index statement_payment_candidates_bank_choice_key
  on private.credit_card_statement_payment_candidates (bank_transaction_id)
  where status in ('selected', 'confirmed');

create index statement_payment_candidates_user_statement_idx
  on private.credit_card_statement_payment_candidates
  (user_id, statement_id, status, created_at);

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
  transaction_row record;
begin
  select candidate.* into candidate_row
  from private.credit_card_statement_payment_candidates candidate
  where candidate.id = checked_candidate_id and candidate.user_id = checked_user_id;
  if not found then return; end if;

  select statement.* into statement_row
  from private.credit_card_statements statement
  where statement.id = candidate_row.statement_id and statement.user_id = checked_user_id
  for update;

  select transaction_row.*, account.account_type,
    account.deleted_at as account_deleted_at
  into transaction_row
  from public.transactions transaction_row
  join public.accounts account
    on account.id = transaction_row.account_id and account.user_id = transaction_row.user_id
  where transaction_row.id = candidate_row.bank_transaction_id
    and transaction_row.user_id = checked_user_id
  for update of transaction_row;

  if transaction_row.transaction_kind is distinct from 'debit'
    or statement_row.amount_due_minor is null
    or statement_row.settlement_currency is null
    or statement_row.statement_date is null
    or statement_row.due_date is null
    or transaction_row.account_type is distinct from 'bank_account'
    or transaction_row.account_deleted_at is not null
    or transaction_row.original_amount_minor is distinct from statement_row.amount_due_minor
    or transaction_row.original_currency is distinct from statement_row.settlement_currency
    or (transaction_row.occurred_at at time zone 'UTC')::date
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

create or replace function private.assert_statement_payment_candidate()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if tg_op <> 'INSERT' then
    perform private.validate_statement_payment_candidate(old.id, old.user_id);
  end if;
  if tg_op <> 'DELETE' then
    perform private.validate_statement_payment_candidate(new.id, new.user_id);
  end if;
  if tg_op = 'DELETE' then return old; end if;
  return new;
end;
$$;

revoke execute on function private.validate_statement_payment_candidate(uuid, uuid)
  from public, anon, authenticated;
revoke execute on function private.assert_statement_payment_candidate()
  from public, anon, authenticated;

create constraint trigger statement_payment_candidates_assert_valid
after insert or update or delete on private.credit_card_statement_payment_candidates
deferrable initially deferred
for each row execute function private.assert_statement_payment_candidate();

create trigger statement_payment_candidates_set_updated_at
before update on private.credit_card_statement_payment_candidates
for each row execute function public.set_updated_at();

create table private.credit_card_statement_events (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  statement_id uuid not null,
  event_index integer not null,
  event_type text not null,
  actor_user_id uuid references auth.users(id) on delete cascade,
  from_status text,
  to_status text,
  details jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  constraint credit_card_statement_events_statement_fkey
    foreign key (statement_id, user_id)
    references private.credit_card_statements (id, user_id)
    on delete cascade,
  constraint credit_card_statement_events_index_check check (event_index > 0),
  constraint credit_card_statement_events_type_check check (
    event_type in (
      'imported', 'header_corrected', 'line_linked', 'line_transaction_created',
      'line_ignored', 'payment_candidates_found', 'payment_selected',
      'payment_confirmed', 'payoff_created', 'status_changed', 'voided'
    )
  ),
  constraint credit_card_statement_events_actor_check
    check (actor_user_id is null or actor_user_id = user_id),
  constraint credit_card_statement_events_from_status_check
    check (from_status is null or from_status in ('review', 'unpaid', 'paid', 'void')),
  constraint credit_card_statement_events_to_status_check
    check (to_status is null or to_status in ('review', 'unpaid', 'paid', 'void')),
  constraint credit_card_statement_events_details_check check (
    jsonb_typeof(details) = 'object' and octet_length(details::text) <= 65536
  ),
  constraint credit_card_statement_events_statement_index_key
    unique (statement_id, event_index),
  constraint credit_card_statement_events_id_user_id_key unique (id, user_id)
);

create index credit_card_statement_events_user_statement_idx
  on private.credit_card_statement_events
  (user_id, statement_id, event_index desc);

create or replace function private.protect_credit_card_statement_event()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if tg_op = 'UPDATE' or exists (
    select 1 from private.credit_card_statements statement
    where statement.id = old.statement_id and statement.user_id = old.user_id
  ) then
    raise exception using errcode = '23514',
      message = 'Credit Card bill events are immutable';
  end if;
  return old;
end;
$$;

revoke execute on function private.protect_credit_card_statement_event()
  from public, anon, authenticated;

create trigger credit_card_statement_events_immutable
before update or delete on private.credit_card_statement_events
for each row execute function private.protect_credit_card_statement_event();

alter table private.account_opening_balance_revisions enable row level security;
alter table private.account_opening_balance_revision_amounts enable row level security;
alter table private.transaction_calculation_treatments enable row level security;
alter table private.credit_card_statements enable row level security;
alter table private.credit_card_statement_lines enable row level security;
alter table private.credit_card_statement_payment_candidates enable row level security;
alter table private.credit_card_statement_events enable row level security;

revoke all privileges on table private.account_opening_balance_revisions from public, anon, authenticated;
revoke all privileges on table private.account_opening_balance_revision_amounts from public, anon, authenticated;
revoke all privileges on table private.transaction_calculation_treatments from public, anon, authenticated;
revoke all privileges on table private.credit_card_statements from public, anon, authenticated;
revoke all privileges on table private.credit_card_statement_lines from public, anon, authenticated;
revoke all privileges on table private.credit_card_statement_payment_candidates from public, anon, authenticated;
revoke all privileges on table private.credit_card_statement_events from public, anon, authenticated;

create policy "Owners are isolated on opening balance revisions"
on private.account_opening_balance_revisions for all
to authenticated
using ((select auth.uid()) = user_id)
with check ((select auth.uid()) = user_id);

create policy "Owners are isolated on opening balance amounts"
on private.account_opening_balance_revision_amounts for all
to authenticated
using ((select auth.uid()) = user_id)
with check ((select auth.uid()) = user_id);

create policy "Owners are isolated on transaction treatments"
on private.transaction_calculation_treatments for all
to authenticated
using ((select auth.uid()) = user_id)
with check ((select auth.uid()) = user_id);

create policy "Owners are isolated on Credit Card bills"
on private.credit_card_statements for all
to authenticated
using ((select auth.uid()) = user_id)
with check ((select auth.uid()) = user_id);

create policy "Owners are isolated on Credit Card bill lines"
on private.credit_card_statement_lines for all
to authenticated
using ((select auth.uid()) = user_id)
with check ((select auth.uid()) = user_id);

create policy "Owners are isolated on Credit Card payment candidates"
on private.credit_card_statement_payment_candidates for all
to authenticated
using ((select auth.uid()) = user_id)
with check ((select auth.uid()) = user_id);

create policy "Owners are isolated on Credit Card bill events"
on private.credit_card_statement_events for all
to authenticated
using ((select auth.uid()) = user_id)
with check ((select auth.uid()) = user_id);
