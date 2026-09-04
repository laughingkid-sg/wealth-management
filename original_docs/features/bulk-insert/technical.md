# Bulk Insert technical design

Status: delivered. The React SPA, authenticated Go API, asynchronous Go worker,
hosted Postgres schema, private Storage protocol, strict parser, reconciliation,
and document-specific Credit Card post-processing are implemented. Bulk routes
and worker job claims remain controlled by `BULK_IMPORT_ENABLED`.

Hosted migrations `20260904043716_create_bulk_import_foundation.sql`,
`20260904043721_create_account_balances_and_credit_card_bills.sql`, and forward
repair `20260904061318_disambiguate_credit_card_validation_records.sql` are
applied with matching local/remote history. The latest hosted checks pass 28
Bulk pgTAP assertions, report no public/private schema lint errors, and confirm
signed private-Storage upload, stat, signed read, and deletion. A live provider
compatibility call returned strict structured JSON from `qwen3.8-flash` with
thinking disabled. A later isolated hosted synthetic exercise completed the
authenticated API-to-worker path: the uploaded bytes and SHA-256 matched, the
worker produced one created candidate, signed evidence and the complete bounded
Debug audit were readable, and every temporary database row, Auth user, and
Storage object was removed afterward. The isolated exercise does not claim that
an unrestricted continuously running production worker was restarted.

This document describes the implementation boundary for uploading multiple PDFs or images, extracting one or more transaction candidates from each logical document, and passing those candidates through the existing Transactions reconciliation pipeline. Product requirements and copy belong in the accompanying [feature README](README.md).

## Design goals

- Reuse the existing Go API, Go worker, durable Postgres job queue, private `transaction-attachments` Storage bucket, canonical transaction schema, evidence links, parse audit, reconciliation rules, Prompt Preview, Debug view, and source-deletion outbox.
- Treat one uploaded document as raw evidence which may yield many independent transaction candidates.
- Keep every external call outside database transactions and make every write retry-safe.
- Never trust a browser-supplied user ID, Account ID, MIME type, checksum, file name, model Account selection, or model transaction field.
- Preserve the current evidence rule for Gmail and phone sources while adding a candidate-scoped rule for multi-transaction bulk documents.
- Keep uploaded originals viewable as transaction evidence and removable through the existing durable Storage-cleanup workflow.
- Provide one shared ingestion pipeline with server-owned document processors, so a Credit Card bill adds domain logic after extraction without creating another uploader, parser, queue, audit store, or evidence model.

## Component boundary

| Component | Responsibility |
| --- | --- |
| React SPA | Template management, batch creation, temporary Account overrides, file grouping/order, direct signed upload, progress/history, per-candidate review, retry/cancel, Prompt Preview, and Debug display. |
| Go API | Authenticates the Supabase user; owns template and batch mutations; validates active owned Accounts; creates upload reservations and signed upload tokens; finalizes uploads; submits/cancels/retries batches; returns owner-safe projections; signs private downloads. |
| Go worker | Verifies uploaded bytes; inspects/rasterizes every page; splits long documents into bounded chunks; calls the LLM; strictly validates multi-candidate output; aggregates and deduplicates candidates; runs reconciliation; maintains progress; and performs durable Storage cleanup. |
| Hosted Supabase | Auth, Postgres state and queue, owner-scoped Realtime batch progress, canonical transactions, private configuration/audit/operational rows, and private object Storage. |

The browser may use Supabase Auth and a provider-issued signed upload token. It does not receive a service-role key, database connection string, raw Storage object path outside the reservation response, or direct write access to operational tables.

## End-to-end flow

```text
Select Bulk Import Template
  + optional Account override for this batch only
  -> create draft batch with immutable template + Account snapshots
  -> reserve file paths and receive signed upload tokens
  -> browser uploads directly to private Supabase Storage
  -> finalize each reservation
  -> group/reorder images; each PDF remains one document
  -> submit batch atomically
  -> prepare each document and enumerate every page (maximum 50)
  -> parse bounded page chunks asynchronously
  -> aggregate strict transaction candidates
  -> remove exact same-batch duplicates
  -> reconcile each candidate
       existing match -> attach evidence and conservatively enrich
       safe new       -> create transaction and attach evidence
       conflict       -> candidate-level Review
       transfer       -> candidate-level Review for paired Accounts
       failure        -> retain audit and allow document retry
```

“One go” means one user action submits the entire batch. Document groups execute independently. A document that fits the provider limits uses one LLM request; a longer document uses multiple bounded requests so every page is attempted rather than silently discarded.

## Shared pipeline and document processors

Bulk Import owns the complete common pipeline: reservation, Storage upload, byte verification, page preparation, prompt assembly, model calls, strict decoding, candidate persistence, deduplication, reconciliation, audit, retry, cancellation, evidence viewing, and cleanup.

The immutable `document_type_snapshot` selects one server-owned processor:

- `generic_transactions` handles physical receipts, invoices, e-wallet histories, bank statements, transaction confirmations, and other documents.
- `credit_card_bill` extends the shared output with a typed bill summary and invokes the Account Balances domain after candidate reconciliation.

A processor may provide immutable prompt guidance, a strict document-summary schema, post-reconciliation validation, and a link to its domain result. It cannot implement its own upload path, Storage bucket, provider client, parse-attempt table, candidate matcher, or deletion workflow. User-authored template guidance remains subordinate to the processor's platform contract.

For `credit_card_bill`, submission requires exactly one selected active Credit Card Account. Post-processing creates exactly one bill for the document generation, maps its statement lines to the already reconciled Bulk candidates, and runs payment detection. This work is idempotent and occurs inside the existing asynchronous document lifecycle. Missing or ambiguous bill fields produce a reviewable bill result rather than a second parse workflow.

## Existing infrastructure to reuse

