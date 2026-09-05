-- Transaction parsing revamp — P0 (schema + rename).
--
-- Generalize the bulk candidate model into a provider-neutral source_candidates
-- table shared by Gmail and Bulk Import, create the global script_definitions
-- store for the Tengo pre/post-process scripts, and add rule -> script
-- references. Additive + rename only; no data backfill (dev pipeline data is
-- dropped separately). See documentation/td-transaction-parsing-revamp.md.
--
-- This migration deliberately does NOT touch public.accounts or
-- private.gmail_connections.

begin;

-- 1. Rename the candidate table. Foreign keys into it (transaction_data_sources,
--    transaction_jobs, and the self-referential duplicate FK), indexes, the
--    updated-at trigger, the scope trigger, and the RLS policy all follow the
--    rename automatically (Postgres tracks them by identity, not name).
alter table private.bulk_import_candidates rename to source_candidates;

-- 2. Provider discriminator + email-facing columns. Existing rows are bulk.
--    The default 'bulk_import' is retained so the existing bulk INSERT (which
--    does not name origin) is unchanged; the Gmail path sets origin explicitly.
alter table private.source_candidates
  add column origin text not null default 'bulk_import',
  add column suggested_account_id uuid,
  add column suggested_transaction_id uuid,
  add column match_confidence smallint;

alter table private.source_candidates
  add constraint source_candidates_origin_check
    check (origin in ('gmail_email', 'bulk_import'));

-- A suggested account (email review UX) must be one the owner holds.
alter table private.source_candidates
  add constraint source_candidates_suggested_account_fkey
    foreign key (suggested_account_id, user_id)
    references public.accounts (id, user_id)
    on delete set null;

alter table private.source_candidates
  add constraint source_candidates_match_confidence_check
    check (match_confidence is null or match_confidence between 0 and 100);

-- 3. Relax bulk-only columns so Gmail rows can omit them. output_ordinal and
--    fingerprint stay NOT NULL (email uses ordinal as the candidate index and a
--    sha256 of the canonical candidate as the fingerprint); attempt_generation
--    defaults to 1 for the single email parse generation.
alter table private.source_candidates
  alter column batch_id drop not null,
  alter column document_id drop not null,
  alter column attempt_generation set default 1;

-- 4. Union the status vocabulary: add 'dangling' for email's no-account outcome.
alter table private.source_candidates drop constraint bulk_candidates_status_check;
alter table private.source_candidates
  add constraint source_candidates_status_check check (
    status in (
      'pending_reconciliation', 'created', 'attached', 'review_required',
      'dangling', 'duplicate', 'failed', 'cancelled', 'superseded'
    )
  );

-- 5. Email idempotency: one candidate per (source, ordinal) for non-bulk rows.
--    (The bulk unique(document_id, attempt_generation, output_ordinal) leaves
--    email rows unprotected because document_id is null and nulls are distinct.)
create unique index source_candidates_email_ordinal_key
  on private.source_candidates (data_source_id, output_ordinal)
  where document_id is null;

-- 6. Scope trigger: keep the bulk document/chunk/batch/account validation, but
--    only for bulk rows. Non-bulk (Gmail) rows carry no bulk scope; assert their
--    shape and that the parse attempt belongs to the same owned source.
create or replace function private.assert_bulk_candidate_scope()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  document_row record;
  attempt_row record;
