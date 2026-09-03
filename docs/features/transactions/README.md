# Transactions requirements

## User goal

> I can add account-linked transactions myself, refresh finance-labelled Gmail messages, turn reliable evidence into canonical transactions, inspect every supporting source, and resolve uncertain, failed, or unmatched evidence without losing the original data.

Focused design reference: [Wealth Builder Transactions — Prompt and Matching Design](https://rcnubep1n9x.sg.larksuite.com/docx/BVk8dHLivos0odxu9o6lJomigMb).

## Delivery status

The Transactions product flow is implemented on `codex/feat-transaction`: Gmail OAuth and refresh, durable asynchronous processing, configurable Qwen guidance, typed Account matching keys, account-aware reconciliation, exact parse-call audit, transaction and source review views, attachment inspection, editing, unmatching, raw-source deletion, retry, internal-transfer creation, direct manual creation, merchant/user-note editing, dismissible terminal Gmail results, shared global-rule administration, and read-only prompt preview. Migrations through `20260903055808_add_global_source_rule_editor_metadata.sql` are applied to the hosted development project with matching local/remote history. The migration rehearsal, hosted database, Go, focused frontend, and authenticated-browser checks recorded in [technical implementation](technical.md) pass. Verification did not call Qwen.

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

Only one Gmail refresh may be active for a user at a time. Failures are surfaced without deleting already stored evidence. Progress for a queued or running refresh remains visible and cannot be dismissed. Once that run completes or fails, its result banner may be dismissed independently for that user and run; dismissing one terminal result does not hide a later refresh.

Prompt administration is separate from the ingestion flow. An authenticated user may edit platform-wide Gmail source rules on **Global Settings** during the current development phase. **Prompt Preview** can either evaluate one owned past email with the production matcher or assemble a manually chosen rule combination. Neither preview mode calls Qwen or changes transaction data.

## Transaction semantics

### Debit, credit, and internal transfer

- `debit` means money leaving the linked Account.
- `credit` means money entering the linked Account.
- `internal transfer` is not a third direction value. It is one outgoing debit transaction and one incoming credit transaction joined by an `internal_transfer` relationship in a junction table.

Each transfer leg keeps its own Account, amount, currency, time, category, line items, and optional source evidence. Both legs and their link are created atomically. The two Accounts must be distinct. A source ordinarily has at most one active evidence link. The sole exception is that the same source may support exactly both legs of one internal-transfer pair; it cannot support unrelated transactions. The transfer UI lets the user assign evidence to the outgoing leg, incoming leg, or both legs where that exception applies.

### Manual transaction entry

The Transactions header places **Add transaction** beside **Internal transfer**. A manual transaction may use any active Account owned by the signed-in user and records:

- debit or credit direction;
- a required title and date/time, plus an optional merchant or payee;
- a positive original amount and three-letter currency, plus an optional SGD amount;
- an optional active category;
- up to 100 optional line items; and
- optional user notes of at most 4,000 characters.

The form accepts money in major units and converts it exactly to stored integer minor units. If the original currency is SGD, the SGD field mirrors the original amount and is not independently editable. For another currency, the SGD amount remains optional; the product never estimates one.

Before insertion, the browser performs an advisory duplicate check over the user's visible transactions. A likely duplicate has the same Account, debit/credit kind, exact original amount and currency, and a transaction time within ten minutes in either direction. The user sees the matching records and may review the form or select **Create anyway**. This warning does not impose a uniqueness rule and cannot guarantee that another concurrent insert will not race the check.

A successful manual insert is immediately `confirmed`, uses `creation_method = manual`, has no match confidence or evidence link, and has not yet been marked as user-modified. The creation form cannot write parser references, Account evidence, or other server-owned provenance into transaction details.

### Money

- Amounts are positive integer minor units; direction is represented by `transaction_kind`, not by a signed amount.
- Every transaction stores `original_amount_minor` and its ISO 4217 `original_currency`.
- `sgd_amount_minor` is optional for foreign-currency transactions and is populated only when a source or the user supplies the SGD value. For an original SGD transaction, it mirrors `original_amount_minor`. The product does not invent an exchange rate.
- For a foreign purchase, the UI displays the original-currency amount and the supplied SGD amount when both exist.

### Line items and flexible details

Line items are optional and are capped at 100 per transaction. When present, each item uses schema version 1, has a non-empty description, a positive integer quantity, an ISO currency, optional non-negative integer minor-unit price/total/tax/discount fields, and a JSON object for flexible provider-specific details. Transaction-level non-core facts and source payloads also use JSON objects so later providers can retain data that does not belong in canonical columns. React renders known fields and safe JSON values, never provider HTML.

## Matching and deduplication rules

The Gmail provider message ID provides exact ingestion deduplication per user. Reconciliation then matches evidence across different senders/providers.

Automatic reconciliation first requires source evidence to resolve exactly one active Account owned by the user. Account resolution compares the model’s typed `card_last_four` or `masked_bank_reference` evidence with active `card_last_four` or `bank_account_suffix` matching keys. Account names, arbitrary metadata, `account_identifier`, and generic `additional_identifiers` never participate in automatic matching, and the Account catalogue and configured keys are never sent to Qwen. Missing or unmatched Account evidence produces a dangling source; evidence that maps to several Accounts requires review.

Matching keys are immutable identities. A card key normalizes only masking characters and must contain exactly four ASCII digits; a full card number is rejected rather than truncated. Before a model-provided card suffix may participate in matching, the source must present exactly one suffix in masked-card or explicit card context, such as `Mastercard (**** 2562)` or `card ending in 2562`; a bare order/year/invoice number is retained only as audit detail. A bank suffix is lowercased and removes Unicode whitespace plus `*`, `•`, and `-`, while retaining other characters. The original value remains available for display. A normalized key is permanently unique per user and type, including after retirement; the same row may be reactivated for its original Account but cannot be reassigned. Recognized legacy Account metadata, including **Last 4 Digit**, is backfilled without removing or changing the descriptive metadata.

Model confidence and citation syntax alone never authorise automatic creation. The server derives creation eligibility only when email text itself contains a bounded Account identifier, an exact currency-qualified source amount, and either a bounded merchant phrase or an exact reference. Attachment-only evidence, raw/bare-dollar amounts, fabricated paths, and rule constants not present in source text cannot create a new transaction automatically. A user may still create a validated transaction manually from Review or Dangling after choosing an Account.

Once source evidence resolves exactly one Account, automatic pairing runs before the source's automatic-creation eligibility check. An existing transaction is a pairing candidate only when it has the same owner and resolved Account, the same debit/credit direction, the exact original amount, a timestamp no more than ten minutes before or after the source timestamp inclusive, and compatible currency. Currency is compatible when the values are equal or either side is missing; it is incompatible only when both values are known and different.

Exactly one pairing candidate attaches the source to that transaction, even when source corroboration is insufficient to create a new transaction automatically. More than one pairing candidate sends the source to Review. With no pairing candidate, the existing strict automatic-creation eligibility and confidence flow applies: an eligible candidate with sufficient cited parse confidence may create a new confirmed transaction, while an ineligible or lower-confidence candidate goes to Review.

Shared references, merchant normalization, and match scores do not provide an automatic fallback or disambiguate multiple pairing candidates.

The rule applies when sources are processed after this change. There is no historical reconciliation backfill or migration; development transaction and source data will be cleared and ingested again for verification.

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

Transaction detail supports editing title, merchant or payee, Account, date/time, original amount/currency, optional SGD amount, category, line items, and user notes. Merchant and `user_notes` are editable for every canonical transaction, whether it was created manually, from source evidence, or as an internal-transfer leg; editing notes must not replace server-owned provenance in the same details object. Transaction detail also shows active evidence, lets the user inspect each source, and requires confirmation before unmatching. Source detail shows sanitized email, private attachments, parse facts/errors, Account selection, same-Account transaction search, attach/create actions, and retry where applicable. An owner-only **Debug** view shows the latest ten attempt summaries with bounded previews; any shortened prompt, input, provider request/response, model output, validated candidate, or rule provenance field can be loaded individually in its exact stored lexical form. Separate dialogs create one manual transaction or the two linked legs of an internal transfer; the latter supports outgoing, incoming, or both-leg evidence selection.

The Transactions navigation section contains four independent pages:

- **Transactions**: the four-tab transaction/evidence workspace described above.
- **Prompt Preview**: read-only inspection of the exact assembled system prompt and provider request template.
- **Global Settings**: platform-wide Gmail source rules shared by every user.
- **Settings**: the current user's bounded default prompt, versioned Gmail source rules, and Account matching keys.

Personal source rules require a sender condition (exact address, domain, or RE2 expression); optional subject and content RE2 expressions combine with it using AND semantics. The single highest-priority matching global rule and the single highest-priority matching user rule are selected independently. A tie at the highest matching priority is treated as a visible configuration failure rather than chosen arbitrarily. User guidance is subordinate to the immutable platform contract and cannot change the response schema, authorisation boundary, source-only evidence rules, or no-invention requirement.

The platform prompt itself is a build-embedded Go asset and cannot be viewed as an editable setting. Global Settings exposes only the shared rule name, optional sender/content RE2 matchers, prompt fragment, priority, and active state for editing. The deterministic `extraction_config` remains visible but read-only. Every authenticated user may make these global changes for now; future admin-only authorization is deferred. Updates use the rule version for optimistic concurrency, so a stale editor receives a conflict and must reload. Rules are disabled or reactivated instead of hard-deleted, preserving provenance. A saved change affects future parses and sources retried manually after the change; it does not automatically reparse existing evidence.

Prompt Preview has two modes. **Automatic** selects one past Gmail email owned by the current user and runs the same active global and personal matching logic as production. **Manual** explicitly selects an optional global rule, the optional user default, and an optional personal rule, including inactive rules for inspection, without evaluating their matchers. Both modes display the exact assembled system prompt, the selected component metadata, and a Qwen request template whose dynamic email and eligible receipt/invoice attachment content is replaced with clear placeholders. Preview responses must not be cached. Building a preview must not call Qwen, enqueue work, create a parse-attempt audit, retry or reparse a source, or write any transaction/evidence data.

User-facing Prompt Preview copy uses provider-neutral **LLM** terminology. The exact debug request JSON may still expose the configured model identifier, currently `qwen3.8-flash`.

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

- Automatic exchange-rate conversion when neither the source nor the user supplies an SGD amount.
- Phone-notification ingestion or the unspecified third source channel in this delivery.
- User-managed category definitions.
- Financial-provider APIs or imports other than Gmail.
- Automatic visual parsing of attachments whose filenames do not indicate a receipt or invoice.
- Attachments above 5 MiB or with unsupported MIME types.
- General canonical-transaction deletion or evidence-retention automation beyond explicit raw-source deletion.
- Editing the immutable platform prompt at runtime, or restricting global-rule editing to a future admin role.
- Calling Qwen, creating audit rows, or reparsing stored evidence from Prompt Preview.
- Public registration, OAuth sign-in for the Wealth Builder product, or public access to transaction evidence.

## Acceptance criteria and current status

| Acceptance criterion | Implementation status |
| --- | --- |
| Unauthenticated or cross-user callers cannot access sources, transactions, attachments, sync progress, or Gmail connection data. | Verified through Supabase session validation, owner-scoped SQL, RLS/owner tests, and Go ownership checks. Hosted anonymous REST requests to Transactions and sync data returned `401`, and the private source schema was unavailable with `406`. The hosted project contains only one user, so cross-user denial is verified by automated RLS/owner tests rather than a live second-user attempt. |
| A user can connect Gmail, trigger a refresh, and see asynchronous progress and completion. | OAuth/PKCE, durable sync runs/jobs, Realtime, and polling fallback are implemented; a live initial run reached completion with no active or failed work. Authenticated owner-session passes loaded the Transactions workspace, Settings, Failed source inspection, sanitized email evidence, and Debug audit on desktop/mobile without console errors, and the final pass verified persisted terminal-banner dismissal alongside the manual create, duplicate-override, and edit paths. |
| Labelled messages are persisted at most once per user and retry does not lose stored evidence. | Verified live: the first run stored five unique `odin-finance` sources and an idempotent five-message rerun created zero duplicate sources. Deterministic attachment paths, idempotent queueing, bounded retry, and lease recovery also have automated coverage. |
| A source with one Account/direction/exact-amount/compatible-currency match within the inclusive ten-minute window attaches to that transaction; multiple matches go to Review, and zero matches follows strict automatic-creation checks. | Focused reconciliation tests cover unique pairing before automatic-creation eligibility, multi-candidate review, and the no-candidate creation path. |
| A reliable source creates or supports an Account-linked transaction; ambiguous/no-account evidence goes to Review or Dangling. | Parser and reconciliation tests cover account-linked creation and ambiguous handling. The live run correctly ended with five Dangling sources, zero Failed sources, and no active work because its evidence matched no Account; all three scoped parser retries subsequently parsed and reconciled successfully. |
| Multiple sources can support one transaction and can be inspected, unmatched, or reattached. | Implemented through the evidence junction table and source/transaction dialogs. |
| Account resolution uses explicit owner-scoped matching keys without exposing Account data to Qwen. | Implemented with typed private keys, permanent per-user uniqueness, legacy metadata backfill, exact typed comparison, and owner-scoped API validation. |
| A user can configure default and source-specific parser guidance without overriding platform safety rules. | Implemented through the independent Settings page, versioned private configuration, deterministic priority handling, and immutable prompt-prefix assembly. |
| An authenticated user can create, edit, disable, and reactivate shared Gmail source rules while deterministic extraction configuration stays read-only. | Verified through authenticated browser acceptance, focused frontend checks, hosted pgTAP, and the transaction-pooler Go integration test for optimistic updates that preserve `extraction_config`. The UI also verifies safe new-rule defaults and warns when both optional matchers make a rule catch all Gmail evidence. |
| A user can preview the exact prompt assembly manually or from an owned past email without invoking or mutating the parse pipeline. | Manual preview was verified in the authenticated browser with the exact assembled system message and dynamic request placeholders. Automatic mode's empty state was verified after transaction evidence was reset. Eight focused prompt/global-rule frontend tests pass, and Qwen was not called. |
| The owner can inspect the exact Qwen input/output trail for a source. | Implemented through bounded private audit columns, capped latest-attempt previews, and owner-scoped on-demand exact-field loading; provider authentication headers are never stored. |
| The owner can add a debit or credit manually against any active owned Account, with the confirmed fields, major-unit money entry, no source evidence, and an immediately confirmed result. | Verified through authenticated browser acceptance using direct Supabase Data REST: a major-unit transaction with a line item was created as confirmed/manual with zero evidence. |
| Manual creation warns about a same-Account, same-kind, exact-amount/currency transaction within ten minutes without blocking an intentional duplicate. | Verified in the authenticated browser: the exact advisory warning appeared and **Create anyway** created the intentional duplicate. |
| Merchant/payee and user notes can be edited on every owned canonical transaction without overwriting source provenance. | Verified through the Go PATCH path: merchant and `user_notes` changed while the existing line-item details remained intact. |
| Active Gmail progress remains visible, while each completed or failed result can be dismissed for only that user and run. | Verified in the authenticated browser: terminal dismissal persisted for that user/run, while the active-progress path remains non-dismissible. |
| Explicit raw-source deletion removes related database and Storage evidence without deleting a manually created or edited transaction. | Verified through hosted integration tests and 37 focused database assertions: one transaction atomically deletes database evidence, preserves protected canonical transactions, tombstones provider identity, and enqueues exact paths for Storage cleanup outside the transaction. Cleanup cannot be terminally abandoned: failures and expired final leases requeue with cooldown until object deletion succeeds. |
| An internal transfer creates exactly one debit leg, one credit leg, and one junction link atomically. | Verified by the Go integration suite and database integrity assertions. |
| The owner can safely view sanitized email and supported private attachments. | Verified with server-side sanitization, sandboxed rendering, private Storage, ownership checks, and a live five-minute signed attachment URL whose ranged download succeeded. |
| Monetary and line-item contracts use integer minor units and preserve original and optional SGD values. | Verified across database, Go, frontend, and browser checks, including exact major-unit conversion and browser-authored decimal-string line-item amounts. The Go storage decoder preserves those amounts and their line-item details. |
| Review, Dangling, and Failed queues support resolution and retry without deleting source evidence. | Implemented in the Go actions and React workspace. |

The authenticated acceptance fixtures were temporary: all three synthetic transaction rows and any associated links were removed after verification.

See [technical implementation](technical.md) for the exact component, schema, route, runtime, configuration, and verification contracts.
