# Account Balances and Credit Card technical design

Status: delivered. The React Account-balance and Credit Card experiences use the
authenticated Go API and hosted schema, and the Bulk Import worker invokes the
implemented Credit Card post-processor. Opening-balance revisions, calculation
treatments, bill reconciliation, payment suggestions, missing-leg completion,
payoff, void, and audit paths are implemented.

Hosted migrations `20260904043716_create_bulk_import_foundation.sql`,
`20260904043721_create_account_balances_and_credit_card_bills.sql`, and forward
repair `20260904061318_disambiguate_credit_card_validation_records.sql` are
applied with matching local/remote history. The latest hosted checks pass all 33
focused Account Balance/Credit Card pgTAP assertions and report no public/private
schema lint errors. The Go API and worker also start against the hosted project,
and every route described below requires an authenticated Supabase user.

This design implements the requirements in the accompanying [feature README](README.md). It preserves the current split between browser-accessible Account CRUD, the Go API for financial workflows, and the separate [Bulk Insert technical design](../bulk-insert/technical.md) for user-uploaded evidence.

## Design boundaries

| Boundary | Responsibility |
| --- | --- |
| React SPA | Displays Account baselines/current balances, treatment explanations and permitted changes, and a top-level Credit Card workspace grouped by active Card Account. It reuses the Account-scoped bill detail for bill-line reconciliation, payment suggestions, and payoff confirmation. It never uploads bill files, calculates with floating-point values, or writes private financial workflow rows directly. |
| Go Account Balances API/domain | Authenticates the user; creates/corrects baselines; derives Account/net-worth/spending projections; receives the internal Credit Card bill processor result from Bulk Import; manages bill review; detects/links payments; and invokes the existing internal-transfer service for payoff or missing-leg completion. |
| Existing Transactions domain | Remains the sole owner of canonical financial events and internal-transfer pairs. It supplies confirmed transaction data and `private.transaction_links`; it does not treat a statement as a Transaction. |
| Bulk Insert domain | Is the only upload path and owns Storage, extraction, sources, candidates, audits, jobs, reconciliation, retry, and cleanup. Its server-owned `credit_card_bill` processor invokes the Account Balances domain after shared candidate reconciliation; it does not duplicate bill-domain storage or payment rules. |
| Hosted Supabase | Holds Account baseline columns, private statement/treatment records, composite ownership FKs, RLS, and the existing private source/Storage data. |

No browser receives a database connection string, service-role key, private source payload, raw Storage path, or a direct write grant for baselines, corrections, statements, statement lines, or treatments.

## Minimal schema footprint

The implementation creates seven private domain tables, reuses the Bulk Import API-idempotency table, and changes two existing public tables. It deliberately does not add a payment-allocation table, a billing scheduler, or a second upload/evidence model.

| Object | Change | Reason |
| --- | --- | --- |
| `public.accounts` | Add opening-balance value, as-of time, and version projection columns. | Owner-readable current state without exposing mutation access. |
| `public.transactions` | Permit `creation_method = 'credit_card_statement'` and retain current canonical fields. | Marks a transaction created exactly once from a statement line without cloning transaction semantics. |
| `private.account_opening_balance_revisions` | New. | One immutable revision header per Account/version. |
| `private.account_opening_balance_revision_amounts` | New. | One exact currency/minor-unit amount per revision. |
| `private.transaction_calculation_treatments` | New. | Stores user/system spending treatment without mutating raw Transaction evidence. |
| `private.credit_card_statements` | New. | One imported bill and its optional matched or newly created full payment transfer. |
| `private.credit_card_statement_lines` | New. | One line projected from the imported bill document, optionally linked one-to-one to a canonical Transaction. |
| `private.credit_card_statement_payment_candidates` | New. | Retains zero, one, or several credible Bank debits so the user can select an ambiguous payment. |
| `private.credit_card_statement_events` | New. | Immutable ordered audit events for import, review, line, payment, payoff, and status actions. |

`private.transaction_links` is not altered. `credit_card_statements.payoff_transaction_link_id` points to its existing internal-transfer row.

## Existing-table changes

### `public.accounts`

Add:

| Column | Contract |
| --- | --- |
| `opening_balances jsonb not null default '{}'::jsonb` | A JSON object mapping uppercase ISO 4217 currency code to a canonical minor-unit integer string. `{}` means unconfigured. |
| `opening_balance_as_of timestamptz null` | Required when `opening_balances` is non-empty; null only when balances are `{}`. |
| `opening_balance_version integer not null default 0` | `0` is unconfigured. The first explicit balance is version `1`; every correction increments the value. |

Examples:

```json
{}
```

```json
{
  "SGD": "125000",
  "USD": "4200"
}
```

```json
{
  "SGD": "-35000"
}
```

The final example is valid only for a Bank Account and represents an overdraft.

