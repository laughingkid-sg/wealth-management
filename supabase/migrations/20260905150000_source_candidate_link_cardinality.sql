-- Transaction parsing revamp — per-candidate link cardinality.
--
-- One email now parses into several transactions, so one data_source links to
-- several transactions (one per candidate). The previous cardinality guard
-- capped a non-bulk source at two active links total, which is incompatible with
-- multi-transaction email. Unify the rule: every candidate scope (and legacy
-- null-scope links, grouped together) may have at most two active links, and two
-- only as the legs of one internal transfer. Bulk documents still require a
-- candidate scope on every link.

begin;

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
  checked_source_type text;
  affected_candidate record;
begin
  select source.source_type
  into checked_source_type
  from private.data_sources source
  where source.user_id = checked_user_id and source.id = checked_source_id
  for update;

  if not found then
    return;
  end if;

  -- Bulk document evidence must always be candidate-scoped.
  if checked_source_type = 'bulk_upload_document' and exists (
    select 1 from private.transaction_data_sources link
    where link.user_id = checked_user_id
      and link.data_source_id = checked_source_id
      and link.detached_at is null
      and link.bulk_import_candidate_id is null
  ) then
    raise exception using errcode = '23514',
      message = 'bulk document evidence requires a candidate scope';
  end if;

  -- Each candidate scope may have at most two active links, and two only as the
  -- legs of one internal transfer. Links without a candidate scope (legacy
  -- per-source manual links) are grouped together under the nil scope and held
  -- to the same rule.
  for affected_candidate in
    select coalesce(link.bulk_import_candidate_id, '00000000-0000-0000-0000-000000000000'::uuid) as scope,
      count(*)::integer as link_count,
      array_agg(link.transaction_id order by link.transaction_id) as transaction_ids
    from private.transaction_data_sources link
    where link.user_id = checked_user_id
      and link.data_source_id = checked_source_id
      and link.detached_at is null
    group by coalesce(link.bulk_import_candidate_id, '00000000-0000-0000-0000-000000000000'::uuid)
  loop
    if affected_candidate.link_count > 2 then
      raise exception using errcode = '23514',
        message = 'a source candidate may have at most two active transaction links';
    end if;
    if affected_candidate.link_count = 2 and not exists (
      select 1 from private.transaction_links transfer
      where transfer.user_id = checked_user_id
        and (
          (transfer.debit_transaction_id = affected_candidate.transaction_ids[1]
            and transfer.credit_transaction_id = affected_candidate.transaction_ids[2])
          or
          (transfer.debit_transaction_id = affected_candidate.transaction_ids[2]
            and transfer.credit_transaction_id = affected_candidate.transaction_ids[1])
        )
    ) then
      raise exception using errcode = '23514',
        message = 'two active source links must be the legs of one internal transfer';
    end if;
  end loop;
end;
$$;

revoke execute on function private.assert_source_active_links(uuid, uuid)
  from public, anon, authenticated;

commit;
