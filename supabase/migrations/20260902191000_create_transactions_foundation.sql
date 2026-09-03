-- Transactions use canonical, account-owned records in the exposed schema.
-- Provider payloads, OAuth material, durable jobs, and evidence links stay in
-- the non-exposed private schema and are available only to server-side code.

create schema if not exists private;
revoke all on schema private from public, anon, authenticated;

-- The composite key lets dependent rows enforce that an account belongs to the
-- same user without relying on application-only checks.
alter table public.accounts
  add constraint accounts_id_user_id_key unique (id, user_id);

create table public.transaction_categories (
  id uuid primary key default gen_random_uuid(),
  parent_name text not null,
  name text not null,
  emoji text not null,
  sort_order integer not null,
  active boolean not null default true,
  created_at timestamptz not null default now(),
  constraint transaction_categories_parent_name_check
    check (char_length(btrim(parent_name)) between 1 and 100),
  constraint transaction_categories_name_check
    check (char_length(btrim(name)) between 1 and 100),
  constraint transaction_categories_emoji_check
    check (char_length(btrim(emoji)) between 1 and 16),
  constraint transaction_categories_sort_order_check check (sort_order >= 0),
  constraint transaction_categories_parent_name_name_key unique (parent_name, name),
  constraint transaction_categories_sort_order_key unique (sort_order)
);

comment on table public.transaction_categories is
  'System-managed global transaction category catalogue.';

insert into public.transaction_categories (parent_name, name, emoji, sort_order)
values
  ('Income', 'Paychecks', '💵', 10),
  ('Income', 'Interest', '💸', 20),
  ('Income', 'Business Income', '💰', 30),
  ('Income', 'Other Income', '💰', 40),
  ('Gifts & Donations', 'Charity', '🎗', 50),
  ('Gifts & Donations', 'Gifts', '🎁', 60),
  ('Auto & Transport', 'Auto Payment', '🚗', 70),
  ('Auto & Transport', 'Public Transit', '🚃', 80),
  ('Auto & Transport', 'Gas', '⛽', 90),
  ('Auto & Transport', 'Auto Maintenance', '🔧', 100),
  ('Auto & Transport', 'Parking & Tolls', '🏢', 110),
  ('Auto & Transport', 'Taxi & Ride Shares', '🚕', 120),
  ('Housing', 'Mortgage', '🏠', 130),
  ('Housing', 'Rent', '🏠', 140),
  ('Housing', 'Home Improvement', '🔨', 150),
  ('Bills & Utilities', 'Garbage', '🗑', 160),
  ('Bills & Utilities', 'Water', '💧', 170),
  ('Bills & Utilities', 'Gas & Electric', '⚡', 180),
  ('Bills & Utilities', 'Internet & Cable', '🌐', 190),
  ('Bills & Utilities', 'Phone', '📱', 200),
  ('Food & Dining', 'Groceries', '🍏', 210),
  ('Food & Dining', 'Restaurants & Bars', '🍽', 220),
  ('Food & Dining', 'Coffee Shops', '☕', 230),
  ('Travel & Lifestyle', 'Travel & Vacation', '🏝', 240),
  ('Travel & Lifestyle', 'Entertainment & Recreation', '🎥', 250),
  ('Travel & Lifestyle', 'Personal', '👑', 260),
  ('Travel & Lifestyle', 'Pets', '🐶', 270),
  ('Travel & Lifestyle', 'Fun Money', '😜', 280),
  ('Shopping', 'Shopping', '🛍', 290),
  ('Shopping', 'Clothing', '👕', 300),
  ('Shopping', 'Furniture & Housewares', '🪑', 310),
  ('Shopping', 'Electronics', '🖥', 320),
  ('Children', 'Child Care', '👶', 330),
  ('Children', 'Child Activities', '⚽', 340),
  ('Education', 'Student Loans', '🎓', 350),
  ('Education', 'Education', '🏫', 360),
  ('Health & Wellness', 'Medical', '💊', 370),
  ('Health & Wellness', 'Dentist', '🦷', 380),
  ('Health & Wellness', 'Fitness', '💪', 390),
  ('Financial', 'Loan Repayment', '💰', 400),
  ('Financial', 'Financial & Legal Services', '🗄', 410),
  ('Financial', 'Financial Fees', '🏦', 420),
  ('Financial', 'Cash & ATM', '🏧', 430),
  ('Financial', 'Insurance', '☂️', 440),
  ('Financial', 'Taxes', '🏛️', 450),
  ('Other', 'Uncategorized', '❓', 460),
  ('Other', 'Check', '💸', 470),
  ('Other', 'Miscellaneous', '💲', 480),
  ('Business', 'Advertising & Promotion', '📣', 490),
  ('Business', 'Business Utilities & Communication', '📞', 500),
  ('Business', 'Employee Wages & Contract Labor', '💵', 510),
  ('Business', 'Business Travel & Meals', '🍴', 520),
  ('Business', 'Business Auto Expenses', '🚖', 530),
  ('Business', 'Business Insurance', '📁', 540),
  ('Business', 'Office Supplies & Expenses', '📎', 550),
  ('Business', 'Office Rent', '🏢', 560),
  ('Business', 'Postage & Shipping', '📦', 570),
  ('Transfers', 'Transfer', '🔁', 580),
  ('Transfers', 'Credit Card Payment', '💳', 590),
  ('Transfers', 'Balance Adjustments', '⚖️', 600);

