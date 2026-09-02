# Transactions technical implementation

## Implementation status

The React workspace, Go API, Go worker, database migrations, and automated-test fixtures described here are implemented on `codex/feat-transaction`. Migrations through `20260902230003` are applied to the hosted development project, migration histories match, hosted database lint and all 189 pgTAP assertions pass, the hosted transaction-store race/integration suite passes, and the final Go/frontend release checks pass. Earlier scoped Gmail → Storage → Qwen → reconciliation, replay-idempotency, and signed-attachment checks also passed. The current FairPrice/Citi `2562` reparse is recorded separately below because sending the retained source to Qwen requires an explicit outbound-data approval.

## Component boundary

Transactions uses four cooperating boundaries:

| Component | Responsibility |
| --- | --- |
| React SPA | Supabase session, Transactions/Review/Dangling/Failed views, an independent Transaction Settings page, owner-only parse debugging, safe reference-data reads, progress monitoring, and user actions through Go. |
| Go API (`backend/cmd/api`) | Authenticates the Supabase user, owns Gmail OAuth, starts sync runs, returns safe transaction/source/settings/audit projections, signs attachment access, stages evidence deletion, and performs invariant-preserving mutations. |
| Go worker (`backend/cmd/worker`) | Claims durable jobs, fetches Gmail, stores attachments, selects global and user parser rules, assembles Qwen prompts, stores the complete call audit, validates candidates, and reconciles sources. |
| Hosted Supabase | Auth, Postgres, RLS-protected public projections, non-exposed private operational tables, Realtime sync updates, and private attachment Storage. |

The browser uses Supabase Data REST only for the user’s active Account choices, the global category catalogue, and the existing Account-detail transaction lookup. The main Transactions/source listings and every privileged or multi-table operation go through Go. Gmail, Qwen, raw sources, evidence links, OAuth tokens, worker jobs, Storage service credentials, and attachment signing are never browser-side concerns.

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

Ingestion has its own completion timestamp. A run becomes terminal only after ingestion is complete and all child jobs are no longer queued/running. A terminal Gmail-ingestion failure sets the run to `failed`. Source-level terminal failures remain visible and can produce a completed run with a redacted error summary. The safe progress projection includes discovered messages, saved sources, parsed sources, failed sources, transactions created, sources linked, review count, dangling count, and lifecycle timestamps.

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

The platform prompt is immutable, versioned application code. Version 2 fixes the response schema, source-only evidence requirement, exact evidence-path grammar, no-invention rule, absence of Account data, and the rule that email/attachment content is evidence rather than instruction. Configured guidance is appended in this stable order and is explicitly subordinate to that contract:

1. the single highest-priority matching global rule;
2. the user's default instructions, when non-empty;
3. the single highest-priority matching user source rule.

Global rules live in `private.source_parser_rules`. They are system-managed, versioned, provider-specific RE2 matchers with optional deterministic constants/capture groups and a prompt fragment. The seeded OCBC v2 rule recognizes explicit SGD purchase/debit messages and may supply debit/SGD constants plus a card-last-four capture. A lower-priority generic masked-card rule captures source evidence such as line-broken `Mastercard (**** 2562)` without inferring issuer, owner, amount, or direction. Historical rule rows remain available for parse provenance.

User defaults live in `private.user_parser_settings` and are capped at 4,000 characters. User source rules live in `private.user_source_parser_rules`. Each rule has a required Gmail sender condition (`exact`, `domain`, or RE2), optional subject and content RE2 conditions, a prompt fragment, active state, priority, and monotonically increasing version. Present conditions use AND semantics. Global and user winners are selected independently; if two matching rules share the highest priority within either class, parsing records a configuration failure instead of choosing by ID. Go validates every user-entered expression with its RE2-compatible regular-expression engine before storage.