Database validation must reject:

- a non-object value, more than 20 currencies, or more than 16 KiB serialized JSON;
- a key that is not exactly three uppercase ASCII letters;
- a non-string amount, decimal/floating-point representation, plus sign, leading zero other than `"0"`, or value outside PostgreSQL `bigint` range;
- a non-empty map with a null baseline timestamp, or an empty map with a non-null timestamp;
- a first baseline or correction with an empty map, a no-op correction that repeats both the current map and as-of time, or a baseline timestamp later than the server's current time;
- a negative amount for any Account other than `bank_account`; and
- an Account-side/type mismatch discovered after an Account edit.

The JSON shape can be checked with a stable validation function. Negative-value validation needs the Account row, so a `BEFORE INSERT OR UPDATE` trigger must validate the JSON against `NEW.account_type`. The function must use a fixed search path, accept no caller-controlled SQL, be uncallable by browser roles, and exist solely as an integrity trigger—not as a browser API.

History is normalized in `private.account_opening_balance_revisions` and `private.account_opening_balance_revision_amounts`. A revision stores the owned Account, contiguous version, as-of time, optional/required reason, owner actor, and server time. Its child rows store one uppercase currency plus one `bigint` minor-unit amount. The first revision has no reason; later revisions require a 1–500 character reason. There is no JSON history ceiling or silent pruning.

The migration backfills all existing Accounts with the default empty object, null timestamp, and version `0`, and inserts no revision rows. This is additive and does not claim those Accounts had an opening balance of zero.

The existing table-wide browser `INSERT`/`UPDATE` grants must be replaced by explicit column grants. The exact authenticated browser surface is:

- `INSERT (user_id, side, account_type, name, institution_name, account_identifier, notes, metadata, sort_order)`;
- `UPDATE (side, account_type, name, institution_name, account_identifier, notes, metadata, sort_order, deleted_at)`; and
- `SELECT` remains owner-scoped by the existing Accounts RLS policy.

That omits `id`, all timestamps, `user_id` on update, and every `opening_balance_*` field. Browser roles retain direct Account directory CRUD but cannot insert or update a baseline, its version, or its correction history. The Go API is the only write path for balances/corrections. Owner-scoped `SELECT` may expose the current balance fields and history because the row is already restricted by Accounts RLS.

Deferred validation locks the Account and proves that revision versions are contiguous from one, every revision has 1–20 valid currency rows, later revisions are not no-ops, and the current JSON/as-of/version projection exactly equals the latest normalized revision. Revision headers and amount rows are immutable; a normal Account-directory update cannot change the protected projection because browser column grants omit it. Account type changes are revalidated so a negative baseline cannot be moved to an ineligible type.

### `public.transactions`

Extend the existing `transactions_creation_method_check` from `automatic_source`, `user_source`, `manual`, and `internal_transfer` to also allow `credit_card_statement`. Only the Go statement-line create action may write that value. The existing browser manual-insert column grants already omit `creation_method` and must continue to do so; the statement service must not expose a general-purpose provenance override.

No statement amount, summary, or payoff record is inserted into `public.transactions` merely because a statement exists.

## New tables

All new tables live in `private`, use UUID primary keys, include `user_id`, enable RLS as defense in depth, revoke grants from browser roles, and use composite ownership foreign keys where a related row is user-owned. Every foreign-key and RLS-filter column receives an appropriate index.

### `private.transaction_calculation_treatments`

One current treatment per canonical Transaction.

| Column | Contract |
| --- | --- |
| `transaction_id uuid` | Primary key and owner-matched FK to `public.transactions (user_id, id)` with `on delete cascade`. |
| `user_id uuid` | Required FK to `auth.users`; must match the transaction owner. |
| `spending_basis text` | `transaction_total`, `line_items`, or `exclude`. |
| `source text` | `system` or `user`. |
| `reason text` | Required 1–500 characters for user entries; fixed safe reason for system entries. |
| `created_at`, `updated_at` | Server timestamps. |

The absence of a row means the effective basis is `transaction_total`, unless the server derives a fully reconciled line-item treatment while displaying a read-only preview. Persisting a user choice or a system exclusion creates the row.

System-owned `exclude` rows are created atomically for both legs of a Credit Card payoff transfer with reason `credit_card_payoff`. The API rejects attempts to edit or delete them. User-owned rows may be updated only through Go after validation.

`line_items` is valid only when every item has a canonical `line_total_minor`, each line currency equals the Transaction original currency, and the signed-independent sum equals `original_amount_minor`. The validator uses decimal-string/`bigint` arithmetic and rejects an incomplete list rather than guessing an unallocated remainder.

This table changes only spending calculation. The API has no `exclude_from_account_balance` option in v1. Confirmed transactions always retain their Account-balance effect.