- `private.data_sources` remains the durable raw-evidence identity.
- `private.source_parse_attempts` remains the exact, bounded request/response/model audit store. A bulk chunk produces one attempt row.
- `private.transaction_jobs` remains the leased queue with `FOR UPDATE SKIP LOCKED`, heartbeat renewal, bounded attempts, and exponential backoff.
- `private.transaction_data_sources` remains the transaction-to-evidence junction.
- `public.transactions`, `private.transaction_links`, categories, Account matching keys, and the pure reconciliation package remain authoritative.
- `private.transaction_user_locks` continues to serialize automatic transaction creation per owner, closing same-batch and cross-source write races.
- The `transaction-attachments` bucket remains private, limited to 5 MiB per object, and restricted to the existing PDF/image MIME allowlist.
- Existing short-lived, owner-checked signed download URLs remain the only browser preview path after submission.
- Existing audit-field truncation and exact-field retrieval are extended to bulk attempts instead of building a second Debug format.
- Existing staged raw-source deletion and `source_attachment_cleanup` jobs remove database dependants first and Storage objects durably afterward.

## Data model

All operational/configuration tables live in `private` unless explicitly marked `public`. UUID foreign keys include `user_id` wherever possible so cross-user references fail at the database boundary. Every foreign-key lookup and cascade column must be indexed.

### `private.bulk_import_templates`

One user-owned reusable parsing configuration.

| Column | Contract |
| --- | --- |
| `id uuid` | Primary key. |
| `user_id uuid` | Required FK to `auth.users(id)` with `on delete cascade`; unique with `id`. |
| `title text` | Trimmed, 1–100 characters. Case-insensitively unique per user, including archived templates. |
| `document_type text` | One of `physical_receipt`, `invoice`, `e_wallet_history`, `bank_statement`, `credit_card_bill`, `transaction_confirmation`, `other`. The value selects a server-owned processor. |
| `parsing_prompt text` | Trimmed, 1–8,000 characters. It may explain interpretation but may not supply actual transaction values. |
| `version integer` | Starts at 1 and increments on every edit for optimistic concurrency. |
| `archived_at timestamptz` | Null when active. Templates are archived/unarchived, not hard-deleted through the product. |
| timestamps | `created_at`, `updated_at`. |

Indexes: unique `(user_id, lower(btrim(title)))`; `(user_id, archived_at, updated_at desc)` for the list.

### `private.bulk_import_template_accounts`

The template's default Accounts: one or more for generic types and exactly one Credit Card for `credit_card_bill`.

- Columns: `user_id`, `template_id`, `account_id`, `sort_order`, `created_at`.
- Primary key `(template_id, account_id)` and unique `(template_id, sort_order)`.
- Composite FKs enforce that the template and Account have the same owner.
- Account active state is checked by Go when creating/editing a template and again when creating a batch; a soft-deleted Account cannot be selected for new work.
- Template mutation validates that `credit_card_bill` has exactly one Account whose type is `credit_card`; other types require one or more Accounts.
- Archiving or deleting an Account does not rewrite historical batch snapshots.

### `public.bulk_import_batches`

Owner-readable progress projection and Realtime source. All mutations go through Go.

| Column | Contract |
| --- | --- |
| `id`, `user_id` | UUID primary key and owner FK; unique `(user_id, id)`. |
| `template_id` | Nullable reference for navigation only; history does not depend on current template content. |
| `template_version` | Positive snapshot version. |
| `title_snapshot` | Required 1–100 character title. |
| `document_type_snapshot` | Same closed document-type set. |
| `parsing_prompt_snapshot` | Required and at most 8,000 characters. |
| `status` | `draft`, `queued`, `running`, `cancelling`, `completed`, `completed_with_errors`, `failed`, or `cancelled`. |
| counters | Non-negative file, document, page, parsed-candidate, created, attached, review, failed, and duplicate counts. |
| cancellation | Nullable `cancel_requested_at`; cancellation never rolls back already committed results. |
| errors/timestamps | Bounded redacted `error_summary`, `started_at`, `completed_at`, `created_at`, `updated_at`. |

Checks enforce terminal timestamps and status/cancellation consistency. Indexes: `(user_id, created_at desc, id desc)` for history and a partial `(user_id, status, created_at)` for active batches. Only one active batch per user is **not** required; per-user concurrency is limited in the API/worker instead.

### `private.bulk_import_batch_accounts`

Immutable Account catalogue used by one batch. It is copied from the template defaults and replaced by a temporary batch override only while the batch is still a draft.

- `user_id`, `batch_id`, `account_id`, `account_ref`, `sort_order`.
- Snapshot columns: `account_name`, `institution_name`, `account_type`.
- `account_ref` is an opaque batch-local value such as `account_1`; it is not a database ID.
- Unique `(batch_id, account_id)`, `(batch_id, account_ref)`, and `(batch_id, sort_order)`.
- Composite owner FKs to the batch and Account.
- At least one active Account is required at batch creation and rechecked at submission.
- Batch creation, override, and submission require exactly one active `credit_card` Account when `document_type_snapshot = 'credit_card_bill'`; other document types retain one-or-more Accounts.

Only `account_ref`, name, institution, and type are sent to the LLM. Database IDs, Account metadata, matching keys, notes, balances, and other users' Accounts are never included.

### `private.bulk_import_documents`

One logical document group. A PDF always owns its own document row. Each uploaded image starts in its own row, after which draft images can be regrouped and reordered.

- `id`, `user_id`, `batch_id` and a preallocated unique `source_scope_id` UUID.
- `sort_order` and optional safe display label.
- Status: `draft`, `queued`, `preparing`, `parsing`, `aggregating`, `reconciling`, `completed`, `completed_with_errors`, `failed`, or `cancelled`.
- `attempt_generation` increments when the user retries the document.
- Bounded, schema-versioned `document_summary jsonb` stores validated processor-specific output; it is null for generic documents and contains only the normalized bill header for `credit_card_bill`.
- Non-negative page/candidate/result counters, bounded `error_summary`, lifecycle timestamps.
- Nullable `data_source_id`; after submission it equals the preallocated `source_scope_id` and references the owner-matched `private.data_sources` row.

The preallocated source scope permits safe object paths before the source row is created. Submission creates the immutable source and sets the FK in one transaction. The document-to-source FK uses `on delete cascade`, so deleting submitted raw evidence removes its bulk document, files, chunks, and candidates after the cleanup outbox has captured the exact object paths.

