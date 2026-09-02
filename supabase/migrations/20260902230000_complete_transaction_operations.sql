-- Complete the safe operational projection used by the Transactions API and
-- worker without rewriting the already-deployed foundation migrations.

alter table public.transaction_sync_runs
  add column ingestion_completed_at timestamptz,
  add column sources_parsed_count integer not null default 0,
  add column sources_failed_count integer not null default 0,
  add constraint transaction_sync_runs_sources_parsed_count_check
    check (sources_parsed_count >= 0),
  add constraint transaction_sync_runs_sources_failed_count_check
    check (sources_failed_count >= 0);

create unique index transaction_sync_runs_one_active_per_user_idx
  on public.transaction_sync_runs (user_id)
  where status in ('queued', 'running');

create index transaction_jobs_expired_lease_idx
  on private.transaction_jobs (lease_expires_at, created_at)
  where status = 'running';

create index data_sources_user_status_keyset_idx
  on private.data_sources (user_id, parse_status, received_at desc, id desc);

create index transactions_user_keyset_idx
  on public.transactions (user_id, occurred_at desc, id desc);

-- Cover composite ownership foreign keys in their declared column order. The
-- nullable relationships use partial indexes because foreign-key checks never
-- need to find rows whose optional reference is null.
create index transactions_user_account_id_idx
  on public.transactions (user_id, account_id);

create index transaction_links_user_debit_transaction_id_idx
  on private.transaction_links (user_id, debit_transaction_id);

create index transaction_links_user_credit_transaction_id_idx
  on private.transaction_links (user_id, credit_transaction_id);

create index transaction_sync_runs_user_gmail_connection_id_idx
  on public.transaction_sync_runs (user_id, gmail_connection_id)
  where gmail_connection_id is not null;

create index data_sources_user_suggested_account_id_idx
  on private.data_sources (user_id, suggested_account_id)
  where suggested_account_id is not null;

alter table private.data_sources
  add column reconciliation_reason text,
  add column suggested_transaction_id uuid,
  add constraint data_sources_reconciliation_reason_check
    check (reconciliation_reason is null or char_length(reconciliation_reason) <= 1000),
  add constraint data_sources_user_suggested_transaction_fkey
    foreign key (user_id, suggested_transaction_id)
    references public.transactions (user_id, id)
    on delete restrict;

create index data_sources_user_suggested_transaction_id_idx
  on private.data_sources (user_id, suggested_transaction_id)
  where suggested_transaction_id is not null;

-- Revalidate internal transfers at commit so both legs may be inserted in one
-- transaction while ownership, kind, and active distinct-account invariants
-- are checked against the final state. Keep the validation in a regular
-- function so changes to either the link or either linked transaction can run
-- the same checks.
create or replace function private.validate_transaction_link(
  checked_user_id uuid,
  checked_link_id uuid,
  checked_debit_transaction_id uuid,
  checked_credit_transaction_id uuid
)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  debit_kind text;
  credit_kind text;
  debit_account_id uuid;
  credit_account_id uuid;
begin
  -- Lock linked transactions in a stable order. This serializes concurrent
  -- attempts to reuse either transaction in another transfer.
  perform 1
  from public.transactions transaction_row
  where transaction_row.user_id = checked_user_id
    and transaction_row.id in (checked_debit_transaction_id, checked_credit_transaction_id)
  order by transaction_row.id
  for update;

  select transaction_kind, account_id into debit_kind, debit_account_id
  from public.transactions
  where id = checked_debit_transaction_id and user_id = checked_user_id;

  select transaction_kind, account_id into credit_kind, credit_account_id
  from public.transactions
  where id = checked_credit_transaction_id and user_id = checked_user_id;

  if debit_kind is distinct from 'debit' or credit_kind is distinct from 'credit' then
    raise exception using
      errcode = '23514',
      message = 'an internal transfer link requires one debit and one credit transaction';
  end if;

  if debit_account_id is not distinct from credit_account_id then
    raise exception using
      errcode = '23514',
      message = 'an internal transfer link requires two distinct accounts';
  end if;

  -- FOR SHARE conflicts with soft-delete updates while the final active-state
  -- check runs, without taking stronger write locks on the account rows.
  perform 1
  from public.accounts account
  where account.user_id = checked_user_id
    and account.id in (debit_account_id, credit_account_id)
  order by account.id
  for share;

  if (
    select count(*)
    from public.accounts account
    where account.user_id = checked_user_id
      and account.id in (debit_account_id, credit_account_id)
      and account.deleted_at is null
  ) <> 2 then
    raise exception using
      errcode = '23514',
      message = 'an internal transfer link requires two active owned accounts';
  end if;

  if exists (
    select 1
    from private.transaction_links existing_link
    where existing_link.user_id = checked_user_id
      and existing_link.id <> checked_link_id
      and (
        checked_debit_transaction_id in (existing_link.debit_transaction_id, existing_link.credit_transaction_id)
        or checked_credit_transaction_id in (existing_link.debit_transaction_id, existing_link.credit_transaction_id)
      )
  ) then
    raise exception using
      errcode = '23505',
      message = 'a transaction can belong to only one internal transfer link';
  end if;

