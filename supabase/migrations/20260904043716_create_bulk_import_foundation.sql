-- Bulk Import extends the existing evidence-first transaction pipeline with
-- user-uploaded PDF/image documents. This is a forward-only migration; the
-- already-applied Transactions migrations remain unchanged.

alter table public.transactions
  add column time_precision text not null default 'exact',
  add constraint transactions_time_precision_check
    check (time_precision in ('exact', 'date')),
  add constraint transactions_date_precision_noon_utc_check check (
    time_precision <> 'date'
    or occurred_at = (
      date_trunc('day', occurred_at at time zone 'UTC') + interval '12 hours'
    ) at time zone 'UTC'
  );

comment on column public.transactions.time_precision is
  'exact uses the stored timestamp; date uses a canonical noon-UTC placeholder and calendar-day matching.';

create table private.api_idempotency_records (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  operation text not null,
  key_digest bytea not null,
  request_hash bytea not null,
  resource_type text,
  resource_id uuid,
  status text not null default 'processing',
  response_status integer,
  response_body jsonb,
  expires_at timestamptz not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint api_idempotency_operation_check
    check (char_length(btrim(operation)) between 1 and 100),
  constraint api_idempotency_key_digest_check check (octet_length(key_digest) = 32),
  constraint api_idempotency_request_hash_check check (octet_length(request_hash) = 32),
  constraint api_idempotency_resource_type_check
    check (resource_type is null or char_length(btrim(resource_type)) between 1 and 100),
  constraint api_idempotency_status_check
    check (status in ('processing', 'completed', 'failed')),
  constraint api_idempotency_response_status_check
    check (response_status is null or response_status between 100 and 599),
  constraint api_idempotency_response_body_check check (
    response_body is null
    or (
      jsonb_typeof(response_body) in ('object', 'array')
      and octet_length(response_body::text) <= 1048576
    )
  ),
  constraint api_idempotency_completion_check check (
    (status = 'processing' and response_status is null and response_body is null)
    or (status in ('completed', 'failed') and response_status is not null)
  ),
  constraint api_idempotency_expiry_check check (expires_at > created_at),
  constraint api_idempotency_user_operation_key_key
    unique (user_id, operation, key_digest),
  constraint api_idempotency_id_user_id_key unique (id, user_id)
);

comment on table private.api_idempotency_records is
  'Server-only, owner-scoped mutation idempotency records. Raw client keys are never stored.';

create table private.bulk_import_templates (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  title text not null,
  document_type text not null,
  parsing_prompt text not null,
  version integer not null default 1,
  archived_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint bulk_templates_title_check
    check (char_length(btrim(title)) between 1 and 100),
  constraint bulk_templates_document_type_check check (
    document_type in (
      'physical_receipt', 'invoice', 'e_wallet_history', 'bank_statement',
      'credit_card_bill', 'transaction_confirmation', 'other'
    )
  ),
  constraint bulk_templates_prompt_check
    check (char_length(btrim(parsing_prompt)) between 1 and 8000),
  constraint bulk_templates_version_check check (version > 0),
  constraint bulk_templates_archive_check
    check (archived_at is null or archived_at >= created_at),
  constraint bulk_templates_id_user_id_key unique (id, user_id)
);

create unique index bulk_templates_user_title_key
  on private.bulk_import_templates (user_id, lower(btrim(title)));

create index bulk_templates_user_archive_updated_idx
  on private.bulk_import_templates (user_id, archived_at, updated_at desc, id desc);

create table private.bulk_import_template_accounts (
  user_id uuid not null references auth.users(id) on delete cascade,
  template_id uuid not null,
  account_id uuid not null,
  sort_order integer not null,
  created_at timestamptz not null default now(),
  primary key (template_id, account_id),
  constraint bulk_template_accounts_template_fkey
    foreign key (template_id, user_id)
    references private.bulk_import_templates (id, user_id)
    on delete cascade,
  constraint bulk_template_accounts_account_fkey
    foreign key (account_id, user_id)
    references public.accounts (id, user_id)
    on delete restrict,
  constraint bulk_template_accounts_sort_check check (sort_order >= 0),
  constraint bulk_template_accounts_sort_key unique (template_id, sort_order)
);

create index bulk_template_accounts_user_idx
  on private.bulk_import_template_accounts (user_id, template_id);

create index bulk_template_accounts_account_idx
  on private.bulk_import_template_accounts (account_id, user_id);

create table public.bulk_import_batches (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  template_id uuid,
  template_version integer not null,
  title_snapshot text not null,
  document_type_snapshot text not null,
  parsing_prompt_snapshot text not null,
  status text not null default 'draft',
  file_count integer not null default 0,
  document_count integer not null default 0,
  page_count integer not null default 0,
  parsed_candidate_count integer not null default 0,
  created_count integer not null default 0,
  attached_count integer not null default 0,
  review_count integer not null default 0,
  failed_count integer not null default 0,
  duplicate_count integer not null default 0,
  cancel_requested_at timestamptz,
  error_summary text,
  started_at timestamptz,
  completed_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint bulk_batches_template_fkey
    foreign key (template_id, user_id)
    references private.bulk_import_templates (id, user_id)
    on delete restrict,
  constraint bulk_batches_template_version_check check (template_version > 0),
  constraint bulk_batches_title_check
    check (char_length(btrim(title_snapshot)) between 1 and 100),
  constraint bulk_batches_document_type_check check (
    document_type_snapshot in (
      'physical_receipt', 'invoice', 'e_wallet_history', 'bank_statement',
      'credit_card_bill', 'transaction_confirmation', 'other'
    )
  ),
  constraint bulk_batches_prompt_check
    check (char_length(btrim(parsing_prompt_snapshot)) between 1 and 8000),
  constraint bulk_batches_status_check check (
    status in (
      'draft', 'queued', 'running', 'cancelling', 'completed',
      'completed_with_errors', 'failed', 'cancelled'
    )
  ),
  constraint bulk_batches_counts_check check (
    file_count >= 0 and file_count <= 20
    and document_count >= 0 and document_count <= 20
    and page_count >= 0 and page_count <= 1000
    and parsed_candidate_count >= 0
    and created_count >= 0
    and attached_count >= 0
    and review_count >= 0
    and failed_count >= 0
    and duplicate_count >= 0
  ),
  constraint bulk_batches_cancel_check check (
    (status <> 'cancelling' or cancel_requested_at is not null)
    and (cancel_requested_at is null or cancel_requested_at >= created_at)
  ),
  constraint bulk_batches_lifecycle_check check (
    (status in ('draft', 'queued') and started_at is null and completed_at is null)
    or (status in ('running', 'cancelling') and started_at is not null and completed_at is null)
    or (
      status in ('completed', 'completed_with_errors', 'failed', 'cancelled')
      and completed_at is not null
      and completed_at >= coalesce(started_at, created_at)
    )
  ),
  constraint bulk_batches_error_check
    check (error_summary is null or char_length(error_summary) <= 2000),
  constraint bulk_batches_id_user_id_key unique (id, user_id)
);

