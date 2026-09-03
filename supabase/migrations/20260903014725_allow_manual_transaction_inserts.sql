-- Browser clients may create only confirmed manual transactions. Canonical
-- updates, deletion, source reconciliation, and internal transfers remain Go
-- API responsibilities.

create or replace function private.transaction_line_items_v1_are_valid(value jsonb)
returns boolean
language plpgsql
immutable
set search_path = ''
as $$
declare
  item jsonb;
  amount_key text;
  amount_value jsonb;
  amount_type text;
  decimal_value text;
  description_value text;
  quantity_text text;
begin
  if value is null or pg_catalog.jsonb_typeof(value) is distinct from 'array' then
    return false;
  end if;
  -- The count bound alone does not constrain arbitrarily large nested details.
  if pg_catalog.octet_length(value::text) > 262144 then
    return false;
  end if;
  if pg_catalog.jsonb_array_length(value) > 100 then
    return false;
  end if;

  for item in
    select entry.value
    from pg_catalog.jsonb_array_elements(value) as entry(value)
  loop
    if pg_catalog.jsonb_typeof(item) is distinct from 'object' then
      return false;
    end if;

    if exists (
      select 1
      from pg_catalog.jsonb_object_keys(item) as item_key(key_name)
      where item_key.key_name not in (
        'schema_version',
        'description',
        'quantity',
        'unit_price_minor',
        'line_total_minor',
        'tax_minor',
        'discount_minor',
        'currency',
        'details'
      )
    ) then
      return false;
    end if;

    if not (item ?& array[
      'schema_version',
      'description',
      'quantity',
      'currency',
      'details'
    ]::text[]) then
      return false;
    end if;

    if pg_catalog.jsonb_typeof(item -> 'schema_version') is distinct from 'number'
      or item ->> 'schema_version' <> '1' then
      return false;
    end if;

    if pg_catalog.jsonb_typeof(item -> 'description') is distinct from 'string' then
      return false;
    end if;
    description_value := pg_catalog.regexp_replace(
      pg_catalog.regexp_replace(item ->> 'description', '^[[:space:]]+', ''),
      '[[:space:]]+$',
      ''
    );
    if pg_catalog.char_length(description_value) not between 1 and 250 then
      return false;
    end if;

    if pg_catalog.jsonb_typeof(item -> 'quantity') is distinct from 'number' then
      return false;
    end if;
    quantity_text := item ->> 'quantity';
    if pg_catalog.octet_length(quantity_text) > 19 then
      return false;
    end if;
    if quantity_text !~ '^[0-9]+$' then
      return false;
    end if;
    if quantity_text::pg_catalog.numeric < 1
      or quantity_text::pg_catalog.numeric > 9223372036854775807::pg_catalog.numeric then
      return false;
    end if;

    if pg_catalog.jsonb_typeof(item -> 'currency') is distinct from 'string'
      or item ->> 'currency' !~ '^[A-Z]{3}$' then
      return false;
    end if;

    foreach amount_key in array array[
      'unit_price_minor',
      'line_total_minor',
      'tax_minor',
      'discount_minor'
    ]::text[]
    loop
      if item ? amount_key then
        amount_value := item -> amount_key;
        amount_type := pg_catalog.jsonb_typeof(amount_value);

        if amount_type = 'null' then
          -- Optional Go pointer fields may be encoded as JSON null.
          decimal_value := null;
        elsif amount_type = 'number' then
          decimal_value := amount_value::text;
        elsif amount_type = 'string' then
          -- Data REST clients serialize bigint-safe minor units as strings.
          decimal_value := amount_value #>> '{}';
        else
          return false;
        end if;

        if decimal_value is not null then
          if decimal_value !~ '^[0-9]+$' then
            return false;
          end if;

          -- Ignore representational leading zeroes, then check length before
          -- numeric conversion so oversized values never reach a cast.
          decimal_value := pg_catalog.ltrim(decimal_value, '0');
          if decimal_value <> '' then
            if pg_catalog.octet_length(decimal_value) > 19 then
              return false;
            end if;
            if decimal_value::pg_catalog.numeric > 9223372036854775807::pg_catalog.numeric then
              return false;
            end if;
          end if;
        end if;
      end if;
    end loop;

    if pg_catalog.jsonb_typeof(item -> 'details') is distinct from 'object' then
      return false;
    end if;
  end loop;

  return true;
end;
$$;

create or replace function private.transaction_details_are_valid(value jsonb)
returns boolean
language plpgsql
immutable
set search_path = ''
as $$
begin
  if value is null or pg_catalog.jsonb_typeof(value) is distinct from 'object' then
    return false;
  end if;
  if pg_catalog.octet_length(value::text) > 16384 then
    return false;
  end if;
  if value ? 'user_notes'
    and (
      pg_catalog.jsonb_typeof(value -> 'user_notes') is distinct from 'string'
      or pg_catalog.char_length(value ->> 'user_notes') > 4000
    ) then
    return false;
  end if;

  return true;
end;
$$;

revoke execute on function private.transaction_line_items_v1_are_valid(jsonb)
  from public, anon, authenticated;
revoke execute on function private.transaction_details_are_valid(jsonb)
  from public, anon, authenticated;

-- CHECK expressions invoke their functions with the DML caller's EXECUTE
-- privileges. Authenticated/service roles need these narrow validators, but
-- authenticated still has no USAGE on the non-exposed private schema and
-- therefore cannot invoke them as API functions.
grant execute on function private.transaction_line_items_v1_are_valid(jsonb)
  to authenticated, service_role;
grant execute on function private.transaction_details_are_valid(jsonb)
  to authenticated, service_role;

alter table public.transactions
  add constraint transactions_line_items_v1_check
    check (private.transaction_line_items_v1_are_valid(line_items)) not valid,
  add constraint transactions_details_safe_check
    check (private.transaction_details_are_valid(details)) not valid;

alter table public.transactions
  validate constraint transactions_line_items_v1_check;

alter table public.transactions
  validate constraint transactions_details_safe_check;

create or replace function private.assert_transaction_category_active()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if new.category_id is not null
    and not exists (
      select 1
      from public.transaction_categories category
      where category.id = new.category_id
        and category.active
    ) then
    raise exception using
      errcode = '23514',
      message = 'transaction category must be active';
  end if;

  return new;
end;
$$;

revoke execute on function private.assert_transaction_category_active()
  from public, anon, authenticated;

create trigger transactions_assert_active_category
before insert or update of category_id on public.transactions
for each row execute function private.assert_transaction_category_active();

-- Remove any table-wide or prior column-level INSERT reach before granting the
-- exact Data REST create surface. Defaults/provenance and immutable fields are
-- intentionally omitted.
revoke insert on table public.transactions from public, anon, authenticated;
revoke insert (
  id,
  creation_method,
  match_confidence,
  user_modified_at,
  created_at,
  updated_at
) on table public.transactions from public, anon, authenticated;

grant insert (
  user_id,
  account_id,
  transaction_kind,
  title,
  merchant_name,
  original_amount_minor,
  original_currency,
  sgd_amount_minor,
  occurred_at,
  category_id,
  line_items,
  details,
  review_status
) on table public.transactions to authenticated;

create policy "Users can insert confirmed manual transactions"
on public.transactions for insert
to authenticated
with check (
  (select auth.uid()) is not null
  and (select auth.uid()) = user_id
  and creation_method = 'manual'
  and review_status = 'confirmed'
  and match_confidence is null
  and user_modified_at is null
  and (details - 'user_notes') = '{}'::jsonb
);