The inserted `private.data_sources` row uses `source_type = 'bulk_upload_document'`, `provider = 'user_upload'`, and a null `provider_message_id` because user uploads intentionally have no permanent provider tombstone. Its immutable `raw_data` contains only the batch/document IDs, document type snapshot, ordered file metadata, and Storage paths; file bytes stay in Storage. Its aggregate parse status does not replace candidate status: a valid multi-candidate parse can be `parsed` while individual candidates remain in Review.

### `private.bulk_import_files`

One signed-upload reservation and its verified object metadata.

- `id`, `user_id`, `batch_id`, `document_id`, `sort_order`.
- Sanitized display filename, declared MIME type, declared byte size, and client-computed SHA-256.
- Server-computed MIME type, byte size, and SHA-256 after worker verification.
- Exact Storage object path.
- Status: `reserved`, `uploaded`, `verified`, `failed`, or `cleanup_pending`.
- `reservation_expires_at`, `finalized_at`, timestamps, bounded error.

Checks enforce 1–5,242,880 bytes, the bucket MIME allowlist, 64 lowercase hexadecimal checksum characters, and non-negative ordering. Unique `(document_id, sort_order)` and unique `(batch_id, storage_object_path)` prevent accidental reuse. Index `(user_id, verified_sha256)` supports owner-only duplicate warnings but is deliberately not unique because intentional re-import is allowed.

### `private.bulk_import_chunks`

Durable orchestration state for bounded LLM requests.

- `id`, `user_id`, `batch_id`, `document_id`, `attempt_generation`, `chunk_index`.
- A bounded JSON object describing ordered source file/page references; no file bytes.
- Status: `queued`, `parsing`, `valid`, `partially_valid`, `failed`, or `cancelled`.
- Page count, valid/invalid candidate counts, bounded error, timestamps.
- Unique `(document_id, attempt_generation, chunk_index)`.

Chunk rows allow parallel work, retry visibility, and exact progress without placing large manifests in job payloads.

### Reuse of `private.source_parse_attempts`

Each provider call inserts one existing parse-attempt row against the document's `data_source_id`. `bulk_import_chunk_id` plus a positive `attempt_ordinal` is unique, so automatic retries append immutable audit rows instead of overwriting an earlier call. `request_metadata` adds batch ID, document ID, generation, chunk ID/index, page manifest, template snapshot version, document type, and selected Account refs. Existing fields retain:

- assembled system prompt and prompt components;
- normalized text/manifest input;
- exact provider request and response;
- exact model output;
- strict-decoding result;
- attachment usage and validation/error status.

The same bounded audit envelope is written when the provider fails or strict
decoding rejects its output. It retains every boundary reached: model name,
assembled prompt, normalized input/manifest, prompt components, provider
request/response, and raw model output when available. A bounded error summary
includes the provider or decoder diagnostic. Non-object raw bodies are wrapped
under an audit-only field so malformed output remains inspectable without being
mistaken for a validated candidate. Credentials and authorization headers never
enter this envelope.

Add nullable `bulk_import_chunk_id` and `attempt_ordinal` with an owner-matched FK and a unique constraint per provider call. A user-requested document retry creates a new chunk generation; a transient call retry increments the ordinal on the same chunk. Earlier audits remain immutable.

### `private.bulk_import_candidates`

One model-proposed transaction within one document.

- `id`, `user_id`, `batch_id`, `document_id`, `data_source_id`, `source_parse_attempt_id`.
- `attempt_generation`, output ordinal, server-computed candidate fingerprint.
- Credit Card bill candidates additionally require an explicit one-based `line_index` and a `line_kind` of `activity`, `refund`, `fee`, `interest`, or `payment` in the validated payload.
- Strictly validated `parsed_candidate jsonb` using the existing candidate/evidence shape plus bulk-only Account selection fields.
- Server-resolved `account_id` when safe; the untrusted model `account_ref` remains in the audit/candidate JSON.
- Status: `pending_reconciliation`, `created`, `attached`, `review_required`, `duplicate`, `failed`, `cancelled`, or `superseded`.
- Nullable `transaction_id`, `duplicate_of_candidate_id`, bounded `reconciliation_reason`/error, timestamps.

Indexes support `(user_id, status, created_at desc)`, `(batch_id, document_id, status)`, `(user_id, account_id, created_at)`, and `(document_id, attempt_generation, fingerprint)`. A unique `(document_id, attempt_generation, output_ordinal)` makes aggregation retry-safe while deliberately leaving fingerprints non-unique so repeated evidence is retained and marked duplicate. A unique owner/source identity supports safe evidence FKs.

### Evidence-link extension

Add nullable `bulk_import_candidate_id` to `private.transaction_data_sources`.

- Existing Gmail/phone links keep it null and retain the current rule: one active transaction link, or exactly two links when they are the legs of one internal transfer.
- A bulk-document link must identify an owned candidate belonging to the same `data_source_id`.
- Each bulk candidate may have one active transaction link, or exactly two links when those transactions form its internal-transfer pair.
- One bulk document may therefore support many transactions without granting one extracted candidate unlimited links.
- The existing active unique `(transaction_id, data_source_id)` remains: a single document can be evidence for a canonical transaction only once, including across retries.
- Deferred constraint triggers validate source type, candidate ownership, candidate/source relationship, cardinality, and transfer pairing at commit.

Evidence roles use the existing vocabulary: physical receipts and invoices use `merchant_receipt`; other uploaded document types use `other` until the product defines a new evidence role.

This changes the cardinality scope, not the safety rule. It avoids cloning the same raw file into one `data_sources` row per extracted transaction and allows one deletion to clean the complete document correctly.

## Ownership, RLS, grants, and Realtime

- Enable RLS explicitly on every new public and private table.
- Revoke all privileges on every new table from `public`, `anon`, and `authenticated` before granting anything back.
- Grant `authenticated` only `SELECT` on `public.bulk_import_batches`; its policy is `using ((select auth.uid()) = user_id)`.
- Do not grant browser roles any access to private template, Account snapshot, document, file, chunk, candidate, audit, job, or evidence-link tables. Go returns safe projections.
- Do not grant browser roles direct insert/update/delete on batches; these are multi-row workflows with invariants and remain Go-only.
- Add `public.bulk_import_batches` to `supabase_realtime` only after confirming the publication exists. RLS remains the authorization boundary; publication membership does not replace it.
- Index every `user_id` used by RLS and every FK column used for joins/cascades.
- Explicit grants are part of the migration because new public tables may no longer be exposed to Data/GraphQL automatically. RLS and grants are separate controls.
- Avoid new `SECURITY DEFINER` functions unless a deferred cross-table invariant requires one. Any such function stays in `private`, uses `set search_path = ''`, performs an explicit owner check, and has `EXECUTE` revoked from `PUBLIC`, `anon`, and `authenticated`.