comment on column public.bulk_import_batches.status is
  'The API maps product Processing to running and Cancel requested to cancelling.';

create index bulk_batches_user_history_idx
  on public.bulk_import_batches (user_id, created_at desc, id desc);

create index bulk_batches_user_active_idx
  on public.bulk_import_batches (user_id, status, created_at, id)
  where status in ('draft', 'queued', 'running', 'cancelling');

create table private.bulk_import_batch_accounts (
  user_id uuid not null references auth.users(id) on delete cascade,
  batch_id uuid not null,
  account_id uuid not null,
  account_ref text not null,
  sort_order integer not null,
  account_name text not null,
  institution_name text not null,
  account_type text not null,
  created_at timestamptz not null default now(),
  primary key (batch_id, account_id),
  constraint bulk_batch_accounts_batch_fkey
    foreign key (batch_id, user_id)
    references public.bulk_import_batches (id, user_id)
    on delete cascade,
  constraint bulk_batch_accounts_account_fkey
    foreign key (account_id, user_id)
    references public.accounts (id, user_id)
    on delete restrict,
  constraint bulk_batch_accounts_ref_check
    check (account_ref ~ '^account_[1-9][0-9]{0,2}$'),
  constraint bulk_batch_accounts_sort_check check (sort_order >= 0),
  constraint bulk_batch_accounts_name_check
    check (char_length(btrim(account_name)) between 1 and 100),
  constraint bulk_batch_accounts_institution_check
    check (char_length(btrim(institution_name)) between 1 and 100),
  constraint bulk_batch_accounts_type_check
    check (char_length(btrim(account_type)) between 1 and 50),
  constraint bulk_batch_accounts_ref_key unique (batch_id, account_ref),
  constraint bulk_batch_accounts_sort_key unique (batch_id, sort_order)
);

create index bulk_batch_accounts_user_idx
  on private.bulk_import_batch_accounts (user_id, batch_id);

create index bulk_batch_accounts_account_idx
  on private.bulk_import_batch_accounts (account_id, user_id);

create table private.bulk_import_documents (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  batch_id uuid not null,
  source_scope_id uuid not null default gen_random_uuid(),
  data_source_id uuid,
  sort_order integer not null,
  display_label text,
  status text not null default 'draft',
  attempt_generation integer not null default 1,
  document_summary jsonb,
  page_count integer not null default 0,
  candidate_count integer not null default 0,
  created_count integer not null default 0,
  attached_count integer not null default 0,
  review_count integer not null default 0,
  failed_count integer not null default 0,
  duplicate_count integer not null default 0,
  error_summary text,
  started_at timestamptz,
  completed_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint bulk_documents_batch_fkey
    foreign key (batch_id, user_id)
    references public.bulk_import_batches (id, user_id)
    on delete cascade,
  constraint bulk_documents_source_fkey
    foreign key (data_source_id, user_id)
    references private.data_sources (id, user_id)
    on delete cascade,
  constraint bulk_documents_source_identity_check
    check (data_source_id is null or data_source_id = source_scope_id),
  constraint bulk_documents_sort_check check (sort_order >= 0),
  constraint bulk_documents_label_check
    check (display_label is null or char_length(btrim(display_label)) between 1 and 250),
  constraint bulk_documents_status_check check (
    status in (
      'draft', 'queued', 'preparing', 'parsing', 'aggregating',
      'reconciling', 'completed', 'completed_with_errors', 'failed', 'cancelled'
    )
  ),
  constraint bulk_documents_generation_check check (attempt_generation > 0),
  constraint bulk_documents_summary_check check (
    document_summary is null
    or (
      jsonb_typeof(document_summary) = 'object'
      and octet_length(document_summary::text) <= 65536
    )
  ),
  constraint bulk_documents_counts_check check (
    page_count >= 0 and page_count <= 50
    and candidate_count >= 0
    and created_count >= 0
    and attached_count >= 0
    and review_count >= 0
    and failed_count >= 0
    and duplicate_count >= 0
  ),
  constraint bulk_documents_source_required_check
    check (status in ('draft', 'cancelled') or data_source_id is not null),
  constraint bulk_documents_error_check
    check (error_summary is null or char_length(error_summary) <= 2000),
  constraint bulk_documents_lifecycle_check check (
    (status in ('draft', 'queued') and completed_at is null)
    or (status in ('preparing', 'parsing', 'aggregating', 'reconciling') and started_at is not null and completed_at is null)
    or (
      status in ('completed', 'completed_with_errors', 'failed', 'cancelled')
      and (status = 'cancelled' or completed_at is not null)
    )
  ),
  constraint bulk_documents_batch_sort_key unique (batch_id, sort_order),
  constraint bulk_documents_source_scope_key unique (source_scope_id),
  constraint bulk_documents_data_source_key unique (data_source_id),
  constraint bulk_documents_id_user_id_key unique (id, user_id),
  constraint bulk_documents_id_user_batch_key unique (id, user_id, batch_id)
);

