# Transactions requirements

## User goal

> I can refresh finance-labelled Gmail messages, turn reliable evidence into account-linked transactions, inspect every supporting source, and resolve uncertain, failed, or unmatched evidence without losing the original data.

## Delivery status

The Transactions product flow is implemented on `codex/feat-transaction`: Gmail OAuth and refresh, durable asynchronous processing, configurable Qwen guidance, typed Account matching keys, account-aware reconciliation, exact parse-call audit, transaction and source review views, attachment inspection, editing, unmatching, raw-source deletion, retry, and internal-transfer creation all have frontend and Go implementations. See [technical implementation](technical.md) for exact migration and verification evidence.

## Product model

Transactions is multi-user. A canonical transaction is a debit or credit on one Account and may be supported by several source records—for example a bank alert, payment-provider notice, and merchant receipt for the same purchase.

Email is the first implemented input. The source model is intended to support three transaction-data channels in total: Gmail email, a later phone-notification channel, and a third future channel whose provider contract is not yet specified. The current database constraint accepts `gmail_email` and reserves `phone_notification`; adding the third channel requires an explicit product decision and migration.

Raw sources are durable evidence, not canonical transactions. Every canonical transaction must link to an active Account owned by the same user. Missing Account evidence is valid source input: it leaves the source in the Dangling queue instead of failing parsing. Ambiguous Account evidence or other unsafe matches stay in Review. A parsing failure stays in Failed and can be retried from the stored source without fetching Gmail again.

## Implemented user journey

1. The signed-in user connects Gmail through Google OAuth using read-only Gmail access. No Gmail password is accepted or stored.
2. The user selects **Refresh Gmail**. The API starts a background sync and returns immediately.
3. The first refresh fetches at most the five newest messages under the exact Gmail label `odin-finance`. Later refreshes use Gmail History for newly added messages and messages newly given that label. An invalid, expired, or legacy non-History cursor triggers bounded full-label recovery, rather than silently retaining only five messages. A message that disappears after Gmail lists it is skipped; other retrieval failures retain the prior cursor for retry.
4. Each message is stored once per user before parsing. Supported attachments are stored privately and source parsing is queued.
5. The worker assembles an immutable platform prompt plus applicable global, user-default, and source-specific guidance. A global deterministic rule may add trusted facts; `qwen3.8-flash` then returns a structured transaction candidate with evidence citations and thinking disabled.
6. Reconciliation matches only typed Account keys against cited source evidence. It either attaches the source to an existing transaction, creates a reliable account-linked transaction, sends an ambiguous source to Review, or leaves a no-account source Dangling.
7. The UI reports progress asynchronously through Supabase Realtime with secure polling as a fallback. The user can leave and return while the server-side work continues.
8. The user can inspect email and attachment evidence, attach a source to an existing transaction, create a transaction from it, retry parsing, edit canonical fields, or unmatch evidence. Unmatching retains the source and its audit history.

Only one Gmail refresh may be active for a user at a time. Failures are surfaced without deleting already stored evidence.

## Transaction semantics

### Debit, credit, and internal transfer

- `debit` means money leaving the linked Account.
- `credit` means money entering the linked Account.
- `internal transfer` is not a third direction value. It is one outgoing debit transaction and one incoming credit transaction joined by an `internal_transfer` relationship in a junction table.

Each transfer leg keeps its own Account, amount, currency, time, category, line items, and optional source evidence. Both legs and their link are created atomically. The two Accounts must be distinct. A source ordinarily has at most one active evidence link. The sole exception is that the same source may support exactly both legs of one internal-transfer pair; it cannot support unrelated transactions. The transfer UI lets the user assign evidence to the outgoing leg, incoming leg, or both legs where that exception applies.

### Money

- Amounts are positive integer minor units; direction is represented by `transaction_kind`, not by a signed amount.
- Every transaction stores `original_amount_minor` and its ISO 4217 `original_currency`.
- `sgd_amount_minor` is optional and is populated only when a source supplies the SGD value. The product does not invent an exchange rate.
- For a foreign purchase, the UI displays the original-currency amount and the source-supplied SGD amount when both exist.

### Line items and flexible details

