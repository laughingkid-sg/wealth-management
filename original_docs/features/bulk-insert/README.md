# Bulk Insert requirements

## Delivery status

**Delivered.** The React experience is connected to the authenticated Go API,
the asynchronous Go worker, hosted Supabase Postgres, private Storage, and the
configured LLM provider. Templates, signed uploads, grouping, processing,
reconciliation, retry/cancellation, history, evidence, Prompt Preview, Debug,
and Credit Card post-processing use the production data paths. The immutable
platform and document-type prompts are build-embedded assets, and new model
responses are checked against their exact server-issued page manifest before
they can enter reconciliation.

The product feature is referred to as **Bulk Insert** in planning and appears as **Bulk Import** in the user interface. This document is the delivered product contract.

## User goal

> I can save guidance for a recurring financial document, upload several related images or PDFs, extract every transaction represented by those files, and reuse the existing transaction reconciliation flow without losing the original evidence.

Bulk Insert extends the evidence-first Transactions model beyond Gmail. It is intended for sources that cannot be fetched reliably by a scheduled integration, including physical receipts, invoices, bank statements, credit-card statements, e-wallet histories, screenshots of transaction histories, and transaction confirmations.

## Product principles

- Uploaded files are raw evidence; they are not canonical transactions.
- Every transaction fact—such as amount, currency, date, merchant, direction, category, line items, and references—must come from the uploaded document. The model must not invent transaction data from the template or selected Accounts.
- A saved prompt may explain how to interpret the document, for example its date format, timezone, debit/credit notation, table layout, or refund convention.
- Bulk Insert reuses the existing transaction candidate, Account assignment, reconciliation, deduplication, Review, evidence, audit, and deletion concepts wherever their current contracts apply.
- Bulk Insert is the only user-upload entry point. Document-specific features reuse its upload, Storage, parsing, jobs, audit, candidate, and evidence lifecycle, then add a bounded post-processing step instead of building another importer.
- A document group is processed independently. One file may yield many transactions, and several ordered files may represent one logical document.
- A batch is asynchronous and may partially succeed. One failed or uncertain candidate must not discard other successfully processed evidence.

## Bulk Import Templates

A **Bulk Import Template** is a reusable, user-owned configuration for one recurring document format. It contains:

- a required title, unique among that user's active and archived templates;
- one required document type;
- a required parsing prompt of no more than 8,000 characters;
- one or more default active Accounts owned by the user; and
- an active or archived state.

Supported document types are:

- Physical receipt
- Invoice
- E-wallet history
- Bank statement
- Credit Card bill
- Transaction confirmation
- Other

The document type selects a server-owned processing mode. Most types use generic transaction processing. **Credit Card bill** uses the same Bulk Import pipeline and then invokes the Credit Card bill processor described in [Account Balances and Credit Card](../account-balances/README.md). Users can guide extraction through the saved prompt, but cannot replace or edit the platform processing mode.

Users can create, edit, archive, and restore templates. Templates are archived rather than permanently deleted so historical batches remain understandable. Title uniqueness includes archived templates, so an archived title cannot be reused by another template.

Editing a template changes only future batches. Creating a batch stores an immutable snapshot of the template title, document type, parsing prompt, and default Account selections. Historical batches and retries continue to use that snapshot even if the original template is later edited or archived.

## Account selection and model disclosure

A template may contain multiple default Accounts. When starting a batch, the user may add or remove selected Accounts for that batch. This override applies only to that upload and never changes the saved template.

One or more active Accounts must be selected before processing begins. Selected Accounts must belong to the signed-in user.

A Credit Card bill template and batch must select exactly one active Credit Card Account because one bill belongs to one Card. Other document types retain the one-or-more Account behavior, including a Bank statement that contains activity for several Accounts.

For parsing, the model may receive a limited Account choice list containing only:

- a temporary request-scoped reference;
- Account name;
- institution; and
- Account type.