Indexes: primary key covers transaction lookup; add `(user_id, spending_basis)` only if the reporting query plan requires it after measurement.

### `private.credit_card_statements`

One bill created automatically from one Bulk Import `credit_card_bill` document for one active Credit Card Account. The internal table name keeps `statement` because it represents the issuer statement; the UI consistently calls the record a bill.

| Column | Contract |
| --- | --- |
| `id uuid` | Primary key; unique with `user_id`. |
| `user_id uuid` | Required owner FK. |
| `account_id uuid` | Required composite FK to the one active owned Credit Card selected by the Bulk Import batch. Go and trigger validation require `account_type = 'credit_card'`. |
| `bulk_document_id uuid` | Required composite FK to the owned `private.bulk_import_documents` row with `on delete restrict`. The document must use the `credit_card_bill` processor, have its derived `data_source_id`, and that source must have type `bulk_upload_document`. A Bulk document may back one bill only. |
| `bulk_attempt_generation integer` | Required positive value copied from the Bulk document during idempotent post-processing. It is immutable and identifies the exact candidate result set used to seed the bill. |
| `period_start date`, `period_end date` | Nullable only in `review`; when populated, start must not follow end. |
| `statement_date date`, `due_date date` | Nullable only in `review`; when populated, due date cannot precede statement date. |
| `settlement_currency text` | Nullable only in `review`; otherwise an ISO 4217 three-letter code. |
| `amount_due_minor bigint` | Nullable only in `review`; otherwise a positive integer in settlement currency. Zero-due bills are not supported in v1. |
| `unresolved_candidate_count integer` | Durable non-negative count of extracted candidates that could not be projected safely into bill lines. A positive count forces `review` and survives reloads even when no line row can represent the failed or omitted result. |
| `status text` | `review`, `unpaid`, `paid`, or `void`. `unpaid` is the user-visible replacement for the earlier internal `open` concept. |
| `payoff_transaction_link_id uuid null` | Optional unique FK to the existing owned internal-transfer link. Present only when status is `paid`. |
| `version integer` | Positive optimistic-concurrency version for bill review and payment actions. |
| `created_at`, `updated_at`, `paid_at` | Server timestamps; `paid_at` is required only for `paid`. |

Constraints and deferred validation enforce:

- one Bulk document cannot create two bills;
- one payoff transfer cannot settle two bills;
- only a Credit Card Account can own the bill;
- the referenced source belongs to the user and uses the Bulk Import Credit Card bill processor;
- the bill Account is exactly the one selected active Credit Card Account for that Bulk document; contradictory document Account evidence keeps the bill in `review` but cannot change the Account;
- `unpaid` and `paid` require all header fields, while `review` permits only fields the document could not supply or reconcile safely to remain null;
- a positive unresolved-candidate count requires `review` and forbids a payoff link or paid timestamp;
- paid state requires exactly one payoff link, and review/unpaid/void states require none;
- the payoff link is an `internal_transfer` whose debit leg belongs to an active owned Bank Account, credit leg belongs to this bill's Card Account, both original currencies equal `settlement_currency`, and both amounts equal `amount_due_minor`; automatic detection additionally requires its time to be between statement date and due date, inclusive; and
- period overlap for the same Card Account is rejected unless an explicitly documented replacement/void workflow is added later.

Indexes: `(user_id, account_id, period_end desc, id desc)`, unique `bulk_document_id`, unique non-null `payoff_transaction_link_id`, and indexes for all FKs. A deferred validator locks the Card Account before checking period overlap, closing concurrent insert races without requiring a new extension.

### `private.credit_card_statement_lines`

One line projected from the imported document. Once the bill is generated, these evidence-backed lines are the source of truth for reconciliation. Every match or safe missing-Transaction creation starts from one of these lines.

