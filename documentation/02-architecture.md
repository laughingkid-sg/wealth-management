# 02 — Architecture

## Components

There are three application processes plus hosted Supabase. Nothing else is
self-hosted in the dev stack.

| Component | Process | Talks to | Trust level |
| --- | --- | --- | --- |
| **SPA** | `frontend` (Vite dev server / static build) | Supabase (publishable key + user token); Go API (user token) | Untrusted client. Holds only public config. |
| **API** | `backend/cmd/api` | Postgres (service role, via pooler); Supabase Auth; Supabase Storage; Google OAuth token endpoint | Trusted server. Holds all secrets. |
| **Worker** | `backend/cmd/worker` | Postgres (service role, via pooler); Supabase Storage; Gmail API; LLM provider | Trusted server. Holds all secrets. |
| **Supabase** | Hosted | — | Managed. Enforces Auth + RLS + Storage policies. |

The API and worker are **separate binaries** built from the **same Go module**.
They share `internal/config` and most `internal/*` packages, but only the API
serves HTTP and only the worker consumes jobs.

## Data-flow diagram

```text
                         ┌───────────────────────────────────────────────┐
                         │                 Hosted Supabase                │
   Browser (SPA)         │  Auth · Postgres(public+private) · Storage ·   │
   ┌───────────┐  RLS    │  Realtime                                      │
   │  React    │◀───────▶│  public schema (RLS, owner-scoped)             │
   │  Vite app │  token  │  private schema (NO browser grants)            │
   └─────┬─────┘         │  Storage bucket: transaction-attachments       │
         │ Bearer token  └──────────────▲───────────────▲────────────────┘
         │  /api/*                       │ service role  │ service role
         ▼                               │               │
   ┌───────────┐  verify token   ┌───────┴──────┐  ┌─────┴──────┐
   │  Go API   │────────────────▶│   Postgres   │  │  Storage   │
   │ cmd/api   │  enqueue jobs   │ (txn pooler  │  │ (signed    │
   └───────────┘                 │  :6543)      │  │  URLs)     │
                                 └───────▲──────┘  └────────────┘
                                         │ claim / complete jobs
                                 ┌───────┴──────┐   outbound I/O
                                 │  Go worker   │──────────────▶ Gmail API
                                 │ cmd/worker   │──────────────▶ LLM provider (Qwen)
                                 └──────────────┘
```

## Trust boundaries

1. **Browser ↔ Supabase.** The browser uses the **publishable** key and the
   signed-in user's access token. Every browser-reachable table has **RLS** that
   restricts rows to `auth.uid() = user_id`. The `private` schema has **no grants
   to `anon`/`authenticated`**, so the browser cannot see raw evidence, tokens,
   jobs, or audit rows even though they live in the same database.

2. **Browser ↔ Go API.** The browser sends `Authorization: Bearer <supabase access
   token>`. The API verifies the token with Supabase (`internal/auth`) and derives
   the user id server-side; it never trusts a client-supplied user id. The API then
   uses the **service role** connection to do the work within that verified user's
   scope.

3. **Server ↔ Postgres.** API and worker connect through the **Supabase
   transaction pooler** on port `6543` (enforced in config: host must end in
   `.pooler.supabase.com`, port must be `6543`, `sslmode=require`). They use the
   service role and are responsible for enforcing per-user scoping in SQL.

4. **Server ↔ external providers.** Gmail refresh tokens and OAuth PKCE verifiers
   are **encrypted** (AES via `internal/secret`, 32-byte key) before storage. Model
   output and email/document content are treated as **untrusted** and validated by
   the server and by database CHECK constraints. Account metadata and matching keys
   are **never sent to the LLM**.

## The durable job queue

Asynchronous work is a **Postgres-backed durable queue** (`private.transaction_jobs`),
not an external broker. Key properties (see `internal/jobs`):

- Jobs are **claimed atomically** in a short SQL transaction using row locks
  (`FOR UPDATE SKIP LOCKED`-style claiming). The design explicitly forbids
  `LISTEN/NOTIFY`, advisory locks, temp tables, or session state, so it works
  through the transaction pooler.
