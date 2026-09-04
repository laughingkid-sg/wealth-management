# 09 — Conventions & Workflows

The previous team encoded working agreements in [`AGENTS.md`](../AGENTS.md). This
page distills the parts that still govern day-to-day maintenance, plus the testing
and git practices observed in the repo.

## Architectural boundaries (keep these)

- **Frontend / API / DB stay separated.** Browser code uses Supabase only for the
  session and data explicitly meant for the browser (accounts CRUD, safe reads,
  Realtime, the one confirmed-manual insert). All sensitive/multi-row logic lives
  in the Go API.
- **Use Supabase Data REST + RLS** for simple browser CRUD; **route everything else
  through the Go API.**
- **Build page by page.** Implement the requested page/behaviour and its supporting
  API/data work — not speculative infrastructure. No deployment/IaC/cloud-VM work
  unless explicitly asked.

## Go conventions

- Thin handlers: verify → validate → call service/store → consistent JSON.
- Thread `context.Context` through all I/O.
- Stores own SQL; handlers/services don't build ad-hoc queries.
- Validate untrusted input at the boundary; never expose DB errors or secrets.
- Keep config in env (`internal/config`); provide safe examples in `.env.example`.
- Prefer the standard library; the dependency set is intentionally tiny.

## Frontend conventions

- Strict TypeScript; no `any`; model API data explicitly (mirror `database.types.ts`).
- Small, accessible, semantic components; responsive; explicit loading/empty/error
  states on data-backed screens.
- No global state library and no router library unless a concrete requirement
  demands it (navigation is query-param based today).
- Money in **minor units** in data (`*_amount_minor` bigint), major units at the UI
  edge.

## Supabase / database

- Change schema **only** through the migration workflow; review generated
  migrations and RLS before applying. Never hand-edit the remote schema.
- Enable RLS on every browser-accessible table with an **ownership** policy.
- Use the hosted/remote project by default; do **not** start local Supabase.
- Verify current CLI/API behaviour via the Supabase skill rather than memory.
- Keep local and remote **migration history aligned**
  (`supabase migration list --linked`).

## Testing

| Layer | How |
| --- | --- |
| Go unit + integration | `go test ./...` in `backend/`. Integration tests (e.g. `transactionstore/*_integration_test.go`, `transactione2e`) require a reachable Postgres. |
| Go static analysis | `go vet ./...` |
| Database (pgTAP) | `supabase test db` runs `supabase/tests/*.test.sql` (RLS, constraints, OAuth state, operations, deletion/recovery, bulk, credit card). |
| Frontend lint | `npm run lint` (oxlint) |
| Frontend build/typecheck | `npm run build` (`tsc -b && vite build`) |

Run the **narrowest** relevant checks after a change. When you touch RLS or
constraints, add/extend a pgTAP test.

## Git workflow

- One dedicated branch per change, `codex/` prefix by default (the current branch is
  `codex/feat-docker-dev-stack`).
- **Conventional Commits**: `feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`.
- Small, focused commits per logical update; open a PR per branch; merge after
  review.
- Update the affected feature docs **in the same change** when behaviour changes.
- Do not commit secrets or populated `.env` files. Do not commit planning/instruction
  files unless asked.
- Use `git` + the `gh` CLI for GitHub operations (no browser automation).

> **Co-author attribution.** The repo's history uses a Codex co-author trailer, and
> the user's global instruction is to co-author as Claude. When you commit, follow
> the attribution the harness gives you for this session (currently
> `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`). Only commit when asked.

## Documentation upkeep

- This `documentation/` set is the maintainer/architecture view.
- `original_docs/` holds the previous team's archived product + per-feature
  requirement/technical docs. If you keep using that structure, update the affected
  feature's `README.md`/`technical.md` when its behaviour/data/API changes, and only
  touch `original_docs/product/overview.md` for genuine cross-feature decisions.
- When a change spans code + schema + docs, update all three together.