| Column | Contract |
| --- | --- |
| `id uuid` | Primary key; unique with `user_id`. |
| `user_id uuid`, `statement_id uuid` | Required composite FK to the owned statement. |
| `bulk_candidate_id uuid null` | Required for an activity/refund/fee/interest/payment line that came from extracted evidence; owner-matched FK to the pinned Bulk candidate belonging to this statement's document and attempt generation. Null only for a summary/annotation row that has no transaction candidate. |
| `line_index integer` | Required one-based position copied exactly from the validated Credit Card bill candidate. |
| `line_kind text` | `activity`, `refund`, `fee`, `interest`, `payment`, or `summary`. Activity/fee/interest create a Card `debit` when missing; refund creates a Card `credit`; payment may only attach to an existing Bank → Card internal-transfer credit leg. |
| `line_fingerprint bytea` | Required 32-byte server-computed identity; unique within the bill. Model fingerprints are never trusted. |
| `resolution_status text` | `pending`, `linked`, or `ignored`. A document activity/refund/fee/interest/payment line begins `pending`; `linked` means its canonical Transaction has been attached/created; `ignored` requires a bounded user review reason. Summary lines are created as `ignored` with the fixed system reason `statement_summary`. |
| `resolution_reason text null` | Required 1–500 characters when `resolution_status = 'ignored'`; null for `pending` and `linked`, except the fixed summary reason. |
| `link_exception_reason text null` | Required 1–500 characters only when the attached Transaction's amount/currency or date differs from a populated document-line value. It records, for example, issuer FX conversion or a documented date-posting delay. |
| `occurred_on date null` | Required for non-summary lines when the document supplies a date. |
| `occurred_at timestamptz`, `time_precision text` | Exact source time when available. Date-only evidence uses noon UTC with `date` precision and matches by UTC calendar day rather than the ten-minute exact-time window. |
| `description text` | Required sanitized display text, 1–500 characters. |
| `amount_minor bigint null`, `currency text null` | Exact document amount when present. Both null for non-monetary summary/annotation rows; otherwise a positive amount and ISO currency. |
| `transaction_id uuid null` | Optional composite FK to an owned canonical Transaction. |
| `created_at`, `updated_at` | Server timestamps. |

Constraints and deferred validation enforce:

- `(statement_id, line_index)` is unique;
- a statement-document line cannot be duplicated by fingerprint/index during repeat import;
- an extracted Bulk candidate can appear on one statement line only and must belong to the statement's exact document and `bulk_attempt_generation`;
- a linked Transaction has the same owner and Card Account as the statement;
- a linked Transaction appears on no other statement line;
- summary lines cannot link to a Transaction;
- `linked` requires exactly one Transaction, `pending` requires no Transaction, and `ignored` requires no Transaction plus its review reason; and
- an attached Transaction's original amount/currency must equal populated document values and its date must fall within the inclusive billing period, unless the user supplies `link_exception_reason`; and
- a payment line may link only to the Credit Card credit leg of an existing owner-scoped internal transfer whose debit leg belongs to an active owned Bank Account.

An imported payment line may link to an existing Bank → Card transfer. After all line reconciliation is terminal, the payment detector may also use that exact qualifying transfer as the bill's payoff link and mark the bill Paid. A payment line alone, an ambiguous link, or a Bank debit without a Card leg cannot mark the bill Paid.

Indexes: `(user_id, statement_id, line_index)`, unique non-null `bulk_candidate_id`, unique non-null `transaction_id`, and the required FK indexes.

### `private.credit_card_statement_payment_candidates`

The detector persists every credible owner-matched debit from an active Bank Account instead of discarding ambiguity or guessing. Each row belongs to one bill and one canonical Bank debit and has status `suggested`, `selected`, `confirmed`, or `dismissed`, a bounded reason/optional score, and selection/confirmation timestamps. Any number may remain suggested; partial unique indexes allow only one selected/confirmed choice per bill and prevent one Bank debit being selected/confirmed for two bills. The FK is restrictive so a suggestion cannot disappear silently.

Selecting an ambiguous candidate is a user action in the Credit Card workspace. Confirmation re-locks the bill and debit, repeats amount/currency/date/Account checks, creates only the missing Card credit leg, links the transfer, and marks that candidate confirmed atomically. The transfer's canonical time is the Card credit-leg timestamp; debit and credit legs must be within ten minutes of one another.

### `private.credit_card_statement_events`

Every import, header correction, line resolution, payment detection/selection/confirmation, payoff, void, and status transition appends an ordered immutable event with owner, optional owner actor, bounded structured details, and server time. Events are server-only and cascade only when an eligible Review bill itself is discarded. They are audit metadata and never affect balances or spending.

## Bulk Import processor integration

This feature depends on the Bulk Insert migration and its `bulk_upload_document` source type. The two forward migrations were applied in dependency order: the Bulk Insert foundation first, then Account Balances and Credit Card.

The Credit Card bill path is a document processor inside Bulk Import's shared asynchronous pipeline. There is no browser-triggered “create from document” handoff and no Credit Card uploader. After candidate reconciliation reaches a terminal state, Bulk Import calls a narrow Account Balances domain operation with IDs only; that operation loads and validates all data server-side. The resulting bill projection is the only entry point for subsequent line reconciliation.

The internal integration consumes:

| Bulk Insert output | Bill use |
| --- | --- |
| Owned `bulk_document_id` and generation | Become the bill's immutable source identity; its server-derived `data_source_id` proves the private uploaded evidence exists. |
| Document identity and safe display metadata | Shown as evidence context only. |
| Strictly validated `document_summary` | Seeds bill header fields. Missing/conflicting values remain Review fields; summary amounts never create Transactions. |
| Candidate outputs and terminal reconciliation outcomes | Seed evidence-backed activity/payment lines. Created/attached Card Transactions are linked after Account/period checks; unresolved candidates remain Review lines. |
| Selected-Account snapshot and Account evidence | The batch supplies exactly one active Credit Card; contradictory document evidence keeps the bill in Review but cannot silently retag it. |
| Candidate/transaction links | Pre-link known canonical transactions only when consistent with the resolved Card and bill period. |