create index bulk_documents_user_batch_status_idx
  on private.bulk_import_documents (user_id, batch_id, status, sort_order);

create index bulk_documents_data_source_idx
  on private.bulk_import_documents (data_source_id, user_id)
  where data_source_id is not null;

create table private.bulk_import_files (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  batch_id uuid not null,
  document_id uuid not null,
  sort_order integer not null,
  display_filename text not null,
  declared_mime_type text not null,
  declared_byte_size bigint not null,
  declared_sha256 bytea not null,
  verified_mime_type text,
  verified_byte_size bigint,
  verified_sha256 bytea,
  storage_object_path text not null,
  status text not null default 'reserved',
  reservation_expires_at timestamptz not null,
  finalized_at timestamptz,
  error_summary text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint bulk_files_batch_fkey
    foreign key (batch_id, user_id)
    references public.bulk_import_batches (id, user_id)
    on delete cascade,
  constraint bulk_files_document_fkey
    foreign key (document_id, user_id, batch_id)
    references private.bulk_import_documents (id, user_id, batch_id)
    on delete cascade,
  constraint bulk_files_sort_check check (sort_order >= 0),
  constraint bulk_files_name_check
    check (char_length(btrim(display_filename)) between 1 and 250),
  constraint bulk_files_declared_mime_check check (
    declared_mime_type in (
      'application/pdf', 'image/bmp', 'image/jpeg', 'image/png',
      'image/tiff', 'image/webp', 'image/heic'
    )
  ),
  constraint bulk_files_declared_size_check
    check (declared_byte_size between 1 and 5242880),
  constraint bulk_files_declared_sha_check check (octet_length(declared_sha256) = 32),
  constraint bulk_files_verified_mime_check check (
    verified_mime_type is null
    or verified_mime_type in (
      'application/pdf', 'image/bmp', 'image/jpeg', 'image/png',
      'image/tiff', 'image/webp', 'image/heic'
    )
  ),
  constraint bulk_files_verified_size_check
    check (verified_byte_size is null or verified_byte_size between 1 and 5242880),
  constraint bulk_files_verified_sha_check
    check (verified_sha256 is null or octet_length(verified_sha256) = 32),
  constraint bulk_files_verified_fields_check check (
    (verified_mime_type is null and verified_byte_size is null and verified_sha256 is null)
    or (verified_mime_type is not null and verified_byte_size is not null and verified_sha256 is not null)
  ),
  constraint bulk_files_path_check check (
    storage_object_path ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.(pdf|bmp|jpg|png|tif|tiff|webp|heic)$'
  ),
  constraint bulk_files_status_check
    check (status in ('reserved', 'uploaded', 'verified', 'failed', 'cleanup_pending')),
  constraint bulk_files_reservation_expiry_check
    check (reservation_expires_at > created_at),
  constraint bulk_files_finalized_check check (
    (status = 'reserved' and finalized_at is null)
    or (status <> 'reserved' and (finalized_at is not null or status in ('failed', 'cleanup_pending')))
  ),
  constraint bulk_files_error_check
    check (error_summary is null or char_length(error_summary) <= 2000),
  constraint bulk_files_document_sort_key unique (document_id, sort_order),
  constraint bulk_files_batch_path_key unique (batch_id, storage_object_path),
  constraint bulk_files_id_user_id_key unique (id, user_id)
);

create index bulk_files_user_batch_status_idx
  on private.bulk_import_files (user_id, batch_id, status, created_at);

create index bulk_files_user_verified_sha_idx
  on private.bulk_import_files (user_id, verified_sha256)
  where verified_sha256 is not null;

create table private.bulk_import_chunks (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  batch_id uuid not null,
  document_id uuid not null,
  attempt_generation integer not null,
  chunk_index integer not null,
  page_manifest jsonb not null,
  status text not null default 'queued',
  page_count integer not null,
  valid_candidate_count integer not null default 0,
  invalid_candidate_count integer not null default 0,
  error_summary text,
  started_at timestamptz,
  completed_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint bulk_chunks_batch_fkey
    foreign key (batch_id, user_id)
    references public.bulk_import_batches (id, user_id)
    on delete cascade,
  constraint bulk_chunks_document_fkey
    foreign key (document_id, user_id, batch_id)
    references private.bulk_import_documents (id, user_id, batch_id)
    on delete cascade,
  constraint bulk_chunks_generation_check check (attempt_generation > 0),
  constraint bulk_chunks_index_check check (chunk_index >= 0),
  constraint bulk_chunks_manifest_check check (
    jsonb_typeof(page_manifest) = 'object'
    and octet_length(page_manifest::text) <= 65536
  ),
  constraint bulk_chunks_status_check
    check (status in ('queued', 'parsing', 'valid', 'partially_valid', 'failed', 'cancelled')),
  constraint bulk_chunks_page_count_check check (page_count between 1 and 5),
  constraint bulk_chunks_candidate_counts_check
    check (valid_candidate_count >= 0 and invalid_candidate_count >= 0),
  constraint bulk_chunks_error_check
    check (error_summary is null or char_length(error_summary) <= 2000),
  constraint bulk_chunks_lifecycle_check check (
    (status = 'queued' and started_at is null and completed_at is null)
    or (status = 'parsing' and started_at is not null and completed_at is null)
    or (status in ('valid', 'partially_valid', 'failed', 'cancelled') and completed_at is not null)
  ),
  constraint bulk_chunks_document_generation_index_key
    unique (document_id, attempt_generation, chunk_index),
  constraint bulk_chunks_id_user_id_key unique (id, user_id)
);

