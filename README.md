# Wealth Builder

Wealth Builder is a private, multi-user personal-finance SPA for organizing financial accounts and turning transaction evidence into account-linked records. The current product includes working **Accounts** and **Transactions** features backed by a hosted Supabase project.

Users sign in with a provisioned Supabase email/password account. Public registration is not available. Google OAuth is used only to connect Gmail for transaction ingestion; it is not a Wealth Builder sign-in method.

## Current features

### Accounts

Signed-in users can maintain their own account directory:

- Asset and liability types covering bank, brokerage, digital-wallet, crypto, RSU, robo-advisor, retirement, credit-card, personal-loan, and other accounts.
- Required account and institution names, with optional safe identifiers, notes, and custom metadata.
- Search, filtering, alphabetical sorting, expandable metadata, create/edit, soft-delete, and restore.
- Responsive loading, empty, validation, and error states.

Accounts remains a descriptive directory: it does not store balances, positions, valuations, or market data.

### Transactions

Transactions are debit or credit records linked to an active Account. The implemented workflow includes:

- Gmail connection with read-only access and refreshes from the exact `odin-finance` label. The first refresh imports at most five recent messages; later refreshes use Gmail History with bounded recovery.
- Durable background ingestion, attachment storage, LLM parsing, and account-aware reconciliation through a separate Go worker.
- Raw email and attachment evidence, parse audit/debug data, and Review, Dangling, and Failed queues for uncertain or unresolved sources.
- Typed Account matching keys and conservative deduplication that can attach several evidence sources to one transaction without sending the Account catalogue to the model.
- Manual debit/credit entry with major-unit input, original and optional SGD amounts, categories, notes, line items, and an advisory duplicate warning.
- Atomic internal transfers represented by one outgoing debit and one incoming credit joined through a relationship table.
- Transaction editing, evidence inspection, attach/create/retry actions, unmatching, and deliberate raw-source deletion.
- Personal parser settings and Account matching keys, shared global Gmail rules, and a side-effect-free Prompt Preview for inspecting the assembled system prompt and provider request template.

Refresh work continues on the server after the browser request returns. The UI follows progress through Supabase Realtime with polling as a fallback.

## Architecture

```text
React + TypeScript SPA
  ├─ Supabase Auth, owner-safe Data REST, and Realtime
  └─ authenticated requests to the Go Transactions API
                                  │
Hosted Supabase ◀─────────────────┤
  ├─ public RLS-protected data    │
  ├─ private operational data     │
  └─ private attachment Storage   │
                                  │
Go API ── Gmail OAuth, protected actions, attachment signing
Go worker ── Gmail ingestion, durable jobs, LLM parsing, reconciliation
```

Repository layout:

```text
frontend/   React, TypeScript, Vite, and Supabase browser client
backend/    Go HTTP API and independent background worker
supabase/   Hosted-project configuration, migrations, seed data, and pgTAP tests
docs/       Product and feature requirements plus technical contracts
```

The browser uses the Supabase publishable key and the signed-in user's access token. It talks directly to Supabase for Auth, Accounts CRUD, safe reference reads, Realtime progress, and the narrowly constrained manual-transaction insert. Gmail integration, source evidence, attachment access, prompt/rule administration, canonical edits, and other privileged or multi-row workflows go through the Go API.

The API and worker share the server-only configuration and connect to Postgres through the hosted Supabase transaction pooler on port `6543`. They are separate processes: the API accepts user actions, while the worker claims durable jobs and performs external I/O without holding database transactions open.

## Data and security

- Every user-owned public row is protected by ownership-aware RLS. Browser grants are explicit; browser transaction writes are limited to validated manual inserts.
- Raw sources, Gmail credentials, jobs, evidence links, matching keys, parser configuration, and parse audits live in a non-exposed private schema.
- Transaction attachments use a private Supabase Storage bucket. Supported PDFs and images are limited to 5 MiB each and are exposed only through short-lived, owner-checked signed URLs.
- Gmail refresh tokens and OAuth PKCE data are encrypted before storage. No Gmail password is accepted or stored.
- Stored email HTML is sanitized before display; private original HTML is not returned by the API.
- Account metadata and matching keys are not sent to the LLM. Model output and source content remain untrusted and are validated by the server.
- The Supabase service-role key, database URL, Google secret, encryption key, and model-provider key are backend-only. Never place them in `VITE_*` variables or frontend code.
- Global transaction rules are shared. During the current development phase, any authenticated user can edit them through Go; an admin-only authorization model is deferred.

## Run locally against hosted Supabase

Local development uses the configured hosted Supabase project. Do not start a local Supabase or Docker stack for this repository.

Prerequisites:

- Node.js/npm for the frontend.
- Go 1.23 or newer for the API and worker.
- A hosted Supabase project with the repository migrations applied and a provisioned email/password user.
- Google OAuth credentials with the exact local callback registered.
- Alibaba Cloud Token Plan credentials for the configured `qwen3.8-flash` parser.

### 1. Configure the backend

Create an ignored local file from the safe template and replace every placeholder with the matching hosted-project or provider value:

```bash
cp -n backend/.env.example .env
```

The example documents the complete contract. In particular, `SUPABASE_DB_URL` must be the transaction-pooler URL on port `6543` with `sslmode=require`, `GOOGLE_OAUTH_REDIRECT_URL` must exactly match the registered callback, and `FRONTEND_ORIGIN` must match the Vite origin. Do not commit `.env`.

The Go binaries read process environment variables; they do not load `.env` automatically. Start the API and worker in separate terminals, sourcing the same file in each:

```bash
cd backend
set -a
source ../.env
set +a
go run ./cmd/api
```

```bash
cd backend
set -a
source ../.env
set +a
go run ./cmd/worker
```

The API listens on `http://localhost:8080` by default and exposes `GET /healthz`.

### 2. Configure and start the frontend

```bash
cd frontend
cp -n .env.example .env.local
# Set VITE_SUPABASE_URL and VITE_SUPABASE_PUBLISHABLE_KEY for the hosted project.
npm install
npm run dev
```

Vite serves the SPA on `http://localhost:5173` by default and proxies `/api` to the local Go API on port `8080`. Sign in with the provisioned Supabase user, then connect Gmail from Transactions when testing ingestion.

### 3. Optional code checks

```bash
cd backend
go test ./...
go vet ./...
go build ./cmd/api ./cmd/worker

cd ../frontend
npm run lint
npm run build
```

Database migrations, pgTAP suites, and advisors run against the configured hosted project through the repository's Supabase workflow. Keep local and remote migration history aligned.

## Documentation

- [Documentation index](docs/README.md)
- [Product overview](docs/product/overview.md)
- [Accounts requirements](docs/features/accounts/README.md)
- [Accounts technical implementation](docs/features/accounts/technical.md)
- [Transactions requirements](docs/features/transactions/README.md)
- [Transactions technical implementation](docs/features/transactions/technical.md)

Read the smallest relevant feature document before changing behavior; the technical pages contain the detailed schema, API, security, configuration, and verification contracts.