## Private Storage and upload protocol

Reuse `transaction-attachments` with its current private flag, 5 MiB object limit, and allowlist:

- `application/pdf`
- `image/bmp`
- `image/jpeg`
- `image/png`
- `image/tiff`
- `image/webp`
- `image/heic`

The API also enforces at most 20 files and 50 MiB declared/verified bytes per batch. The bucket enforces only per-object MIME/size; batch totals remain an application/database invariant.

Reservation creation locks the owned batch row and recomputes the count and declared-byte total of all non-failed reservations before inserting, so concurrent requests cannot exceed either batch limit. Submission repeats the calculation from verified server values. Document preparation similarly locks the document row before recording its verified page total and fails the document if that total exceeds 50.

Object path:

```text
<user_uuid>/<source_scope_uuid>/<file_uuid>.<canonical_extension>
```

The server generates every segment and canonical extension. User filenames are display metadata only and never become object paths.

Upload sequence:

1. Authenticated React requests a reservation with batch/document ID, filename, MIME type, size, and browser-computed SHA-256.
2. Go verifies draft ownership, quotas, allowed MIME, Account/template snapshots, and duplicate checksum history. It inserts a reservation and asks Supabase Storage for a signed upload token scoped to the exact random path with upsert disabled.
3. React uploads directly with `uploadToSignedUrl`; the service-role key never reaches the browser.
4. React finalizes the reservation through Go. Go verifies the exact owner/batch/path and object existence through the Storage API, then marks it `uploaded`; it does not trust Storage metadata as content validation.
5. Before any model call, the worker downloads at most 5 MiB, verifies the actual byte count, cryptographic digest, MIME signature, and decoder/rasterizer acceptance, then stores the verified values.
6. A mismatch fails that document and queues object cleanup; unverified bytes never reach the LLM.

The application reservation expires after 15 minutes. Application expiry does not claim to revoke an already issued provider token; a late object remains unreachable by product state and is removed by the cleanup workflow.

The migration replaces the earlier single `FOR ALL` restrictive policy with operation-specific restrictive read/update/delete policies and a reservation-gated insert policy. `private.bulk_import_storage_insert_allowed` accepts only the authenticated owner, exact server-generated path, unexpired `reserved` file row, and Draft batch; authenticated users receive no list/read/update/delete policy. A live hosted rehearsal verified signed upload, object stat, five-minute signed read, and deletion against the private bucket.

Never insert, update, or delete `storage.objects` directly. It is read-only metadata; upload, download, signing, and deletion use the Storage API. Submitted documents are viewed through the existing five-minute owner-checked signed download route. Signed download URLs cannot be treated as revocable before expiry, so expiry stays short.

## Document grouping and page processing

- Default: every selected file is a separate document.
- Images may be grouped and ordered while the batch is a draft.
- A PDF is one document and cannot be grouped with other files in v1.
- Maximum 50 pages per document. An image counts as one page; PDF page count is inspected server-side.
- Every accepted page must be attempted. No page may be skipped merely because the document exceeds the provider attachment limit.
- Preparation runs converters in a no-network, resource-bounded process with a private temporary directory, `0600` files, timeout, memory/process limits, and unconditional cleanup.
- PDF pages are rasterized at 144 DPI with `pdftoppm`. BMP/TIFF/WEBP/HEIC conversion uses ImageMagick in the worker runtime and `sips` on macOS development hosts. These binaries are deployment dependencies and run in a private temporary directory under the configured timeout and output-size limits.
- Chunks are contiguous and preserve file/page ordering. Each provider request obeys the existing maximum five visual inputs, 5 MiB aggregate visual bytes, 8 MiB request, one MiB response, and 30-second request timeout.
- Oversized rendered pages are deterministically resized/compressed within legibility bounds. If one page still cannot fit, the chunk fails visibly; it is never silently omitted.
- Chunks contain at most five ordered visual inputs within the configured request and rendered-byte limits. The implementation does not add page overlap; server fingerprints and canonical reconciliation still prevent exact duplicates from creating duplicate transactions.

## Prompt assembly and model contract

Bulk Insert uses build-embedded, versioned text assets under
`backend/internal/bulkprompt/prompts/`. The platform, generic-document, and
Credit Card bill contracts are reviewed separately from Go source and are not
editable in the UI. The assembled system message is stable:

```text
1. Immutable bulk-transaction extraction contract
2. Immutable document-type guidance
3. Saved parsing prompt snapshot (owner-authored, delimited, subordinate)
4. Output-schema and evidence-path contract
```

One provider request also carries a user message containing:

```json
{
  "document": {
    "type": "credit_card_bill",
    "chunk_index": 0,
    "page_manifest": ["file[0].page[1]", "file[0].page[2]"]
  },
  "allowed_accounts": [
    {
      "account_ref": "account_1",
      "name": "DBS Live Fresh",
      "institution": "DBS",
      "account_type": "credit_card"
    }
  ]
}
```

The ordered page images follow in the same multimodal request. The model receives no database UUIDs, matching keys, Account numbers, Account metadata, notes, user ID, or other batch/template data.

The response is one strict JSON object. For example, a Credit Card bill chunk
with one selected Account and no visible statement values or lines returns this
valid raw model shape:

```json
{
  "schema_version": 1,
  "document_summary": {
    "card_account_ref": null,
    "period_start": null,
    "period_end": null,
    "statement_date": null,
    "due_date": null,
    "settlement_currency": null,
    "amount_due_minor": null,
    "minimum_payment_minor": null,
    "previous_balance_minor": null,
    "evidence": []
  },
  "transactions": []
}
```