The model must not receive database IDs, Account numbers, matching keys, notes, balances, or other Account metadata. The temporary reference is valid only for that parsing request and is resolved to an owned Account by the server.

With one selected Account, the server binds that Account after parsing; the
model cannot choose another Account and does not need page evidence for the
server-owned assignment. With multiple selected Accounts, the model must select
one exact temporary Account reference for each transaction and cite the page
that supports the selection. A missing, unknown, or uncited reference fails the
strict chunk output and remains diagnosable and retryable. A valid selection
that conflicts with other Account evidence goes to Review. Users can correct
the Account in Review.

## Upload and document grouping

A user starts from a Bulk Import Template, optionally overrides its Accounts for this batch, and uploads PDF or supported image files. Supported images match the existing transaction-evidence allowlist: BMP, JPEG, PNG, TIFF, WEBP, and HEIC.

Limits are:

- maximum 5 MiB per file;
- maximum 20 files per batch;
- maximum 50 MiB total per batch; and
- maximum 50 pages or ordered images per document group.

The user can group several related images into one logical document and reorder images within that group before processing. By default, each uploaded image is its own document group. In v1, each PDF is a standalone document group whose pages retain their original order; a PDF cannot be combined with another file. A file belongs to exactly one group in a batch.

All explicitly uploaded files are eligible for visual parsing, regardless of their filenames. The Gmail rule that visually parses only attachments whose names imply a receipt or invoice does not apply to Bulk Insert.

The interface validates type and size before upload where possible and validates them again on the server. It shows an actionable error for an unsupported, corrupted, encrypted, unreadable, oversized, or over-page-limit document. Invalid files do not prevent the user from correcting the batch before processing.

## Confirmed user journey

1. The user opens **Bulk Import** under the Transactions navigation section.
2. The user creates or selects an active Bulk Import Template.
3. The template's default Accounts are preselected. The user may override them for this batch without modifying the template.
4. The user uploads up to 20 PDFs or images, groups related images, and orders each document group.
5. The product warns when a file checksum has appeared previously for that user. The user may remove it or explicitly continue with an intentional re-import.
6. The user starts the batch with one action. The server snapshots the effective template configuration and selected Accounts, stores the raw evidence, queues each document group, and returns immediately.
7. Document groups process independently in the background. Several groups may process concurrently within server capacity; they are never combined into one model request.
8. Each model request contains build-embedded platform and document-type contracts, the subordinate saved parsing prompt, limited selected-Account descriptors, and the ordered document content for that group. The contract states the complete accepted JSON shape, evidence fields, line-item rules, date-only rule, and bill-summary rules.
9. Go strictly decodes the response, checks every evidence source against the exact page manifest, binds a sole Account server-side or verifies a cited multi-Account selection, then passes each valid candidate through category resolution, within-batch deduplication, and database-wide reconciliation.
10. Safe new candidates become canonical transactions automatically. A unique existing transaction match receives the new evidence and may be enriched. Uncertain or conflicting candidates go individually to Review. Parsing or processing failures remain retryable by document group.
11. After generic reconciliation, the selected document processor runs. For a Credit Card bill it creates the bill, links or creates its statement transactions through the shared candidate outcomes, and checks for a matching Bank-to-Card payment. It does not upload, parse, or deduplicate the document again.
12. The user may leave the page and return while work continues. Batch history retains progress, results, evidence, prompt snapshots, parse audit details, and links to any document-specific result.

## Parsing and reconciliation outcomes

Bulk Insert extracts the existing complete transaction shape where the document supplies it, including:

- debit or credit direction;
- original amount and currency;
- optional SGD amount explicitly present in the document;
- transaction calendar date;
- title, merchant, or payee;
- Account selection evidence;
- category suggestion;
- line items and quantities;
- transaction references; and
- flexible source-specific details.

For each valid candidate, the existing reconciliation and deduplication pipeline determines the outcome:

- **Existing match:** attach the uploaded evidence to the existing transaction and fill only fields that are missing.
- **Safe new transaction:** create an Account-linked transaction automatically and attach its evidence.
- **Review:** retain the candidate and evidence when Account assignment, duplicate resolution, or transaction facts are uncertain or conflicting.
- **Failed:** retain the raw document and safe error details when parsing or validation cannot produce usable candidates.

Automatic enrichment must never overwrite a field previously edited by a user. Conflicting amounts, currencies, dates, Accounts, or line items go to Review rather than being silently replaced or merged.

Duplicate detection runs both against existing transactions and among candidates in the same batch. A candidate must not become a duplicate canonical transaction merely because two uploaded pages overlap or two document groups describe the same activity. A duplicate file checksum is a warning, not an absolute prohibition, because the user may intentionally re-import a document with different guidance.

An internal-transfer candidate always goes to Review. The user selects or confirms the outgoing and incoming Accounts before the existing paired-transfer flow creates the two linked transaction legs.

Review operates per candidate, not per file or batch. A document that yields many candidates may therefore create its safe transactions while retaining only its uncertain candidates for review.

Every Credit Card bill candidate also carries an explicit one-based `line_index` and a `line_kind` of activity, refund, fee, interest, or payment. Bulk extraction currently returns a calendar `occurred_on` value; it is stored using a noon-UTC placeholder with `time_precision = date`, and reconciliation compares the calendar day rather than pretending the source supplied an exact time.

### Credit Card bill outcome

When the document type is **Credit Card bill**, one successfully parsed document group creates one Credit Card bill linked to the same raw evidence. Its specialized processor:

1. stores the extracted statement period, statement date, due date, settlement currency, amount due, and summary values;
2. links uniquely matched Credit Card transactions and creates safe missing transactions through the shared reconciliation service;
3. leaves ambiguous or conflicting lines in Review;
4. searches for an existing exact Bank-to-Card internal transfer for the amount due and currency between the statement date and due date;
5. marks the bill **Paid** when exactly one transfer matches, otherwise marks it **Unpaid**; and
6. if only a credible Bank debit exists, offers a review action that can create the missing Credit Card credit leg and link both as one internal transfer after user confirmation.

Missing or uncertain required bill header fields keep the bill in Review. The Credit Card workspace owns correction and review of the resulting bill, but it does not offer a second upload path.

The **Credit Card workspace is the sole Review surface** for a Credit Card bill. Bulk Import shows processing progress and a link to the resulting bill; it does not expose a second candidate-review workflow for that document type.

## Batch and document-group lifecycle

### Batch states

- **Draft:** files can still be added, removed, grouped, and reordered; Accounts can still be overridden.
- **Queued:** processing has been requested, but no document group is actively calling the model.
- **Processing:** at least one group is being processed or reconciled.
- **Cancel requested:** the user has requested cancellation while a group is active. No additional queued group may start.
- **Completed:** every non-cancelled group reached a successful terminal result, including results that produced Review candidates.
- **Completed with errors:** at least one group succeeded and at least one group failed.
- **Failed:** every processed group failed and none produced a successful result.
- **Cancelled:** all work has stopped after cancellation; already completed and safely reconciled results remain intact.

### Document-group states

- **Queued:** waiting to start.
- **Processing:** model parsing or reconciliation is active.
- **Completed:** parsing and reconciliation finished, including groups with candidates in Review.
- **Failed:** the group reached a retryable terminal error.
- **Cancelled:** the group had not started when cancellation took effect.

The page shows progress as document-group counts and, once available, candidate outcome counts. Progress persists server-side and updates asynchronously, with secure polling available as a fallback to live updates.

The user may cancel queued work. Queued groups become Cancelled. An already active model request is allowed to finish safely; the batch remains Cancel requested until that work reaches a safe boundary. Cancellation does not roll back transactions or evidence already reconciled.

