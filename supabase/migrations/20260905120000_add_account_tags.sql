-- Add visible, free-form tags to accounts. Tags are distinct from the
-- key/value `metadata` object: they are rendered directly on the account row,
-- not hidden behind the details toggle.

create or replace function public.account_tags_are_valid(tags text[])
returns boolean
language sql
immutable
set search_path = ''
as $$
  select coalesce(
    bool_and(tag is not null and char_length(btrim(tag)) between 1 and 40),
    true
  )
  from unnest(tags) as tag;
$$;

revoke execute on function public.account_tags_are_valid(text[]) from public;

alter table public.accounts
  add column tags text[] not null default '{}'::text[];

alter table public.accounts
  add constraint accounts_tags_count_check check (cardinality(tags) <= 20);

alter table public.accounts
  add constraint accounts_tags_values_check check (public.account_tags_are_valid(tags));

comment on column public.accounts.tags is 'User-facing labels shown directly on the account row. Safe descriptive text only.';