`document_summary` is null for the generic processor. For `credit_card_bill`,
it is required and every declared key is nullable when the document does not
provide a trustworthy value. Every populated source-derived value requires an
evidence entry with its exact `document_summary.<field>` name. Summary amounts
are bill metadata only; they never create canonical transactions or enter
balance/spending calculations.

The embedded generic and Credit Card contracts contain the complete candidate
shape and permitted evidence-field names. They require positive integer minor
units, uppercase currencies, a calendar `occurred_on`, constant
`time_precision = "date"`, exact debit/credit values, and bounded optional
fields. Line items require schema version 1, a non-empty description, positive
integer quantity, the candidate currency, nullable non-negative minor-unit
amounts, and a JSON object for flexible details. Credit Card lines additionally
require a unique positive `line_index` and `line_kind` of `activity`, `refund`,
`fee`, `interest`, or `payment`, with direction fixed by the line kind.

Each chunk returns only summary values visible in its own pages. Aggregation combines compatible non-null values. Conflicting period, Account, currency, due-date, or amount-due values are retained as review evidence and must not be guessed or silently selected.

The exact JSON schema is versioned and shared by the prompt, strict Go decoder, tests, and audit. Unknown fields, trailing JSON, more than 100 candidates in one chunk, invalid currencies/minor units/dates, invalid line items, or unsupported evidence paths are rejected.

Bulk evidence paths must both match the server-issued `file[n].page[n]` grammar
and equal one entry in that chunk's exact `page_manifest`. A syntactically valid
path for another page is rejected. Duplicate evidence for one field is rejected.
Model confidence is advisory; server validation and derived minimum field
confidence remain authoritative. File content is explicitly declared untrusted
evidence, never instructions. The model has no tools and cannot influence object
paths, SQL, job types, Account IDs, or network destinations.

The Bulk v1 schema requires `occurred_on` as a calendar date and `time_precision = "date"`. The normalized candidate uses `12:00:00Z` as a non-display placeholder, and matching compares the UTC calendar day. The placeholder must never be presented as a source-provided time; exact-time evidence from other source types retains the existing inclusive ten-minute matcher.

## Account-reference resolution

1. At submission, Go locks and revalidates every selected Account as active and owned, then freezes the Account snapshots and refs.
2. With exactly one Account, the raw model contract uses an empty transaction `account_ref` and nullable bill `card_account_ref`; Go deterministically writes the sole ref before validation and requires no page citation for that server-owned value. A model-supplied alternative cannot retag the candidate.
3. With multiple Accounts, the model must return one supplied `account_ref` and cite the exact manifest page supporting that selection. Go performs an exact lookup in the immutable batch snapshot.
4. A missing, unknown, duplicated, uncited, or off-manifest ref fails strict chunk validation and remains available through failed-attempt Debug. A structurally valid selection that conflicts with typed Account evidence produces candidate-level Review, never a guessed Account.
5. If typed Account evidence resolves through existing matching keys to a different owned Account than the selected ref, the candidate goes to Review.
6. Account names/institution/type help extraction only. They never authorize access or override the server mapping.
7. Account correction during Review accepts an Account ID through Go, which independently checks ownership and active state.
8. A Credit Card bill batch has exactly one selected active Credit Card Account, which the server binds as the bill Account. Contradictory document evidence keeps the bill in Review; the model can never choose a different Account by name alone.

## Asynchronous jobs, cancellation, and retry

Extend the existing job-type constraint and router with narrowly scoped kinds:

- `bulk_document_prepare`
- `bulk_document_chunk_parse`
- `bulk_document_aggregate`
- `bulk_candidate_reconciliation`
- `bulk_document_post_process`

Job payloads contain only UUIDs; manifests and prompt snapshots are loaded from owner-matched rows. Add nullable batch/document/chunk/candidate FK columns and a typed positive `attempt_generation` to `private.transaction_jobs`, with scope checks requiring the correct IDs for each kind. Partial unique indexes prevent more than one queued/running job for the same owner, generation, and logical unit.

Processing rules:

- Claim remains atomic with `FOR UPDATE SKIP LOCKED`; provider/Storage/conversion work happens after commit.
- Chunk completion atomically records its audit/result and queues aggregate work only when all chunks in that document generation are terminal.
- Aggregation writes candidate rows and queues reconciliation in one short transaction.
- Candidate reconciliation repeats matching under the existing per-user lock before any automatic create.
- Post-processing starts only after every candidate in the document generation is terminal. It dispatches to the server-owned processor, records one idempotent domain result, and never calls the model again.
- At-least-once delivery is expected; unique constraints and state-checked updates make duplicate deliveries no-ops.
- Ordinary attempts retain the existing maximum of five with exponential backoff. Invalid model/schema output is a visible terminal candidate/document failure rather than a blind automatic retry unless the error class is transient.

Cancellation is cooperative:

- A cancel request sets `cancel_requested_at`, changes the batch to `cancelling`, and atomically cancels queued jobs.
- An active Storage/conversion/LLM call is allowed to return. Its bounded attempt audit is retained, but before persisting candidates or creating/linking a transaction, the worker rechecks cancellation and relinquishes/cancels remaining work.
- Existing committed transactions and evidence links are not rolled back. The final status and counters make partial completion explicit.
- A batch becomes `cancelled` only when no job remains queued/running.

Retry is per document:

- Only failed or completed-with-errors documents are retryable.
- A Credit Card bill document cannot be retried while a retained bill references it. A Review-stage bill may be discarded first; Unpaid, Paid, and Void bills retain their pinned evidence generation.
- Retry increments `attempt_generation` and uses the batch's original title, document type, prompt, and Account snapshots even if the template was later edited.
- Verified originals are reused; the browser does not upload again.
- New chunk/parse audits are appended. Prior audits remain visible.
- Already created/attached results remain. New candidates still pass same-batch and canonical deduplication, and the active `(transaction_id, data_source_id)` uniqueness prevents duplicate evidence links from the same document.

## Deduplication and reconciliation

### Inside one batch

Before jobs reach canonical reconciliation, aggregation computes a versioned fingerprint from server-resolved Account, direction, exact minor amount, currency, normalized occurred time, normalized references, and normalized merchant/title. Exact fingerprints caused by repeated screenshots or optional page overlap are marked `duplicate` and point to the earliest candidate. Similar-but-not-exact candidates are not merged automatically.