end;
$$;

create or replace function private.assert_transaction_link_integrity()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  perform private.validate_transaction_link(
    new.user_id,
    new.id,
    new.debit_transaction_id,
    new.credit_transaction_id
  );
  return new;
end;
$$;

create or replace function private.assert_linked_transaction_integrity()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  affected record;
begin
  for affected in
    select transfer.id, transfer.user_id,
      transfer.debit_transaction_id, transfer.credit_transaction_id
    from private.transaction_links transfer
    where transfer.user_id = new.user_id
      and new.id in (transfer.debit_transaction_id, transfer.credit_transaction_id)
    order by transfer.id
  loop
    perform private.validate_transaction_link(
      affected.user_id,
      affected.id,
      affected.debit_transaction_id,
      affected.credit_transaction_id
    );
  end loop;
  return new;
end;
$$;

revoke execute on function private.validate_transaction_link(uuid, uuid, uuid, uuid) from public, anon, authenticated;
revoke execute on function private.assert_transaction_link_integrity() from public, anon, authenticated;
revoke execute on function private.assert_linked_transaction_integrity() from public, anon, authenticated;

create constraint trigger transactions_assert_transfer_link_integrity
after update on public.transactions
deferrable initially deferred
for each row
when (
  old.account_id is distinct from new.account_id
  or old.transaction_kind is distinct from new.transaction_kind
)
execute function private.assert_linked_transaction_integrity();

-- Serialize all active-link changes for one source on the source row itself.
-- This closes the write-skew window where two concurrent transactions could
-- each observe zero existing evidence links and both commit.
create or replace function private.assert_source_active_links(
  checked_user_id uuid,
  checked_source_id uuid
)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  active_count integer;
  active_transactions uuid[];
begin
  perform 1
  from private.data_sources source
  where source.user_id = checked_user_id
    and source.id = checked_source_id
  for update;

  select count(*)::integer, array_agg(link.transaction_id order by link.transaction_id)
  into active_count, active_transactions
  from private.transaction_data_sources link
  where link.user_id = checked_user_id
    and link.data_source_id = checked_source_id
    and link.detached_at is null;

  if active_count > 2 then
    raise exception using
      errcode = '23514',
      message = 'a source may have at most two active transaction links';
  end if;

  if active_count = 2 and not exists (
    select 1
    from private.transaction_links transfer
    where transfer.user_id = checked_user_id
      and (
        (transfer.debit_transaction_id = active_transactions[1]
          and transfer.credit_transaction_id = active_transactions[2])
        or
        (transfer.debit_transaction_id = active_transactions[2]
          and transfer.credit_transaction_id = active_transactions[1])
      )
  ) then
    raise exception using
      errcode = '23514',
      message = 'two active source links must be the legs of one internal transfer';
  end if;
end;
$$;

create or replace function private.assert_transaction_data_source_cardinality()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  old_user_id uuid;
  old_source_id uuid;
  new_user_id uuid;
  new_source_id uuid;
  affected record;
begin
  if tg_op <> 'INSERT' then
    old_user_id := old.user_id;
    old_source_id := old.data_source_id;
  end if;
  if tg_op <> 'DELETE' then
    new_user_id := new.user_id;
    new_source_id := new.data_source_id;
  end if;

  for affected in
    select distinct value.user_id, value.source_id
    from (values
      (old_user_id, old_source_id),
      (new_user_id, new_source_id)
    ) as value(user_id, source_id)
    where value.user_id is not null and value.source_id is not null
    order by value.user_id, value.source_id
  loop
    perform private.assert_source_active_links(affected.user_id, affected.source_id);
  end loop;

  if tg_op = 'DELETE' then
    return old;
  end if;
  return new;
end;
$$;

create or replace function private.assert_transaction_link_source_cardinality()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  old_user_id uuid;
  old_debit_id uuid;
  old_credit_id uuid;
  new_user_id uuid;
  new_debit_id uuid;
  new_credit_id uuid;
  affected record;