create index bulk_chunks_user_document_status_idx
  on private.bulk_import_chunks
  (user_id, document_id, attempt_generation, status, chunk_index);

alter table private.source_parse_attempts
  add column bulk_import_chunk_id uuid,
  add column attempt_ordinal integer,
  add constraint source_parse_attempts_id_user_id_key unique (id, user_id),
  add constraint source_parse_attempts_id_user_source_key
    unique (id, user_id, data_source_id),
  add constraint source_parse_attempts_bulk_chunk_fkey
    foreign key (bulk_import_chunk_id, user_id)
    references private.bulk_import_chunks (id, user_id)
    on delete cascade,
  add constraint source_parse_attempts_bulk_attempt_check check (
    (bulk_import_chunk_id is null and attempt_ordinal is null)
    or (bulk_import_chunk_id is not null and attempt_ordinal > 0)
  );

create unique index source_parse_attempts_bulk_chunk_ordinal_key
  on private.source_parse_attempts (bulk_import_chunk_id, attempt_ordinal)
  where bulk_import_chunk_id is not null;

create index source_parse_attempts_user_bulk_chunk_idx
  on private.source_parse_attempts (user_id, bulk_import_chunk_id, attempt_ordinal)
  where bulk_import_chunk_id is not null;

create table private.bulk_import_candidates (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  batch_id uuid not null,
  document_id uuid not null,
  data_source_id uuid not null,
  source_parse_attempt_id uuid not null,
  attempt_generation integer not null,
  output_ordinal integer not null,
  fingerprint bytea not null,
  parsed_candidate jsonb not null,
  account_id uuid,
  status text not null default 'pending_reconciliation',
  transaction_id uuid,
  duplicate_of_candidate_id uuid,
  reconciliation_reason text,
  error_summary text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint bulk_candidates_batch_fkey
    foreign key (batch_id, user_id)
    references public.bulk_import_batches (id, user_id)
    on delete cascade,
  constraint bulk_candidates_document_fkey
    foreign key (document_id, user_id, batch_id)
    references private.bulk_import_documents (id, user_id, batch_id)
    on delete cascade,
  constraint bulk_candidates_source_fkey
    foreign key (data_source_id, user_id)
    references private.data_sources (id, user_id)
    on delete cascade,
  constraint bulk_candidates_parse_attempt_fkey
    foreign key (source_parse_attempt_id, user_id, data_source_id)
    references private.source_parse_attempts (id, user_id, data_source_id)
    on delete cascade,
  constraint bulk_candidates_account_fkey
    foreign key (account_id, user_id)
    references public.accounts (id, user_id)
    on delete restrict,
  constraint bulk_candidates_transaction_fkey
    foreign key (transaction_id, user_id)
    references public.transactions (id, user_id)
    on delete restrict,
  constraint bulk_candidates_generation_check check (attempt_generation > 0),
  constraint bulk_candidates_output_ordinal_check check (output_ordinal >= 0),
  constraint bulk_candidates_fingerprint_check check (octet_length(fingerprint) = 32),
  constraint bulk_candidates_payload_check check (
    jsonb_typeof(parsed_candidate) = 'object'
    and octet_length(parsed_candidate::text) <= 2097152
  ),
  constraint bulk_candidates_status_check check (
    status in (
      'pending_reconciliation', 'created', 'attached', 'review_required',
      'duplicate', 'failed', 'cancelled', 'superseded'
    )
  ),
  constraint bulk_candidates_result_check check (
    (status in ('created', 'attached') and transaction_id is not null and duplicate_of_candidate_id is null)
    or (status = 'duplicate' and transaction_id is null and duplicate_of_candidate_id is not null)
    or (status not in ('created', 'attached', 'duplicate') and transaction_id is null and duplicate_of_candidate_id is null)
  ),
  constraint bulk_candidates_reason_check
    check (reconciliation_reason is null or char_length(reconciliation_reason) <= 2000),
  constraint bulk_candidates_error_check
    check (error_summary is null or char_length(error_summary) <= 2000),
  constraint bulk_candidates_document_output_key
    unique (document_id, attempt_generation, output_ordinal),
  constraint bulk_candidates_id_user_id_key unique (id, user_id),
  constraint bulk_candidates_owner_source_id_key unique (id, user_id, data_source_id),
  constraint bulk_candidates_duplicate_identity_key
    unique (id, user_id, data_source_id, document_id, attempt_generation),
  constraint bulk_candidates_duplicate_fkey
    foreign key (
      duplicate_of_candidate_id, user_id, data_source_id, document_id, attempt_generation
    )
    references private.bulk_import_candidates
      (id, user_id, data_source_id, document_id, attempt_generation)
    on delete cascade
);

create index bulk_candidates_user_status_idx
  on private.bulk_import_candidates (user_id, status, created_at desc, id desc);

create index bulk_candidates_batch_document_status_idx
  on private.bulk_import_candidates (batch_id, document_id, status, output_ordinal);

create index bulk_candidates_user_account_created_idx
  on private.bulk_import_candidates (user_id, account_id, created_at desc)
  where account_id is not null;

create index bulk_candidates_document_fingerprint_idx
  on private.bulk_import_candidates
  (document_id, attempt_generation, fingerprint, output_ordinal);

create index bulk_candidates_source_attempt_idx
  on private.bulk_import_candidates (source_parse_attempt_id, user_id);

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

revoke execute on function private.assert_bulk_candidate_scope()
  from public, anon, authenticated;

