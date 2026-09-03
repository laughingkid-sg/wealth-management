# Transactions technical implementation

Focused design reference: [Wealth Builder Transactions — Prompt and Matching Design](https://rcnubep1n9x.sg.larksuite.com/docx/BVk8dHLivos0odxu9o6lJomigMb).

## Implementation status

The React workspace, Go API, Go worker, database migrations, and automated-test fixtures described here are implemented on `codex/feat-transaction`. Migrations through `20260903055808_add_global_source_rule_editor_metadata.sql` passed hosted dry runs and exact transaction-wrapped rollback rehearsals, are applied to the hosted development project, and appear in matching local/remote history. All 244 pgTAP assertions across ten suites pass, and hosted database lint reports no schema warnings or errors for the private or public schema. The full Go test, vet, race, and API/worker build gates pass; the hosted global-rule ownership/optimistic-update integration test also passes through the transaction pooler. Frontend lint and production build pass together with eight focused prompt/global-rule tests. Authenticated-browser verification covers Global Settings, safe rule defaults and catch-all warning, the automatic preview empty state after evidence reset, manual preview output and placeholders, and zero console warnings or errors. Qwen was not called.

## Component boundary

Transactions uses four cooperating boundaries:

| Component | Responsibility |
| --- | --- |
| React SPA | Supabase session, Transactions/Review/Dangling/Failed views, independent personal and global settings pages, side-effect-free Prompt Preview, owner-only parse debugging, safe reference-data reads, progress monitoring and terminal-result dismissal, manual transaction insertion through authenticated Data REST, and all other user actions through Go. |
| Go API (`backend/cmd/api`) | Authenticates the Supabase user, owns Gmail OAuth, starts sync runs, returns safe transaction/source/settings/audit projections, manages private global rules, assembles read-only prompt previews, signs attachment access, stages evidence deletion, and performs invariant-preserving edits and multi-row mutations. It does not expose a manual-create route. |
| Go worker (`backend/cmd/worker`) | Claims durable jobs, fetches Gmail, stores attachments, selects global and user parser rules through the shared prompt-selection package, assembles Qwen prompts, stores the complete call audit, validates candidates, and reconciles sources. |
| Hosted Supabase | Auth, Postgres, RLS-protected public projections, a least-privilege manual-transaction INSERT surface, non-exposed private operational and parser-configuration tables, Realtime sync updates, and private attachment Storage. |

The browser uses Supabase Data REST for the user’s active Account choices, the global category catalogue, the existing Account-detail transaction lookup, the advisory manual-duplicate lookup, and one narrowly constrained manual transaction insert. It sends the signed-in user's JWT and publishable key; it never uses a service-role key. The main Transactions/source listings, canonical edits, and every privileged or multi-table operation go through Go. Gmail, Qwen, raw sources, evidence links, OAuth tokens, worker jobs, Storage service credentials, and attachment signing are never browser-side concerns.

## Identity and OAuth

React sends its Supabase access token as `Authorization: Bearer <access-token>` on every protected Go request. The current Go verifier calls the project’s `/auth/v1/user` endpoint with that bearer token and a server-only Supabase API key. The returned Auth user UUID becomes the only actor ID. Request bodies and query parameters never select a user.

The Gmail callback cannot carry the browser bearer token, so `POST /v1/transactions/gmail/connect` first creates a 10-minute, single-use OAuth state row. The database stores only a SHA-256 digest of the random state and an encrypted PKCE verifier associated with the authenticated user. Google receives an S256 challenge, read-only Gmail scope, offline access, and explicit consent. The callback atomically consumes the state before exchanging the code. The returned refresh token is AES-GCM encrypted with user-bound associated data before it is stored in `private.gmail_connections`.

Connecting again replaces the encrypted token, clears prior connection errors, and resets the Gmail cursor and last-sync timestamp so the next refresh performs the bounded initial import again.

## Asynchronous flow

```text
React POST /gmail/sync-runs
  -> Go creates transaction_sync_runs row + gmail_ingestion job atomically
  -> worker claims job with a five-minute lease
  -> Gmail message saved idempotently in private.data_sources
  -> supported attachments uploaded to private Storage
  -> one source_parsing job queued for the stored source
  -> global/user rule selection + immutable prompt assembly
  -> Qwen call + exact audit + strict response/evidence validation
  -> one reconciliation job queued
  -> typed source evidence matched to active Account matching keys
  -> attach existing transaction | create transaction | review | dangling
  -> sync progress recomputed; terminal run published through Realtime
```

Only one `queued` or `running` sync run is allowed per user. Jobs support `gmail_ingestion`, `source_parsing`, `reconciliation`, and `source_attachment_cleanup`, use exponential retry delay, and are claimed with `FOR UPDATE SKIP LOCKED`. Ordinary jobs have a maximum of five attempts; cleanup jobs retry in five-attempt bursts and enter a fifteen-minute cooldown between bursts until their exact objects are deleted. The worker records lease ownership and heartbeats/completes only its own lease. It does not hold a transaction while calling Gmail, Storage, or Qwen. A crashed worker’s expired lease can be reclaimed; an expired final cleanup lease requeues with the same durable payload while other final attempts become safely visible failures.

Ingestion has its own completion timestamp. A run becomes terminal only after ingestion is complete and all child jobs are no longer queued/running. A terminal Gmail-ingestion failure sets the run to `failed`. Source-level terminal failures remain visible and can produce a completed run with a redacted error summary. The safe progress projection includes discovered messages, saved sources, parsed sources, failed sources, transactions created, sources linked, review count, dangling count, and lifecycle timestamps. Queued/running progress cannot be dismissed. A completed or failed banner can be dismissed using a local key scoped to the authenticated user UUID and sync-run UUID, so a dismissal neither crosses users nor suppresses the next run; an in-memory dismissal still works when browser storage is unavailable.

## Gmail ingestion and cursors

The Gmail client resolves the exact `odin-finance` label and uses `gmail.readonly`.

- Initial connection: only an empty cursor captures the mailbox History ID first, then lists at most the five newest labelled messages. Capturing History first prevents a message arriving during the list from being skipped on the next refresh.
- Incremental refresh: call Gmail History without a single-event filter, locally accept `messagesAdded` and `labelsAdded` only when the latter includes the resolved label, deduplicate IDs across entries/pages, and save the final History response ID only after successful persistence.
- The History walk is capped at three pages of 100 events. Reaching the cap returns an error and does not advance the cursor, preventing silent loss.
- An expired Gmail History ID, or any legacy non-empty cursor that is not a History cursor, uses bounded full-label recovery (100 messages per page with a defensible cap), captured from a pre-list History ID. Reaching the cap fails without advancing the cursor. Provider-message uniqueness makes recovery idempotent.
- A per-message Gmail 404 during listing metadata, complete-message retrieval, or attachment retrieval means the message disappeared after it was listed. It is skipped and the captured cursor may advance; any other provider error fails the run without cursor advancement.

`private.data_sources` has a unique partial index over user, source type, provider, and provider message ID. Each inserted Gmail source increments the durable saved count in the same database transaction. Attachment paths are deterministic, and source-parse enqueueing is idempotent, so retry can repair a partially completed ingestion without duplicating evidence or decrementing progress.

The Gmail source JSON stores provider IDs, normalized headers, subject, sender, plain text, private original `html_raw`, sanitized display HTML, a `body_truncated` boolean, and attachment metadata. The provider accepts at most 224 KiB cumulatively across decoded `text/plain` and `text/html` parts, converts invalid byte sequences to valid UTF-8, and truncates only at a UTF-8 boundary. Subject and sender values are each bounded to 8 KiB, leaving safe room for their prefixes and `received_at` beneath Qwen's 256 KiB normalized-text limit. Ordinary messages remain byte-for-byte intact after existing line-ending normalization. The API exposes only sanitized HTML/plain text; it never exposes `html_raw`, raw MIME, or attachment bytes.

## Email and attachment handling

Server-side sanitization removes unsafe HTML elements/attributes and remote-resource behavior before email display. React renders sanitized HTML in an iframe with an empty `sandbox` and `referrerPolicy="no-referrer"`; plain text is rendered as text when HTML is absent.

The private `transaction-attachments` bucket has a 5 MiB object limit and accepts only:

- `application/pdf`
- `image/bmp`
- `image/jpeg`
- `image/png`
- `image/tiff`
- `image/webp`
- `image/heic`

The Go Storage client repeats MIME, signature, and size validation, uses `user_id/source_id/sha256.extension` object paths, and upserts safely for retries. Object metadata—not bytes—is saved under `raw_data.attachments`. Browser roles are blocked from the bucket. `GET /v1/transactions/sources/{id}/attachments` first proves source ownership, then returns five-minute signed URLs generated with the server-only key. Eligible PDFs are rendered to at most three PNG pages; BMP/TIFF/HEIC are converted before Qwen receives image data URLs. The worker admits at most five visual inputs and at most 5 MiB of visual bytes in aggregate, skipping optional pages or images beyond either limit.

Only stored attachments whose filename contains `receipt` or `invoice`, case-insensitively, are eligible for visual parsing. Other supported files remain available as evidence.

### Visual-parser runtime dependencies and fallback

- JPEG, PNG, and WEBP evidence goes directly to Qwen.
- PDF evidence requires Poppler’s `pdftoppm` executable on `PATH`. At most the first three pages are rendered to PNG.
- BMP, TIFF, and HEIC evidence currently uses macOS `sips` to convert to PNG. On a non-macOS worker, either provide an equivalent supported conversion path before deployment or accept the documented fallback.
- Conversion runs in a mode-appropriate temporary directory with a ten-second timeout. At most five visual inputs and 5 MiB total visual bytes are sent to Qwen.

If `pdftoppm`/`sips` is unavailable, conversion fails, times out, or produces an unusable/oversized image, that optional visual is skipped. The private original stays stored and viewable, and parsing continues with email text and any other usable visual inputs. A Storage download failure is different: it is retried as a worker failure because the eligible evidence could not be read safely.

## Parser and rule contract

The platform prompt is the build-embedded asset `backend/internal/prompts/transactions/system_v2.txt`, loaded by `backend/internal/prompts/prompts.go` with Go `embed`. It is versioned with the binary and has no runtime edit path. Version 2 fixes the response schema, source-only evidence requirement, exact evidence-path grammar, no-invention rule, absence of Account data, and the rule that email/attachment content is evidence rather than instruction. `backend/internal/transactionprompt` is the shared selection/assembly boundary used by production parsing and Prompt Preview. Configured guidance is appended in this stable order and is explicitly subordinate to the platform contract:

1. the single highest-priority matching global rule;
2. the user's default instructions, when non-empty;
3. the single highest-priority matching user source rule.

Global rules live in `private.source_parser_rules`. They are shared, versioned Gmail rules with optional sender/content RE2 matchers, a prompt fragment, priority, active state, and optional deterministic constants/capture groups in `extraction_config`. If both matchers are present, both must match. The seeded OCBC v2 rule recognizes explicit SGD purchase/debit messages and may supply debit/SGD constants plus a card-last-four capture. A lower-priority generic masked-card rule captures source evidence such as line-broken `Mastercard (**** 2562)` without inferring issuer, owner, amount, or direction. Historical rule rows remain available for parse provenance.

During the current development phase, every authenticated user may list, create, and edit these shared rules through the trusted Go API. Editable fields are `name`, `sender_matcher`, `content_matcher`, `prompt_fragment`, `priority`, and `active`; the provider remains Gmail. Deterministic `extraction_config` is returned for inspection but omitted from mutation input, is preserved during updates, and starts as `{}` for a newly created rule. Updates require `expected_version`, increment the stored version, record the authenticated editor and update time, and return a conflict when another edit has already advanced the row. There is no hard-delete route: setting `active = false` preserves the rule and its historical provenance. Configuration changes affect future source parses and manually retried sources only; they do not enqueue historical reparsing.

User defaults live in `private.user_parser_settings` and are capped at 4,000 characters. User source rules live in `private.user_source_parser_rules`. Each rule has a required Gmail sender condition (`exact`, `domain`, or RE2), optional subject and content RE2 conditions, a prompt fragment, active state, priority, and monotonically increasing version. Present conditions use AND semantics. Global and user winners are selected independently; if two matching rules share the highest priority within either class, parsing records a configuration failure instead of choosing by ID. Go validates every user-entered expression with its RE2-compatible regular-expression engine before storage.

The worker calls Alibaba Cloud Token Plan at the configured OpenAI-compatible base URL using exactly `qwen3.8-flash`, `response_format: {"type":"json_object"}`, and `enable_thinking: false`. JSON Object mode is intentional for the Singapore endpoint because [Alibaba Cloud documents JSON Schema structured output as unsupported for Singapore](https://www.alibabacloud.com/help/en/model-studio/qwen-structured-output). Because JSON Object mode guarantees valid JSON but not an application schema, the prompt states the exact nested response shape and the Go decoder, evidence checks, and domain validation remain authoritative. Requests have bounded text, attachment, total-body, response, and time limits.

The model receives normalized source evidence and eligible attachment images, but no Account catalogue, Account metadata, configured matching keys, or other users' data. The model response is treated as untrusted. Strict decoding rejects unknown fields and extra JSON values. The server—not the model—binds user ownership, auto-eligibility, and aggregate confidence. Populated decisive fields, including generic additional identifiers, must cite the exact grammar `received_at` or a path rooted at `subject`, `sender`, `text`, or `attachment` with dot-name and numeric-index segments; a candidate field name or extracted value is never a source path. Only server-applied rules may add a `rule:<id>:v<version>` citation. `received_at` is used only as an occurred-at fallback when no explicit event timestamp exists. Aggregate confidence is the minimum valid confidence among decisive citations so a strong citation cannot hide a weak required fact.

Every parse attempt stores bounded, owner-private audit data: the assembled system prompt, normalized user input, exact JSON request sent to Alibaba, exact JSON response returned by Alibaba, exact model-output object, server-validated candidate, selected rule IDs/versions, prompt components, attachment usage, validation state, and redacted error. Authentication headers and API keys are never stored. Failed and invalid attempts retain every boundary reached, so the Debug view can distinguish provider, schema, evidence, and configuration failures.

### Prompt Preview

Prompt Preview is a read-only assembly path; it does not share the worker's execution or persistence boundary. **Automatic** mode accepts an owned Gmail source ID, loads the same normalized source/configuration input as the worker, and calls the same active global and personal rule selectors. **Manual** mode accepts optional global and owned personal rule IDs plus an `include_user_default` flag, then assembles those chosen components without evaluating their matchers; inactive rules remain selectable for inspection.

Both modes return `assembled_system_prompt`, `prompt_components`, `provider_request`, optional selected-source metadata, and rule-selection metadata. `provider_request` uses the production Qwen request envelope—`qwen3.8-flash`, JSON Object response mode, and thinking disabled—but replaces the email user-message content with `<EMAIL CONTENT OMITTED FROM PREVIEW>`. It adds `<ELIGIBLE RECEIPT OR INVOICE IMAGE OMITTED FROM PREVIEW>` only when the selected source metadata contains an attachment that could pass the worker's pre-download receipt/invoice gate. The template therefore shows request shape rather than disclosing source content or claiming that a later download/render will succeed.

Preview responses use `Cache-Control: no-store`. The preview handlers perform no Alibaba call and do not enqueue jobs, insert parse-attempt audit, update source status, retry/reparse evidence, or write transactions or links.

The normalized candidate contains:

- `transaction_kind`: `debit` or `credit`
- `title` and optional normalized `merchant_name`
- positive `original_amount_minor` and ISO 4217 `original_currency`
- optional positive `sgd_amount_minor`
- RFC3339 `occurred_at`
- reference identifiers
- optional Account evidence: card last four, masked bank reference, and other safe identifiers
- versioned line items
- optional category leaf name

Missing Account evidence is a valid parse and becomes dangling at reconciliation. An absent category citation or an invalid citation attached only to the optional category removes the category and all of its evidence before general validation, so the otherwise usable parse remains uncategorized. Unknown or ambiguous category names likewise remain uncategorized. Invalid required transaction facts and invalid citations for required or other populated optional fields produce a stored failed source/parse attempt that the user can retry. Automatic reconciliation and user-confirmed creation preserve a suggested category only when its citation is valid and exactly one active global leaf resolves.

## Reconciliation contract

Reconciliation first resolves Account evidence against active, explicitly configured matching keys owned by the job user. Only `card_last_four` can match a `card_last_four` key, and only `masked_bank_reference` can match a `bank_account_suffix` key. Account names, `accounts.account_identifier`, arbitrary Account metadata, and candidate `additional_identifiers` are deliberately excluded.

Card keys remove whitespace and common masking characters but must yield exactly four ASCII digits; the system never truncates a longer value. Model-provided card evidence is matchable only when the normalized source contains exactly one masked-card or explicit card-context suffix with the same value. Missing, conflicting, or bare four-digit occurrences are demoted to non-matching audit detail while the exact model output remains unchanged. Bank suffix keys lowercase and remove Unicode whitespace plus `*`, `•`, and `-`, retaining other characters. Stored display values remain unchanged. `(user_id, key_type, normalized_value)` is permanently unique, including retired rows. Matching-key identity is immutable; retirement/reactivation changes lifecycle state only.

- No Account evidence or no owned match → `dangling`.
- More than one owned Account match → `review_required`.
- Exactly one Account match → evaluate automatic pairing before automatic-creation eligibility.

The automatic-pairing candidate set contains only transactions with the same owner and resolved Account, the same debit/credit kind, the exact original amount, and an absolute timestamp difference of at most ten minutes inclusive. Currency is compatible unless both the parsed candidate and existing transaction have known, different currencies; equal currencies or a missing currency on either side are accepted.

Exactly one pairing candidate attaches the source, including when server-derived source corroboration would not permit automatic creation. More than one candidate returns `review_required`. No candidate proceeds to the existing strict automatic-creation path. Shared references, normalized merchants, and scores are not automatic fallbacks and do not disambiguate multiple candidates.

On the no-candidate path, cited confidence of at least 0.75 and server-derived source corroboration creates a confirmed Account-linked transaction; otherwise the source goes to Review. Corroboration requires source text to contain a bounded Account identifier, exact ISO-scaled currency-qualified amount, and merchant or reference; it does not trust attachment-only facts, bare digits, bare `$`, or model citations. Automatic and user-created transactions preserve reference/Account evidence in the transaction `details` JSON. Optional categories are applied only when exactly one active leaf matches; otherwise the transaction remains uncategorized.

This reconciliation refactor requires no schema migration or historical backfill. Development transaction/source data will be cleared and rerun through the updated worker behavior.

## Database shape

All monetary columns use `bigint` integer minor units. Timestamps use `timestamptz`; user-owned primary keys are UUIDs. Ownership-sensitive foreign keys use composite `(user_id, id)` references where cross-table ownership must be enforced.

### Public, RLS-protected projection

| Table | Purpose and important fields |
| --- | --- |
| `public.transaction_categories` | Global, system-managed category catalogue: `parent_name`, `name`, `emoji`, `sort_order`, `active`. Authenticated users can select only. |
| `public.transactions` | Canonical records: required owner/Account, `transaction_kind`, title/merchant, original amount/currency, optional SGD, time, optional category, `line_items` array, `details` object, review/confidence, `creation_method`, optional `user_modified_at`, and timestamps. Browser users can select and have column-scoped INSERT permission only for confirmed manual creation; Go performs edits and all other writes. |
| `public.transaction_sync_runs` | Owner-safe async projection: lifecycle state/timestamps, ingestion-complete marker, message/source/progress/outcome counts, redacted error summary. Owner-select only and published to Supabase Realtime. |

`public.transactions` references an active Account owned by the same user; a trigger also blocks inserts/Account changes to soft-deleted Accounts. A separate trigger rejects any supplied category that is missing or inactive. `transaction_kind` stores only `debit` or `credit`, and amounts remain positive. Where a transaction is linked as an internal-transfer leg, database integrity checks protect the pair from being made a same-Account transfer.

### Non-exposed private operational schema

| Table | Purpose |
| --- | --- |
| `private.gmail_connections` | Encrypted refresh token, token metadata, exact label, History cursor, status, last sync/error. One Gmail connection per user. |
| `private.gmail_oauth_states` | Single-use state digest and encrypted PKCE verifier with expiry/consumption protections. |
| `private.data_sources` | Durable generic evidence, provider identity, JSON payload, parser provenance/state, Account/transaction suggestions, and reconciliation reason. Current allowed types: `gmail_email`, `phone_notification`. |
| `private.source_parser_rules` | Shared versioned Gmail rules: name, optional sender/content matchers, prompt fragment, deterministic extraction configuration, priority, active state, last authenticated editor, and timestamps. |
| `private.user_parser_settings` | One versioned, bounded default parser-instruction value per user. |
| `private.user_source_parser_rules` | Versioned owner-specific Gmail sender/subject/content matching plus prompt guidance and priority. |
| `private.account_matching_keys` | Typed, immutable Account identities with permanent per-user uniqueness and retire/reactivate lifecycle. |
| `private.source_parse_attempts` | Exact bounded prompt/input/provider/model audit, rule provenance, validated candidate, validation state, and errors. |
| `private.transaction_user_locks` | Minimal per-user row-lock target that serializes Gmail sync creation/ingestion with raw-source deletion. |
| `private.deleted_provider_messages` | SHA-256 provider-identity tombstones that prevent deliberate raw deletions from being recreated without retaining the source UUID, provider message ID, content, or paths. |
| `private.transaction_data_sources` | Evidence junction with source role, confidence, matched-by provenance, and detachable audit fields. A source ordinarily has one active link; exactly two are allowed only when they are the debit and credit legs of the same internal-transfer pair. Unmatching soft-detaches evidence before reuse. |
| `private.transaction_links` | Relationship junction; currently one `internal_transfer` debit/credit pair. Deferred trigger checks same owner, distinct rows and Accounts, correct kinds, and exclusive transfer membership. |
| `private.transaction_jobs` | Durable queued/running/completed/failed/cancelled jobs with attempts, schedule, lease owner/expiry, payload references, and redacted error. The source-attachment cleanup kind acts as an outbox with no source FK, records a cumulative failure count, requeues instead of becoming terminal, and is removed after successful object deletion. |

Migration `20260902230001_add_transaction_configuration_and_audit.sql` adds parser settings, source rules, typed Account matching keys, parse-call audit columns, transaction provenance, and raw-source deletion cascades. It backfills only recognized Account metadata aliases while preserving the original metadata, retires the original OCBC row in favor of a prompt-bearing v2, and seeds the generic masked-card rule. All new configuration tables remain in the non-exposed `private` schema with RLS enabled and browser grants revoked.

Forward migration `20260902230002_add_durable_source_deletion.sql` adds the per-user coordination row, one-way deletion tombstones, and the cleanup outbox job kind. It intentionally follows the already-applied configuration migration instead of rewriting hosted migration history.

Forward migration `20260902230003_keep_source_cleanup_retrying.sql` adds cumulative cleanup-failure monitoring, recovers any prior terminal cleanup row, and changes the monitoring index without rewriting either applied predecessor. The worker must be deployed after this migration because its retry path writes the new column.

Migration `20260903014725_allow_manual_transaction_inserts.sql` adds the browser manual-insert grant and policy, the active-category trigger, and database validators for transaction line items and details. It was created through the imperative Supabase migration workflow, passed hosted dry-run and exact rollback rehearsal, and is applied after `20260902230003` without rewriting prior history. Focused coverage lives in `supabase/tests/transactions_manual_insert.test.sql`; the existing transaction RLS test distinguishes the allowed manual insert from still-denied browser UPDATE/DELETE operations. Both pass as part of the nine hosted pgTAP suites.

Migration `20260903055808_add_global_source_rule_editor_metadata.sql` adds the required human-readable `name` and nullable `updated_by_user_id` foreign key to global rules, backfills stable names for the seeded rules, adds the editor lookup index and bounded-name check, and reasserts RLS plus revoked browser grants on the private table. Auth-user deletion clears the attribution while preserving the rule, extraction configuration, and version; the existing update trigger still advances `updated_at`. Focused schema/security coverage is in `supabase/tests/transactions_global_source_rules.test.sql`. The migration passed a hosted dry run and exact transaction-wrapped rollback rehearsal, is applied after `20260903014725`, and appears in matching local/remote migration history.

### Manual-entry extension inventory

| Surface | Responsibility and current verification state |
| --- | --- |
| `ManualTransactionDialog.tsx`, `manualTransactionModel.ts`, and the Data REST client | Major-unit form validation, active reference choices, duplicate preflight, explicit confirmed insert, and returned-row parsing. Verified through focused frontend tests and authenticated-browser acceptance. |
| `TransactionDetailDialog.tsx`, Go PATCH decoding, and the transaction store | Edit `merchant_name` and `user_notes` for every canonical creation method; merge the notes key without replacing other details. Verified through Go tests and an authenticated edit that preserved line-item details. |
| `syncBannerDismissal.ts` and `TransactionsPage.tsx` | Per-user/per-run terminal Gmail-result dismissal while active progress remains non-dismissible. Verified through focused frontend tests and persisted authenticated-browser dismissal. |
| `20260903014725_allow_manual_transaction_inserts.sql` | Least-privilege insert authorization, active-category enforcement, and JSON validation. Rehearsed, applied, present in migration history, and covered by passing pgTAP. |
| `transactions_manual_insert.test.sql` and the adjusted transaction RLS test | Owner success plus authorization, immutable-column, Account/category, JSON/size, forged-provenance, anonymous, UPDATE, and DELETE denials. Passing within the current 244 assertions across ten suites. |

### Line-item JSON

`transactions.line_items` is always a JSON array with no more than 100 items and no more than 262,144 serialized bytes. The Go API and parser validate every item before save, and a database check applies the same v1 boundary to direct inserts:

```json
{
  "schema_version": 1,
  "description": "Example item",
  "quantity": 2,
  "unit_price_minor": "450",
  "line_total_minor": "900",
  "tax_minor": "81",
  "discount_minor": "0",
  "currency": "SGD",
  "details": {}
}
```

Every item must be an object containing only the v1 keys shown above: `schema_version`, `description`, `quantity`, `currency`, `details`, and the four optional amount keys. Version is exactly 1, description is nonblank and at most 250 characters, quantity is a positive integer within the Postgres `bigint` range, currency is three uppercase letters, and item `details` is an object. Optional item amounts may be zero and may arrive as Go-written JSON integer numbers, frontend-written decimal-integer strings, or JSON null; non-integer, negative, and out-of-`bigint` values fail validation.

Transaction `details` must be an object whose serialized form is at most 16,384 bytes. Its optional `user_notes` value must be a string of at most 4,000 characters. The general check deliberately permits server-owned provenance keys, while browser-insert RLS permits no key except `user_notes`.

HTTP responses serialize all minor-unit values as decimal strings to avoid JavaScript precision loss; Postgres and Go use `bigint`/`int64`. UI forms accept major-unit decimal strings and convert major ↔ minor values with string parsing and `BigInt`, never binary floating-point arithmetic. The current frontend also rejects a converted minor-unit value above `Number.MAX_SAFE_INTEGER` because Data REST may return Postgres numerics as JSON numbers; this is a browser transport limitation, not the database `bigint` limit. The Go storage decoder accepts both Go-written JSON integer amounts and browser-written bigint-safe decimal strings, canonicalizes them for the response contract, and preserves line-item details; this compatibility fix prevents browser-created items from disappearing on a later Go read or edit response.

## Manual transaction creation through Data REST

There is intentionally no Go manual-create route. React sends one `POST /rest/v1/transactions?select=...` request with the Supabase publishable key and the current user's bearer token, and requests `return=representation`. The payload includes only `user_id`, `account_id`, `transaction_kind`, `title`, `merchant_name`, `original_amount_minor`, `original_currency`, `sgd_amount_minor`, `occurred_at`, `category_id`, `line_items`, `details`, and an explicit `review_status = confirmed`. It omits `creation_method`, allowing the existing `manual` default to apply.

The `authenticated` role receives INSERT only on those columns. It receives no INSERT grant for `id`, `creation_method`, `match_confidence`, `user_modified_at`, `created_at`, or `updated_at`, and still receives no table UPDATE or DELETE grant. The INSERT policy requires a non-null `auth.uid()` equal to `user_id`, `creation_method = 'manual'`, `review_status = 'confirmed'`, null match confidence, null `user_modified_at`, and `(details - 'user_notes') = '{}'::jsonb`. Existing ownership enforcement rejects a missing, deleted, or cross-owner Account, the category trigger rejects a missing/inactive category, and the JSON checks above validate browser-supplied line items and details. Because this path cannot write the private evidence junction, the created canonical row has no source evidence.

Before posting, React uses the existing owner-RLS SELECT surface to query the same Account, transaction kind, exact original minor amount and currency, and `occurred_at` within ±10 minutes. Matches produce an advisory warning and a **Create anyway** path. This preflight is not a database uniqueness constraint and intentionally does not block an accepted duplicate or claim race-free deduplication.

## Go HTTP API

Every route except the OAuth callback requires the authenticated Supabase bearer token. UUID/resource lookups remain owner-scoped. JSON actions reject unknown fields, extra JSON values, and request bodies over 1 MiB.

### Gmail and progress

| Method and path | Behavior |
| --- | --- |
| `POST /v1/transactions/gmail/connect` | Returns Google `authorization_url` for the authenticated user. |
| `GET /v1/transactions/gmail/oauth/callback` | Consumes OAuth state/code and redirects to `FRONTEND_ORIGIN?gmail=connected` or `gmail=connection_failed`. |
| `GET /v1/transactions/gmail/connection` | Safe status: connected/status/email/selected label/last sync/redacted error; never a token. |
| `POST /v1/transactions/gmail/sync-runs` | Returns `202` with the queued safe run projection; rejects missing Gmail connection or an existing active run with `409`. |
| `GET /v1/transactions/sync-runs/latest` | Restores the owner’s newest run, or `404` when none exists. |
| `GET /v1/transactions/sync-runs/{id}` | Returns owner-scoped progress for one run. |

### Transactions and evidence

| Method and path | Behavior |
| --- | --- |
| `GET /v1/transactions` | Keyset-paginated records with Account/category labels, line items/details, source count, and transfer counterpart. Filters: `kind`, `review`, `account_id`, `search`, `limit` (1–100), `cursor`. |
| `GET /v1/transactions/{id}/sources` | Active safe evidence summaries plus source-link IDs for one owned transaction. |
| `PATCH /v1/transactions/{id}` | Edits title, optional `merchant_name`, active Account, time, original amount/currency, optional SGD amount, optional category, line items, and optional `user_notes`. Notes are merged into `details`; null/blank removes only that key and preserves server-owned provenance. Every successful edit sets `user_modified_at`. |
| `POST /v1/transactions/internal-transfers` | Atomically creates validated debit and credit legs plus their `internal_transfer` link; optional owned source IDs can support the outgoing leg, incoming leg, or both legs of that one pair. |
| `GET /v1/transactions/sources` | Keyset-paginated `dangling`, `review`, or `failed` source summaries with parsed suggestions/reasons. Defaults to dangling. |
| `GET /v1/transactions/sources/{id}/email` | Owner-scoped sanitized HTML/plain text. |
| `GET /v1/transactions/sources/{id}/attachments` | Owner-scoped stored metadata and five-minute signed URLs. |
| `GET /v1/transactions/sources/{id}/debug` | Owner-scoped latest-ten attempt summaries with bounded field previews and explicit `has_more`, `truncated`, and `truncated_fields` markers. |
| `GET /v1/transactions/sources/{id}/debug/attempts/{attempt_id}/fields/{field}` | Loads one whitelisted audit field in its exact stored lexical form on demand after source, attempt, and owner checks. |
| `DELETE /v1/transactions/sources/{id}` | Atomically deletes database evidence, records a non-reversible provider tombstone, and queues exact Storage cleanup paths; returns `202 cleanup_pending` when object cleanup remains or `200 completed` when no objects exist. Active Gmail ingestion returns `409`. |
| `POST /v1/transactions/sources/{id}/attach` | Attaches actionable evidence to an owned existing transaction. |
| `POST /v1/transactions/sources/{id}/create-transaction` | Creates a confirmed transaction from the validated stored candidate and chosen active Account. |
| `POST /v1/transactions/sources/{id}/retry` | Requeues a failed source without refetching Gmail; idempotent when already queued/running. |
| `POST /v1/transactions/source-links/{id}/unmatch` | Soft-detaches evidence; an otherwise unattached source returns to Dangling. |

### Transaction settings

| Method and path | Behavior |
| --- | --- |
| `GET /v1/transactions/settings` | Returns the user's default instructions/version, all user source rules, and active or retired Account matching keys. |
| `PUT /v1/transactions/settings/default-instructions` | Replaces the bounded default instructions and increments their version. |
| `POST /v1/transactions/settings/source-rules` | Creates a validated, version-1 Gmail source rule. |
| `PUT /v1/transactions/settings/source-rules/{id}` | Replaces an owned rule's editable fields and increments its version. |
| `DELETE /v1/transactions/settings/source-rules/{id}` | Soft-retires an owned rule and increments its version so historical audit provenance remains valid. |
| `POST /v1/transactions/settings/matching-keys` | Creates a normalized matching key for an owned active Account; conflicts return `409`. |
| `PATCH /v1/transactions/settings/matching-keys/{id}` | Retires or reactivates an immutable owned matching key using `{ "active": boolean }`. |

### Global settings and prompt preview

| Method and path | Behavior |
| --- | --- |
| `GET /v1/transactions/global-settings` | Returns every shared global Gmail source rule, including read-only `extraction_config`, version, editor attribution, and timestamps. |
| `POST /v1/transactions/global-settings/source-rules` | Creates a version-1 global Gmail rule with an empty deterministic extraction configuration; returns `201`. |
| `PUT /v1/transactions/global-settings/source-rules/{id}` | Replaces the editable fields, preserves deterministic extraction configuration, and increments the version. Requires `expected_version`; a stale version returns `409`. |
| `GET /v1/transactions/prompt-preview/sources` | Returns the newest 100 Gmail source summaries owned by the caller: `id`, `subject`, `sender`, `received_at`, and `parse_status`. It returns no body or attachment content. |
| `POST /v1/transactions/prompt-preview` | Builds a manual or automatic prompt preview and returns `mode`, `assembled_system_prompt`, `prompt_components`, `provider_request`, selected-source metadata when applicable, and rule-selection metadata. It performs no provider call or parse-pipeline write. |

Automatic preview accepts only `{ "mode": "automatic", "data_source_id": "<owned-gmail-source-uuid>" }`; cross-owner, missing, or non-Gmail sources are not returned. Manual preview requires `include_user_default` and may add `global_rule_id` and an owner-scoped `user_rule_id`; it rejects `data_source_id`. An automatic highest-priority rule tie returns `409`. All global-settings and preview responses are private and carry `Cache-Control: no-store` plus `Pragma: no-cache`.

Transaction/source list cursors are versioned, scope-bound base64url values. Results sort newest first using `(timestamp, id)` keysets. Default frontend pages contain 50 items. Responses serialize monetary values as decimal strings and reject malformed service contracts in the TypeScript client.

## React implementation

The workspace navigation contains four independent Transactions pages: **Transactions**, **Prompt Preview**, **Global Settings**, and **Settings**. Their page components are `TransactionsPage.tsx`, `PromptPreviewPage.tsx`, `GlobalTransactionSettingsPage.tsx`, and `TransactionSettingsPage.tsx` under `frontend/src/features/transactions/`.

- Transactions supports title/merchant search, debit/credit and review filters, cursor “load more,” original/SGD display, Account/category labels, evidence counts, and transfer badges.
- **Add transaction** sits beside **Internal transfer** and opens a manual-entry dialog for any active owned Account. It validates debit/credit, required title/date/original money, optional merchant/SGD/category/notes, and at most 100 line items; runs the owner-scoped duplicate preflight; and inserts a confirmed row through Data REST after the user accepts any warning.
- Sync startup restores the latest run. Active runs subscribe to owner-readable `transaction_sync_runs` changes and also poll every ten seconds when Realtime is ready or every three seconds when it is not. Monitoring pauses after 40 checks while server work continues and can be resumed. Queued/running banners are not dismissible; completed/failed banners can be dismissed and use a browser-local key scoped to the current user and run.
- Source inspection loads sanitized email and private signed attachments, previews PDF/images, and supports Failed retry with a safe error summary or Review/Dangling attach/create resolution.
- Source inspection exposes an explicit Debug panel with bounded latest-attempt previews and on-demand exact-field loading for the complete owner-only Qwen audit. A separate destructive-confirmation flow reports whether durable raw-source Storage cleanup is pending.
- Transaction detail edits canonical fields and versioned line items, including merchant/payee and user notes for manual, source-created, and internal-transfer records. It displays the transfer counterpart and active evidence and uses a confirmation step before unmatch.
- Internal-transfer creation collects and validates both legs before one Go request, including outgoing, incoming, or both-leg evidence selection.
- Transaction Settings edits the default parser instructions, versioned sender/subject/content rules, and immutable Account matching keys. It explains prompt precedence, RE2/AND semantics, normalized values, and retire/reactivate behavior.
- Global Settings lists shared Gmail source rules, creates or edits their bounded fields, displays deterministic extraction configuration in a read-only disclosure, and surfaces stale-version `409` conflicts with a reload action. Disabling a rule is the only removal lifecycle exposed.
- Prompt Preview loads global rules, personal settings, and recent owned email summaries. Automatic mode selects a past email and delegates matching to Go; manual mode selects optional global/default/personal components. Results render the exact assembled system message, the placeholder-based Qwen request structure, component provenance, and rule-selection reasons without exposing dynamic source content.
- Dialogs trap focus, close with Escape, restore focus, and block background interaction. Tabs support arrow/Home/End keys, and the workspace has a mobile navigation drawer.

The frontend reads owned Account options and global categories from Data REST with the publishable key plus user bearer token. The source-resolution candidate picker and manual duplicate preflight use existing RLS-protected transaction reads. Manual transaction creation is the sole direct browser insert; every source, attachment, OAuth, sync, reconciliation, canonical edit, multi-row mutation, and main list operation goes through Go.

## Runtime configuration

Backend values are process environment variables. Real values belong in an ignored local environment file or deployment secret manager and must never be committed or copied into frontend variables. The Go binaries validate the complete runtime contract on startup.

| Variable | Requirement and use |
| --- | --- |
| `APP_ENV` | Optional; defaults to `development`. Non-development enforces HTTPS and forbids the test refresh token. |
| `API_ADDRESS` | Optional Go listen address; defaults to `:8080`. |
| `SUPABASE_URL` | Required hosted project URL; HTTPS outside development. |
| `SUPABASE_DB_URL` | Required Supabase transaction-pooler Postgres URL on port 6543. Host must end in `.pooler.supabase.com`; missing `sslmode` is normalized to `require`, and any explicitly weaker mode is rejected. |
| `SUPABASE_SERVICE_ROLE_KEY` | Required server-only key used as the Auth user-endpoint API key and for private Storage upload/download/signing. Never expose it to React. |
| `GOOGLE_OAUTH_CLIENT_ID` | Required Google web OAuth client ID. |
| `GOOGLE_OAUTH_CLIENT_SECRET` | Required server-only Google OAuth secret. |
| `GOOGLE_OAUTH_REDIRECT_URL` | Required exact registered callback; HTTPS except `localhost` in development. |
| `TRANSACTION_TOKEN_ENCRYPTION_KEY` | Required base64 encoding of exactly 32 random bytes for refresh-token and PKCE-verifier encryption. |
| `ALIBABA_TOKEN_PLAN_API_KEY` | Required server-only Token Plan key with credits. |
| `ALIBABA_TOKEN_PLAN_BASE_URL` | Optional only because the Singapore compatible endpoint is the safe default: `https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1`. Must be HTTPS. |
| `ALIBABA_TOKEN_PLAN_MODEL` | Optional only because it defaults to `qwen3.8-flash`; any other value is rejected. |
| `FRONTEND_ORIGIN` | Required exact OAuth redirect destination; HTTPS except localhost development. |
| `GMAIL_SYNC_LABEL` | Optional only because it defaults to and must equal `odin-finance`. |
| `GMAIL_INITIAL_BACKFILL_MAX_MESSAGES` | Required and must equal `5`. |
| `WORKER_POLL_SECONDS` | Optional positive worker idle poll interval; defaults to `5`. |
| `OUTBOUND_HTTP_TIMEOUT_SECONDS` | Optional positive shared outbound timeout; defaults to `20`, maximum `120`. Qwen additionally caps its request at 30 seconds. |
| `GOOGLE_TEST_REFRESH_TOKEN` | Optional development-only local test fallback when no stored connection exists. Rejected outside development. |

Frontend variables are `VITE_SUPABASE_URL`, `VITE_SUPABASE_PUBLISHABLE_KEY`, and optional `VITE_API_BASE_URL`. `VITE_API_BASE_URL` defaults to `/api`; Vite rewrites that prefix to the local Go API at `http://localhost:8080`. No Supabase secret/service-role key, Gmail token, encryption key, or Alibaba key may use a `VITE_*` name.

The API and worker are separate processes and both need the backend environment. Database access uses a reusable `pgxpool` with simple protocol, zero minimum and five maximum connections, short transactions, and no prepared-statement/session-state dependency so it is safe with the transaction proxy.

## Security controls

- `public.transaction_categories`, `public.transactions`, and `public.transaction_sync_runs` have RLS. Owner data uses `(select auth.uid()) = user_id`. Authenticated browser grants remain select-only except for the explicit column-level `transactions` INSERT grant used by confirmed manual creation; browser UPDATE/DELETE remain denied.
- The `private` schema has no `anon`/`authenticated` grants. Its tables also enable RLS as defense in depth.
- Browser roles are explicitly blocked from `transaction-attachments`; only Go’s server key can access the private bucket.
- Source, transaction, Account, link, sync, and attachment paths are checked against the authenticated user before reads or writes.
- Personal parser settings and source-rule changes, matching keys, Debug records, preview email selection, and deletion plans are owner-scoped in SQL as well as at the HTTP boundary.
- Global source rules remain in the non-exposed private schema and are writable only through authenticated Go handlers. As an explicit development-phase limitation, any authenticated user may currently change configuration shared by all users; a future admin authorization boundary is out of scope. Deterministic `extraction_config` cannot be supplied through these handlers.
- Qwen never receives the Account catalogue or matching-key table. User guidance is appended beneath an immutable platform contract and email/attachment instructions are treated as untrusted content.
- Raw deletion is database-first and owner-scoped. A per-user coordination lock prevents races with Gmail ingestion; database cascades clear source jobs, attempts, and links; transaction provenance limits automatic cleanup to a never-edited `automatic_source` record with no remaining active evidence or transfer link. Exact Storage paths are committed as leased cleanup work before any external call, a one-way provider digest prevents reingestion of deliberately deleted evidence, and cleanup failures or expired final leases remain queued with monitored cooldown until success.
- Automatic canonical creation takes the same stable per-user transaction lock and repeats Account/nearby-transaction reconciliation inside the write transaction. Two workers holding stale create decisions therefore converge on one transaction and two evidence links rather than creating duplicates.
- The model never chooses user ownership. Gmail/provider content, HTML, filenames, parser output, category names, and cursor values are validated before use.
- Transactions and internal-transfer legs can reference only active same-user Accounts. Any supplied transaction category must also be active. Transfer pair integrity is enforced in both Go and a deferred database trigger.
- OAuth state is expiring/single-use, PKCE is required, refresh tokens are encrypted at rest by the application, and secrets/errors are not returned to the browser.
- Public-table grants are explicit, so the feature does not depend on automatic Data API exposure behavior.

## Verification and release gate

The repository contains focused Go tests for Auth, OAuth state/token storage, Gmail listing/history and parsing, attachment Storage/signing, ingestion idempotency, durable jobs/leases, parser-provider contracts, deterministic rules, semantic card corroboration, reconciliation races, HTTP validation, source actions, cleanup recovery, SQL store behavior, browser-authored line-item decoding, the embedded provider request template, shared prompt selection, and strict global-settings/preview HTTP contracts. All 244 pgTAP assertions across ten transaction suites pass, including global-rule schema/security, manual-insert grants, RLS, ownership, JSON, size, and immutability coverage.

The hosted `TestGlobalSourceRuleOptimisticUpdatePreservesExtractionConfigAndPreviewOwnership` integration test passes through the configured transaction pooler. Frontend lint and production build pass, as do eight focused prompt/global-rule tests covering request construction and UI behavior.

`backend/internal/transactione2e/harness_test.go` also provides a scoped, non-destructive live harness. It is disabled by default and claims only the newly created run. It normally requires exactly one active stored Gmail-connection owner. In development only, when no active connection exists and `GOOGLE_TEST_REFRESH_TOKEN` is configured, it may instead select exactly one distinct owner of an active Account and create a development-token-enabled run. Zero or multiple eligible owners fail closed; the token and selected UUID are never logged or persisted. Because the fallback deliberately creates no Gmail connection or cursor, a later fallback run repeats the bounded initial window; provider-message uniqueness keeps that replay idempotent. Run it only with reviewed hosted credentials and an intentionally labelled test message:

```sh
cd backend
LIVE_TRANSACTIONS_E2E=1 go test ./internal/transactione2e -run TestLiveTransactionsPipeline
```

Run the final branch checks:

```sh
cd backend
go test ./...
go vet ./...
go build ./cmd/api ./cmd/worker

cd ../frontend
npm run lint
npm run build
```

For a future release rerun, validate each migration with a hosted dry run and transaction-wrapped rollback rehearsal, run the transaction pgTAP tests and Supabase security/performance advisors, perform the scoped live harness, and confirm local/remote migration history after any hosted application. Do not start local Supabase/Docker unless the user explicitly requests it. The combined verified results are shown below.

### Status at this documentation update

| Gate | Status |
| --- | --- |
| Prompt/global-settings extension | **Passed.** Hosted migration, database, Go, targeted frontend, and authenticated-browser gates pass. Manual preview showed the exact assembled system message and provider request placeholders; Qwen was not called. |
| Go implementation and focused coverage | **Passed.** The full Go test suite, race suite, vet, and API/worker builds pass, including embedded prompt assembly, global-setting HTTP contracts, browser-authored line-item decoding, and details-preserving merchant/notes edits. |
| Frontend implementation and focused coverage | **Passed.** Frontend lint and production build pass. Eight focused prompt/global-rule tests also pass. |
| Hosted migration rehearsal/application/history | **Passed.** Both `20260903014725_allow_manual_transaction_inserts.sql` and `20260903055808_add_global_source_rule_editor_metadata.sql` passed hosted dry runs and exact transaction-wrapped rollback rehearsals, are applied in order, and appear in matching local/remote migration history. No local Supabase or Docker instance was used. |
| Hosted database validation | **Passed.** All 244 pgTAP assertions across ten suites pass, including global-rule schema/security, manual-insert grants, RLS, ownership, active Account/category enforcement, JSON and size constraints, and browser UPDATE/DELETE denial. Hosted database lint reports no schema warnings or errors for either the private or public schema. |
| Hosted Go store integration | **Passed.** The race-enabled transaction-store integration suite passes through the configured transaction pooler, including serialized concurrent create decisions, deletion staging/tombstones, non-terminal cleanup retry and expired-lease recovery, source reingestion prevention, and bounded/exact Debug behavior. The hosted `TestGlobalSourceRuleOptimisticUpdatePreservesExtractionConfigAndPreviewOwnership` test additionally verifies optimistic updates preserve deterministic extraction configuration and that preview sources remain owner-scoped. |
| Live Gmail → private Storage ingestion | **Passed.** The initial scoped run fetched and stored five unique `odin-finance` sources, including one private attachment. |
| Live Qwen parsing and reconciliation | **Not exercised in this final verification.** Parser, citation, invalid-category discard, and semantic card-corroboration behavior remain covered by automated tests; no fresh provider call is claimed. |
| Idempotent replay | **Passed.** Repeating the same five-message run created zero duplicate sources. |
| Five-minute signed attachment access | **Passed.** A live ranged download through the signed URL succeeded within its five-minute lifetime. |
| Hosted anonymous/private-schema denial | **Passed for the exercised surfaces.** Anonymous REST requests to Transactions and sync data returned `401`; requesting the private source schema returned `406`. |
| Signed-out browser, mobile, and accessibility controls | **Passed.** Desktop and mobile signed-out views and accessibility controls were exercised with no console warnings. |
| Authenticated browser workflow | **Passed.** Global Settings loaded and saved rules with safe defaults and a catch-all warning when both optional matchers were empty. After transaction evidence was reset, automatic Prompt Preview showed its empty-source state; manual preview displayed the exact assembled system message and provider request placeholders without calling Qwen. The browser recorded zero console warnings or errors. Earlier acceptance also verified terminal Gmail-banner persistence, manual transaction creation and duplicate override, merchant/notes editing, zero evidence, and fixture cleanup. |
| Cross-user source/transaction/sync/attachment denial | **Passed in automated RLS/owner tests; live second-user attempt unavailable.** The hosted project has only one user, so no live cross-user claim is made. |

The manual-entry and prompt/global-settings release gates are complete. The unavailable live second-user check remains covered by automated ownership and RLS tests. Qwen was not exercised during prompt-preview verification or the prior final acceptance.