begin
  if tg_op <> 'INSERT' then
    old_user_id := old.user_id;
    old_debit_id := old.debit_transaction_id;
    old_credit_id := old.credit_transaction_id;
  end if;
  if tg_op <> 'DELETE' then
    new_user_id := new.user_id;
    new_debit_id := new.debit_transaction_id;
    new_credit_id := new.credit_transaction_id;
  end if;

  for affected in
    select distinct evidence.user_id, evidence.data_source_id
    from private.transaction_data_sources evidence
    join (values
      (old_user_id, old_debit_id),
      (old_user_id, old_credit_id),
      (new_user_id, new_debit_id),
      (new_user_id, new_credit_id)
    ) as transaction_value(user_id, transaction_id)
      on transaction_value.user_id = evidence.user_id
      and transaction_value.transaction_id = evidence.transaction_id
    where evidence.detached_at is null
    order by evidence.user_id, evidence.data_source_id
  loop
    perform private.assert_source_active_links(affected.user_id, affected.data_source_id);
  end loop;

  if tg_op = 'DELETE' then
    return old;
  end if;
  return new;
end;
$$;

revoke execute on function private.assert_source_active_links(uuid, uuid) from public, anon, authenticated;
revoke execute on function private.assert_transaction_data_source_cardinality() from public, anon, authenticated;
revoke execute on function private.assert_transaction_link_source_cardinality() from public, anon, authenticated;

-- Refuse to install the invariant over incompatible pre-existing rows rather
-- than silently grandfathering an unsafe state.
do $$
declare
  affected record;
begin
  if exists (
    select 1
    from private.transaction_links transfer
    left join public.transactions debit
      on debit.user_id = transfer.user_id
      and debit.id = transfer.debit_transaction_id
    left join public.transactions credit
      on credit.user_id = transfer.user_id
      and credit.id = transfer.credit_transaction_id
    left join public.accounts debit_account
      on debit_account.user_id = debit.user_id
      and debit_account.id = debit.account_id
    left join public.accounts credit_account
      on credit_account.user_id = credit.user_id
      and credit_account.id = credit.account_id
    where debit.id is null
      or credit.id is null
      or debit_account.id is null
      or credit_account.id is null
      or debit.transaction_kind is distinct from 'debit'
      or credit.transaction_kind is distinct from 'credit'
      or debit.account_id is not distinct from credit.account_id
      or debit_account.deleted_at is not null
      or credit_account.deleted_at is not null
  ) then
    raise exception 'existing internal transfer links violate kind or active-account invariants';
  end if;

  for affected in
    select link.user_id, link.data_source_id
    from private.transaction_data_sources link
    where link.detached_at is null
    group by link.user_id, link.data_source_id
    having count(*) > 1
    order by link.user_id, link.data_source_id
  loop
    perform private.assert_source_active_links(affected.user_id, affected.data_source_id);
  end loop;
end;
$$;

create constraint trigger transaction_data_sources_assert_cardinality
after insert or update or delete on private.transaction_data_sources
deferrable initially deferred
for each row execute function private.assert_transaction_data_source_cardinality();

create constraint trigger transaction_links_assert_source_cardinality
after insert or update or delete on private.transaction_links
deferrable initially deferred
for each row execute function private.assert_transaction_link_source_cardinality();

-- Realtime publication membership is separate from RLS. The owner-scoped
-- SELECT policy on transaction_sync_runs remains the authorization boundary.
do $$
begin
  if exists (select 1 from pg_publication where pubname = 'supabase_realtime')
    and not exists (
      select 1
      from pg_publication_tables
      where pubname = 'supabase_realtime'
        and schemaname = 'public'
        and tablename = 'transaction_sync_runs'
    ) then
    execute 'alter publication supabase_realtime add table public.transaction_sync_runs';
  end if;
end;
$$;

-- A deliberately narrow first-party rule. It activates only for OCBC mail
-- that explicitly describes an SGD debit/card purchase. Invalid or unmatched
-- rule configuration is ignored safely by the worker.
insert into private.source_parser_rules (
  provider,
  sender_matcher,
  content_matcher,
  extraction_config,
  version,
  priority,
  active
)
select
  'gmail',
  '(?i)@(?:[a-z0-9-]+\.)*ocbc\.com(?:\.sg)?(?:>|$)',
  '(?is)\b(?:charged|purchase|spent|debited)\b.*\bSGD\b',
  jsonb_build_object(
    'constants', jsonb_build_object(
      'transaction_kind', 'debit',
      'original_currency', 'SGD'
    ),
    'extractors', jsonb_build_object(
      'card_last_four', jsonb_build_object(
        'pattern', '(?i)(?:card|ending|ends in)[^0-9]{0,16}([0-9]{4})\b',
        'group', 1
      )
    )
  ),
  1,
  100,
  true
where not exists (
  select 1
  from private.source_parser_rules
  where provider = 'gmail'
    and sender_matcher = '(?i)@(?:[a-z0-9-]+\.)*ocbc\.com(?:\.sg)?(?:>|$)'
    and content_matcher = '(?is)\b(?:charged|purchase|spent|debited)\b.*\bSGD\b'
    and version = 1
);
