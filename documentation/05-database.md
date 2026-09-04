# 05 — Database & Storage

Hosted Supabase **Postgres 17**. This page reflects the **live** linked project
(`wealth-management`, ref `unjvbgyawsrzgwqxxhxt`), verified with
`supabase db dump --linked`, and the migration files in `supabase/migrations/`.

## Two schemas: `public` vs `private`

The security model hinges on this split:

- **`public`** — a small number of tables the browser may reach. Every one has
  **RLS enabled** with owner-scoped policies. Exposed via PostgREST.
- **`private`** — everything sensitive: raw evidence, encrypted tokens, jobs,
  audit rows, matching keys, credit-card internals, bulk-import internals. RLS is
  enabled **and there are no grants to `anon`/`authenticated`**, so the browser
  cannot touch it at all. The Go API/worker reach it with the service role.

There are **no enum types** — categorical columns are `text` with `CHECK`
constraints (e.g. `account_type`, `parse_status`, `status`). This keeps values
greppable and migrations simple; the allowed sets are in the CHECKs.

## `public` tables

### `public.accounts`
The account directory (assets & liabilities). Browser CRUD via RLS.
Key columns: `user_id`, `side` (`asset|liability`), `account_type`, `name`,
`institution_name`, optional `account_identifier`/`notes`, `metadata` jsonb,
`sort_order`, `deleted_at` (soft delete), and opening-balance fields
(`opening_balances` jsonb, `opening_balance_as_of`, `opening_balance_version`).

Notable CHECKs: `side`/`account_type` pairing is enforced —
assets: `bank_account, brokerage, digital_wallet, crypto_wallet, crypto_exchange,
rsu, robo_advisor, retirement_account, other`;
liabilities: `credit_card, personal_loan, other`. Name/institution 1–100 chars,
notes ≤500, `metadata` must be a JSON object, opening-balance shape validated by
`private.opening_balances_are_valid(...)`.

### `public.transactions`
Canonical debit/credit records. Evidence lives in `private.transaction_data_sources`.
Key columns: `user_id`, `account_id`, `transaction_kind` (`debit|credit`),
`title`, optional `merchant_name`, `original_amount_minor` (bigint, >0),
`original_currency` (`^[A-Z]{3}$`), optional `sgd_amount_minor`, `occurred_at`,
`category_id`, `line_items` jsonb array, `details` jsonb object, `review_status`
(`pending|review_required|confirmed`), `match_confidence` (0–100),
`creation_method` (`automatic_source|user_source|manual|internal_transfer|credit_card_statement`),
`time_precision` (`exact|date`), `user_modified_at`.

`details` and `line_items` are validated by `private.transaction_details_are_valid`
and `private.transaction_line_items_v1_are_valid`. `time_precision='date'` forces a
canonical **noon-UTC** timestamp for calendar-day matching.

### `public.transaction_sync_runs`
One row per Gmail refresh. Tracks `status`
(`queued|running|completed|failed|cancelled`) and progress counters
(`messages_found_count`, `sources_saved_count`, `transactions_created_count`,
`sources_linked_count`, `dangling_sources_count`, `review_required_count`,
`sources_parsed_count`, `sources_failed_count`). The SPA subscribes to this via
Realtime for progress.

### `public.transaction_categories`
System-managed global category catalogue (`parent_name`, `name`, `emoji`,
`sort_order`, `active`). Read-only to users.

### `public.bulk_import_batches`
The one bulk-import table in `public` (so the SPA can read batch status).
Holds immutable **snapshots** (`title_snapshot`, `document_type_snapshot`,
`parsing_prompt_snapshot`, `template_version`), a rich `status`
(`draft|queued|running|cancelling|completed|completed_with_errors|failed|cancelled`),
and progress counters. File/document/page counts are capped (≤20 files, ≤20
documents, ≤1000 pages).

## `private` tables (grouped)

You rarely need column-level detail here; treat these as server-owned. Grouped by
domain:

**Transactions core**
- `data_sources` — immutable raw provider inputs (Gmail / phone / bulk upload).
  `source_type ∈ {gmail_email, phone_notification, bulk_upload_document}`,
  `parse_status ∈ {pending,parsing,parsed,review_required,dangling,failed}`.
- `transaction_data_sources` — links transactions ↔ evidence sources.
- `transaction_links` — joins the two legs of an internal transfer.
- `deleted_provider_messages` — one-way tombstones (32-byte digest) so a
  deliberately deleted raw message is never re-ingested.
- `source_parse_attempts` — full parse audit: assembled system prompt, normalized
  input, provider request/response, model output, prompt components. Powers the
  Debug view.
- `transaction_jobs` — the durable job queue (see [02](02-architecture.md)).
- `transaction_user_locks` — per-user serialization.
- `api_idempotency_records` — idempotency keys for privileged mutations.

**Gmail / OAuth**
- `gmail_connections` — encrypted refresh token (`bytea`), `selected_label`
  (default `odin-finance`), `sync_cursor`, `status`.
- `gmail_oauth_states` — single-use, expiring PKCE state (digests + encrypted
  verifier; raw state never stored).

**Parser configuration**
- `source_parser_rules`, `user_source_parser_rules`, `user_parser_settings` —
  default/global/user parsing rules and settings.
- `account_matching_keys` — typed keys used to match evidence to an account
  (unique per `user_id, key_type, normalized_value`). **Never sent to the LLM.**