Line items are optional. When present, each item uses schema version 1, has a non-empty description, a positive integer quantity, an ISO currency, optional non-negative integer minor-unit price/total/tax/discount fields, and a JSON object for flexible provider-specific details. Transaction-level non-core facts and source payloads also use JSON objects so later providers can retain data that does not belong in canonical columns. React renders known fields and safe JSON values, never provider HTML.

## Matching and deduplication rules

The Gmail provider message ID provides exact ingestion deduplication per user. Reconciliation then matches evidence across different senders/providers.

Automatic reconciliation first requires source evidence to resolve exactly one active Account owned by the user. Account resolution compares the model’s typed `card_last_four` or `masked_bank_reference` evidence with active `card_last_four` or `bank_account_suffix` matching keys. Account names, arbitrary metadata, `account_identifier`, and generic `additional_identifiers` never participate in automatic matching, and the Account catalogue and configured keys are never sent to Qwen. Missing or unmatched Account evidence produces a dangling source; evidence that maps to several Accounts requires review.

Matching keys are immutable identities. A card key normalizes only masking characters and must contain exactly four ASCII digits; a full card number is rejected rather than truncated. Before a model-provided card suffix may participate in matching, the source must present exactly one suffix in masked-card or explicit card context, such as `Mastercard (**** 2562)` or `card ending in 2562`; a bare order/year/invoice number is retained only as audit detail. A bank suffix is lowercased and removes Unicode whitespace plus `*`, `•`, and `-`, while retaining other characters. The original value remains available for display. A normalized key is permanently unique per user and type, including after retirement; the same row may be reactivated for its original Account but cannot be reassigned. Recognized legacy Account metadata, including **Last 4 Digit**, is backfilled without removing or changing the descriptive metadata.

Model confidence and citation syntax alone never authorise an automatic action. The server derives auto-eligibility only when email text itself contains a bounded Account identifier, an exact currency-qualified source amount, and either a bounded merchant phrase or an exact reference. Attachment-only evidence, raw/bare-dollar amounts, fabricated paths, and rule constants not present in source text remain Review. A user may still create a validated transaction manually from Review or Dangling after choosing an Account.

After Account and debit/credit kind agree, automatic attachment requires one of these unambiguous combinations:

- an exact shared transaction/reference identifier; or
- exact original amount and currency, the same normalized merchant, and transaction times within ten minutes.

The ten-minute window is only a supporting signal. Account plus amount and currency alone is not safe enough; an existing plausible but insufficient match goes to Review. Multiple candidates with close scores also go to Review. If no existing transaction is plausible, a candidate with sufficient cited parse confidence may create a new confirmed transaction; lower-confidence candidates go to Review.

Matching searches existing owned transactions within 24 hours on either side of the source time, but the ten-minute rule remains the automatic time-match threshold.

An optional category is resolved against one active global leaf. Missing, unknown, ambiguous, or incorrectly cited category suggestions do not invalidate an otherwise usable parse; the category and its citations are discarded and the transaction remains uncategorized. Citation failures for required facts or any other populated optional fact still fail closed. Both automatic reconciliation and user-confirmed creation preserve a category suggestion only when it has valid source evidence and resolves to that unique active leaf.

## Sources, email, and attachments

Each source stores its owner, type, provider, provider IDs, receive/ingest timestamps, parse state/provenance, and a flexible JSON payload. Gmail source JSON includes normalized subject, sender, text, private original HTML (`html_raw`), sanitized display HTML, a non-sensitive `body_truncated` marker, and attachment metadata; parser attempts are separate audit records. Cumulative decoded plain-text and HTML body content is capped at 224 KiB on a valid UTF-8 boundary before persistence. This leaves bounded room for normalized subject, sender, and receive-time context under the parser's 256 KiB text limit without truncating ordinary messages. Raw MIME and attachment bytes are never stored in JSONB.

Original HTML remains private evidence and is never returned by an API. The frontend receives only server-sanitized HTML, displays it in a sandboxed frame with referrer suppression, and falls back to plain text when HTML is absent.

Attachment rules are:

- private Supabase Storage bucket `transaction-attachments`;
- PDF or supported images only: BMP, JPEG, PNG, TIFF, WEBP, and HEIC; MIME type and file signature are both validated;
- maximum 5 MiB per attachment;
- deterministic owner/source/checksum object paths for retry safety;
- short-lived signed URLs issued by Go only after source ownership is checked;
- visual parsing only when the filename case-insensitively contains `receipt` or `invoice`.