create trigger bulk_candidates_assert_scope
before insert or update on private.bulk_import_candidates
for each row execute function private.assert_bulk_candidate_scope();

alter table private.transaction_data_sources
  add column bulk_import_candidate_id uuid,
  add constraint transaction_data_sources_bulk_candidate_fkey
    foreign key (bulk_import_candidate_id, user_id, data_source_id)
    references private.bulk_import_candidates (id, user_id, data_source_id)
    on delete cascade;

create index transaction_data_sources_bulk_candidate_idx
  on private.transaction_data_sources (user_id, bulk_import_candidate_id)
  where bulk_import_candidate_id is not null and detached_at is null;

alter table private.transaction_jobs
  drop constraint transaction_jobs_job_type_check,
  add column bulk_import_batch_id uuid,
  add column bulk_import_document_id uuid,
  add column bulk_import_chunk_id uuid,
  add column bulk_import_candidate_id uuid,
  add column attempt_generation integer,
  add constraint transaction_jobs_job_type_check check (
    job_type in (
      'gmail_ingestion', 'source_parsing', 'reconciliation', 'source_attachment_cleanup',
      'bulk_document_prepare', 'bulk_document_chunk_parse',
      'bulk_document_aggregate', 'bulk_candidate_reconciliation',
      'bulk_document_post_process'
    )
  ),
  add constraint transaction_jobs_bulk_batch_fkey
    foreign key (bulk_import_batch_id, user_id)
    references public.bulk_import_batches (id, user_id)
    on delete cascade,
  add constraint transaction_jobs_bulk_document_fkey
    foreign key (bulk_import_document_id, user_id)
    references private.bulk_import_documents (id, user_id)
    on delete cascade,
  add constraint transaction_jobs_bulk_chunk_fkey
    foreign key (bulk_import_chunk_id, user_id)
    references private.bulk_import_chunks (id, user_id)
    on delete cascade,
  add constraint transaction_jobs_bulk_candidate_fkey
    foreign key (bulk_import_candidate_id, user_id)
    references private.bulk_import_candidates (id, user_id)
    on delete cascade,
  add constraint transaction_jobs_bulk_scope_check check (
    (
      job_type not like 'bulk_%'
      and bulk_import_batch_id is null
      and bulk_import_document_id is null
      and bulk_import_chunk_id is null
      and bulk_import_candidate_id is null
      and attempt_generation is null
    )
    or (
      job_type = 'bulk_document_prepare'
      and sync_run_id is null and data_source_id is not null
      and bulk_import_batch_id is not null and bulk_import_document_id is not null
      and bulk_import_chunk_id is null and bulk_import_candidate_id is null
      and attempt_generation > 0
    )
    or (
      job_type = 'bulk_document_chunk_parse'
      and sync_run_id is null and data_source_id is not null
      and bulk_import_batch_id is not null and bulk_import_document_id is not null
      and bulk_import_chunk_id is not null and bulk_import_candidate_id is null
      and attempt_generation > 0
    )
    or (
      job_type in ('bulk_document_aggregate', 'bulk_document_post_process')
      and sync_run_id is null and data_source_id is not null
      and bulk_import_batch_id is not null and bulk_import_document_id is not null
      and bulk_import_chunk_id is null and bulk_import_candidate_id is null
      and attempt_generation > 0
    )
    or (
      job_type = 'bulk_candidate_reconciliation'
      and sync_run_id is null and data_source_id is not null
      and bulk_import_batch_id is not null and bulk_import_document_id is not null
      and bulk_import_chunk_id is null and bulk_import_candidate_id is not null
      and attempt_generation > 0
    )
  );

create index transaction_jobs_user_bulk_batch_idx
  on private.transaction_jobs (user_id, bulk_import_batch_id, status, created_at)
  where bulk_import_batch_id is not null;

create index transaction_jobs_bulk_document_idx
  on private.transaction_jobs (bulk_import_document_id, user_id, attempt_generation)
  where bulk_import_document_id is not null;

create index transaction_jobs_bulk_chunk_idx
  on private.transaction_jobs (bulk_import_chunk_id, user_id)
  where bulk_import_chunk_id is not null;

create index transaction_jobs_bulk_candidate_idx
  on private.transaction_jobs (bulk_import_candidate_id, user_id)
  where bulk_import_candidate_id is not null;

create unique index transaction_jobs_bulk_document_active_key
  on private.transaction_jobs
  (user_id, job_type, bulk_import_document_id, attempt_generation)
  where status in ('queued', 'running')
    and job_type in ('bulk_document_prepare', 'bulk_document_aggregate', 'bulk_document_post_process');

create unique index transaction_jobs_bulk_chunk_active_key
  on private.transaction_jobs (user_id, bulk_import_chunk_id)
  where status in ('queued', 'running') and job_type = 'bulk_document_chunk_parse';

create unique index transaction_jobs_bulk_candidate_active_key
  on private.transaction_jobs (user_id, bulk_import_candidate_id)
  where status in ('queued', 'running') and job_type = 'bulk_candidate_reconciliation';