- A claimed job carries a **lease**; the worker heartbeats to keep it. If the lease
  is lost the handler returns `ErrLeaseLost` and the job becomes claimable again.
- The worker is a simple loop: `ProcessOne` → if it processed something, loop
  immediately; otherwise sleep `WORKER_POLL_SECONDS` (default 5s) and retry.
- Multiple workers are safe (each claims distinct jobs); the single dev worker is
  the norm.

### Job kinds

| Kind (`private.transaction_jobs.job_type`) | Constant | Handler |
| --- | --- | --- |
| `gmail_ingestion` | `KindGmailIngest` | `ingestion.GmailIngestionHandler` |
| `source_parsing` | `KindSourceParse` | `transactionworker.Handler` |
| `reconciliation` | `KindReconcile` | `transactionworker.Handler` |
| `source_attachment_cleanup` | `KindSourceAttachmentCleanup` | `transactionworker.Handler` |
| `bulk_document_prepare` | `KindBulkDocumentPrepare` | `bulkworker` (flag-gated) |
| `bulk_document_chunk_parse` | `KindBulkDocumentChunkParse` | `bulkworker` |
| `bulk_document_aggregate` | `KindBulkDocumentAggregate` | `bulkworker` |
| `bulk_candidate_reconciliation` | `KindBulkCandidateReconcile` | `bulkworker` |
| `bulk_document_post_process` | `KindBulkDocumentPostProcess` | `bulkworker` |

Bulk handlers are only registered when `BULK_IMPORT_ENABLED=true`.

## The Transactions pipeline (end to end)

This is the core workflow; understand it before touching Transactions code.

```text
1. Connect Gmail        SPA → API: begin OAuth (PKCE) → Google consent → callback
                        API stores an ENCRYPTED refresh token in gmail_connections.

2. Create a sync run    SPA → API: POST /gmail/sync-runs
                        API inserts transaction_sync_runs (queued) + enqueues
                        a gmail_ingestion job. HTTP returns immediately.

3. Ingest (worker)      Worker claims gmail_ingestion. First run: import up to
                        GMAIL_INITIAL_BACKFILL_MAX_MESSAGES (=5). Later runs use
                        Gmail History with bounded recovery. Each message becomes a
                        private.data_sources row (immutable raw evidence) + stored
                        attachments in the private Storage bucket. Enqueues
                        source_parsing per new source.

4. Parse (worker)       Worker claims source_parsing. Builds a system prompt from
                        default + user + global rules (NO account catalogue), sends
                        normalized content to the LLM, records a
                        source_parse_attempts audit row (prompt, request, response,
                        model output), validates the candidate.

5. Reconcile (worker)   Worker claims reconciliation. Matches the parsed candidate
                        to an account using typed matching keys + conservative
                        dedup. Outcome: a transaction is created/linked, or the
                        source lands in the Review / Dangling / Failed queue.

6. Review (SPA → API)   User inspects evidence and attach/create/retry/unmatch, or
                        deliberately deletes a raw source (which enqueues
                        source_attachment_cleanup to remove Storage objects).
```

Progress is surfaced to the browser through **Supabase Realtime** on
`transaction_sync_runs`, with polling as a fallback (`GET /sync-runs/latest`).

## Bulk Import pipeline (flag-gated)

Mirrors the transactions pipeline for uploaded documents:

```text
template → batch (immutable snapshot of prompt + selected accounts) →
reserve+finalize file uploads (signed Storage upload) → submit →
bulk_document_prepare → bulk_document_chunk_parse → bulk_document_aggregate →
bulk_candidate_reconciliation → bulk_document_post_process (e.g. create
credit-card bills) → candidates resolved into transactions.
```

Bulk documents flow into the **same evidence model**: they become
`private.data_sources` rows with `source_type = 'bulk_upload_document'` and
`provider = 'user_upload'`.

## Why this shape

- **Thin, RLS-guarded browser writes** keep sensitive/multi-row logic off the
  client while still giving the SPA fast direct reads.
- **A separate worker** means external I/O (Gmail, LLM, Storage) never holds a
  database transaction open, and slow/uncertain work can't block user actions.
- **A Postgres queue** avoids operating a broker and keeps everything inside the
  one managed datastore, which suits a small private deployment.