Visual model input is independently capped at five rendered images and 5 MiB total. Optional pages or images beyond either cap are skipped rather than causing source parsing to fail.

Non-receipt attachments remain viewable evidence but are not sent to the model. PDF rendering and image-conversion runtime behavior is documented in [technical implementation](technical.md).

## User interface requirements

The responsive Transactions workspace has four keyboard-accessible tabs:

- **Transactions**: cursor-paginated account-linked records, search, debit/credit filter, review-state filter, original and optional SGD amounts, source counts, and transfer relationships.
- **Review**: low-confidence or ambiguous sources awaiting a decision.
- **Dangling**: sources without a reliable Account or sources returned here after unmatching.
- **Failed**: stored sources whose parsing or terminal processing failed, showing a safe failure reason and retrying from the retained source without refetching Gmail.

Transaction detail supports editing title, Account, date/time, original amount/currency, optional SGD amount, category, and line items. It shows active evidence, lets the user inspect each source, and requires confirmation before unmatching. Source detail shows sanitized email, private attachments, parse facts/errors, Account selection, same-Account transaction search, attach/create actions, and retry where applicable. An owner-only **Debug** view shows the latest ten attempt summaries with bounded previews; any shortened prompt, input, provider request/response, model output, validated candidate, or rule provenance field can be loaded individually in its exact stored lexical form. A separate dialog creates the two linked legs of an internal transfer and supports outgoing, incoming, or both-leg evidence selection.

Transactions has an independent **Settings** page within its navigation section. The user can edit a bounded default prompt, create versioned Gmail source rules, and create or retire matching keys for active Accounts. Source rules require a sender condition (exact address, domain, or RE2 expression); optional subject and content RE2 expressions combine with it using AND semantics. The single highest-priority matching global rule and the single highest-priority matching user rule are selected independently. A tie at the highest matching priority is treated as a visible configuration failure rather than chosen arbitrarily. User guidance is subordinate to the immutable platform contract and cannot change the response schema, authorisation boundary, source-only evidence rules, or no-invention requirement.

Raw-source deletion requires explicit confirmation and waits for any active Gmail refresh to finish. One short database transaction removes the stored email, parse/debug attempts, queued source work, and evidence links; applies the confirmed canonical-transaction cleanup rule; records a one-way provider-identity tombstone; and queues exact private-Storage paths for cleanup. The API reports whether Storage cleanup is still pending. A worker deletes those objects outside the database transaction and removes its outbox row after success. A failed cleanup remains durable and retries in five-attempt bursts with a cooldown and cumulative monitoring rather than becoming terminal. The tombstone prevents a later Gmail retry or backfill from recreating deliberately deleted evidence. Transactions with other sources remain. User-created, user-confirmed, edited, or internal-transfer transactions remain even when their final source is removed. Only an automatically created, never-edited transaction is removed with its line items when the deleted source was its final active evidence.

Every data-backed view includes loading, empty, error, retry, success, and paginated-loading states. Dialogs manage focus, support Escape, and prevent background interaction. Mobile navigation is available for narrow layouts.

## Transaction categories

The category catalogue is global and system-managed.

| Group | Categories |
| --- | --- |
| Income | Paychecks; Interest; Business Income; Other Income |
| Gifts & Donations | Charity; Gifts |
| Auto & Transport | Auto Payment; Public Transit; Gas; Auto Maintenance; Parking & Tolls; Taxi & Ride Shares |
| Housing | Mortgage; Rent; Home Improvement |
| Bills & Utilities | Garbage; Water; Gas & Electric; Internet & Cable; Phone |
| Food & Dining | Groceries; Restaurants & Bars; Coffee Shops |
| Travel & Lifestyle | Travel & Vacation; Entertainment & Recreation; Personal; Pets; Fun Money |
| Shopping | Shopping; Clothing; Furniture & Housewares; Electronics |
| Children | Child Care; Child Activities |
| Education | Student Loans; Education |
| Health & Wellness | Medical; Dentist; Fitness |
| Financial | Loan Repayment; Financial & Legal Services; Financial Fees; Cash & ATM; Insurance; Taxes |
| Other | Uncategorized; Check; Miscellaneous |
| Business | Advertising & Promotion; Business Utilities & Communication; Employee Wages & Contract Labor; Business Travel & Meals; Business Auto Expenses; Business Insurance; Office Supplies & Expenses; Office Rent; Postage & Shipping |
| Transfers | Transfer; Credit Card Payment; Balance Adjustments |

