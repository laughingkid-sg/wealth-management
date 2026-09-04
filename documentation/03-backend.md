# 03 — Backend (Go API & worker)

Module: `github.com/zhengteck/wealth-builder/backend` · Go **1.23**.
Two entrypoints, one module, shared `internal/*` packages. No web framework — it
uses the standard-library `net/http` mux with Go 1.22+ `"METHOD /path/{param}"`
patterns.

## Directory layout

```text
backend/
├── cmd/
│   ├── api/main.go        HTTP API entrypoint (wires stores + handlers, serves mux)
│   └── worker/main.go     Worker entrypoint (wires job handlers, polls the queue)
├── internal/              all application code (not importable outside the module)
├── .air.api.toml          Air hot-reload config for the API
├── .air.worker.toml       Air hot-reload config for the worker
├── Dockerfile.dev         dev image (Go + Air), used by both api and worker
├── .env.example           full server-only env contract
└── go.mod / go.sum
```

### Dependencies (deliberately minimal)

- `github.com/jackc/pgx/v5` — Postgres driver + connection pool.
- `github.com/microcosm-cc/bluemonday` — HTML sanitisation for stored email bodies.
- `github.com/google/uuid` — ids.

That's the entire direct dependency set. Prefer the standard library before adding
anything new.

## The two entrypoints

### `cmd/api/main.go`

1. `config.LoadFromEnv()` validates the **entire** runtime contract up front and
   fails fast on any missing/invalid variable.
2. Opens the pgx pool via `database.OpenTransactionPooler`.
3. Builds shared collaborators: `auth` verifier, `transactionstore`, attachment
   Storage client, token `cipher`, Gmail connection persistence, Google OAuth
   client + flow.
4. Registers feature handlers on one `http.ServeMux`:
   - `transactions.NewHandler(...).Register(mux, verifier)`
   - `accountbalances`, `creditcard`, and (if `BULK_IMPORT_ENABLED`) `bulkimport`.
5. Wraps the mux with `securityHeaders` (`X-Content-Type-Options: nosniff`,
   `X-Frame-Options: DENY`) and serves with explicit timeouts + graceful shutdown
   on SIGINT/SIGTERM.
6. `GET /healthz` returns `204` (used by the Compose healthcheck).

### `cmd/worker/main.go`

1. Same config + pool.
2. Builds provider clients: Google OAuth, Gmail HTTP, Storage, Alibaba Qwen parser.
3. Builds a `jobs.Router` mapping each `jobs.Kind` to a handler
   (`ingestion.GmailIngestionHandler`, `transactionworker.Handler`, and — when the
   flag is on — `bulkworker.JobHandler`).
4. Runs `jobs.Worker.ProcessOne` in a loop, sleeping `WorkerPollInterval` when the
   queue is empty, until the context is cancelled.

## Package map (`internal/`)

### Composition / cross-cutting
| Package | Responsibility |
| --- | --- |
| `config` | Loads and **validates** all server-only configuration. Pins the LLM model, Gmail label, backfill count; enforces pooler host/port/ssl; decodes the 32-byte encryption key. The single source of truth for env. |
| `database` | pgx pool creation (`OpenTransactionPooler`) and Postgres array helpers. |
| `auth` | `Verifier` that validates a Supabase access token and returns the user. `requireUser` middleware wraps every protected route. |
| `secret` | AES cipher (`secret.New`) for encrypting/decrypting Gmail refresh tokens and PKCE verifiers. |
| `jobs` | Durable job contracts: `Kind` constants, `Job`, `Store`, `Router`, and the `Worker` claim/lease/heartbeat loop. |
| `providers` | Outbound HTTP clients: Google OAuth (`gmail.go`), Gmail API, and Alibaba Qwen (`alibaba*.go`). |
| `emailcontent` | Sanitises stored email HTML (bluemonday) before it can be displayed. |
| `prompts` / `transactionprompt` / `parserrules` | Assemble the transaction parsing **system prompt** from default + user + global rules. `prompts/transactions/system_v2.txt` is the base template. |

### Transactions domain
| Package | Responsibility |
| --- | --- |
| `transactions` | HTTP handlers for the transactions feature (`http.go`, `prompt_settings.go`). The largest route surface. |
| `transactionstore` | All transaction-domain SQL: sources, candidates, operations, settings, global settings, reconciliation, source-admin/deletion, attachments, bulk bridge. Split across many files by concern. |
| `ingestion` | Gmail ingestion handler (`gmail.go`): backfill vs History sync, source creation, attachment storage. |
| `transactionworker` | Worker handlers for parse / reconcile / attachment-cleanup, plus rule application. |
| `reconciliation` | Matching + corroboration logic that turns a parsed candidate into a created/linked transaction or a queue outcome. |
| `gmailconnection` | Gmail connection persistence + the OAuth (PKCE) flow service. |
| `attachmentstorage` | Supabase Storage client for the private `transaction-attachments` bucket (upload, signed URLs, cleanup); `bulk.go` handles bulk blobs. |