Each failed document group can be retried independently without re-uploading its files or restarting successful groups. A retry uses the batch's original immutable template and Account snapshot, creates a new parse attempt, and preserves earlier attempts for audit. To use an edited template or different Accounts, the user starts a new batch.

## Evidence, history, and deletion

Uploaded files remain privately viewable as transaction evidence. A batch history entry shows its template snapshot, selected Account snapshot, uploaded groups, ordering, progress, candidate outcomes, warnings, failures, and retry history.

Prompt Preview supports Bulk Import Templates and shows the exact assembled model input structure with dynamic file content replaced by clear placeholders. It does not invoke the model or create parse work.

Debug exposes the exact stored model input and output for an owned Bulk Import
parse attempt, subject to the same owner-only access and secret-redaction
guarantees as the existing Transactions Debug experience. Provider failures and
strict-decoding failures also retain the bounded prompt, normalized input,
provider request/response, raw model output when available, model name, prompt
components, validation state, and a bounded diagnostic. A failed attempt can
therefore be understood before the user retries it.

Deleting uploaded evidence requires explicit confirmation and uses the existing confirmed source-deletion and private-Storage cleanup behaviour. Deletion removes dependent evidence data and links without leaving orphaned rows or objects, while preserving canonical transactions protected by the existing user-created, user-confirmed, edited, or independently evidenced transaction rules. Active batches must first reach a safe terminal state before their evidence can be deleted. A Credit Card bill that still references the document blocks evidence deletion under the bill-retention rules; Bulk Import remains the sole cleanup owner after that reference becomes removable.

A Draft batch may be deleted directly. A Cancelled batch may be deleted only after submitted sources have been removed through the same staged cleanup workflow. An individual terminal document may be deleted when it has no retained Credit Card bill or other protected dependency; the API deletes its raw source first so the existing cascade and Storage outbox remain authoritative.

## Bulk Import interface

The Transactions navigation order becomes:

1. Transactions
2. Bulk Import
3. Credit Card
4. Prompt Preview
5. Global Settings
6. Settings

Bulk Import is a separate responsive page with:

- template selection, creation, editing, archiving, and restoration;
- default and per-batch Account selection;
- drag-and-drop and file-picker upload;
- file validation, duplicate warnings, grouping, and reordering;
- a clear batch summary before processing;
- current asynchronous progress and cancellation;
- batch history and filters;
- per-group results, failures, retry, and evidence inspection;
- links from specialized results, including a processed Credit Card bill, to their feature-specific review page; and
- confirmed deletion for eligible uploaded evidence.

## UI states and errors

The page and its major panels provide explicit states for:

- initial loading and paginated history loading;
- no templates, no active templates, and no prior batches;
- draft with no files, files uploading, upload success, and partial upload failure;
- checksum warning and intentional re-import confirmation;
- invalid file type, size, total batch size, file count, or page count;
- missing template, missing prompt, missing Account, or archived/inaccessible Account;
- queued, processing, cancel requested, cancelled, completed, completed-with-errors, and failed batches;
- individual parsing, provider, validation, or reconciliation failures with safe retry guidance;
- candidates sent to Review; and
- stale or unavailable data with retry controls.

An upload retry must not duplicate a successfully stored file. A processing retry must not duplicate an existing transaction or evidence link. Provider and database errors shown to users must not expose model credentials, database details, storage paths, prompts belonging to another user, or raw internal errors.

## Accessibility and responsive behaviour

- All template, Account, upload, grouping, ordering, cancellation, retry, archive, restoration, and deletion actions are keyboard accessible.
- File selection does not depend solely on drag and drop.
- Reordering provides keyboard controls and an announced position, not only a pointer gesture.
- Status and progress changes are exposed to assistive technology without repeatedly stealing focus.
- Warnings, failures, and candidate outcomes use text and icons in addition to colour.
- Dialogs trap focus, close with Escape when no unsafe operation is in progress, restore focus to their trigger, and prevent background interaction.
- Destructive actions and intentional duplicate-file imports require clear confirmation.
- Long filenames, prompts, errors, and JSON/debug values wrap or scroll within their own bounded regions without causing page-level horizontal scrolling.
- The full upload, progress, history, and retry flow remains usable on narrow screens.