create or replace function private.assert_bulk_job_scope()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  document_row record;
begin
  if new.job_type not like 'bulk_%' then return new; end if;

  select document.batch_id, document.data_source_id
  into document_row
  from private.bulk_import_documents document
  where document.id = new.bulk_import_document_id
    and document.user_id = new.user_id;

  if document_row.batch_id is distinct from new.bulk_import_batch_id
    or document_row.data_source_id is distinct from new.data_source_id then
    raise exception using errcode = '23514',
      message = 'a bulk job must use one owned batch, document, and source';
  end if;

  if new.bulk_import_chunk_id is not null and not exists (
    select 1 from private.bulk_import_chunks chunk
    where chunk.id = new.bulk_import_chunk_id
      and chunk.user_id = new.user_id
      and chunk.document_id = new.bulk_import_document_id
      and chunk.batch_id = new.bulk_import_batch_id
      and chunk.attempt_generation = new.attempt_generation
  ) then
    raise exception using errcode = '23514',
      message = 'a bulk chunk job must use its exact document generation';
  end if;

  if new.bulk_import_candidate_id is not null and not exists (
    select 1 from private.bulk_import_candidates candidate
    where candidate.id = new.bulk_import_candidate_id
      and candidate.user_id = new.user_id
      and candidate.document_id = new.bulk_import_document_id
      and candidate.batch_id = new.bulk_import_batch_id
      and candidate.data_source_id = new.data_source_id
      and candidate.attempt_generation = new.attempt_generation
  ) then
    raise exception using errcode = '23514',
      message = 'a bulk candidate job must use its exact document generation';
  end if;

  return new;
end;
$$;

revoke execute on function private.assert_bulk_job_scope()
  from public, anon, authenticated;

create trigger transaction_jobs_assert_bulk_scope
before insert or update on private.transaction_jobs
for each row execute function private.assert_bulk_job_scope();

-- Validate template and batch Account membership at transaction commit. The
-- parent row is locked so concurrent membership edits serialize.
create or replace function private.validate_bulk_template_accounts(
  checked_template_id uuid,
  checked_user_id uuid
)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  checked_document_type text;
  selected_count integer;
  active_count integer;
  credit_card_count integer;
begin
  select template.document_type
  into checked_document_type
  from private.bulk_import_templates template
  where template.id = checked_template_id and template.user_id = checked_user_id
  for update;

  if not found then
    return;
  end if;

  select count(*)::integer,
    count(*) filter (where account.deleted_at is null)::integer,
    count(*) filter (
      where account.deleted_at is null and account.account_type = 'credit_card'
    )::integer
  into selected_count, active_count, credit_card_count
  from private.bulk_import_template_accounts selected
  join public.accounts account
    on account.id = selected.account_id and account.user_id = selected.user_id
  where selected.template_id = checked_template_id and selected.user_id = checked_user_id;

  if selected_count = 0 or selected_count <> active_count then
    raise exception using errcode = '23514',
      message = 'a bulk import template requires one or more active owned accounts';
  end if;

  if checked_document_type = 'credit_card_bill'
    and (selected_count <> 1 or credit_card_count <> 1) then
    raise exception using errcode = '23514',
      message = 'a credit card bill template requires exactly one active credit card account';
  end if;
end;
$$;

create or replace function private.assert_bulk_template_accounts()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if tg_table_name = 'bulk_import_templates' then
    perform private.validate_bulk_template_accounts(
      coalesce(new.id, old.id), coalesce(new.user_id, old.user_id)
    );
  else
    if tg_op <> 'INSERT' then
      perform private.validate_bulk_template_accounts(old.template_id, old.user_id);
    end if;
    if tg_op <> 'DELETE' then
      perform private.validate_bulk_template_accounts(new.template_id, new.user_id);
    end if;
  end if;
  if tg_op = 'DELETE' then return old; end if;
  return new;
end;
$$;

create or replace function private.validate_bulk_batch_accounts(
  checked_batch_id uuid,
  checked_user_id uuid
)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  checked_document_type text;
  checked_status text;
  selected_count integer;
  active_count integer;
  credit_card_count integer;
begin
  select batch.document_type_snapshot, batch.status
  into checked_document_type, checked_status
  from public.bulk_import_batches batch
  where batch.id = checked_batch_id and batch.user_id = checked_user_id
  for update;

  if not found then
    return;
  end if;

  select count(*)::integer,
    count(*) filter (where account.deleted_at is null)::integer,
    count(*) filter (
      where account.deleted_at is null and account.account_type = 'credit_card'
    )::integer
  into selected_count, active_count, credit_card_count
  from private.bulk_import_batch_accounts selected
  join public.accounts account
    on account.id = selected.account_id and account.user_id = selected.user_id
  where selected.batch_id = checked_batch_id and selected.user_id = checked_user_id;

  if selected_count = 0 then
    raise exception using errcode = '23514',
      message = 'a bulk import batch requires one or more owned accounts';
  end if;

  if checked_status in ('draft', 'queued') and selected_count <> active_count then
    raise exception using errcode = '23514',
      message = 'a draft or queued bulk import batch requires active accounts';
  end if;

  if checked_document_type = 'credit_card_bill'
    and (selected_count <> 1 or credit_card_count <> 1) then
    raise exception using errcode = '23514',
      message = 'a credit card bill batch requires exactly one active credit card account';
  end if;
end;
$$;

create or replace function private.assert_bulk_batch_accounts()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if tg_table_name = 'bulk_import_batches' then
    perform private.validate_bulk_batch_accounts(
      coalesce(new.id, old.id), coalesce(new.user_id, old.user_id)
    );
  else
    if tg_op <> 'INSERT' then
      perform private.validate_bulk_batch_accounts(old.batch_id, old.user_id);
    end if;
    if tg_op <> 'DELETE' then
      perform private.validate_bulk_batch_accounts(new.batch_id, new.user_id);
    end if;
  end if;
  if tg_op = 'DELETE' then return old; end if;
  return new;
end;
$$;

revoke execute on function private.validate_bulk_template_accounts(uuid, uuid)
  from public, anon, authenticated;
revoke execute on function private.assert_bulk_template_accounts()
  from public, anon, authenticated;
revoke execute on function private.validate_bulk_batch_accounts(uuid, uuid)
  from public, anon, authenticated;
revoke execute on function private.assert_bulk_batch_accounts()
  from public, anon, authenticated;

