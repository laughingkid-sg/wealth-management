# Design Review — Complexity & Simplification

> **Date:** 2026-09-04 · **Scope:** whole system · **Nature:** advisory. No code was
> changed to produce this. Findings are ranked by value-to-effort. Nothing here is
> "force it" — several items are explicitly *don't touch* or *not now*.

## Verdict

The architecture is fundamentally sound and **most complexity is earned** by the
problem domain: untrusted LLM output, asynchronous external I/O (Gmail, LLM,
Storage), and finance-grade correctness. The concerns below are about places that
are **heavier than the current scope** (one private user at a time) warrants, plus a
few genuine low-risk simplifications — not about design mistakes.

## Complexity that is earned — leave it alone

| Area | Why it stays |
| --- | --- |
| Separate worker + Postgres durable job queue | Keeps external I/O off the request path and out of open DB transactions. Queue-in-Postgres suits a small deployment. |
| `public`/`private` schema split + ownership RLS | The backbone of the security model. |
| Dual-layer validation (Go boundary **and** DB CHECK / validation functions) | Justified for LLM- and email-derived content. |
| Token encryption, single-use OAuth state, private bucket + signed URLs | Proportionate to a finance app. |
| `http` / `service` / `store` triple per feature | A lot of indirection for the app's size, but consistent and testable. **Don't churn it; just don't expand it further.** |
| `transaction_user_locks`, `api_idempotency_records`, `deleted_provider_messages` tombstones, durable attachment-cleanup job | Each individually justified. Collectively heavy, but removing any one trades correctness for tidiness. **Keep.** |

## Findings (ranked)

### 1. Config is over-pinned  · impact: med · effort: low · risk: low
`backend/internal/config/config.go` doesn't just *default* the model, Gmail label,
and initial backfill — it **rejects** anything but `qwen3.8-flash`, `odin-finance`,
and exactly `5`, hard-failing startup otherwise.

- **Cost:** a routine ops change (bump the model version, change the label, backfill
  10) needs a **code change + redeploy**; any env drift crashes boot.
- **Change:** relax these three from "must equal X" to "non-empty, with a sensible
  default." Keep the genuinely safety-critical validations (pooler host/port/ssl,
  32-byte key, https-outside-dev).
- **Highest value-to-effort item.** Hours, not days.

### 2. A few god-files  · impact: high (readability) · effort: med · risk: low
| File | Lines | Note |
| --- | --- | --- |
| `backend/internal/transactions/http.go` | ~1,755 | All transaction handlers in one file. |
| `frontend/src/App.tsx` | ~1,313 | Shell + nav + routing **+ the entire Accounts UI**. |
| `frontend/src/features/transactions/api.ts` | ~1,482 | One monolithic client. |
| `frontend/src/features/account-balances/AccountFinanceDetailPage.tsx` | ~1,400 | Single page component. |

None are architecturally wrong; they're big enough to slow every future change and
review. Best targets: split `transactions/http.go` by sub-domain (gmail / sources /
settings / transactions), and extract `AccountsPage` out of `App.tsx`.

### 3. Inconsistent frontend structure  · impact: low-med · effort: low · risk: low
Every feature is a folder under `features/` **except Accounts, which lives inside
`App.tsx`**. Move Accounts into `features/accounts/` to match the rest.

Related (optional): navigation is hand-rolled query-param + `popstate`. Avoiding a
router library is defensible, but the cost shows up as `App.tsx` sprawl. A ~30-line
routes module (no new dependency) would contain it. Note the deliberate tradeoff
before adding a router lib.

### 4. Bulk Import largely re-implements the Transactions pipeline  · impact: high · effort: high · risk: high — **not now**
Bulk has its own `bulkstore`, `bulkstorage` (thin wrapper over `attachmentstorage`),
`bulkprompt`, `bulkparse`, `bulkworker`, **and a separate 5-stage job chain**
(`prepare → chunk_parse → aggregate → reconcile → post_process`) — even though bulk
documents already land in the **same `data_sources` evidence model** as Gmail.

- **Opportunity:** converge the two evidence pipelines (shared reconciliation,
  shared storage, one ingestion abstraction).
- **Caution:** high-effort, high-risk, and both pipelines work today. File as the
  biggest source of duplicated surface; revisit **only** if bulk and email logic
  keep drifting. Do not undertake casually.

### 5. Parser-rule layering is sized for a product you don't have yet  · impact: med · effort: med · risk: med
Default rules + per-user rules + **globally-shared rules** + prompt preview +
matching keys + parser settings is a large configuration surface for effectively one
user. The "global rules are shared and **any authenticated user can edit them**"
path is an explicit deferred-admin shortcut — extra machinery **and** a latent
footgun the moment a second user exists.

- **Change:** decide this deliberately. If multi-user isn't on the near roadmap,
  collapse global-vs-user (or drop the shared layer) to remove a whole axis of
  complexity. If multi-user *is* coming, add the admin authorization model instead
  of leaving the open-edit shortcut in place. See also
  [08 — Security](08-security.md).

### 6. Two sources of truth for the storage bucket  · impact: low · effort: low · risk: low
Both `20260902191000_create_transactions_foundation.sql` and
`20260904043716_create_bulk_import_foundation.sql` `insert ... on conflict do update`
the same `transaction-attachments` bucket, each with its own copy of the MIME list
and 5 MiB limit. Idempotent today, but two definitions can silently diverge. Keep one
canonical definition (later migrations can reference, not redefine, the limits).

## If you only do three things

1. **Relax the config pins** (#1) — defaults, not assertions.
2. **Extract `AccountsPage` from `App.tsx`** and split `transactions/http.go`
   (#2 / #3) — mechanical, low-risk, big readability win.
3. **Decide the parser-rule layering now** (#5) — keep the shared-rules axis only if
   multi-user is actually planned; otherwise cut it before it grows.

## Tracking

| # | Finding | Impact | Effort | Risk | Status |
| --- | --- | --- | --- | --- | --- |
| 1 | Relax over-pinned config validation | Med | Low | Low | proposed |
| 2 | Split god-files (`transactions/http.go`, `App.tsx`, `api.ts`, `AccountFinanceDetailPage`) | High | Med | Low | proposed |
| 3 | Move Accounts into `features/`; consider a tiny routes module | Low-Med | Low | Low | proposed |
| 4 | Converge Bulk Import with the Transactions evidence pipeline | High | High | High | deferred / not now |
| 5 | Decide parser-rule layering (drop shared layer or add admin auth) | Med | Med | Med | needs product decision |
| 6 | Single canonical storage-bucket definition | Low | Low | Low | proposed |

These are recommendations, not committed work. Promote any of them into the team's
active backlog (`docs/TODO.md`) when you decide to act.