## Out of scope

- Automatically capturing screenshots from a phone, browser, operating system, or third-party financial application.
- Scheduled imports or provider scraping for platforms without an API.
- Combining every file in a batch into one model request.
- Deriving transaction facts from the template, selected Account metadata, or external financial data when those facts are absent from the document.
- Sending Account numbers, matching keys, database IDs, balances, notes, or arbitrary Account metadata to the model.
- Automatically completing an internal transfer without user review of both Accounts.
- Automatically changing a saved template when Accounts are overridden for one batch.
- Reprocessing historical batches automatically after a template edit.
- Permanently deleting templates.
- Public template sharing or platform-wide Bulk Import Templates.
- Automatic exchange-rate conversion when the document does not supply the converted value.

## Acceptance criteria

| Acceptance criterion | Status |
| --- | --- |
| An authenticated user can create a uniquely titled template with one document type, a required prompt of at most 8,000 characters, and one or more owned active default Accounts. | Implemented |
| A Credit Card bill template/batch requires exactly one active owned Credit Card Account, while other document types may retain multiple Accounts. | Implemented |
| A user can edit, archive, and restore an owned template; a restored or newly saved title cannot conflict with another template owned by that user. | Implemented |
| Starting a batch records an immutable template and Account snapshot, and per-batch Account overrides never modify the saved template. | Implemented |
| A user can upload supported PDFs/images within the 5 MiB-per-file, 20-file, 50 MiB-per-batch, and 50-page-per-group limits. | Implemented |
| A user can combine and order several files as one document group, while one file or group may yield multiple transaction candidates. | Implemented |
| Every explicit Bulk Import file is visually parsed regardless of filename, and every document group is processed independently through one or more bounded, retryable model requests. | Implemented |
| The model receives only temporary Account references, names, institutions, and types for Accounts selected in that batch; a sole Account is bound server-side, while a multi-Account selection must be exact and page-cited; no persistent IDs or sensitive Account metadata are disclosed. | Implemented |
| All extracted transaction facts are supported by document content; saved guidance may explain interpretation but cannot supply missing transaction facts. | Implemented |
| Processing is asynchronous, persists across navigation, reports per-group and candidate progress, supports safe queued-work cancellation, and permits independent failed-group retries. | Implemented |
| Retry uses the original batch snapshot, preserves prior attempts, and cannot duplicate a successfully stored file, transaction, or evidence link. | Implemented |
| Dedupe runs both within the batch and against existing transactions; a repeated checksum warns but can be intentionally accepted. | Implemented |
| A unique existing match receives evidence and missing-field enrichment without overwriting user-edited data; conflicts go to Review. | Implemented |
| Safe candidates create Account-linked transactions, uncertain candidates enter Review independently, and an internal transfer always requires Review. | Implemented |
| A Credit Card bill uses this same upload and parsing pipeline, creates one linked bill through document-specific post-processing, reconciles its lines through shared candidate outcomes, and checks for an existing payment without a second import path. | Implemented |
| Raw files remain privately viewable as evidence and confirmed deletion cleans dependent database and Storage data without orphaning records or deleting protected canonical transactions. | Implemented |
| Bulk Import appears after Transactions in navigation and provides accessible template, upload, grouping, progress, history, cancellation, retry, inspection, and deletion experiences. | Implemented |
| Prompt Preview can assemble the build-embedded Bulk Import contract with file placeholders without invoking the model, and owner-only Debug can show bounded successful or failed request/output diagnostics. | Implemented |

See the [technical implementation](technical.md) for the architecture, database objects, API contracts, prompt assets, worker behaviour, Storage layout, security policies, and verification record.