The operation idempotently creates one bill for the document, pins the current `attempt_generation`, projects its lines, persists `unresolved_candidate_count`, and runs payment detection. Complete headers plus a zero unresolved count and terminal unambiguous line outcomes yield Paid or Unpaid. Missing headers, unresolved or omitted candidates, Account conflicts, or ambiguous payment matches yield Review. The bill is created even when review is required so the user has one place to resolve the result.

**Implemented Bulk Import integration points:** its migration adds `unique (id, user_id)` to `private.bulk_import_documents` and `private.bulk_import_candidates`. Its processor exposes the typed bill summary and triggers one `bulk_document_post_process` job after candidate reconciliation. Retry and source-deletion workflows check retained bill references. The Bulk-owned candidate-reconciliation service also exposes one narrow in-transaction operation for a user-resolved bill line: create the canonical Card Transaction from the pinned candidate, update that candidate outcome, and attach its existing evidence with `creation_method = 'credit_card_statement'`.

No new Storage bucket, signed-upload route, provider client, standalone model request, parse-attempt store, source row, candidate table, dedupe algorithm, retry mechanism, or source-deletion path belongs to Account Balances. Only the shared Bulk post-processing job invokes the bill-domain operation.

The foreign key keeps raw Bulk evidence from being deleted while **any retained bill** references it, including a Void audit record. `DELETE /v1/credit-card-statements/{id}` is allowed only while the bill is in Review and deletes its projection lines in the same transaction; it never deletes a canonical Transaction, source, Bulk audit, or file. Unpaid bills are voided instead; Paid and Void bills are retained permanently in v1. After a permitted Review discard, Bulk Import's source-deletion lifecycle remains the sole owner of evidence cleanup.

## API surface

All routes require a Supabase bearer token. The server obtains the actor only from the token; requests never carry `user_id`.

### Account balances and treatments

| Method and path | Behaviour |
| --- | --- |
| `GET /v1/accounts/balances` | Returns owner-safe Account baseline, calculated per-currency balance, calculation cutoff, and treatment-aware supporting totals. Unconfigured Accounts return a state, not `0`. |
| `PUT /v1/accounts/{id}/opening-balance` | Validates and replaces the baseline atomically, appends exactly one immutable history entry, and requires `expected_version`. The first save has no correction reason; later edits require one. |
| `GET /v1/accounts/{id}/opening-balance/history` | Returns the owner-only immutable baseline correction history. |
| `GET /v1/transaction-calculation-treatments/{transaction_id}` | Returns the owned Transaction's effective treatment and version ETag, including the default or immutable system treatment. |
| `PUT /v1/transaction-calculation-treatments/{transaction_id}` | Sets a permitted user treatment with reason; validates line-item completeness. Rejects system-owned payoff treatment changes. |

Calculation treatments use their own top-level collection rather than a crossed `/v1/transactions/{id}/...` wildcard. This keeps the treatment resource unambiguous alongside the established `/v1/transactions/sync-runs/{id}` progress route while preserving owner-scoped authentication and Transaction identity.

Baseline history is read from the normalized immutable `private.account_opening_balance_revisions` and `private.account_opening_balance_revision_amounts` rows. The browser cannot read or mutate those private rows directly; the Go API returns an owner-scoped projection. `public.accounts.opening_balances` is only the owner-readable current projection, and deferred validation proves it exactly matches the latest revision.

### Credit Card bills