create constraint trigger bulk_templates_assert_accounts
after insert or update on private.bulk_import_templates
deferrable initially deferred
for each row execute function private.assert_bulk_template_accounts();

create constraint trigger bulk_template_accounts_assert_accounts
after insert or update or delete on private.bulk_import_template_accounts
deferrable initially deferred
for each row execute function private.assert_bulk_template_accounts();

create constraint trigger bulk_batches_assert_accounts
after insert or update on public.bulk_import_batches
deferrable initially deferred
for each row execute function private.assert_bulk_batch_accounts();

create constraint trigger bulk_batch_accounts_assert_accounts
after insert or update or delete on private.bulk_import_batch_accounts
deferrable initially deferred
for each row execute function private.assert_bulk_batch_accounts();

-- Uploaded-source identity and raw payload are immutable after submission.
create or replace function private.protect_bulk_data_source_identity()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if old.source_type = 'bulk_upload_document' or new.source_type = 'bulk_upload_document' then
    if old.id is distinct from new.id
      or old.user_id is distinct from new.user_id
      or old.source_type is distinct from new.source_type
      or old.provider is distinct from new.provider
      or old.provider_message_id is distinct from new.provider_message_id
      or old.provider_thread_id is distinct from new.provider_thread_id
      or old.received_at is distinct from new.received_at
      or old.ingested_at is distinct from new.ingested_at
      or old.raw_data is distinct from new.raw_data
      or old.created_at is distinct from new.created_at then
      raise exception using errcode = '23514',
        message = 'submitted bulk source identity and raw data are immutable';
    end if;
  end if;
  return new;
end;
$$;

revoke execute on function private.protect_bulk_data_source_identity()
  from public, anon, authenticated;

create trigger data_sources_protect_bulk_identity
before update on private.data_sources
for each row execute function private.protect_bulk_data_source_identity();

alter table private.data_sources
  drop constraint data_sources_source_type_check,
  add constraint data_sources_source_type_check
    check (source_type in ('gmail_email', 'phone_notification', 'bulk_upload_document')),
  add constraint data_sources_bulk_provider_check check (
    source_type <> 'bulk_upload_document'
    or (provider = 'user_upload' and provider_message_id is null)
  );

-- Extend evidence cardinality from source scope to candidate scope for bulk
-- documents. Legacy Gmail/phone behavior remains unchanged.
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
  active_count integer;
  active_transactions uuid[];
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

  if checked_source_type = 'bulk_upload_document' then
    if exists (
      select 1 from private.transaction_data_sources link
      where link.user_id = checked_user_id
        and link.data_source_id = checked_source_id
        and link.detached_at is null
        and link.bulk_import_candidate_id is null
    ) then
      raise exception using errcode = '23514',
        message = 'bulk document evidence requires a candidate scope';
    end if;

    for affected_candidate in
      select link.bulk_import_candidate_id,
        count(*)::integer as link_count,
        array_agg(link.transaction_id order by link.transaction_id) as transaction_ids
      from private.transaction_data_sources link
      where link.user_id = checked_user_id
        and link.data_source_id = checked_source_id
        and link.detached_at is null
      group by link.bulk_import_candidate_id
    loop
      if affected_candidate.link_count > 2 then
        raise exception using errcode = '23514',
          message = 'a bulk candidate may have at most two active transaction links';
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
          message = 'two active bulk candidate links must be one internal transfer';
      end if;
    end loop;
  else
    if exists (
      select 1 from private.transaction_data_sources link
      where link.user_id = checked_user_id
        and link.data_source_id = checked_source_id
        and link.bulk_import_candidate_id is not null
    ) then
      raise exception using errcode = '23514',
        message = 'non-bulk evidence cannot have a bulk candidate scope';
    end if;

    select count(*)::integer, array_agg(link.transaction_id order by link.transaction_id)
    into active_count, active_transactions
    from private.transaction_data_sources link
    where link.user_id = checked_user_id
      and link.data_source_id = checked_source_id
      and link.detached_at is null;

    if active_count > 2 then
      raise exception using errcode = '23514',
        message = 'a source may have at most two active transaction links';
    end if;
    if active_count = 2 and not exists (
      select 1 from private.transaction_links transfer
      where transfer.user_id = checked_user_id
        and (
          (transfer.debit_transaction_id = active_transactions[1]
            and transfer.credit_transaction_id = active_transactions[2])
          or
          (transfer.debit_transaction_id = active_transactions[2]
            and transfer.credit_transaction_id = active_transactions[1])
        )
    ) then
      raise exception using errcode = '23514',
        message = 'two active source links must be the legs of one internal transfer';
    end if;
  end if;
end;
$$;

revoke execute on function private.assert_source_active_links(uuid, uuid)
  from public, anon, authenticated;

-- Keep all server-owned rows private even if the private schema is exposed in
-- a future configuration. Only the owner-readable batch projection is granted.
alter table private.api_idempotency_records enable row level security;
alter table private.bulk_import_templates enable row level security;
alter table private.bulk_import_template_accounts enable row level security;
alter table public.bulk_import_batches enable row level security;
alter table private.bulk_import_batch_accounts enable row level security;
alter table private.bulk_import_documents enable row level security;
alter table private.bulk_import_files enable row level security;
alter table private.bulk_import_chunks enable row level security;
alter table private.bulk_import_candidates enable row level security;

revoke all privileges on table private.api_idempotency_records from public, anon, authenticated;
revoke all privileges on table private.bulk_import_templates from public, anon, authenticated;
revoke all privileges on table private.bulk_import_template_accounts from public, anon, authenticated;
revoke all privileges on table public.bulk_import_batches from public, anon, authenticated;
revoke all privileges on table private.bulk_import_batch_accounts from public, anon, authenticated;
revoke all privileges on table private.bulk_import_documents from public, anon, authenticated;
revoke all privileges on table private.bulk_import_files from public, anon, authenticated;
revoke all privileges on table private.bulk_import_chunks from public, anon, authenticated;
revoke all privileges on table private.bulk_import_candidates from public, anon, authenticated;