create table private.source_parser_rules (
  id uuid primary key default gen_random_uuid(),
  provider text not null,
  sender_matcher text,
  content_matcher text,
  extraction_config jsonb not null default '{}'::jsonb,
  version integer not null default 1,
  priority integer not null default 0,
  active boolean not null default true,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint source_parser_rules_provider_check
    check (char_length(btrim(provider)) between 1 and 100),
  constraint source_parser_rules_sender_matcher_check
    check (sender_matcher is null or char_length(btrim(sender_matcher)) between 1 and 500),
  constraint source_parser_rules_content_matcher_check
    check (content_matcher is null or char_length(btrim(content_matcher)) between 1 and 1000),
  constraint source_parser_rules_extraction_config_object_check
    check (jsonb_typeof(extraction_config) = 'object'),
  constraint source_parser_rules_version_check check (version > 0)
);

create table private.gmail_connections (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  provider text not null default 'gmail',
  encrypted_refresh_token bytea not null,
  token_metadata jsonb not null default '{}'::jsonb,
  selected_label text not null default 'odin-finance',
  sync_cursor text,
  status text not null default 'active',
  last_synced_at timestamptz,
  last_error text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint gmail_connections_provider_check check (provider = 'gmail'),
  constraint gmail_connections_encrypted_token_check check (octet_length(encrypted_refresh_token) > 0),
  constraint gmail_connections_token_metadata_object_check
    check (jsonb_typeof(token_metadata) = 'object'),
  constraint gmail_connections_selected_label_check
    check (char_length(btrim(selected_label)) between 1 and 225),
  constraint gmail_connections_status_check
    check (status in ('active', 'revoked', 'error', 'disconnected')),
  constraint gmail_connections_user_provider_key unique (user_id, provider),
  constraint gmail_connections_id_user_id_key unique (id, user_id)
);

create table private.data_sources (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  source_type text not null,
  provider text not null,
  provider_message_id text,
  provider_thread_id text,
  received_at timestamptz not null,
  ingested_at timestamptz not null default now(),
  raw_data jsonb not null,
  parser_rule_id uuid references private.source_parser_rules(id) on delete restrict,
  parser_rule_version integer,
  parse_status text not null default 'pending',
  parse_confidence smallint,
  parse_error text,
  suggested_account_id uuid,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint data_sources_source_type_check
    check (source_type in ('gmail_email', 'phone_notification')),
  constraint data_sources_provider_check
    check (char_length(btrim(provider)) between 1 and 100),
  constraint data_sources_raw_data_object_check check (jsonb_typeof(raw_data) = 'object'),
  constraint data_sources_parser_rule_version_check
    check (parser_rule_version is null or parser_rule_version > 0),
  constraint data_sources_parse_status_check
    check (parse_status in ('pending', 'parsing', 'parsed', 'review_required', 'dangling', 'failed')),
  constraint data_sources_parse_confidence_check
    check (parse_confidence is null or parse_confidence between 0 and 100),
  constraint data_sources_parse_error_check
    check (parse_error is null or char_length(parse_error) <= 2000),
  constraint data_sources_user_suggested_account_fkey
    foreign key (user_id, suggested_account_id)
    references public.accounts (user_id, id)
    on delete restrict,
  constraint data_sources_id_user_id_key unique (id, user_id)
);

