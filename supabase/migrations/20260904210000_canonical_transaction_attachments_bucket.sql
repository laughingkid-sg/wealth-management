-- Canonical definition of the private `transaction-attachments` Storage bucket.
--
-- The same bucket is also upserted, with identical values, by two earlier migrations:
--   * 20260902191000_create_transactions_foundation.sql
--   * 20260904043716_create_bulk_import_foundation.sql
-- Those historical files are already applied and must NOT be edited. This migration
-- runs last, so its values always win on a fresh replay, making it the single source
-- of truth for the bucket's privacy, size limit, and allowed MIME types.
--
-- Any future change to the bucket configuration must be made HERE ONLY. Do not add a
-- new `storage.buckets` upsert for this bucket in another migration.
--
-- This upsert is idempotent: against the current database it is a no-op because the
-- values already match.

insert into storage.buckets (id, name, public, file_size_limit, allowed_mime_types)
values (
  'transaction-attachments',
  'transaction-attachments',
  false,
  5242880, -- 5 MiB
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