Across different document groups, duplicates may represent useful corroborating evidence. They proceed to canonical reconciliation so each distinct document can attach once to the same transaction.

### Against existing transactions

Reuse the existing conservative matcher:

- same owned Account;
- same debit/credit direction;
- exact original minor amount;
- compatible original currency;
- within the inclusive ten-minute window;
- reference and normalized merchant remain explainable scoring signals.

Automatic creation keeps the existing confidence/corroboration gates, adapted so visual evidence and a server-resolved selected Account can establish eligibility. Multiple matches always require Review.

### Conservative enrichment

On a unique existing match:

- Always attach the new document evidence, subject to candidate cardinality and document/transaction uniqueness.
- Never overwrite a transaction with `user_modified_at` set.
- Fill only clearly absent optional fields on untouched automatic transactions: `merchant_name`, `sgd_amount_minor`, `category_id`, empty line items, and missing internal references.
- Never change Account, direction, original amount/currency, or canonical occurrence time during enrichment.
- Treat timestamps inside the existing ten-minute match window as the same event and retain the canonical timestamp.
- If both existing and candidate line items are non-empty and differ after canonical JSON normalization, send the candidate to Review instead of attaching or patching.
- Conflicting Account, direction, amount, currency, or out-of-window date never qualifies as an automatic attach.
- Different titles/merchant wording from distinct evidence sources do not overwrite canonical data; both remain inspectable in evidence/audit.

Enrichment and evidence attachment occur atomically after a repeated in-transaction match. Field-level before/after values are recorded in bounded reconciliation metadata.

### Internal transfers

Any candidate explicitly marked as a possible transfer, or a detected pair of opposite directions sharing a reference/amount within the batch, goes to Review. The user chooses the two active owned Accounts. The existing atomic internal-transfer operation creates or links one outgoing debit and one incoming credit joined by `private.transaction_links`. No bulk candidate auto-creates only one transfer leg.

### Credit Card bill post-processing

After the document's candidates reach terminal reconciliation outcomes, the `credit_card_bill` processor calls the Account Balances domain with the owned document ID, attempt generation, validated bill summary, and candidate outcome IDs. The receiving service loads all rows server-side and idempotently:

1. creates or updates the one reviewable bill for that exact document generation;
2. links statement lines to candidates that attached to or created Credit Card transactions;
3. leaves ambiguous, failed, or conflicting candidates as statement-line Review items;
4. searches for exactly one existing Bank-to-Card internal-transfer link with the same Credit Card Account, amount due, settlement currency, and occurrence time between statement date and due date, inclusive;
5. marks the bill Paid when that exact transfer exists, otherwise Unpaid; and
6. records a unique Bank-debit-only suggestion for review when its amount, currency, date window, and normalized payee/reference indicate the card issuer but no Card credit leg exists.

Confirming a Bank-debit-only suggestion reuses a Transactions domain operation that locks the existing Bank debit, creates the missing Credit Card credit, and joins the two with one internal-transfer link. It never edits the original debit's source evidence or creates a second Bank leg. Ambiguous payment candidates are shown for manual selection and never mark the bill Paid automatically.

## HTTP API surface

All endpoints require a valid Supabase bearer token; the resolved Auth UUID is the only user identity.

Templates:

- `GET /v1/transactions/bulk-import/templates?include_archived=`
- `POST /v1/transactions/bulk-import/templates`
- `PATCH /v1/transactions/bulk-import/templates/{id}` with `expected_version`
- `POST /v1/transactions/bulk-import/templates/{id}/archive`
- `POST /v1/transactions/bulk-import/templates/{id}/restore`

Batches and uploads:

- `GET /v1/transactions/bulk-import/batches` with keyset pagination
- `GET /v1/transactions/bulk-import/batches/{id}`
- `POST /v1/transactions/bulk-import/batches` with template ID and optional batch-only Account override
- `POST /v1/transactions/bulk-import/batches/{id}/files/reservations`
- `POST /v1/transactions/bulk-import/batches/{id}/files/{file_id}/finalize`
- `PUT /v1/transactions/bulk-import/batches/{id}/documents` for draft-only image grouping/order
- `POST /v1/transactions/bulk-import/batches/{id}/submit`
- `POST /v1/transactions/bulk-import/batches/{id}/cancel`
- `DELETE /v1/transactions/bulk-import/batches/{id}` for Draft batches and Cancelled batches whose submitted sources have already completed staged deletion
- `POST /v1/transactions/bulk-import/documents/{id}/retry`
- `DELETE /v1/transactions/bulk-import/documents/{id}` for eligible terminal documents; submitted documents are removed by deleting their raw source through the existing cleanup workflow

Review/evidence:

- `GET /v1/transactions/bulk-import/batches/{id}/candidates`
- batch/document responses include an owner-safe specialized-result link when post-processing created a Credit Card bill; bill mutations remain in the Account Balances API
- candidate-aware attach/create/Account-correction/internal-transfer actions reuse the current transaction action service and invariants rather than duplicating SQL
- submitted document files reuse owner-checked attachment listing/signing; draft previews use an owner-checked batch file route
- source/document deletion reuses staged database deletion plus durable Storage cleanup

Prompt/debug:

- Extend the existing prompt-preview endpoint with `mode: "bulk_template"`, a template ID, and optional Account override.
- Extend the existing source Debug endpoints to list chunk attempts and load exact bounded fields by attempt ID.

Mutation endpoints use idempotency keys or resource/version preconditions. Digests of client keys, canonical request hashes, bounded responses, and a 24-hour expiry live in server-only `private.api_idempotency_records`; raw keys are never stored. Repeating create, submit, cancel, finalize, delete, or retry returns the existing state rather than duplicating work.

## Frontend boundaries

Navigation order becomes: Transactions, Bulk Import, Credit Card, Prompt Preview, Global Settings, Settings.

Bulk Import contains:

- a Templates view for create/edit/archive/restore;
- a New Batch flow for template selection, temporary Account override, drag/drop uploads, checksum warnings, image grouping/order, and submission;
- batch history with Realtime progress and polling fallback;
- document results with independent retry and clear partial-success counts;
- candidate-level Review with Account correction, attach/create, and internal-transfer actions for generic documents; Credit Card bill uncertainty is reviewed only in Credit Card;
- a specialized-result card linking a processed Credit Card bill to its Bills review page; Bulk Import remains the only upload interface;
- file evidence preview through short-lived signed URLs;
- a Debug button exposing the existing bounded audit sections.

The client validates limits for fast feedback, but server and bucket enforcement are authoritative. Leaving/reloading the page does not stop submitted work. Template or Account override changes invalidate any prompt preview. Dialogs follow the existing Escape-to-close behavior unless a non-cancellable mutation is actively committing.

## Prompt Preview and Debug

Bulk Prompt Preview is side-effect free:

- loads an owned template and optional owned Account override;
- assembles the exact production system prompt and provider request envelope;
- includes Account refs/names/institutions/types;
- replaces all dynamic file/page content with explicit placeholders;
- performs no upload, provider call, job enqueue, audit insert, or transaction write;
- returns `Cache-Control: no-store`.

Document Debug reuses the current component and authorization checks. It adds chunk/generation context and candidate reconciliation outcomes. Exact prompt, normalized manifest, provider request, provider response, model output, and prompt components remain separately loadable within current byte ceilings. Secrets and authorization headers are never stored. Logs contain IDs, statuses, durations, sizes, and redacted error classes—not prompts, Account names, filenames, document text, image bytes, or model output.

## Deletion and retention

- Originals and audits have no automatic retention deadline in the current product.
- Archiving a template preserves all batches and snapshots.
- Deleting a Draft batch removes its private rows and queues cleanup for every reserved/uploaded path. Deleting a Cancelled submitted batch first stages deletion of each raw source and succeeds only after protected dependencies are absent; it cannot bypass source cleanup.
- Deleting one eligible terminal document follows the same rule: a draft document may be removed directly, while a submitted document is deleted through its `data_source_id` so dependants and Storage cleanup remain atomic/durable.
- Deleting a submitted document invokes the existing raw-source deletion transaction. Candidate rows, attempts, jobs, and evidence links cascade; untouched automatically-created transactions with no other evidence may be removed under the existing rule; user-modified, transfer-linked, manual, or otherwise evidenced transactions remain.
- A retained Credit Card bill restricts deletion of its backing document. The Bills workflow must first discard an eligible draft/review bill or retain the evidence for an Unpaid, Paid, or Void bill; it never deletes the Storage object itself.
- Storage deletion is never performed by editing `storage.objects`. Exact paths are carried in a durable cleanup job until the Storage API confirms deletion.
- A checksum alone is not retained as a deletion tombstone for user uploads; intentional re-import is allowed.

## Security and abuse controls

- Enforce user ownership at API lookup and composite FK boundaries; never accept a `user_id` body/query parameter.
- Keep all template prompts, raw files, Account snapshots, candidates, jobs, and audits private to Go.
- Require active owned Accounts at draft creation, override, submission, Review, and transfer creation.
- Use random server-generated object paths, non-upserting signed uploads, short application reservations, strict batch/file/page quotas, and per-user active-work limits.
- Verify content magic, decoded dimensions/page count, actual checksum and size before parsing. Reject polyglots or malformed files when the trusted decoder does not accept the declared format.
- Run converters without shell interpolation or network access and with time, memory, output-size, page-count, and process limits.
- Treat document content and EXIF/PDF metadata as prompt-injection-capable evidence. The immutable platform prompt forbids following document instructions.
- Strip metadata from rendered provider images when feasible; never follow embedded links or remote resources.
- Strictly decode model JSON, cap arrays/strings, validate evidence paths, derive confidence server-side, and map Account refs only through the frozen owner snapshot.
- Apply request rate limits and worker concurrency limits per user so 20-file/50-page batches cannot monopolize model or conversion capacity.
- Use the transaction-pooler-compatible database pattern: no session state, temp tables, `LISTEN/NOTIFY`, or long transactions.

## Observability

Capture without source contents:

- batches created/submitted/cancelled and terminal outcome;
- queue delay, lease renewal/loss, attempt count, and job duration by kind;
- upload reservation/finalization/verification failures by safe class;
- file bytes, page count, rendered bytes, chunk count, and conversion duration;
- provider latency, response status class, model name, and token usage when the provider returns it;
- strict-decoder/validation failure class;
- candidate created/attached/review/duplicate/failed counts;
- reconciliation reason and enrichment field names, not values;
- cleanup age and retry depth.

Alerts should cover stuck active batches, expired leases, repeated provider failures, cleanup backlog, and a divergence between batch counters and underlying document/candidate states. A deterministic counter-recompute function or repository query should repair projections without replaying model calls.

## Migration plan

1. Forward migration `20260904043716_create_bulk_import_foundation.sql` creates template, template-Account, batch, batch-Account, document, file, chunk, candidate, and API-idempotency tables with checks, composite FKs, indexes, RLS, explicit grants, the bounded document-summary field, and owner-safe uniqueness required by processors.
2. Extend `data_sources.source_type` with `bulk_upload_document` and define the immutable bulk raw-data shape.
3. Extend `source_parse_attempts`, `transaction_jobs`, and `transaction_data_sources` with owner-matched bulk references, scope checks, indexes, revised deferred cardinality triggers, and the post-processing job kind.
4. Add the owner-read-only batch RLS policy/grant and Realtime publication membership.
5. Reassert the existing private Storage bucket settings. Do not mutate Storage object metadata directly.
6. Add pgTAP upgrade, constraint, ownership, grants, RLS, cascade, cardinality, and Storage-policy tests.
7. Implement the shared backend repository/domain code, generic processor, API endpoints, and worker job kinds; then connect the Credit Card bill processor through its narrow Account Balances interface before exposing that document type.
8. Deploy schema before binaries that enqueue new job kinds. Start workers capable of both old and new kinds before exposing the Bulk Import UI.

The migrations were authored through the repository's imperative Supabase migration workflow and applied to hosted development in forward order. `20260904061318_disambiguate_credit_card_validation_records.sql` replaces three validation functions with alias-unambiguous equivalents after lint identified PL/pgSQL record-name ambiguity; it changes no product behavior. Local and remote migration histories match, and no local Docker Supabase stack was used.