comment on table private.data_sources is
  'Immutable user-owned raw provider inputs, including Gmail payloads and future phone notifications.';

create table public.transactions (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  account_id uuid not null,
  transaction_kind text not null,
  title text not null,
  merchant_name text,
  original_amount_minor bigint not null,
  original_currency text not null,
  sgd_amount_minor bigint,
  occurred_at timestamptz not null,
  category_id uuid references public.transaction_categories(id) on delete restrict,
  line_items jsonb not null default '[]'::jsonb,
  details jsonb not null default '{}'::jsonb,
  review_status text not null default 'pending',
  match_confidence smallint,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint transactions_user_account_fkey
    foreign key (user_id, account_id)
    references public.accounts (user_id, id)
    on delete restrict,
  constraint transactions_transaction_kind_check
    check (transaction_kind in ('debit', 'credit')),
  constraint transactions_title_check check (char_length(btrim(title)) between 1 and 250),
  constraint transactions_merchant_name_check
    check (merchant_name is null or char_length(btrim(merchant_name)) between 1 and 250),
  constraint transactions_original_amount_check check (original_amount_minor > 0),
  constraint transactions_original_currency_check
    check (original_currency ~ '^[A-Z]{3}$'),
  constraint transactions_sgd_amount_check
    check (sgd_amount_minor is null or sgd_amount_minor > 0),
  constraint transactions_line_items_array_check check (jsonb_typeof(line_items) = 'array'),
  constraint transactions_details_object_check check (jsonb_typeof(details) = 'object'),
  constraint transactions_review_status_check
    check (review_status in ('pending', 'review_required', 'confirmed')),
  constraint transactions_match_confidence_check
    check (match_confidence is null or match_confidence between 0 and 100),
  constraint transactions_id_user_id_key unique (id, user_id)
);

comment on table public.transactions is
  'Canonical user-owned debit and credit transactions. Evidence lives in private transaction_data_sources.';

create table private.transaction_links (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  link_type text not null default 'internal_transfer',
  debit_transaction_id uuid not null,
  credit_transaction_id uuid not null,
  details jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint transaction_links_user_debit_transaction_fkey
    foreign key (user_id, debit_transaction_id)
    references public.transactions (user_id, id)
    on delete restrict,
  constraint transaction_links_user_credit_transaction_fkey
    foreign key (user_id, credit_transaction_id)
    references public.transactions (user_id, id)
    on delete restrict,
  constraint transaction_links_link_type_check check (link_type = 'internal_transfer'),
  constraint transaction_links_different_transactions_check
    check (debit_transaction_id <> credit_transaction_id),
  constraint transaction_links_details_object_check check (jsonb_typeof(details) = 'object'),
  constraint transaction_links_debit_transaction_key unique (debit_transaction_id),
  constraint transaction_links_credit_transaction_key unique (credit_transaction_id)
);

create table private.transaction_data_sources (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  transaction_id uuid not null,
  data_source_id uuid not null,
  role text not null default 'other',
  match_confidence smallint,
  matched_by text not null,
  attached_at timestamptz not null default now(),
  detached_at timestamptz,
  detached_by_user boolean not null default false,
  created_at timestamptz not null default now(),
  constraint transaction_data_sources_user_transaction_fkey
    foreign key (user_id, transaction_id)
    references public.transactions (user_id, id)
    on delete restrict,
  constraint transaction_data_sources_user_data_source_fkey
    foreign key (user_id, data_source_id)
    references private.data_sources (user_id, id)
    on delete restrict,
  constraint transaction_data_sources_role_check
    check (role in ('primary', 'bank_alert', 'payment_provider', 'merchant_receipt', 'other')),
  constraint transaction_data_sources_match_confidence_check
    check (match_confidence is null or match_confidence between 0 and 100),
  constraint transaction_data_sources_matched_by_check
    check (matched_by in ('automatic', 'user', 'manual_review')),
  constraint transaction_data_sources_detached_at_check
    check ((detached_at is null and detached_by_user = false)
      or (detached_at is not null and detached_by_user = true))
);

