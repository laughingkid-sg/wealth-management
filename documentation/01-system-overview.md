# 01 — System Overview

## What Wealth Builder is

A private personal-finance web application for a small, provisioned set of users.
It has two pillars:

1. **Accounts** — a descriptive directory of the user's financial accounts
   (assets and liabilities). It deliberately stores **no balances, positions, or
   market data** as row values on the directory itself; opening balances and
   credit-card statement data are layered on top through separate features.
2. **Transactions** — debit/credit records linked to an account. Transactions can
   be created from three kinds of **evidence**:
   - **Gmail** emails (read-only, from a specific label), ingested and parsed by an
     LLM in the background.
   - **Bulk-uploaded documents** (receipts, statements, bills) parsed by the same
     provider (feature-flagged).
   - **Manual entry** in the browser.

Layered on transactions are **Account balances / opening balances**, spending
**treatments**, and **Credit-card bills** (statements, line reconciliation,
payment detection, payoff).

## Who uses it and how they sign in

- Users sign in with a **provisioned Supabase email/password** account.
  There is **no public registration** and Supabase is **not** used for social
  sign-in.
- **Google OAuth is used only to connect Gmail** for transaction ingestion —
  never as a Wealth Builder login method. Only read-only Gmail scope is used; no
  Gmail password is ever accepted or stored, and refresh tokens are encrypted.

## Feature status (as delivered)

| Feature | State | Notes |
| --- | --- | --- |
| Accounts | Delivered | Directory used as the anchor for all finance features. Browser CRUD via Supabase RLS. |
| Transactions | Delivered | Gmail + manual flows; shared evidence model; Review / Dangling / Failed queues. |
| Bulk Import | Delivered, **flag-gated** | `BULK_IMPORT_ENABLED=false` by default; tables/storage/jobs always present. |
| Credit Card | Delivered | Opening balances, spending treatments, bulk-generated bills, reconciliation, payment detection, payoff. |
| Dashboard / Investments / Goals / AI assistant | **Not built** | Present in the sidebar as disabled "Soon" items. |

Backlog items (e.g. transaction delete UI, transaction title UI, Tengo engine) are
tracked in [`docs/TODO.md`](../docs/TODO.md). Note the database **already has** a
`transactions.title` column and a `creation_method` that includes
`credit_card_statement`; some backlog items are partially present at the data layer.

## Technology stack

| Layer | Technology |
| --- | --- |
| Frontend | React 19, TypeScript, Vite 8, `@supabase/supabase-js`, `lucide-react` icons. No router library (URL query-param navigation), no global state library. Lint via **oxlint**. |
| Backend | Go 1.23, standard-library `net/http` with Go 1.22+ method-and-pattern routing, `jackc/pgx/v5` pool, `microcosm-cc/bluemonday` for HTML sanitisation. No web framework. |
| Data / Auth | Hosted Supabase (Postgres 17, Auth, Realtime, Storage). |
| LLM parser | Alibaba Cloud "Token Plan" (Qwen, OpenAI-compatible endpoint), model pinned to `qwen3.8-flash`. |
| Dev orchestration | Docker Compose (`compose.yaml`), Air hot-reload for Go. |

## High-level request routing

- **Browser → Supabase directly** for: auth session, `accounts` CRUD, safe
  reference reads (e.g. `transaction_categories`), Realtime sync progress, and the
  single narrow `transactions` manual-insert path. All of these are protected by
  ownership-aware **RLS**.
- **Browser → Go API** (`/api/...`, proxied to the API service) for: Gmail
  connect/sync, source evidence and attachments, canonical transaction edits and
  actions, internal transfers, prompt/rule administration, credit-card bill
  workflows, bulk import, and account balances/treatments. The browser attaches the
  Supabase access token as `Authorization: Bearer <token>`; the API verifies it
  against Supabase.
- **Go worker** has no inbound endpoint. It polls the durable job queue and
  performs all outbound external I/O (Gmail, Storage, LLM) and reconciliation.

See [02 — Architecture](02-architecture.md) for the detailed data-flow and trust
boundaries, and [08 — Security model](08-security.md) for the secrets and trust
rules.
