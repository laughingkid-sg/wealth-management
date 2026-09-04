# CLAUDE.md

Operating guide for Claude Code in this repository. Full documentation is in
[`documentation/`](documentation/README.md); the previous team's working agreement is
in [`AGENTS.md`](AGENTS.md) (still authoritative for conventions). This file is the
fast path.

## What this is

Wealth Builder — a private personal-finance SPA. Three app processes + hosted
Supabase:

- **`frontend/`** — React 19 + TypeScript + Vite. Talks to Supabase directly (RLS)
  and to the Go API via `/api`.
- **`backend/cmd/api`** — Go HTTP API (all privileged/multi-row logic).
- **`backend/cmd/worker`** — Go worker draining a Postgres durable job queue
  (Gmail ingest, LLM parse, reconcile, bulk import).
- **Hosted Supabase** — Postgres 17, Auth, Realtime, Storage. No local Supabase.

Read [documentation/02-architecture.md](documentation/02-architecture.md) before any
non-trivial change.

## Commands

```bash
# Full dev stack (frontend :8085, api :8086, worker background) against hosted Supabase
docker compose up -d --build
docker compose ps
docker compose logs -f frontend api worker
docker compose down

# Backend checks
cd backend && go build ./cmd/api ./cmd/worker && go vet ./... && go test ./...

# Frontend checks
cd frontend && npm run lint && npm run build

# Database (CLI is linked to project wealth-management / unjvbgyawsrzgwqxxhxt)
supabase migration list --linked
supabase db diff --linked -f <name>          # generate a migration from remote drift
supabase test db                             # pgTAP suites in supabase/tests
supabase gen types typescript --linked > frontend/src/lib/database.types.ts
```

Env files (git-ignored): `cp -n backend/.env.example .env` and
`cp -n frontend/.env.example frontend/.env.local`. Details:
[documentation/07-local-development.md](documentation/07-local-development.md).

## Hard rules (do not break)

- **Secrets are server-only.** Service-role key, DB URL, Google secret, encryption
  key, and provider key live in root `.env` only. **Never** put them in `VITE_*` or
  frontend code. Don't log or print secret values.
- **RLS on every browser-reachable table**, with **ownership** policies
  (`auth.uid() = user_id`), not just `TO authenticated`. The `private` schema has no
  browser grants — keep it that way.
- **Browsers write directly only** to `accounts` (CRUD) and the single
  confirmed-manual `transactions` insert. Everything else goes through the Go API
  with `requireUser`.
- **Schema changes go through migrations** (`supabase/migrations/`), reviewed before
  applying. Never hand-edit the remote schema. Keep local/remote history aligned.
- **Use the hosted dev project**; do not run `supabase start`.
- **The model never sees** account metadata or matching keys.
- Money is stored in **minor units** (`*_amount_minor` bigint).
- Config: LLM model, Gmail label, and initial backfill are env-configurable with
  defaults (`qwen3.8-flash`, `odin-finance`, `5`; backfill bounded 1–100). The strict,
  startup-failing checks are the safety-critical ones — DB via pooler `:6543` +
  `sslmode=require`, base64 32-byte key, https outside localhost dev. See
  `internal/config/config.go`.
- **Bulk Import** is gated by `BULK_IMPORT_ENABLED` (default `false`): its API routes
  and worker handlers only exist when enabled.

## Where things live

| Task | Start in |
| --- | --- |
| An HTTP endpoint | `backend/internal/<feature>/http.go` + the matching `frontend/src/features/<feature>/api.ts` |
| SQL for a domain | `backend/internal/<feature>store/` |
| Async job behaviour | `backend/internal/jobs` + `internal/*worker` + `internal/ingestion` |
| A UI page | `frontend/src/features/<feature>/` (Accounts UI is in `frontend/src/App.tsx`) |
| Schema / RLS / storage | `supabase/migrations/` (tests in `supabase/tests/`) |
| Config / env contract | `backend/internal/config/config.go`, `backend/.env.example` |

Route list: [documentation/06-api-reference.md](documentation/06-api-reference.md).
Data model: [documentation/05-database.md](documentation/05-database.md).

## Conventions

- Go: thin handlers (verify → validate → service/store → JSON); thread `context`;
  stores own SQL; prefer the stdlib (tiny dependency set).
- TS: strict, no `any`; small accessible components; explicit loading/empty/error
  states; no router/global-state library unless required.
- Git: dedicated branch (`codex/` prefix), Conventional Commits, small focused
  commits, PR per branch, use `git`/`gh` only. Don't commit secrets or
  planning/instruction files. Commit only when asked; co-author per the session's
  attribution instruction.
- Run the narrowest relevant checks after a change; add a pgTAP test when you touch
  RLS or constraints. Update the affected docs in the same change.

## Backlog

Requested-but-unbuilt work is in [`docs/TODO.md`](docs/TODO.md) (e.g. transaction
delete UI, transaction title UI, Tengo engine). Note some backlog items already
exist at the data layer (`transactions.title`, `creation_method` values) — verify
current state before implementing.