| Method and path | Behaviour |
| --- | --- |
| `GET /v1/accounts/{account_id}/credit-card-statements` | Keyset-paginated owner-scoped bill summaries for one active/soft-deleted owned Credit Card Account. |
| `GET /v1/credit-card-statements/{id}` | Returns the bill, safe evidence reference, its projected lines and linked transactions, and payment result/suggestions. |
| `PATCH /v1/credit-card-statements/{id}` | Corrects Review-stage header fields with evidence-aware confirmation, then reruns bill resolution and payment detection. The batch-selected Card is immutable; rejects changes after Unpaid/Paid/Void state. |
| `POST /v1/credit-card-statements/{id}/lines/{line_id}/attach` | Atomically links an eligible existing Card Transaction to a line. |
| `POST /v1/credit-card-statements/{id}/lines/{line_id}/create-transaction` | Delegates to the narrow Bulk candidate-reconciliation operation to create one evidence-backed Card Transaction, then links it to the line. It is permitted only for activity/refund/fee/interest with a pending pinned candidate; debit/credit kind is fixed by the line kind. |
| `POST /v1/credit-card-statements/{id}/lines/{line_id}/ignore` | Resolves an inapplicable or non-transaction line with a required bounded reason. |
| `POST /v1/credit-card-statements/{id}/payment-candidates/{candidate_id}/select` | Selects one owner-scoped Bank debit from the bill's persisted suggestions after revalidating it. Other suggestions remain auditable and are not silently chosen. |
| `POST /v1/credit-card-statements/{id}/payment-candidates/{candidate_id}/confirm` | Confirms the explicitly selected Bank debit, creates only the missing Card credit leg, links the pair as an internal transfer, and marks the bill Paid atomically. |
| `POST /v1/credit-card-statements/{id}/payoff` | For an Unpaid bill, creates the exact new Bank → Card internal-transfer pair, system exclusion treatments, payoff link, and Paid status in one transaction. |
| `POST /v1/credit-card-statements/{id}/void` | Voids a Review/Unpaid bill with reason; rejects Paid bills. It does not delete sources or Transactions. |
| `DELETE /v1/credit-card-statements/{id}` | Discards a Review-stage bill only. Cascades only to its projection lines; it rejects Unpaid/Paid/Void bills and never deletes canonical Transactions or Bulk evidence. |

Every mutation decodes strict JSON, limits bodies to 1 MiB, rejects unknown fields and extra values, and either uses an idempotency key or a resource-version precondition. Retries return the existing safe result rather than duplicating a baseline correction, Transaction, link, or payoff.

### Mutation request contracts

Amounts cross the API boundary as canonical minor-unit decimal strings, never JSON numbers or major-unit decimals. UUIDs are RFC 4122 strings, dates are `YYYY-MM-DD`, and timestamps are RFC 3339 UTC timestamps.

| Action | Exact request body | Server-owned/derived values |
| --- | --- | --- |
| Set/correct baseline | `{ "balances": {"SGD":"125000"}, "as_of":"2026-09-04T00:00:00Z", "expected_version": 1, "correction_reason":"..." }` | `user_id`, next version, `changed_by_user_id`, `changed_at`, and appended history snapshot. `correction_reason` must be absent/null for version 0 and required otherwise. |
| Set treatment | `{ "spending_basis":"line_items", "reason":"Use the fully itemized receipt" }` | owner, source=`user`, timestamps. The API refuses system treatments and derives/validates all line-item facts from the canonical Transaction. |
| Correct Review bill | `{ "period_start":"2026-08-01", "period_end":"2026-08-31", "statement_date":"2026-09-01", "due_date":"2026-09-25", "settlement_currency":"SGD", "amount_due_minor":"123450", "reason":"Confirmed against statement" }` | Account, Bulk document/generation, source relation, candidate lines, owner, status transition, payment detection, timestamps. Only fields requiring Review are accepted; evidence-derived unchanged fields need not be resubmitted. |
| Attach line | `{ "transaction_id":"…", "link_exception_reason":"…" }` | owner, line status, and match validation. `link_exception_reason` must be omitted when exact document amount/currency/date checks pass and is required otherwise. |
| Create missing line Transaction | `{ "category_id":"…" }` | owner, Account, amount, currency, date, title/merchant evidence, debit/credit direction, evidence link, Bulk-candidate outcome, `creation_method`, and line status. The API derives these from the reviewed statement line's pinned candidate; the browser cannot supply an amount, currency, Account, or direction. |
| Select Bank-debit suggestion | Empty body; candidate identity is in the route. | The candidate must belong to this bill and remain eligible. The server dismisses no alternatives automatically and records the selection event. |
| Confirm selected Bank-debit suggestion | Empty body; candidate identity is in the route. | The candidate must be the current selection. The server derives Card Account, amount, currency, credit direction, title, payment time, link, treatments, and Paid state. |
| Pay statement in full | `{ "bank_account_id":"…" }` | exact amount/currency, current server payment timestamp, fixed debit/credit directions, canonical transfer titles, transfer link, two system treatments, paid timestamp/status. The browser cannot supply a payment amount, Card Account, currency, destination, or settlement allocation. |
| Void/discard bill | `{ "reason":"…" }` for void; empty body for Review discard | owner, status/timestamps; status rules decide whether the operation is allowed. |

The baseline update supplies `expected_version` and returns HTTP `409` with the current safe baseline on conflict. Bill and treatment mutations use an `If-Match` ETag derived from their version/`updated_at`; a stale precondition returns HTTP `412`. Baseline changes, bill-line creation, payment selection/confirmation, payoff, void, and Review discard additionally require an `Idempotency-Key` header (32–128 safe ASCII characters). The server stores only its digest, canonical request hash, bounded response, and expiry in `private.api_idempotency_records`. A matching retry returns the prior result; a reused key with a different canonical request hash returns `409`. Automatic bill creation is protected by the unique Bulk-document constraint and post-processing job generation rather than a browser idempotency key.