**Account balances**
- `account_opening_balance_revisions` + `..._revision_amounts` — versioned opening
  balances.
- `transaction_calculation_treatments` — per-transaction spending treatments.

**Credit card**
- `credit_card_statements`, `credit_card_statement_lines`,
  `credit_card_statement_events`, `credit_card_statement_payment_candidates`.

**Bulk import**
- `bulk_import_templates` + `bulk_import_template_accounts`
- `bulk_import_files`, `bulk_import_documents`, `bulk_import_chunks`,
  `bulk_import_candidates`, `bulk_import_batch_accounts`.

## Row-Level Security

- Every `public` table has RLS **enabled** with policies of the form
  `auth.uid() = user_id` for SELECT/INSERT/UPDATE (accounts allow full owner CRUD;
  sync-runs, transactions, and bulk batches are read-scoped to the owner).
- `public.transaction_categories` is world-readable to `authenticated` (`USING (true)`),
  read-only.
- **The critical write policy** — browsers may INSERT into `public.transactions`
  only when the row is a *confirmed manual* transaction owned by the user:
  ```sql
  WITH CHECK (
    auth.uid() = user_id
    AND creation_method = 'manual'
    AND review_status  = 'confirmed'
    AND match_confidence IS NULL
    AND user_modified_at IS NULL
    AND (details - 'user_notes') = '{}'::jsonb
  )
  ```
  Everything else (edits, source-created transactions, transfers, credit-card
  statement transactions) must go through the Go API.
- All `private` tables have RLS enabled **and no browser grants**, so they are
  unreachable from the client even with a valid token.

> When adding a browser-reachable table, you **must** enable RLS and write an
> ownership policy (not merely `TO authenticated`). Treat `auth.users.user_metadata`
> as user-controlled — never authorize on it.

## Storage

One private bucket: **`transaction-attachments`**.
- `public = false`, `file_size_limit = 5 MiB` (5242880 bytes).
- Allowed MIME types: `application/pdf`, `image/bmp`, `image/jpeg`, `image/png`,
  `image/tiff`, `image/webp`, `image/heic`.
- A **restrictive** `storage.objects` policy blocks all `anon`/`authenticated`
  access to this bucket for reads/updates/deletes. Browser uploads are allowed only
  against a **reserved** object path validated by
  `private.bulk_import_storage_insert_allowed(...)` (reserved, unexpired, batch in
  `draft`). All normal access is via **short-lived signed URLs** minted by the Go
  API after an ownership check.

Both the transactions and bulk-import migrations `insert ... on conflict do update`
the same bucket, so the bucket config is idempotent. The **canonical** definition
now lives in `20260904210000_canonical_transaction_attachments_bucket.sql`, which
runs last and therefore always wins on a fresh replay. Change the bucket's limits or
MIME types **there only**; do not add a new `storage.buckets` upsert for this bucket
in another migration.

## Migrations

Migrations live in `supabase/migrations/` and are timestamp-prefixed. Current set
(oldest → newest):

```
20260902091057_create_accounts
20260902093500_tighten_accounts_privileges
20260902190000_add_digital_wallet_account_type
20260902191000_create_transactions_foundation      (big: sources, jobs, storage bucket, RLS)
20260902205500_revoke_public_rls_event_trigger_execution
20260902211000_harden_transaction_oauth_state_and_jobs
20260902230000_complete_transaction_operations
20260902230001_add_transaction_configuration_and_audit
20260902230002_add_durable_source_deletion
20260902230003_keep_source_cleanup_retrying
20260903014725_allow_manual_transaction_inserts     (the confirmed-manual insert policy)
20260903055808_add_global_source_rule_editor_metadata
20260903123917_expand_account_types
20260904043716_create_bulk_import_foundation        (big: bulk tables + storage policies)
20260904043721_create_account_balances_and_credit_card_bills
20260904061318_disambiguate_credit_card_validation_records
```

### pgTAP tests

`supabase/tests/*.test.sql` are pgTAP suites covering RLS, constraints, OAuth state,
operations, source deletion/recovery, configuration, manual insert, global rules,
bulk-import foundation, and credit-card balances.

## Working with the database (Supabase CLI)

The CLI is already authenticated and **linked** to the dev project. Useful, safe
commands (run from repo root):

```bash
supabase projects list                 # confirm the linked project (unjvbgyawsrzgwqxxhxt)
supabase db dump --linked -f schema.sql   # schema-only snapshot of the live DB
supabase migration list --linked       # compare local vs remote migration history
supabase db diff --linked -f my_change  # generate a migration from remote drift
supabase test db                        # run pgTAP suites
```

To regenerate the frontend types after a schema change:

```bash
supabase gen types typescript --linked > frontend/src/lib/database.types.ts
```

> **Environment note.** This host has **no IPv6**, so direct connections to
> `db.<ref>.supabase.co:5432` fail; the CLI automatically retries via the **IPv4
> transaction pooler**. That is expected and matches the app, which always uses the
> pooler on port `6543`.

> **Caution.** The hosted project is a shared **development** environment. Breaking
> schema changes and rebuilding dev feature data are allowed *within agreed scope*,
> but **preserve auth users** unless explicitly told to remove one, and resolve
> exact targets before any destructive change so no orphaned rows/objects remain.
> Always go through the migration workflow — never hand-edit the remote schema.
