-- Coordinate user-scoped Gmail ingestion with raw-source deletion without
-- holding a database transaction open across Gmail or Storage network calls.
create table private.transaction_user_locks (
  user_id uuid primary key references auth.users(id) on delete cascade,
  created_at timestamptz not null default now()
);

comment on table private.transaction_user_locks is
  'Internal row-lock targets that serialize Gmail sync creation/ingestion with raw-source deletion per user.';

-- A digest prevents a deliberately deleted provider message from being
-- recreated by a retry or a later backfill without retaining its raw provider
-- identifier, source UUID, attachment paths, or message contents.
create table private.deleted_provider_messages (
  user_id uuid not null references auth.users(id) on delete cascade,
  source_type text not null,
  provider text not null,
  provider_message_digest bytea not null,
  deleted_at timestamptz not null default now(),
  primary key (user_id, source_type, provider, provider_message_digest),
  constraint deleted_provider_messages_source_type_check
    check (source_type in ('gmail_email', 'phone_notification')),
  constraint deleted_provider_messages_provider_check
    check (char_length(btrim(provider)) between 1 and 100),
  constraint deleted_provider_messages_digest_check
    check (octet_length(provider_message_digest) = 32)
);

comment on table private.deleted_provider_messages is
  'Minimal one-way provider-identity tombstones that prevent permanently deleted raw messages from being reingested.';

-- Reuse the durable leased queue as the transactional outbox. Cleanup jobs
-- deliberately have no data_source_id FK because the source is removed in the
-- same transaction that inserts the outbox row. The payload is removed with
-- the row after successful Storage deletion.
alter table private.transaction_jobs
  drop constraint transaction_jobs_job_type_check,
  add constraint transaction_jobs_job_type_check
    check (job_type in ('gmail_ingestion', 'source_parsing', 'reconciliation', 'source_attachment_cleanup')),
  add constraint transaction_jobs_source_cleanup_scope_check
    check (job_type <> 'source_attachment_cleanup'
      or (data_source_id is null and sync_run_id is null and max_attempts between 1 and 5));

create index transaction_jobs_attachment_cleanup_recovery_idx
  on private.transaction_jobs (status, updated_at)
  where job_type = 'source_attachment_cleanup' and status = 'failed';

-- The summary debug endpoint truncates these objects, while an owner-scoped
-- field endpoint can retrieve one exact value on demand. Give every field a
-- database-enforced response ceiling; the other six audit fields already have
-- equivalent limits from migration 20260902230001.
alter table private.source_parse_attempts
  add constraint source_parse_attempts_request_metadata_length_check
    check (octet_length(request_metadata::text) <= 65536),
  add constraint source_parse_attempts_parsed_candidate_length_check
    check (parsed_candidate is null or octet_length(parsed_candidate::text) <= 2097152);

alter table private.transaction_user_locks enable row level security;
alter table private.deleted_provider_messages enable row level security;

revoke all privileges on table private.transaction_user_locks from public, anon, authenticated;
revoke all privileges on table private.deleted_provider_messages from public, anon, authenticated;