create table public.transaction_sync_runs (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  gmail_connection_id uuid,
  status text not null default 'queued',
  started_at timestamptz,
  completed_at timestamptz,
  messages_found_count integer not null default 0,
  sources_saved_count integer not null default 0,
  transactions_created_count integer not null default 0,
  sources_linked_count integer not null default 0,
  dangling_sources_count integer not null default 0,
  review_required_count integer not null default 0,
  error_summary text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint transaction_sync_runs_user_connection_fkey
    foreign key (user_id, gmail_connection_id)
    references private.gmail_connections (user_id, id)
    on delete restrict,
  constraint transaction_sync_runs_status_check
    check (status in ('queued', 'running', 'completed', 'failed', 'cancelled')),
  constraint transaction_sync_runs_completed_at_check
    check ((status in ('completed', 'failed', 'cancelled') and completed_at is not null)
      or (status in ('queued', 'running') and completed_at is null)),
  constraint transaction_sync_runs_counts_check
    check (messages_found_count >= 0
      and sources_saved_count >= 0
      and transactions_created_count >= 0
      and sources_linked_count >= 0
      and dangling_sources_count >= 0
      and review_required_count >= 0),
  constraint transaction_sync_runs_error_summary_check
    check (error_summary is null or char_length(error_summary) <= 1000),
  constraint transaction_sync_runs_id_user_id_key unique (id, user_id)
);

create table private.source_parse_attempts (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  data_source_id uuid not null,
  parser_rule_id uuid references private.source_parser_rules(id) on delete restrict,
  parser_rule_version integer,
  model_name text,
  request_metadata jsonb not null default '{}'::jsonb,
  parsed_candidate jsonb,
  validation_status text not null default 'pending',
  error_summary text,
  started_at timestamptz,
  completed_at timestamptz,
  created_at timestamptz not null default now(),
  constraint source_parse_attempts_user_data_source_fkey
    foreign key (user_id, data_source_id)
    references private.data_sources (user_id, id)
    on delete restrict,
  constraint source_parse_attempts_parser_rule_version_check
    check (parser_rule_version is null or parser_rule_version > 0),
  constraint source_parse_attempts_request_metadata_object_check
    check (jsonb_typeof(request_metadata) = 'object'),
  constraint source_parse_attempts_parsed_candidate_object_check
    check (parsed_candidate is null or jsonb_typeof(parsed_candidate) = 'object'),
  constraint source_parse_attempts_validation_status_check
    check (validation_status in ('pending', 'valid', 'invalid', 'failed')),
  constraint source_parse_attempts_error_summary_check
    check (error_summary is null or char_length(error_summary) <= 2000)
);

create table private.transaction_jobs (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  sync_run_id uuid,
  data_source_id uuid,
  job_type text not null,
  payload jsonb not null default '{}'::jsonb,
  status text not null default 'queued',
  attempts integer not null default 0,
  max_attempts integer not null default 5,
  run_after timestamptz not null default now(),
  leased_at timestamptz,
  lease_expires_at timestamptz,
  completed_at timestamptz,
  last_error text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint transaction_jobs_user_sync_run_fkey
    foreign key (user_id, sync_run_id)
    references public.transaction_sync_runs (user_id, id)
    on delete restrict,
  constraint transaction_jobs_user_data_source_fkey
    foreign key (user_id, data_source_id)
    references private.data_sources (user_id, id)
    on delete restrict,
  constraint transaction_jobs_job_type_check
    check (job_type in ('gmail_ingestion', 'source_parsing', 'reconciliation')),
  constraint transaction_jobs_payload_object_check check (jsonb_typeof(payload) = 'object'),
  constraint transaction_jobs_status_check
    check (status in ('queued', 'running', 'completed', 'failed', 'cancelled')),
  constraint transaction_jobs_attempts_check
    check (attempts >= 0 and max_attempts > 0 and attempts <= max_attempts),
  constraint transaction_jobs_lease_check
    check ((leased_at is null and lease_expires_at is null)
      or (leased_at is not null and lease_expires_at is not null and lease_expires_at > leased_at)),
  constraint transaction_jobs_last_error_check
    check (last_error is null or char_length(last_error) <= 2000)
);