### Account balances & credit card
| Package | Responsibility |
| --- | --- |
| `accountbalances` | HTTP + service + store for opening balances, balance listing, and transaction calculation **treatments**. |
| `accountbalancestore` | Postgres implementation for account-balance data. |
| `creditcard` | HTTP + service + store for credit-card **bills/statements**: header correction, line attach/create/ignore, payment candidate select/confirm, payoff, void, discard. |
| `creditcardstore` | Postgres for credit-card statements incl. idempotency, transaction creation, transfers, and post-processing. |

### Bulk import (flag-gated)
| Package | Responsibility |
| --- | --- |
| `bulkimport` | HTTP + service + model for templates, batches, files, documents, candidates, prompt preview, debug. |
| `bulkstore` | Postgres for bulk import (`postgres.go`) + worker-side queries (`worker.go`). |
| `bulkstorage` | Storage client wrapper for bulk uploads (wraps `attachmentstorage`). |
| `bulkparse` | Decoder for the LLM's bulk parse output. |
| `bulkprompt` | Bulk prompt assembly + embedded prompt templates (`prompts/*.txt`: generic, platform, credit-card-bill). |
| `bulkworker` | Worker handlers for the bulk pipeline stages + document renderer (`renderer.go`). |

### Tests
| Package | Responsibility |
| --- | --- |
| `transactione2e` | An end-to-end harness test that drives the full pipeline. |
| `*_test.go` | Unit + integration tests colocated with each package. Integration tests hit Postgres. |

## Configuration contract

Everything comes from environment variables, loaded and validated **once** in
`config.LoadFromEnv`. See [`backend/.env.example`](../backend/.env.example) for the
full list and [07 — Local development](07-local-development.md) for how it is
supplied. Highlights and hard constraints enforced in code:

| Variable | Constraint |
| --- | --- |
| `SUPABASE_URL` | Absolute URL; must be `https` outside development. |
| `SUPABASE_DB_URL` | Must be a `postgres(ql)://` URL on `*.pooler.supabase.com` **port 6543** with `sslmode=require`. |
| `SUPABASE_SERVICE_ROLE_KEY` | Required. Server-only. |
| `GOOGLE_OAUTH_CLIENT_ID` / `_SECRET` | Required. |
| `GOOGLE_OAUTH_REDIRECT_URL` | Must exactly match a URI registered in Google Cloud; `https` unless localhost dev. |
| `TRANSACTION_TOKEN_ENCRYPTION_KEY` | Base64-encoded **32 bytes**. |
| `ALIBABA_TOKEN_PLAN_API_KEY` | Required. |
| `ALIBABA_TOKEN_PLAN_BASE_URL` | Must be `https`. Defaults to the SEA MaaS endpoint. |
| `ALIBABA_TOKEN_PLAN_MODEL` | Any non-empty value. **Defaults** to `qwen3.8-flash`. |
| `FRONTEND_ORIGIN` | Used for CORS/redirects; `https` unless localhost dev. |
| `GMAIL_SYNC_LABEL` | Any non-empty value. **Defaults** to `odin-finance`. |
| `GMAIL_INITIAL_BACKFILL_MAX_MESSAGES` | Optional positive integer, **1–100**. Defaults to `5`. |
| `WORKER_POLL_SECONDS` | Default 5. |
| `OUTBOUND_HTTP_TIMEOUT_SECONDS` | Default 20, max 120. |
| `BULK_IMPORT_ENABLED` | Default `false`. Gates bulk routes + worker handlers. |
| `BULK_IMPORT_*` timeouts / byte caps | Bounded (see `.env.example`). |
| `GOOGLE_TEST_REFRESH_TOKEN` | **Development only**; lets tests skip interactive OAuth. Rejected outside dev. |

> The model, Gmail label, and initial backfill are **operator-configurable via env**
> with sensible defaults (`qwen3.8-flash`, `odin-finance`, `5`); backfill is bounded
> to `1–100` as a guardrail. The genuinely safety-critical validations remain strict
> and *will* fail startup if violated: DB via the transaction pooler on `:6543` with
> `sslmode=require`, a base64 32-byte encryption key, and https outside localhost dev.

## Conventions in the Go code

- **Handlers stay thin**: verify the user (`requireUser`), validate input, call a
  service/store, return consistent JSON. Never leak DB errors or secrets in
  responses.
- **`context.Context` is threaded** through every I/O boundary.
- **Stores own the SQL.** Handlers/services never build ad-hoc SQL; each domain has
  a `*store` package.
- **Untrusted input is validated at the boundary** and again by DB CHECK
  constraints (belt and suspenders — see [05 — Database](05-database.md)).
- **The `Config` struct is never logged or marshalled** (it holds secrets).

## Building, running, testing

```bash
cd backend
go build ./cmd/api ./cmd/worker
go vet ./...
go test ./...
```

Integration tests expect a reachable Postgres (the hosted dev project via the
pooler, or a test database). See [09 — Conventions & workflows](09-conventions-and-workflows.md).