## Calculation algorithms

### Account balances

For each configured Account and original currency:

1. Start with the integer minor-unit opening baseline for that currency, or zero only when that currency is absent from a configured baseline.
2. Select confirmed canonical Transactions owned by that Account with `occurred_at > opening_balance_as_of` and matching `original_currency`.
3. For Asset Accounts, add credits and subtract debits.
4. For Liability Accounts, add debits to amount owed and subtract credits.
5. Return every configured/observed currency separately. Do not convert or sum across currencies.

Transfer legs always participate. A Bank → Card payoff decreases the Asset bank balance and decreases the Liability amount owed. It is neutral only when those Account contributions are combined into net worth.

The calculation never reads bill headers, bill lines, amount due, minimum payment, previous balance, or source evidence as a monetary input.

### Spending

For every confirmed Transaction, determine the effective treatment:

1. A system/user treatment row wins when present.
2. Without a row, use `transaction_total`.
3. `transaction_total` counts the canonical transaction amount once according to the reporting direction/category rules.
4. `line_items` counts validated line totals only; it does not count the canonical header total.
5. `exclude` counts neither header nor line items.

Both payoff legs are `exclude`. A bill has no treatment and no spending value, so neither the imported bill total nor its payoff can double count a card purchase.

### Credit Card bill payment detection

Payment detection runs after bill headers and candidate reconciliation are sufficiently complete:

1. Load owner-scoped `internal_transfer` links whose Card credit leg targets the bill Account.
2. Require exact amount due and settlement currency and a transfer time between statement date and due date, inclusive.
3. Exactly one match sets `payoff_transaction_link_id`, clears any debit-only suggestion, applies the system spending exclusions if missing, and marks the bill `paid` atomically.
4. No complete-transfer match leaves the bill `unpaid` and searches Bank debits in the same amount/currency/date window using normalized issuer/payee/reference evidence.
5. Every credible Bank debit is stored as a separate `private.credit_card_statement_payment_candidates` suggestion. Zero candidates leaves no suggestion; one may be selected by the user; multiple candidates keep the payment portion in Review until the user explicitly selects one.
6. Confirming the selected suggestion locks the candidate, Bank debit, and bill, repeats every check, creates only the missing Card credit, creates the transfer link and system treatments, and marks the bill `paid` in one transaction.

The current-time **Pay in full** action is allowed after the due date because it creates a new payment rather than claiming the bill was already paid within the automatic-detection window.

## Security, RLS, and integrity

- All seven new private domain tables have `enable row level security`, owner-scoped defense-in-depth policies, and no grants to `anon` or `authenticated`. The shared private API-idempotency table is likewise server-only.
- The Go API independently scopes every bill, line, Account, source, Bulk document, Transaction, and transfer lookup by the authenticated owner.
- Composite `(user_id, id)` foreign keys prevent cross-owner joins at the database boundary; all FK columns are indexed.
- No authorization uses `user_metadata`, a browser-submitted user ID, Account name, bill document name, source ID alone, or model output.
- The browser cannot set `creation_method = credit_card_statement`, system treatment rows, payoff links, bill status, derived Bulk source/evidence relationships, correction audit data, or calculated balances.
- Every new validation/trigger function uses a fixed `search_path`, is not exposed as a browser-callable endpoint, and has `EXECUTE` revoked from public roles. No `SECURITY DEFINER` function is introduced unless a migration review proves it is required for a deferred cross-table constraint.
- Database grants and RLS are separate migration steps. New private tables remain unexposed; a future public projection must have explicit grants and RLS before browser use.
- Bulk evidence stays in the existing private Storage bucket. The Credit Card workspace obtains only the existing owner-checked, short-lived signed URL from the Bulk/Transactions API.

## Migration and deployment order

1. Existing hosted migration files and history were preserved; all changes are forward-only.
2. `20260904043716_create_bulk_import_foundation.sql` was applied first, including `bulk_upload_document` source support, document state, candidate state, and source-deletion dependencies.
3. `20260904043721_create_account_balances_and_credit_card_bills.sql` was applied second. It adds Account baseline projection columns, seven private domain tables, validation, checks, indexes, RLS/grants, and the narrow `creation_method` extension.
4. Existing Accounts were backfilled with `{}` and null baseline time. No revision, Transaction, bill, treatment, payment-candidate, event, or financial-value rows were invented.
5. `20260904061318_disambiguate_credit_card_validation_records.sql` was then applied as a behavior-preserving forward repair for PL/pgSQL record-variable/alias ambiguity reported by hosted lint.
6. The compatible Go API/domain/worker and React integration are implemented; Bulk job registration and claims are enabled only when `BULK_IMPORT_ENABLED=true`.