create or replace function private.assert_transaction_account_active()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if not exists (
    select 1
    from public.accounts account
    where account.id = new.account_id
      and account.user_id = new.user_id
      and account.deleted_at is null
  ) then
    raise exception using
      errcode = '23514',
      message = 'transaction account must be active and owned by the transaction user';
  end if;

  return new;
end;
$$;

create or replace function private.assert_transaction_link_integrity()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  debit_kind text;
  credit_kind text;
begin
  -- Lock linked transactions in a stable order. This serializes concurrent
  -- attempts to link the same transaction without session-level locks.
  perform 1
  from public.transactions transaction_row
  where transaction_row.user_id = new.user_id
    and transaction_row.id in (new.debit_transaction_id, new.credit_transaction_id)
  order by transaction_row.id
  for update;

  select transaction_kind into debit_kind
  from public.transactions
  where id = new.debit_transaction_id and user_id = new.user_id;

  select transaction_kind into credit_kind
  from public.transactions
  where id = new.credit_transaction_id and user_id = new.user_id;

  if debit_kind is distinct from 'debit' or credit_kind is distinct from 'credit' then
    raise exception using
      errcode = '23514',
      message = 'an internal transfer link requires one debit and one credit transaction';
  end if;

  if exists (
    select 1
    from private.transaction_links existing_link
    where existing_link.user_id = new.user_id
      and existing_link.id <> new.id
      and (
        new.debit_transaction_id in (existing_link.debit_transaction_id, existing_link.credit_transaction_id)
        or new.credit_transaction_id in (existing_link.debit_transaction_id, existing_link.credit_transaction_id)
      )
  ) then
    raise exception using
      errcode = '23505',
      message = 'a transaction can belong to only one internal transfer link';
  end if;

  return new;
end;
$$;

revoke execute on function private.assert_transaction_account_active() from public, anon, authenticated;
revoke execute on function private.assert_transaction_link_integrity() from public, anon, authenticated;

create trigger transactions_assert_active_account
before insert or update of user_id, account_id on public.transactions
for each row execute function private.assert_transaction_account_active();

create constraint trigger transaction_links_assert_integrity
after insert or update of user_id, debit_transaction_id, credit_transaction_id on private.transaction_links
deferrable initially deferred
for each row execute function private.assert_transaction_link_integrity();

create trigger source_parser_rules_set_updated_at
before update on private.source_parser_rules
for each row execute function public.set_updated_at();

create trigger gmail_connections_set_updated_at
before update on private.gmail_connections
for each row execute function public.set_updated_at();

create trigger data_sources_set_updated_at
before update on private.data_sources
for each row execute function public.set_updated_at();

create trigger transactions_set_updated_at
before update on public.transactions
for each row execute function public.set_updated_at();

create trigger transaction_links_set_updated_at
before update on private.transaction_links
for each row execute function public.set_updated_at();

create trigger transaction_sync_runs_set_updated_at
before update on public.transaction_sync_runs
for each row execute function public.set_updated_at();

create trigger transaction_jobs_set_updated_at
before update on private.transaction_jobs
for each row execute function public.set_updated_at();

create index data_sources_user_parse_received_idx
  on private.data_sources (user_id, parse_status, received_at desc);

create unique index data_sources_user_provider_message_key
  on private.data_sources (user_id, source_type, provider, provider_message_id)
  where provider_message_id is not null;

create index data_sources_parser_rule_id_idx
  on private.data_sources (parser_rule_id)
  where parser_rule_id is not null;

create index data_sources_suggested_account_id_idx
  on private.data_sources (suggested_account_id)
  where suggested_account_id is not null;

