# 10 — Glossary

Domain and codebase terms, as used in Wealth Builder.

| Term | Meaning |
| --- | --- |
| **Account** | A directory entry for a financial account (asset or liability). Descriptive only — no balances stored on the directory row itself. Table: `public.accounts`. |
| **Side** | Whether an account is an `asset` or a `liability`. Determines the allowed `account_type` set. |
| **Account type** | Category of account, e.g. `bank_account`, `brokerage`, `digital_wallet`, `crypto_wallet`, `crypto_exchange`, `rsu`, `robo_advisor`, `retirement_account`, `credit_card`, `personal_loan`, `other`. |
| **Transaction** | A canonical debit or credit linked to an account. Table: `public.transactions`. |
| **Transaction kind** | `debit` or `credit`. |
| **Creation method** | How a transaction came to exist: `automatic_source`, `user_source`, `manual`, `internal_transfer`, `credit_card_statement`. |
| **Minor units** | Money stored as integer smallest currency units (`*_amount_minor` bigint). Major units are for display/entry only. |
| **SGD amount** | Optional Singapore-dollar-normalised amount alongside the original-currency amount. |
| **Time precision** | `exact` (use the timestamp) or `date` (a canonical noon-UTC placeholder, matched by calendar day). |
| **Source / Data source** | A piece of raw **evidence** (a Gmail email, a phone notification, or an uploaded bulk document). Immutable. Table: `private.data_sources`. |
| **Evidence** | The raw material a transaction is derived from; one transaction can have several sources attached. |
| **Sync run** | One Gmail refresh operation, tracked with status + counters. Table: `public.transaction_sync_runs`. |
| **Backfill** | The first Gmail sync, which imports at most `GMAIL_INITIAL_BACKFILL_MAX_MESSAGES` (=5) recent messages. |
| **History sync** | Later Gmail syncs that use the Gmail History API with bounded recovery, tracked by `sync_cursor`. |
| **`odin-finance`** | The exact Gmail label the ingester reads from (`GMAIL_SYNC_LABEL`, pinned). |
| **Review / Dangling / Failed queues** | Outcomes for sources that couldn't be confidently turned into a transaction (`data_sources.parse_status` = `review_required` / `dangling` / `failed`). |
| **Reconciliation** | Matching a parsed candidate to an account (via typed matching keys) and creating/linking a transaction, with conservative dedup. Package: `internal/reconciliation`. |
| **Matching key** | A typed, normalized identifier used to match evidence to an account. Stored in `private.account_matching_keys`; **never sent to the LLM**. |
| **Parser rule** | Instruction text that shapes the LLM parsing prompt. Three layers: default, per-user (`user_source_parser_rules`), and shared global (`source_parser_rules`). |
| **Prompt preview** | A side-effect-free endpoint/page that assembles and shows the system prompt + provider request template without calling the model. |
| **Parse attempt** | An audit record of one parse (prompt, provider request/response, model output). Table: `private.source_parse_attempts`; surfaced in the Debug view. |
| **Internal transfer** | An atomic pair of transactions (outgoing debit + incoming credit) joined via `private.transaction_links`. |
| **Bulk Import** | Uploading documents (receipts/statements/bills) to be parsed in a batch. Flag: `BULK_IMPORT_ENABLED`. Documents become `data_sources` with `source_type='bulk_upload_document'`. |
| **Batch** | A bulk-import run with an immutable snapshot of its prompt + selected accounts. Table: `public.bulk_import_batches` (+ `private.bulk_import_*`). |
| **Candidate** | A parsed, not-yet-resolved transaction proposal from a bulk document. Resolved into a real transaction by the user. |
| **Treatment** | A per-transaction spending calculation treatment used by balance features. Table: `private.transaction_calculation_treatments`. |
| **Opening balance** | A versioned starting balance for an account. Tables: `private.account_opening_balance_revisions(_amounts)`; snapshot fields on `public.accounts`. |
| **Credit-card bill / statement** | A credit-card statement with lines, payment candidates, and events; reconciled and paid off. Tables: `private.credit_card_statement*`. |
| **Durable job** | A unit of async work claimed from `private.transaction_jobs` by the worker. Kinds listed in [02 — Architecture](02-architecture.md#job-kinds). |
| **Lease** | The time-bounded claim a worker holds on a job; lost leases make the job claimable again (`jobs.ErrLeaseLost`). |
| **Transaction pooler** | Supabase's connection pooler on port `6543`; the API/worker connect only through it (`sslmode=require`). |
| **Service role** | The privileged Supabase key the server uses; must never reach the browser. |
| **Publishable key** | The public Supabase key the browser uses (with the user access token). |
| **Qwen / Token Plan** | The LLM parser: Alibaba Cloud Token Plan, OpenAI-compatible endpoint, model pinned to `qwen3.8-flash`. |
| **Provider** | An external service the worker calls: Gmail, the LLM, or Supabase Storage. |
