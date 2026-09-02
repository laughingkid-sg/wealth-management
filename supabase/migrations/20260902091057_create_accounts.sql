create table public.accounts (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  side text not null,
  account_type text not null,
  name text not null,
  institution_name text not null,
  account_identifier text,
  notes text,
  metadata jsonb not null default '{}'::jsonb,
  sort_order integer not null default 0,
  deleted_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint accounts_side_check check (side in ('asset', 'liability')),
  constraint accounts_type_side_check check (
    (side = 'asset' and account_type in ('bank_account', 'brokerage', 'crypto_wallet', 'crypto_exchange', 'rsu'))
    or
    (side = 'liability' and account_type in ('credit_card', 'personal_loan'))
  ),
  constraint accounts_name_length_check check (char_length(btrim(name)) between 1 and 100),
  constraint accounts_institution_length_check check (char_length(btrim(institution_name)) between 1 and 100),
  constraint accounts_identifier_length_check check (account_identifier is null or char_length(account_identifier) <= 100),
  constraint accounts_notes_length_check check (notes is null or char_length(notes) <= 500),
  constraint accounts_metadata_object_check check (jsonb_typeof(metadata) = 'object')
);

comment on table public.accounts is 'User-owned account directory records. Financial balances and transactions are intentionally out of scope.';

create index accounts_user_active_sort_idx
  on public.accounts (user_id, sort_order, created_at)
  where deleted_at is null;

create index accounts_user_active_side_idx
  on public.accounts (user_id, side)
  where deleted_at is null;

create or replace function public.set_updated_at()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  new.updated_at = now();
  return new;
end;
$$;

revoke execute on function public.set_updated_at() from public;

create trigger accounts_set_updated_at
before update on public.accounts
for each row execute function public.set_updated_at();

alter table public.accounts enable row level security;

revoke all privileges on table public.accounts from public, anon, authenticated;
grant select, insert, update on table public.accounts to authenticated;

create policy "Users can select their own accounts"
on public.accounts for select
to authenticated
using ((select auth.uid()) = user_id);

create policy "Users can insert their own accounts"
on public.accounts for insert
to authenticated
with check ((select auth.uid()) = user_id);

create policy "Users can update their own accounts"
on public.accounts for update
to authenticated
using ((select auth.uid()) = user_id)
with check ((select auth.uid()) = user_id);