## Rollout and rollback

Roll out behind a frontend/API feature flag:

1. Apply additive schema and bucket assertions.
2. Deploy compatible API/worker with the feature disabled.
3. Run a private canary covering one receipt, one multi-page statement, grouped screenshots, duplicates, cancellation, retry, and deletion.
4. Enable for the development user, observe queue/provider/cleanup metrics, then widen.

Rollback order:

- Disable new batch creation/submission first.
- Let compatible workers drain or cancel queued bulk jobs; retain read/debug/history access.
- Roll back application code only to a version that safely ignores the added job kinds and columns.
- Do not drop bulk tables or source/evidence columns while batches, evidence links, or cleanup jobs exist. A destructive schema rollback requires an explicit development-data deletion plan and Storage cleanup verification.

## Verification coverage and current record

Database:

- pgTAP proves allowed/denied grants and owner isolation for `anon`, two authenticated users, and server paths.
- Constraint tests cover title uniqueness, prompt length, document/MIME/file/batch/page limits, Account ownership, immutable snapshots, statuses, terminal timestamps, and FK cascades.
- Concurrency tests prove duplicate submit/finalize/retry/job delivery is idempotent, same-user automatic creates serialize, and candidate-scoped evidence cardinality cannot be bypassed.
- Source deletion tests cover many candidate links, automatic transaction cleanup rules, and durable Storage cleanup.

Go:

- Strict API decoding and authorization tests for every endpoint.
- Signed upload path/token tests, MIME/magic/checksum/size tests, page-count and converter resource-limit tests.
- Prompt assembly golden tests and strict multi-candidate decoder/evidence tests.
- Account-ref mapping tests for deterministic sole-Account binding and cited
  multiple-Account selection, including invalid, duplicated, off-manifest, and
  conflicting values.
- Chunk aggregation and exact same-batch duplicate tests.
- Reconciliation tests for attach/create/review, conservative enrichment, user-edit protection, line-item conflict, and internal transfers.
- Worker crash/lease expiry, transient retry, terminal invalid output, cancel-during-call, retry-generation, and progress recomputation tests.

Frontend:

- Template CRUD/archive/version-conflict behavior.
- File limits, checksum warning with deliberate override, image grouping/order, PDF isolation, upload/finalize recovery, submit/cancel/retry.
- Realtime-to-polling progress behavior, empty/error/partial-success states, and per-candidate Review.
- Prompt Preview contains Account descriptors and placeholders but no file content or database IDs.
- Debug exact-field access is owner-only and no source contents leak to console/logs.

Hosted acceptance:

- Migration history matches and database advisors have no unresolved security errors.
- Private bucket remains private with the exact 5 MiB/MIME restrictions.
- Cross-user Data REST, Go API, Realtime, signed upload, download, and Debug access are denied.
- An isolated representative image batch traverses authenticated API creation,
  signed upload/finalization, submission, worker parsing, strict validation,
  reconciliation, evidence/debug reads, and cleanup.

### Status at this documentation update

| Gate | Status |
| --- | --- |
| Hosted migrations and history | **Passed.** `20260904043716`, dependent `20260904043721`, and forward lint repair `20260904061318` are applied in order and local/remote histories match. |
| Hosted database tests | **Passed.** All 28 focused Bulk Import pgTAP assertions pass. |
| Hosted database lint | **Passed.** Public and private schema lint reports no errors after the forward repair. |
| Private Storage protocol | **Passed.** A live synthetic file completed signed upload, object stat, signed read, and deletion without exposing the service-role key to the browser. |
| Provider contract | **Passed.** A live compatibility call to `qwen3.8-flash` returned the expected structured JSON with thinking disabled. This isolated call checks the request/response boundary; complete batch coverage is recorded separately below. |
| Go runtime | **Passed.** The API and worker start against hosted Supabase; Bulk routes reject unauthenticated requests, and the implemented route/domain/worker tests pass within the full Go suite. |
| Frontend | **Passed.** Lint and production build pass against the real Bulk API integration. |
| Isolated hosted API-to-worker E2E | **Passed.** A temporary authenticated user uploaded an image through a signed reservation; signed readback matched the uploaded byte count and SHA-256; the submitted batch reached a terminal result with one created candidate; signed evidence and complete owner-scoped Debug audit were readable; and all temporary Auth, database, and Storage data was removed and verified absent. This scoped run did not restart or claim an unrestricted production worker. |
| Failed-attempt diagnostics | **Passed.** Provider and strict-decoder failures retain bounded prompt/input, provider request/response, raw model output when available, prompt components, model name, validation state, and decoder/provider detail without persisting credentials or authorization headers. |

## Operational tuning and future canary coverage

These do not block the delivered implementation, but should be rechecked when the provider, Storage release, worker image, or representative document set changes:

- Provider-side signed-upload-token expiry/reuse semantics may vary independently of the enforced 15-minute application reservation.
- The deployed worker image must include `pdftoppm` and ImageMagick; macOS development uses `sips` for non-native image conversion.
- The current 144-DPI rendering and no-overlap chunking should be calibrated again against materially different statement layouts.
- The strict 100-candidate-per-chunk ceiling may require document-specific tuning for unusually dense statements.
- Whether provider token-usage fields are consistently returned by the configured OpenAI-compatible endpoint; observability must tolerate their absence.
- Future canaries should repeat the completed API-to-worker path while extending
  representative coverage to multi-page input, grouped screenshots, duplicate
  import, cancellation, retry, and evidence deletion.

## References

- [Supabase Storage buckets and private access](https://supabase.com/docs/guides/storage/buckets/fundamentals)
- [Supabase signed uploads](https://supabase.com/docs/reference/javascript/file-buckets-uploadtosignedurl)
- [Supabase Storage schema is read-only metadata](https://supabase.com/docs/guides/storage/schema/design)
- [Supabase Storage access control](https://supabase.com/docs/guides/storage/security/access-control)
- [Supabase file limits](https://supabase.com/docs/guides/storage/uploads/file-limits)
- [Supabase Data API grants and RLS](https://supabase.com/docs/guides/api/securing-your-api)