begin
  if new.batch_id is null then
    if new.document_id is not null or new.duplicate_of_candidate_id is not null then
      raise exception using errcode = '23514',
        message = 'a non-bulk source candidate must not reference a bulk document or duplicate';
    end if;
    if not exists (
      select 1 from private.source_parse_attempts attempt
      where attempt.id = new.source_parse_attempt_id
        and attempt.user_id = new.user_id
        and attempt.data_source_id = new.data_source_id
    ) then
      raise exception using errcode = '23514',
        message = 'a source candidate parse attempt must belong to the same owned source';
    end if;
    return new;
  end if;

  select document.data_source_id, document.batch_id,
    batch.document_type_snapshot
  into document_row
  from private.bulk_import_documents document
  join public.bulk_import_batches batch
    on batch.id = document.batch_id and batch.user_id = document.user_id
  where document.id = new.document_id and document.user_id = new.user_id;

  select attempt.bulk_import_chunk_id, chunk.document_id,
    chunk.attempt_generation
  into attempt_row
  from private.source_parse_attempts attempt
  join private.bulk_import_chunks chunk
    on chunk.id = attempt.bulk_import_chunk_id and chunk.user_id = attempt.user_id
  where attempt.id = new.source_parse_attempt_id
    and attempt.user_id = new.user_id
    and attempt.data_source_id = new.data_source_id;

  if document_row.data_source_id is distinct from new.data_source_id
    or document_row.batch_id is distinct from new.batch_id
    or attempt_row.document_id is distinct from new.document_id
    or attempt_row.attempt_generation is distinct from new.attempt_generation then
    raise exception using errcode = '23514',
      message = 'a bulk candidate must use one document, source, batch, generation, and parse chunk';
  end if;

  if new.account_id is not null and not exists (
    select 1 from private.bulk_import_batch_accounts selected
    where selected.batch_id = new.batch_id
      and selected.user_id = new.user_id
      and selected.account_id = new.account_id
  ) then
    raise exception using errcode = '23514',
      message = 'a bulk candidate Account must come from the immutable batch selection';
  end if;

  if document_row.document_type_snapshot = 'credit_card_bill'
    and (
      not (new.parsed_candidate ? 'bill_line_index')
      or not (new.parsed_candidate ? 'bill_line_kind')
      or jsonb_typeof(new.parsed_candidate -> 'bill_line_index') <> 'number'
      or (new.parsed_candidate ->> 'bill_line_index') !~ '^[1-9][0-9]*$'
      or new.parsed_candidate ->> 'bill_line_kind' not in (
        'activity', 'refund', 'fee', 'interest', 'payment'
      )
    ) then
    raise exception using errcode = '23514',
      message = 'Credit Card bill candidates require explicit line index and line kind';
  end if;

  return new;
end;
$$;

-- 7. Global Tengo script store (mirrors source_parser_rules: global, RLS on,
--    no browser grants, service-role only). Append-only versions, one active
--    per key.
create table private.script_definitions (
  script_key text not null,
  version integer not null,
  source text not null,
  checksum text not null,
  is_active boolean not null default false,
  notes text,
  updated_by_user_id uuid references auth.users (id) on delete set null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (script_key, version),
  constraint script_definitions_version_check check (version > 0),
  constraint script_definitions_key_check
    check (script_key ~ '^[a-z][a-z0-9_]{1,63}$'),
  constraint script_definitions_source_len_check
    check (octet_length(source) between 1 and 65536),
  constraint script_definitions_checksum_check
    check (checksum ~ '^[0-9a-f]{64}$'),
  constraint script_definitions_notes_len_check
    check (notes is null or char_length(notes) <= 2000)
);

create unique index script_definitions_one_active_per_key
  on private.script_definitions (script_key)
  where is_active;

alter table private.script_definitions enable row level security;
revoke all privileges on table private.script_definitions from public, anon, authenticated;

create trigger script_definitions_set_updated_at
before update on private.script_definitions
for each row execute function public.set_updated_at();

-- 8. Rule -> script references. A matched sender/subject rule may name the
--    pre/post script for its emails; null falls back to the global default key.
alter table private.source_parser_rules
  add column pre_process_script_key text,
  add column post_process_script_key text,
  add constraint source_parser_rules_pre_script_key_check
    check (pre_process_script_key is null or pre_process_script_key ~ '^[a-z][a-z0-9_]{1,63}$'),
  add constraint source_parser_rules_post_script_key_check
    check (post_process_script_key is null or post_process_script_key ~ '^[a-z][a-z0-9_]{1,63}$');

alter table private.user_source_parser_rules
  add column pre_process_script_key text,
  add column post_process_script_key text,
  add constraint user_source_parser_rules_pre_script_key_check
    check (pre_process_script_key is null or pre_process_script_key ~ '^[a-z][a-z0-9_]{1,63}$'),
  add constraint user_source_parser_rules_post_script_key_check
    check (post_process_script_key is null or post_process_script_key ~ '^[a-z][a-z0-9_]{1,63}$');

commit;