Local and remote hosted histories match. No local Docker Supabase stack was used, and the applied repair did not rewrite either predecessor.

## Verification coverage and current record

### Database

- Existing Accounts receive `{}`/null and are displayed as unconfigured, not zero.
- JSON constraints reject invalid currencies, float values, leading zeros, out-of-range values, invalid timestamps, malformed shapes, and negatives on ineligible Account types.
- Bank overdraft values are accepted; Card/loan negative baseline values are rejected.
- Owner isolation, grants, RLS, and cross-owner composite FK tests cover every new table with two authenticated users and anonymous callers.
- Treatment tests cover default totals, reconciled line-item equality, incomplete/mixed-currency line-item rejection, user audit reasons, and locked system payoff rows.
- Bill tests cover automatic Bulk post-processing, document/Card ownership, document uniqueness, period overlap, one-to-one line/transaction relation established only from projected bill lines, summary-line restriction, status transitions, and safe voiding.
- Payment tests cover unique existing-transfer detection, Unpaid fallback, multiple retained suggestions, explicit ambiguous-candidate selection, Bank-debit-only confirmation, missing Card-leg creation, late manual payoff, and duplicate/concurrent requests.
- Calculation fixtures prove card payoff legs alter component Account balances, leave net worth unchanged, and contribute no spending; header/line-item/statement total combinations never double count.

### Go

- Strict request decoding, resource ownership, stale correction/version conflict, and redacted error tests for every route.
- Decimal-string/`bigint` conversion tests at signed boundaries and no floating-point arithmetic in calculation code.
- Bulk processor tests reject foreign, wrong-type, deleted, ambiguous, stale-generation, or already-consumed documents and prove repeated post-processing is a no-op.
- Bill-line attach/create tests preserve raw source evidence, prevent duplicate canonical creation, require the Card Account, and set `credit_card_statement` provenance only from the dedicated action.
- Payment/payoff orchestration tests prove atomic rollback on every failure point and reuse the existing internal-transfer validation rather than copying it.

### Frontend

- Unconfigured, explicit-zero, multi-currency, overdraft, validation, correction confirmation/history, and current-balance explanation states.
- Treatment display/change states, line-item validation explanation, and immutable system payoff treatment.
- Top-level Credit Card workspace with no uploader, automatic Bulk-result appearance, Paid/Unpaid/Review states, bill-line reconciliation/link/create flows, Bank-debit suggestion confirmation, two-way navigation, void behavior, and full payoff confirmation.
- Keyboard, focus, screen-reader status, narrow-screen, loading, empty, retry, and error paths.

### Hosted acceptance

- Current hosted migration history remains intact; local and remote histories match after application.
- Advisors report no unresolved security errors.
- A processed owned Bulk Credit Card bill creates one bill automatically without a second upload, parse, source, provider call, or browser creation action.
- A representative bill links existing Card Transactions, creates one evidence-backed missing Card Transaction, leaves an ambiguous line unresolved, and is navigable from both bill and transaction detail.
- An exact existing Bank-to-Card transfer marks the bill Paid; no match marks it Unpaid; confirming one Bank-debit-only suggestion creates only the Card leg and then marks it Paid.
- A full Bank → Card payoff updates both Account balances, marks the bill Paid once, and does not change spending totals.

### Status at this documentation update

| Gate | Status |
| --- | --- |
| Hosted migrations and history | **Passed.** `20260904043716`, `20260904043721`, and forward repair `20260904061318` are applied in order and local/remote histories match. |
| Hosted database tests | **Passed.** All 33 focused Account Balance/Credit Card pgTAP assertions pass. |
| Hosted database lint | **Passed.** Public and private schema lint reports no errors after the forward repair. |
| Go API/domain/worker | **Passed.** The full Go test and vet gates pass, the API and worker start against hosted Supabase, and the financial-workflow routes enforce authentication. |
| Frontend | **Passed.** Lint and production build pass with the real Account Balance and Credit Card API clients rather than review mocks. |
| Shared provider and Storage dependencies | **Passed at their Bulk boundary.** Live structured-JSON provider compatibility and signed Storage upload/stat/read/delete are recorded in the [Bulk Import technical implementation](../bulk-insert/technical.md). A full synthetic API-to-worker Bulk batch is not claimed here. |

## Explicit future extensions

These are intentionally not designed into the initial schema/workflow:

- partial, multiple, split, external, cash, and overpayment settlement (would require a statement-payment/allocation table);
- multi-currency statement settlement and FX allocation;
- scheduled cycle closure, interest, fees, minimum-payment calculation, and overdue automation;
- card-credit balances and loan overpayments;
- automatic statement-line matching without user confirmation when ambiguity remains;
- user exclusion from individual Account-balance calculation; and
- a generic reporting/dashboard feature beyond the treatment contract defined here.