grant select on table public.bulk_import_batches to authenticated;

create policy "Users can read their own bulk import batches"
on public.bulk_import_batches for select
to authenticated
using ((select auth.uid()) is not null and (select auth.uid()) = user_id);

-- Private policies are defense in depth; browser roles intentionally retain no
-- table or private-schema grants.
create policy "Owners are isolated on bulk import templates"
on private.bulk_import_templates for all
to authenticated
using ((select auth.uid()) = user_id)
with check ((select auth.uid()) = user_id);

create policy "Owners are isolated on bulk import template accounts"
on private.bulk_import_template_accounts for all
to authenticated
using ((select auth.uid()) = user_id)
with check ((select auth.uid()) = user_id);

create policy "Owners are isolated on bulk import batch accounts"
on private.bulk_import_batch_accounts for all
to authenticated
using ((select auth.uid()) = user_id)
with check ((select auth.uid()) = user_id);

create policy "Owners are isolated on bulk import documents"
on private.bulk_import_documents for all
to authenticated
using ((select auth.uid()) = user_id)
with check ((select auth.uid()) = user_id);

create policy "Owners are isolated on bulk import files"
on private.bulk_import_files for all
to authenticated
using ((select auth.uid()) = user_id)
with check ((select auth.uid()) = user_id);

create policy "Owners are isolated on bulk import chunks"
on private.bulk_import_chunks for all
to authenticated
using ((select auth.uid()) = user_id)
with check ((select auth.uid()) = user_id);

create policy "Owners are isolated on bulk import candidates"
on private.bulk_import_candidates for all
to authenticated
using ((select auth.uid()) = user_id)
with check ((select auth.uid()) = user_id);

create trigger api_idempotency_records_set_updated_at
before update on private.api_idempotency_records
for each row execute function public.set_updated_at();

create trigger bulk_import_templates_set_updated_at
before update on private.bulk_import_templates
for each row execute function public.set_updated_at();

create trigger bulk_import_batches_set_updated_at
before update on public.bulk_import_batches
for each row execute function public.set_updated_at();

create trigger bulk_import_documents_set_updated_at
before update on private.bulk_import_documents
for each row execute function public.set_updated_at();

create trigger bulk_import_files_set_updated_at
before update on private.bulk_import_files
for each row execute function public.set_updated_at();

create trigger bulk_import_chunks_set_updated_at
before update on private.bulk_import_chunks
for each row execute function public.set_updated_at();

create trigger bulk_import_candidates_set_updated_at
before update on private.bulk_import_candidates
for each row execute function public.set_updated_at();

create index api_idempotency_expiry_idx
  on private.api_idempotency_records (expires_at)
  where status <> 'processing';

-- Reassert bucket restrictions and permit only exact, unexpired, server-created
-- reservations if the hosted Storage version evaluates authenticated RLS for a
-- signed upload. Signed token creation itself remains a trusted Go operation.
insert into storage.buckets (id, name, public, file_size_limit, allowed_mime_types)
values (
  'transaction-attachments',
  'transaction-attachments',
  false,
  5242880,
  array[
    'application/pdf', 'image/bmp', 'image/jpeg', 'image/png',
    'image/tiff', 'image/webp', 'image/heic'
  ]
)
on conflict (id) do update
set public = excluded.public,
    file_size_limit = excluded.file_size_limit,
    allowed_mime_types = excluded.allowed_mime_types;

create or replace function private.bulk_import_storage_insert_allowed(
  checked_object_name text
)
returns boolean
language sql
stable
security definer
set search_path = ''
as $$
  select (select auth.uid()) is not null and exists (
    select 1
    from private.bulk_import_files file
    join public.bulk_import_batches batch
      on batch.id = file.batch_id and batch.user_id = file.user_id
    where file.user_id = (select auth.uid())
      and file.storage_object_path = checked_object_name
      and file.status = 'reserved'
      and file.reservation_expires_at > now()
      and batch.status = 'draft'
  );
$$;

revoke execute on function private.bulk_import_storage_insert_allowed(text)
  from public, anon, authenticated;
grant execute on function private.bulk_import_storage_insert_allowed(text)
  to authenticated;

drop policy if exists "Transaction attachments require the Go API" on storage.objects;

create policy "Transaction attachments block browser reads"
on storage.objects as restrictive for select
to anon, authenticated
using (bucket_id <> 'transaction-attachments');

create policy "Transaction attachments block browser updates"
on storage.objects as restrictive for update
to anon, authenticated
using (bucket_id <> 'transaction-attachments')
with check (bucket_id <> 'transaction-attachments');

create policy "Transaction attachments block browser deletes"
on storage.objects as restrictive for delete
to anon, authenticated
using (bucket_id <> 'transaction-attachments');

create policy "Transaction attachments gate browser inserts"
on storage.objects as restrictive for insert
to anon, authenticated
with check (
  bucket_id <> 'transaction-attachments'
  or private.bulk_import_storage_insert_allowed(name)
);

create policy "Users can upload reserved transaction attachments"
on storage.objects for insert
to authenticated
with check (
  bucket_id = 'transaction-attachments'
  and private.bulk_import_storage_insert_allowed(name)
);

do $$
begin
  if exists (select 1 from pg_publication where pubname = 'supabase_realtime')
    and not exists (
      select 1 from pg_publication_tables
      where pubname = 'supabase_realtime'
        and schemaname = 'public'
        and tablename = 'bulk_import_batches'
    ) then
    execute 'alter publication supabase_realtime add table public.bulk_import_batches';
  end if;
end;
$$;
