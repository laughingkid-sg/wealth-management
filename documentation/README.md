# Wealth Builder — Documentation

> **Status:** Maintenance handover documentation, written 2026-09-04 from a full
> analysis of the code and the live hosted Supabase project. It describes the
> system **as it actually is today**, not as it was originally planned.

Wealth Builder is a private, single-user-at-a-time personal-finance SPA. Signed-in
users organise financial **accounts** and turn transaction evidence (Gmail emails,
uploaded documents, manual entry) into account-linked **transactions**, with
supporting workflows for **credit-card bills**, **account balances**, and
**bulk document import**.

This `documentation/` folder is a fresh, self-contained handover set. It does not
replace the older `docs/` folder (product/feature requirements written by the
previous team) — it complements it with a maintainer- and Claude-Code-oriented view
of the real architecture, data model, and workflows.

## Start here

| If you want to… | Read |
| --- | --- |
| Understand what the product is and how the pieces fit | [01 — System overview](01-system-overview.md) |
| Understand components, data flow, and trust boundaries | [02 — Architecture](02-architecture.md) |
| Work on the Go API or the background worker | [03 — Backend](03-backend.md) |
| Work on the React SPA | [04 — Frontend](04-frontend.md) |
| Understand the database, RLS, and storage | [05 — Database & storage](05-database.md) |
| Look up an HTTP endpoint | [06 — API reference](06-api-reference.md) |
| Run the stack locally | [07 — Local development](07-local-development.md) |
| Understand the security model and secrets | [08 — Security model](08-security.md) |
| Follow the team's conventions, testing, and git flow | [09 — Conventions & workflows](09-conventions-and-workflows.md) |
| Look up a domain term | [10 — Glossary](10-glossary.md) |
| Review where the system may be over-complex (advisory) | [Design review — complexity & simplification](design-review.md) |

For agent-assisted development, the repository root also has a **`CLAUDE.md`** with
a compressed operating guide (commands, boundaries, gotchas).

## The system in one paragraph

A **React + TypeScript SPA** (Vite) talks directly to **hosted Supabase** for
authentication, owner-scoped reads, Realtime progress, and a small set of
RLS-guarded writes (accounts CRUD, confirmed manual-transaction inserts). Every
privileged or multi-row workflow — Gmail OAuth and ingestion, evidence/attachment
access, canonical transaction edits, credit-card bills, bulk import, prompt/rule
administration — goes through a **Go HTTP API**. A separate **Go worker** drains a
durable Postgres job queue to do external I/O (Gmail, Storage, the LLM parser) and
reconciliation. All three application processes run locally in **Docker Compose**
while pointing at the **hosted** Supabase project (there is intentionally no local
Supabase in the dev stack).

```text
Browser SPA ──▶ Supabase (Auth · RLS reads · Realtime · narrow writes)
     │
     └────────▶ Go API  ──▶ Postgres (service role) · Storage signing · Gmail OAuth
                              │
                Go worker ──▶ durable jobs · Gmail ingest · LLM parse · reconcile
```

## Repository map

```text
frontend/        React + TypeScript + Vite SPA (Supabase browser client)
backend/         Go module: cmd/api, cmd/worker, internal/* packages
supabase/        Migrations, seed, config.toml, pgTAP tests (functions/ is empty)
docs/            Previous team's product & feature requirement docs (kept as-is)
documentation/   This handover documentation set
compose.yaml     Docker Compose dev stack (frontend + api + worker)
AGENTS.md        Previous team's agent working agreement
README.md        Top-level product/run overview
```

## Ground-truth notes

- **Live project:** the Supabase CLI is linked to project `wealth-management`
  (ref `unjvbgyawsrzgwqxxhxt`, region `ap-northeast-1`, Postgres 17). It is a
  **development** environment.
- **Verified schema:** the data-model documentation was cross-checked against a
  live `supabase db dump --linked`, not only the migration files.
- **Bulk Import** ships behind the `BULK_IMPORT_ENABLED` flag (default `false`).
  Its tables, storage, and jobs exist regardless; only the API routes and worker
  handlers are gated.