The worker calls Alibaba Cloud Token Plan at the configured OpenAI-compatible base URL using exactly `qwen3.8-flash`, `response_format: {"type":"json_object"}`, and `enable_thinking: false`. JSON Object mode is intentional for the Singapore endpoint because [Alibaba Cloud documents JSON Schema structured output as unsupported for Singapore](https://www.alibabacloud.com/help/en/model-studio/qwen-structured-output). Because JSON Object mode guarantees valid JSON but not an application schema, the prompt states the exact nested response shape and the Go decoder, evidence checks, and domain validation remain authoritative. Requests have bounded text, attachment, total-body, response, and time limits.

The model receives normalized source evidence and eligible attachment images, but no Account catalogue, Account metadata, configured matching keys, or other users' data. The model response is treated as untrusted. Strict decoding rejects unknown fields and extra JSON values. The server—not the model—binds user ownership, auto-eligibility, and aggregate confidence. Populated decisive fields, including generic additional identifiers, must cite the exact grammar `received_at` or a path rooted at `subject`, `sender`, `text`, or `attachment` with dot-name and numeric-index segments; a candidate field name or extracted value is never a source path. Only server-applied rules may add a `rule:<id>:v<version>` citation. `received_at` is used only as an occurred-at fallback when no explicit event timestamp exists. Aggregate confidence is the minimum valid confidence among decisive citations so a strong citation cannot hide a weak required fact.

Every parse attempt stores bounded, owner-private audit data: the assembled system prompt, normalized user input, exact JSON request sent to Alibaba, exact JSON response returned by Alibaba, exact model-output object, server-validated candidate, selected rule IDs/versions, prompt components, attachment usage, validation state, and redacted error. Authentication headers and API keys are never stored. Failed and invalid attempts retain every boundary reached, so the Debug view can distinguish provider, schema, evidence, and configuration failures.

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
- Exactly one Account match → consider same-user, same-Account, same-kind transactions within ±24 hours of the source time.

Candidate scoring is explainable: Account 50, shared reference 60, exact amount 25, exact currency 15, normalized merchant 12, and within-ten-minutes time 15. A score of at least 90 still cannot automatically attach unless either an exact reference is shared or amount/currency/normalized merchant match exactly within ten minutes. A second candidate within ten score points makes the result ambiguous. Any plausible but unsafe existing match goes to Review.

With no existing candidate, cited confidence of at least 0.75 and server-derived source corroboration creates a confirmed Account-linked transaction; otherwise the source goes to Review. Corroboration requires source text to contain a bounded Account identifier, exact ISO-scaled currency-qualified amount, and merchant or reference; it does not trust attachment-only facts, bare digits, bare `$`, or model citations. Automatic and user-created transactions preserve reference/Account evidence in the transaction `details` JSON. Optional categories are applied only when exactly one active leaf matches; otherwise the transaction remains uncategorized.

## Database shape

All monetary columns use `bigint` integer minor units. Timestamps use `timestamptz`; user-owned primary keys are UUIDs. Ownership-sensitive foreign keys use composite `(user_id, id)` references where cross-table ownership must be enforced.

### Public, RLS-protected projection

| Table | Purpose and important fields |
| --- | --- |
| `public.transaction_categories` | Global, system-managed category catalogue: `parent_name`, `name`, `emoji`, `sort_order`, `active`. Authenticated users can select only. |
| `public.transactions` | Canonical records: required owner/Account, `transaction_kind`, title/merchant, original amount/currency, optional SGD, time, optional category, `line_items` array, `details` object, review/confidence, `creation_method`, optional `user_modified_at`, and timestamps. Browser users can select only; Go performs writes. |
| `public.transaction_sync_runs` | Owner-safe async projection: lifecycle state/timestamps, ingestion-complete marker, message/source/progress/outcome counts, redacted error summary. Owner-select only and published to Supabase Realtime. |

`public.transactions` references an active Account owned by the same user; a trigger also blocks inserts/Account changes to soft-deleted Accounts. `transaction_kind` stores only `debit` or `credit`, and amounts remain positive. Where a transaction is linked as an internal-transfer leg, database integrity checks protect the pair from being made a same-Account transfer.

### Non-exposed private operational schema

| Table | Purpose |
| --- | --- |
| `private.gmail_connections` | Encrypted refresh token, token metadata, exact label, History cursor, status, last sync/error. One Gmail connection per user. |
| `private.gmail_oauth_states` | Single-use state digest and encrypted PKCE verifier with expiry/consumption protections. |
| `private.data_sources` | Durable generic evidence, provider identity, JSON payload, parser provenance/state, Account/transaction suggestions, and reconciliation reason. Current allowed types: `gmail_email`, `phone_notification`. |
| `private.source_parser_rules` | Versioned global regex/rule configuration and priority. |
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

### Line-item JSON

`transactions.line_items` is always a JSON array. The Go API and parser validate every item before save:

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

The HTTP contract serializes all minor-unit values as decimal strings to avoid JavaScript precision loss; Postgres and Go use `bigint`/`int64`. `quantity` is a positive integer. Optional item amounts may be zero. `details` must be an object and is rendered as safe scalar/JSON content, never HTML.

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
| `PATCH /v1/transactions/{id}` | Edits only title, active Account, time, original amount/currency, optional SGD amount, optional category, and line items. |
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

Transaction/source list cursors are versioned, scope-bound base64url values. Results sort newest first using `(timestamp, id)` keysets. Default frontend pages contain 50 items. Responses serialize monetary values as decimal strings and reject malformed service contracts in the TypeScript client.

## React implementation

The workspace navigation contains a Transactions section with two independent pages: the four-tab transaction workspace and `frontend/src/features/transactions/TransactionSettingsPage.tsx`.

- Transactions supports title/merchant search, debit/credit and review filters, cursor “load more,” original/SGD display, Account/category labels, evidence counts, and transfer badges.
- Sync startup restores the latest run. Active runs subscribe to owner-readable `transaction_sync_runs` changes and also poll every ten seconds when Realtime is ready or every three seconds when it is not. Monitoring pauses after 40 checks while server work continues and can be resumed.
- Source inspection loads sanitized email and private signed attachments, previews PDF/images, and supports Failed retry with a safe error summary or Review/Dangling attach/create resolution.
- Source inspection exposes an explicit Debug panel with bounded latest-attempt previews and on-demand exact-field loading for the complete owner-only Qwen audit. A separate destructive-confirmation flow reports whether durable raw-source Storage cleanup is pending.
- Transaction detail edits canonical fields and versioned line items, displays the transfer counterpart and active evidence, and uses a confirmation step before unmatch.
- Internal-transfer creation collects and validates both legs before one Go request, including outgoing, incoming, or both-leg evidence selection.
- Transaction Settings edits the default parser instructions, versioned sender/subject/content rules, and immutable Account matching keys. It explains prompt precedence, RE2/AND semantics, normalized values, and retire/reactivate behavior.
- Dialogs trap focus, close with Escape, restore focus, and block background interaction. Tabs support arrow/Home/End keys, and the workspace has a mobile navigation drawer.

The frontend reads owned Account options and global categories from Data REST with the publishable key plus user bearer token. The source-resolution candidate picker uses the existing RLS-protected Account transaction query; every source, attachment, OAuth, sync, reconciliation, mutation, and main list operation goes through Go.

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

- `public.transaction_categories`, `public.transactions`, and `public.transaction_sync_runs` have RLS. Owner data uses `(select auth.uid()) = user_id`; authenticated browser grants are select-only for this feature.
- The `private` schema has no `anon`/`authenticated` grants. Its tables also enable RLS as defense in depth.
- Browser roles are explicitly blocked from `transaction-attachments`; only Go’s server key can access the private bucket.
- Source, transaction, Account, link, sync, and attachment paths are checked against the authenticated user before reads or writes.
- Parser settings, source-rule changes, matching keys, Debug records, and deletion plans are owner-scoped in SQL as well as at the HTTP boundary.
- Qwen never receives the Account catalogue or matching-key table. User guidance is appended beneath an immutable platform contract and email/attachment instructions are treated as untrusted content.
- Raw deletion is database-first and owner-scoped. A per-user coordination lock prevents races with Gmail ingestion; database cascades clear source jobs, attempts, and links; transaction provenance limits automatic cleanup to a never-edited `automatic_source` record with no remaining active evidence or transfer link. Exact Storage paths are committed as leased cleanup work before any external call, a one-way provider digest prevents reingestion of deliberately deleted evidence, and cleanup failures or expired final leases remain queued with monitored cooldown until success.
- Automatic canonical creation takes the same stable per-user transaction lock and repeats Account/nearby-transaction reconciliation inside the write transaction. Two workers holding stale create decisions therefore converge on one transaction and two evidence links rather than creating duplicates.
- The model never chooses user ownership. Gmail/provider content, HTML, filenames, parser output, category names, and cursor values are validated before use.
- Transactions and internal-transfer legs can reference only active same-user Accounts. Transfer pair integrity is enforced in both Go and a deferred database trigger.
- OAuth state is expiring/single-use, PKCE is required, refresh tokens are encrypted at rest by the application, and secrets/errors are not returned to the browser.
- Public-table grants are explicit, so the feature does not depend on automatic Data API exposure behavior.

## Verification and release gate

The repository contains focused Go tests for Auth, OAuth state/token storage, Gmail listing/history and parsing, attachment Storage/signing, ingestion idempotency, durable jobs/leases, parser-provider contracts, deterministic rules, semantic card corroboration, reconciliation races, HTTP validation, source actions, cleanup recovery, and SQL store behavior. It also contains pgTAP coverage for transaction schema/RLS/storage and the operational migrations.

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

For a future release rerun, validate each migration with a hosted dry run and transaction-wrapped rollback rehearsal, run the transaction pgTAP tests and Supabase security/performance advisors, perform the scoped live harness, and confirm local/remote migration history after any hosted application. Do not start local Supabase/Docker unless the user explicitly requests it. The current release results are recorded below.

### Status at this documentation update

| Gate | Status |
| --- | --- |
| React/Go implementation and focused coverage | **Passed.** Go/frontend build and lint, Go race checks, and integration coverage passed in the completed verification runs. |
| Hosted migration rehearsal/application/history | **Passed.** `20260902230003_keep_source_cleanup_retrying.sql` passed dry run and transaction-wrapped rollback rehearsal, is applied after `20260902230002`, and local/remote histories match through `20260902230003`. No local Supabase or Docker instance was used. |
| Hosted database validation | **Passed.** Database lint reports no schema errors; all 189 pgTAP assertions pass and roll back their fixtures, including 28 source-deletion and 9 cleanup-recovery assertions. |
| Hosted Go store integration | **Passed.** The race-enabled transaction-store integration suite passes through the configured transaction pooler, including serialized concurrent create decisions, deletion staging/tombstones, non-terminal cleanup retry and expired-lease recovery, source reingestion prevention, and bounded/exact Debug behavior. |
| Live Gmail → private Storage ingestion | **Passed.** The initial scoped run fetched and stored five unique `odin-finance` sources, including one private attachment. |
| Live Qwen parsing and reconciliation | **Baseline passed; current targeted check awaiting explicit approval.** Earlier `qwen3.8-flash` checks reached the provider with thinking disabled and reconciled the five-source backfill. The exact FairPrice invalid-category response is now covered by a worker regression: optional category/citation is discarded while semantically corroborated masked-card evidence `Mastercard (**** 2562)` remains; a bare or conflicting four-digit value is demoted, and required or other populated-field citation failures still fail. A fresh retained-source call is not claimed until outbound-data approval is granted. |
| Idempotent replay | **Passed.** Repeating the same five-message run created zero duplicate sources. |
| Five-minute signed attachment access | **Passed.** A live ranged download through the signed URL succeeded within its five-minute lifetime. |
| Hosted anonymous/private-schema denial | **Passed for the exercised surfaces.** Anonymous REST requests to Transactions and sync data returned `401`; requesting the private source schema returned `406`. |
| Signed-out browser, mobile, and accessibility controls | **Passed.** Desktop and mobile signed-out views and accessibility controls were exercised with no console warnings. |
| Authenticated browser workflow | **Passed for read-only acceptance paths.** Using the existing owner session, desktop and mobile navigation reached the independent Transaction Settings page; Account options, the active Citi `2562` matching key, Failed source inspection, sanitized email rendering, and the three-attempt Debug audit loaded without console errors or warnings. Destructive and configuration-save controls were deliberately not invoked during this read-only pass. |
| Cross-user source/transaction/sync/attachment denial | **Passed in automated RLS/owner tests; live second-user attempt unavailable.** The hosted project has only one user, so no live cross-user claim is made. |

The implementation is release-ready on the completed automated, hosted-schema, authenticated-browser, and prior provider evidence above, subject to the explicitly identified current-source approval and the unavailable live second-user check. Neither is implied as a pass.