create index transactions_user_occurred_at_idx
  on public.transactions (user_id, occurred_at desc);

create index transactions_account_occurred_at_idx
  on public.transactions (account_id, occurred_at desc);

create index transactions_category_id_idx
  on public.transactions (category_id)
  where category_id is not null;

create index transactions_user_review_occurred_at_idx
  on public.transactions (user_id, review_status, occurred_at desc)
  where review_status <> 'confirmed';

create index transaction_links_user_id_idx
  on private.transaction_links (user_id);

create index transaction_data_sources_user_transaction_idx
  on private.transaction_data_sources (user_id, transaction_id)
  where detached_at is null;

create index transaction_data_sources_user_data_source_idx
  on private.transaction_data_sources (user_id, data_source_id)
  where detached_at is null;

create unique index transaction_data_sources_active_link_key
  on private.transaction_data_sources (transaction_id, data_source_id)
  where detached_at is null;

create index transaction_sync_runs_user_created_at_idx
  on public.transaction_sync_runs (user_id, created_at desc);

create index source_parse_attempts_user_source_idx
  on private.source_parse_attempts (user_id, data_source_id, created_at desc);

create index source_parse_attempts_parser_rule_id_idx
  on private.source_parse_attempts (parser_rule_id)
  where parser_rule_id is not null;

create index transaction_jobs_claim_idx
  on private.transaction_jobs (status, run_after, created_at)
  where status = 'queued';

create index transaction_jobs_user_sync_run_idx
  on private.transaction_jobs (user_id, sync_run_id)
  where sync_run_id is not null;

create index transaction_jobs_user_data_source_idx
  on private.transaction_jobs (user_id, data_source_id)
  where data_source_id is not null;

alter table public.transaction_categories enable row level security;
alter table public.transactions enable row level security;
alter table public.transaction_sync_runs enable row level security;
alter table private.source_parser_rules enable row level security;
alter table private.gmail_connections enable row level security;
alter table private.data_sources enable row level security;
alter table private.transaction_links enable row level security;
alter table private.transaction_data_sources enable row level security;
alter table private.source_parse_attempts enable row level security;
alter table private.transaction_jobs enable row level security;

revoke all privileges on table public.transaction_categories from public, anon, authenticated;
revoke all privileges on table public.transactions from public, anon, authenticated;
revoke all privileges on table public.transaction_sync_runs from public, anon, authenticated;
revoke all privileges on table private.source_parser_rules from public, anon, authenticated;
revoke all privileges on table private.gmail_connections from public, anon, authenticated;
revoke all privileges on table private.data_sources from public, anon, authenticated;
revoke all privileges on table private.transaction_links from public, anon, authenticated;
revoke all privileges on table private.transaction_data_sources from public, anon, authenticated;
revoke all privileges on table private.source_parse_attempts from public, anon, authenticated;
revoke all privileges on table private.transaction_jobs from public, anon, authenticated;

grant select on table public.transaction_categories to authenticated;
grant select on table public.transactions to authenticated;
grant select on table public.transaction_sync_runs to authenticated;

create policy "Authenticated users can read transaction categories"
on public.transaction_categories for select
to authenticated
using (true);

create policy "Users can read their own transactions"
on public.transactions for select
to authenticated
using ((select auth.uid()) = user_id);

create policy "Users can read their own transaction sync runs"
on public.transaction_sync_runs for select
to authenticated
using ((select auth.uid()) = user_id);

insert into storage.buckets (id, name, public, file_size_limit, allowed_mime_types)
values (
  'transaction-attachments',
  'transaction-attachments',
  false,
  5242880,
  array[
    'application/pdf',
    'image/bmp',
    'image/jpeg',
    'image/png',
    'image/tiff',
    'image/webp',
    'image/heic'
  ]
)
on conflict (id) do update
set public = excluded.public,
    file_size_limit = excluded.file_size_limit,
    allowed_mime_types = excluded.allowed_mime_types;

create policy "Transaction attachments require the Go API"
on storage.objects
as restrictive
for all
to anon, authenticated
using (bucket_id <> 'transaction-attachments')
with check (bucket_id <> 'transaction-attachments');