## Out of scope

- Automatic exchange-rate conversion when no source supplies an SGD amount.
- Phone-notification ingestion or the unspecified third source channel in this delivery.
- User-managed category definitions.
- Financial-provider APIs or imports other than Gmail.
- Automatic visual parsing of attachments whose filenames do not indicate a receipt or invoice.
- Attachments above 5 MiB or with unsupported MIME types.
- General canonical-transaction deletion or evidence-retention automation beyond explicit raw-source deletion.
- Public registration, OAuth sign-in for the Wealth Builder product, or public access to transaction evidence.

## Acceptance criteria and current status

| Acceptance criterion | Implementation status |
| --- | --- |
| Unauthenticated or cross-user callers cannot access sources, transactions, attachments, sync progress, or Gmail connection data. | Verified through Supabase session validation, owner-scoped SQL, RLS/owner tests, and Go ownership checks. Hosted anonymous REST requests to Transactions and sync data returned `401`, and the private source schema was unavailable with `406`. The hosted project contains only one user, so cross-user denial is verified by automated RLS/owner tests rather than a live second-user attempt. |
| A user can connect Gmail, trigger a refresh, and see asynchronous progress and completion. | OAuth/PKCE, durable sync runs/jobs, Realtime, and polling fallback are implemented; a live initial run reached completion with no active or failed work. An authenticated owner-session pass loaded the Transactions workspace, Settings, Failed source inspection, sanitized email evidence, and Debug audit on desktop/mobile without console errors. Mutating controls were deliberately not exercised in that read-only browser pass. |
| Labelled messages are persisted at most once per user and retry does not lose stored evidence. | Verified live: the first run stored five unique `odin-finance` sources and an idempotent five-message rerun created zero duplicate sources. Deterministic attachment paths, idempotent queueing, bounded retry, and lease recovery also have automated coverage. |
| A reliable source creates or supports an Account-linked transaction; ambiguous/no-account evidence goes to Review or Dangling. | Parser and reconciliation tests cover account-linked creation and ambiguous handling. The live run correctly ended with five Dangling sources, zero Failed sources, and no active work because its evidence matched no Account; all three scoped parser retries subsequently parsed and reconciled successfully. |
| Multiple sources can support one transaction and can be inspected, unmatched, or reattached. | Implemented through the evidence junction table and source/transaction dialogs. |
| Account resolution uses explicit owner-scoped matching keys without exposing Account data to Qwen. | Implemented with typed private keys, permanent per-user uniqueness, legacy metadata backfill, exact typed comparison, and owner-scoped API validation. |
| A user can configure default and source-specific parser guidance without overriding platform safety rules. | Implemented through the independent Settings page, versioned private configuration, deterministic priority handling, and immutable prompt-prefix assembly. |
| The owner can inspect the exact Qwen input/output trail for a source. | Implemented through bounded private audit columns, capped latest-attempt previews, and owner-scoped on-demand exact-field loading; provider authentication headers are never stored. |
| Explicit raw-source deletion removes related database and Storage evidence without deleting a manually created or edited transaction. | Verified through hosted integration tests and 37 focused database assertions: one transaction atomically deletes database evidence, preserves protected canonical transactions, tombstones provider identity, and enqueues exact paths for Storage cleanup outside the transaction. Cleanup cannot be terminally abandoned: failures and expired final leases requeue with cooldown until object deletion succeeds. |
| An internal transfer creates exactly one debit leg, one credit leg, and one junction link atomically. | Verified by the Go integration suite and database integrity assertions. |
| The owner can safely view sanitized email and supported private attachments. | Verified with server-side sanitization, sandboxed rendering, private Storage, ownership checks, and a live five-minute signed attachment URL whose ranged download succeeded. |
| Monetary and line-item contracts use integer minor units and preserve original and optional SGD values. | Implemented at the Go boundary, database constraints, parser validation, and strict TypeScript API parsing. |
| Review, Dangling, and Failed queues support resolution and retry without deleting source evidence. | Implemented in the Go actions and React workspace. |

See [technical implementation](technical.md) for the exact component, schema, route, runtime, configuration, and verification contracts.
