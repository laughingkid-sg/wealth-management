# Technical Design — Transaction Parsing Revamp

> **Date:** 2026-09-05 · **Status:** Draft for review · **Companion to:**
> [prd-transaction-parsing-revamp.md](prd-transaction-parsing-revamp.md) ·
> **Not yet approved; nothing built.**

Grounded in the current code: `internal/ingestion`, `internal/transactionworker`,
`internal/reconciliation`, `internal/transactionstore`, `internal/scriptengine`,
`internal/prompts`, and `supabase/migrations`.

## 1. Architecture at a glance

Unchanged: 3-job async chain per email — `gmail_ingestion` → `source_parsing` →
`reconciliation`, on the Postgres durable queue; HTTP returns 202. What changes is the
**inside of `source_parsing`** (now: pre-process → LLM → N candidates → per-candidate
post-process) and the **fan-out of reconciliation** (one job per candidate), plus a
**shared candidate table** and a **script store**.

```
gmail_ingestion ─▶ source_parsing ─────────────────────────────────────────▶ reconciliation ×N
                     │  1 load normalized email                                   (one job per candidate)
                     │  2 PRE-PROCESS  (Tengo: email_pre_process)  ← new           each: Reconcile → persist
                     │  3 assemble prompt + attachments                             outcome to source_candidates
                     │  4 LLM parse → { transactions: [ {candidate,evidence}, … ] } rollup data_sources.parse_status
                     │  5 per candidate: harden → POST-PROCESS (Tengo) → validate ← new
                     │  6 persist N source_candidates + enqueue N reconciliation jobs
```

## 2. Data model

### 2.1 `private.source_candidates` (generalize `bulk_import_candidates` in place)

Rename `private.bulk_import_candidates` → `private.source_candidates`. FKs pointing at
it (`transaction_data_sources.bulk_import_candidate_id`; `transaction_jobs` bulk cols;
the self-FK `duplicate_of_candidate_id`) follow the rename automatically. Then:

**Add**
- `origin text not null` ∈ {`gmail_email`, `bulk_import`} — discriminator; existing rows
  backfill to `bulk_import` (dev data is dropped anyway).
- `suggested_account_id uuid`, `suggested_transaction_id uuid`, `match_confidence int2`
  — email review/attach UX (bulk leaves null). `suggested_account_id` FK →
  `public.accounts(id,user_id)`.

**Relax to nullable** (were NOT NULL, bulk-only): `batch_id`, `document_id`.
**Keep NOT NULL, reused for email:** `attempt_generation` (default 1), `output_ordinal`
(email candidate index 0..N-1), `fingerprint` (email = sha256 of canonical candidate →
free parse idempotency).

**Status vocab** — add `dangling` (email's no-account outcome). Full set:
`pending_reconciliation, created, attached, review_required, dangling, duplicate,
failed, cancelled, superseded`.

**Constraints / trigger**
- `bulk_candidates_result_check`: extend the enumerated statuses to include `dangling`
  (fits the existing "not in (created,attached,duplicate) ⇒ transaction_id null" branch).
- Keep `unique(document_id, attempt_generation, output_ordinal)` (nulls distinct ⇒
  harmless for email). **Add** partial `unique(data_source_id, output_ordinal) where
  document_id is null` for email idempotency.
- `assert_bulk_candidate_scope` trigger: wrap the document/chunk/batch coherence block
  **and** the account-from-batch check in `if new.batch_id is not null then … end if`.
  For email rows (batch_id null), assert `document_id is null` and that the
  `source_parse_attempt` belongs to the same `data_source` (no chunk join).

Bulk semantics are unchanged for bulk rows; email rows use the generalized shape.

### 2.2 `private.script_definitions` (new)

Per the scripting-engine design doc. Append-only, one active per key, **no browser
grants** (service role only):

```
script_key   text        -- 'email_pre_process' | 'transaction_post_process' | future
version      int          -- append-only, per key
source       text          -- Tengo source (≤ engine MaxSourceBytes)
checksum     text          -- sha256 of source
is_active    boolean       -- exactly one active per key (partial unique index)
notes        text
created_at   timestamptz
primary key (script_key, version)
```
Partial unique: `unique (script_key) where is_active`. RLS enabled, owner policy,
no anon/authenticated grants (mirrors `source_parse_attempts`). Owned by a new
`internal/scriptstore` package (SQL only; `*store` convention). Seeded via Supabase CLI.

### 2.4 Rule → script references (script selection by sender/subject)

`applyDeterministicRule` and the `parserrules` `ExtractionConfig`/`CaptureField`/`Values`
deterministic path are **retired** (T1 replace). Rules keep sender/subject matching and
`prompt_fragment`. Add nullable columns to **both** rule tables:

- `private.source_parser_rules`: `pre_process_script_key text`, `post_process_script_key text`
- `private.user_source_parser_rules`: `pre_process_script_key text`, `post_process_script_key text`

(Optional FK to `script_definitions(script_key)` via a keys-catalog table, or validate in
the store — a plain text key validated at seed time is enough for one operator.) Drop
`source_parser_rules.extraction_config` (or leave unused and remove in a follow-up).

**Resolution (per email):** `MatchAndApply(sender, content, rules)` already returns the
single highest-priority matching rule (ambiguity errors). Extend the worker-safe
projection so the selected rule exposes its two script keys. Effective key per stage:
`matched rule's key` → else the **global default key** (`email_pre_process` /
`transaction_post_process`) → else skip that stage. The chosen key+version is recorded in
the parse audit.

### 2.3 `data_sources.parse_status` rollup

`data_sources` no longer holds a single candidate's outcome. `parse_status` becomes a
rollup computed after each candidate reconciles: `failed > review_required > dangling >
parsed` (parsed = all candidates attached/created, or zero candidates). The existing
Dangling/Review/Failed queue filters key off this unchanged. Per-candidate suggestions
and reasons live on `source_candidates`, not the source row.

## 3. Scripting engine integration

### 3.1 Wiring

`cmd/api/main.go` and `cmd/worker/main.go` build one `scriptengine.Engine`
(`New(DefaultOptions())`, cheap, stateless, concurrent-safe) and a `scriptstore.Store`,
threading both into the transaction handlers — same pattern as existing collaborators.
Default module allowlist (`math, text, times, fmt, json, enum, base64, hex`) already
excludes `os`/`exec`.

### 3.2 Pre-process stage (new, `email_pre_process`)

Location: `internal/transactionworker` `handleSourceParse`, **after**
`LoadSourceParseInput` and **before** prompt assembly.

Adapter (`preprocessEmail`) — purpose: strip irrelevant content before the LLM:
1. Build input JSON from the normalized email — **subject, sender, text, received_at,
   attachments: [{filename, mime_type, byte_size}]** (metadata only; no bytes, no
   account data).
2. Resolve the pre-process script key via §2.4 (matched rule's `pre_process_script_key`
   → global default `email_pre_process`); load its active source via `scriptstore`. If
   none / flag off → skip.
3. `engine.Run(ctx, source, inputJSON)`.
4. **Strict-decode** the output into a fixed shape (e.g. `{ subject, sender, text,
   received_at }`); reject unknown fields. Rebuild `NormalizedContent` from it.
5. On any error/timeout/invalid output → **fall back** to the original
   `input.NormalizedContent` (PRD FR-T4). Record `pre_process` outcome (applied /
   fallback + reason) in the parse audit.
6. The (possibly transformed) content is what the LLM sees **and** what is stored as
   `source_parse_attempts.normalized_input`, so evidence `source_path`s stay consistent
   with what the model read.

Security: pre-process output is untrusted content — it flows into the prompt exactly
where email text already does (the platform prompt already treats email content as
untrusted evidence, not instructions). The script cannot see or add account data.

### 3.3 Post-process stage (new, `transaction_post_process`)

Location: `handleSourceParse`, inside the **per-candidate** loop, replacing/augmenting
the `applyDeterministicRule` step (PRD decision T1). Parallels
[`applyDeterministicRule`](../backend/internal/transactionworker/rules.go) and its
`rule:<id>:v<n>` provenance.

This **replaces** `applyDeterministicRule` (T1); the regex `extraction_config`/`Values`
path is removed. Adapter (`postprocessCandidate`), per candidate:
1. Marshal the candidate's **mutable** subset → JSON (the model-facing fields only:
   `transaction_kind, title, merchant_name, original_amount_minor, original_currency,
   sgd_amount_minor, occurred_at, references, account_evidence, line_items,
   category_leaf_name`). **Never** `UserID`, `Confidence`, `AutoEligible`.
2. Resolve the post-process script key via §2.4 (matched rule's `post_process_script_key`
   → global default `transaction_post_process`); load its active source. If none / flag
   off → skip.
3. `engine.Run(ctx, source, candidateJSON)`.
4. **Strict-decode** back into the mutable subset (DisallowUnknownFields); apply only to
   those fields.
5. Record `script:<key>:v<n>` provenance for each field the script set, via a new
   `replaceScriptEvidence` mirroring `replaceRuleEvidence`.
6. Continue the existing hardening tail unchanged: `SanitizeAccountEvidenceForMatching`
   → `DeriveAutoEligibility` → `ValidateParsedResponseAfterRule` → re-`AggregateConfidence`
   → `ValidateCandidate`. These run **after** the script, so a script cannot break an
   invariant: a violating candidate is rejected (dropped per D2, traced in the audit),
   not persisted.

### 3.4 Provenance grammar

`reconciliation.validEvidenceSourcePath` currently allows model paths and, when
`allowRule`, `ruleEvidencePath = ^rule:[^:\s]+:v[1-9][0-9]*$`. Add a sibling
`scriptEvidencePath = ^script:[^:\s]+:v[1-9][0-9]*$` accepted under the same
`allowRule`/after-rule path so `AggregateConfidence` and `ValidateParsedResponseAfterRule`
treat script-set fields as valid, trusted, server-injected evidence.

## 4. reconciliation package changes

- **Batch decode:** `DecodeParsedResponseBatchForRuleApplication(raw) ([]ParsedResponse,
  error)` decoding `{ "transactions": [ {candidate, evidence}, … ] }` strictly
  (DisallowUnknownFields), bounded to ≤50 (PRD NFR4). Keep the single-object decoder for
  any other caller.
- **Same-source match exclusion:** `Reconcile` gains no signature change; instead the
  store excludes transactions already linked to the current `data_source` from the
  `transactions` slice it passes in (via `transaction_data_sources`), so candidate B
  can't auto-attach to the transaction candidate A just created from the same email.
- `Reconcile` decision logic, `MatchWindow` (10 min), `minimumCreateConfidence` (0.75),
  account resolution and typed keys are **unchanged**.

## 5. Worker & store changes

### 5.1 `handleSourceParse`
- Pre-process (§3.2) → assemble prompt → `Parser.ParseTransactionEvidence` (prompt v3,
  array output).
- `DecodeParsedResponseBatchForRuleApplication` → per candidate: evidence checks →
  bind `UserID` → `AggregateConfidence` → **post-process (§3.3)** → sanitize →
  auto-eligible → validate. Invalid candidate ⇒ **drop + record** in the audit (D2).
- Empty array ⇒ mark source `parsed`, zero candidates (D3).
- `SaveParsedSource` persists one `source_candidates` row per valid candidate
  (origin=`gmail_email`, output_ordinal=index, fingerprint, generation 1), linked to the
  single `source_parse_attempts` audit row (must capture its inserted id), and enqueues
  **one `reconciliation` job per candidate** (payload `{data_source_id, candidate_id}`).

### 5.2 `handleReconciliation`
- Payload gains `candidate_id`; loads one `source_candidates` row. Fallback: if absent,
  reconcile all `pending_reconciliation` candidates for the source (protects in-flight
  legacy jobs).
- `LoadReconciliationInput` reads the candidate row (not "latest valid attempt"), plus
  owned accounts and same-source-excluded transactions.
- `PersistReconciliation` writes the outcome to the **candidate row**
  (status/transaction_id/suggested_*/reason/match_confidence) + `transaction_data_sources`;
  the "already linked" short-circuit becomes per-candidate. Serialized-create lock +
  in-tx repeat-reconcile stay, re-loading that candidate. Then **recompute the source
  rollup** in the same tx. Sync-run counters increment per candidate (existing columns).

### 5.3 New `internal/scriptstore`
- `LoadActiveScript(ctx, key) (source, key string, version int, ok bool)` — worker hot path.
- CRUD for the management API: `ListScripts`, `ListVersions(key)`, `GetVersion(key,
  version)`, `CreateVersion(key, source, notes)` (append-only, next version, checksum),
  `Activate(key, version)` (flip active — enforces one-active-per-key). Service role only;
  no browser grants. (Dry-run does not touch the store — it calls `engine.Run` directly.)

## 6. Prompt v3 (array output)

New `internal/prompts/transactions/system_v3.txt`, `TransactionParserVersion → 3`.
Output shape: `{ "transactions": [ { "candidate": {…}, "evidence": [{…}] }, … ] }`.
Added rules: one object **per distinct transaction**; do **not** split one itemized
purchase (that stays `line_items` within one candidate); `[]` when no transaction;
hard cap 50. Evidence grammar and all safety text carry over. Update
`alibaba_http_test.go` version assertion and prompt-content tests.

## 7. API & Frontend

Two UI areas: **per-candidate queue actions** (from the multi-transaction work) and a
**script management UI** (G6). Both go through the Go API with `requireUser` + service
role — the `private` schema keeps no browser grants, so scripts are **never** browser-written
directly. (This supersedes the scripting-engine doc's "no management surface"; CLI seeding
still works as a secondary path.)

### 7.1 Candidate actions (multi-transaction)
- Routes: `POST /v1/transactions/sources/{id}/candidates/{candidate_id}/attach` and
  `.../create-transaction` (per candidate); `.../retry` stays source-level.
- `SourceSummary` gains `candidates[]` (id, index, status, suggested account/txn, reason,
  amount summary). `SourceInspector.tsx` renders one actionable row per candidate; queue
  lists still key off the rollup `parse_status`.

### 7.2 Script management API (new, `requireUser` + service role)
- `GET /v1/transactions/scripts` — list keys with active version + version count.
- `GET /v1/transactions/scripts/{key}/versions` — version list (metadata).
- `GET /v1/transactions/scripts/{key}/versions/{version}` — one version's source.
- `POST /v1/transactions/scripts/{key}/versions` — create a new append-only version
  (body: source + notes; server computes checksum, assigns next version).
- `POST /v1/transactions/scripts/{key}/activate` — set active version (activate/rollback).
- `POST /v1/transactions/scripts/dry-run` — run `{source, input}` through `engine.Run`
  and return output or a sandboxed error. **Does not persist.** Gated (OQ6).
- Parser-rule endpoints extended to read/write `pre_process_script_key` /
  `post_process_script_key`.

Handlers are thin (verify → validate source size/shape → `scriptstore` → JSON), consistent
with the `http`/service/`store` convention. Source is bounded by the engine's
`MaxSourceBytes` before persistence.

### 7.3 Script management FE (new page in the `transactions` feature)
- A **Scripts** settings page beside the existing parser-rule/settings editor: list by
  key, a source editor (monospace `<textarea>`; no heavy editor dependency), version
  history with activate/rollback, notes, and a **dry-run panel** (paste sample input →
  see output/error). Explicit loading/empty/error states; strict types, no `any`.
- The parser-rule editor gains two selects for pre/post script keys (FR15) and shows the
  resolved effective scripts.
- Optional observability: the existing source debug view (`GET
  /v1/transactions/sources/{id}/debug`) shows pre-process outcome (applied/fallback +
  script/version) and per-field `script:<key>:v<n>` provenance.

**Security note:** the script UI is a code-execution authoring surface. Until a multi-user
admin model exists (design-review #5), any authenticated user could edit scripts — so keep
it behind the feature flag and treat as operator-only. Dry-run executes sandboxed code on
request; gate it the same way.

### 7.4 Unified "Parsing Pipeline" Settings view (IA revamp, G7/FR16)

One page, `transaction-pipeline` (new nav entry), that shows the whole pipeline for a
chosen context in **execution order**. It reuses existing endpoints where possible and
absorbs today's `transaction-settings`, `transaction-global-settings`, and
`transaction-prompt-preview` pages (T7 = consolidate).

**Layout (top → bottom):**

```
┌ Context ───────────────────────────────────────────────┐
│ Sender [__________]  Subject [__________]   or ▸ recent │  ← resolves the matching rule
│ Matched rule: <name> (priority N)  ·  or "default only" │
└────────────────────────────────────────────────────────┘
▼ 1. Pre-processing            [script: email_pre_process v3 ▸ edit] [dry-run]   ← editable
     purpose: strip irrelevant content · applied vs fallback shown from audit
▼ 2. Global Prompt             platform vN (read-only) + global rule fragment [edit]   ← editable
▼ 3. User Prompt               user default [edit] + user source-rule fragment [edit]  ← editable
· Attachments                  N eligible images sent with the text (metadata)   ○ read-only
▼ 4. LLM                       model: qwen3.8-flash (marker; no edit)
▼ 5. Post-processing           [script: transaction_post_process v2 ▸ edit] [dry-run]  ← editable
· Server checks (locked)       owner · sanitize account evidence · auto-eligible · validate   ○ read-only
· Reconciliation               resolve account → dedup → create/attach/review/dangling   ○ read-only
┌ Assembled preview ─────────────────────────────────────┐
│ full system prompt (from prompt-preview endpoint)       │
│ [Run end-to-end dry-run on a sample/recent email ▶]     │
└────────────────────────────────────────────────────────┘
```

Read-only stages (○) are shown greyed/locked with a short summary; they make the fixed
server guarantees and the downstream reconciliation visible so the user sees what a script
**cannot** change. Attachments come from the source's `raw_data` metadata (eligible + sent);
Server checks and Reconciliation are descriptive (fixed logic), not configurable here.

**Data sources / endpoints:**
- Context resolution: extend the prompt-preview inputs (which already take
  `global_rule_id` / `user_rule_id` / `include_user_default`) so a sender+subject can
  resolve the matched rule server-side (or the page resolves it by listing rules). The
  matched rule also yields the effective pre/post script keys (§2.4).
- Stage 1/5 read from the script API (§7.2); "edit" opens the script editor (§7.3) inline
  or in a drawer; dry-run uses `POST /scripts/dry-run`.
- Stage 2/3 reuse the global-settings / user-settings editors, surfaced inline per stage.
- Assembled preview reuses `POST /v1/transactions/prompt-preview`
  (`assembled_system_prompt`).
- **End-to-end dry-run** (new, optional): a `POST /v1/transactions/pipeline/dry-run` that,
  given a sample/recent source id (or pasted email) + the resolved context, runs
  pre-script → assembles prompt → **skips or optionally calls** the model → post-script,
  returning each stage's output for inspection. Model call is opt-in to avoid cost;
  default shows pre/prompt/post deterministically.

**Frontend:** a `PipelinePage.tsx` composed of stage sub-components; strict types; explicit
loading/empty/error per stage; no new router (extend `workspace.ts`). Existing settings
pages either redirect into the relevant stage or are removed once parity is reached.

## 8. Migration & dev data

- **Committed forward migration** `supabase/migrations/<ts>_generalize_source_candidates.sql`:
  rename + add columns + relax nullability + union statuses + conditional trigger/
  constraints; **create `private.script_definitions`** (+ RLS, grants, indexes). No
  backfill (dev data dropped). Prod-safe (it only alters empty/compatible structures).
- **Dev-only, uncommitted** truncate (run at the P0 checkpoint against the linked dev
  DB): clears pipeline tables (data_sources, source_parse_attempts, source_candidates,
  transactions, transaction_data_sources, bulk_import_*, credit_card_statements +
  dependents, transaction_sync_runs, transaction_jobs) while **preserving**
  `public.accounts`, `private.gmail_connections`, account matching keys, parser
  config/rules, categories. `public.accounts` and Gmail auth are never touched.
- **Never** run `supabase db reset --linked` (it wipes accounts + Gmail auth).

## 9. Security invariants (must hold after the revamp)

1. Model + both scripts receive **no** account metadata or matching keys.
2. `UserID`, `Confidence`, `AutoEligible` are server-derived **after** the post-process
   script and are not script-writable.
3. Account resolution uses only typed keys, re-derived server-side; a script cannot make
   a stray identifier resolve an account (sanitize runs after the script).
4. `os`/`exec` unreachable; every script run is time/alloc/size-bounded; a script failure
   is isolated to its stage/candidate and audited.
5. Reconciliation stays replayable — scripts take any clock from `input`, never
   `times.now()`.
6. Secrets stay server-only; `private` schema keeps no browser grants.

## 10. Testing

- Go: `scriptstore` unit tests; worker tests for pre-process apply + fallback, per-candidate
  post-process + provenance + invalid-drop, batch decode, same-source exclusion, rollup.
- reconciliation: batch decode bounds; `script:` provenance acceptance.
- pgTAP: `source_candidates` (ownership, rollup, email idempotency, dangling, **bulk
  regression** — bulk constraints/trigger still enforced); `script_definitions`
  (no browser grants, one-active-per-key); rule tables' new script-key columns.
- Script API: version create/list/get, activate/rollback (one-active invariant), dry-run
  output + sandboxed-error path + source-size rejection; authz (`requireUser`).
- FE: Scripts page loading/empty/error states; dry-run render; rule-assignment selects.
- Prompt v3 assertions.
- Full: `go build ./... && go vet ./... && go test ./...`; `npm run lint && npm run build`.

## Implementation status (2026-09-05)

Done + validated on branch `codex/transaction-parsing-revamp` (PR #22):
- **P0–P4** backend: schema/rename + `script_definitions`, batch decode + `script:`
  provenance, `scriptstore`, Tengo pre/post-process, N-candidate persistence +
  per-candidate reconcile + rollup, prompt v3. Live dev-DB integration tests pass.
- **P3c cardinality fix** (migration `20260905150000`): per-candidate link cardinality
  so one email can link to many transactions.
- **P5 backend**: per-candidate `attach` / `create-transaction` + `candidates` list API.
- **P5 frontend**: `SourceCandidateResolver` wired into `SourceInspector` for Gmail
  sources (per-candidate create/attach), replacing the now-defunct per-source form.
- **P6 backend**: script management API (CRUD + activate + dry-run).
- **P6 frontend**: Parser Scripts page + candidate/script API+model layer (tsc/lint/build).
- **P7 frontend (additive)**: Parsing Pipeline overview page showing all stages in order
  (editable stages link to their editors + show the active script; read-only stages for
  context; assembled preview via prompt-preview). Existing settings pages left in place.
- **T1**: regex deterministic extraction retired — `applyDeterministicRule` +
  `ExtractionConfig`/`Values` removed, global-rule matching is sender/content-only, Tengo
  post-process is the sole deterministic path. Verified live.

Remaining (need visual QA / follow-up):
- **P7 full-replace (T7)**: fold settings / global-settings / prompt-preview into the
  pipeline view and retire those pages — best done with visual iteration.
- **FE polish**: CSS for the new `scripts-*` / `candidate-*` / `pipeline-*` classes;
  transfer-from-a-single-candidate flow.
- **Schema follow-up**: drop the now-dormant `source_parser_rules.extraction_config`
  column and update the pgTAP suites that still assert it.
- **P8**: seed initial pre/post scripts + end-to-end acceptance.
- **P8**: seed initial scripts + enable + end-to-end acceptance.
- **T1 cleanup**: remove `applyDeterministicRule`/`ExtractionConfig` + drop the
  `extraction_config` column. Note this is a real refactor: `parserrules.applyOne`
  currently *requires* `extraction_config` to match a global rule, so global-rule
  matching must be reworked to match on sender/content alone before the column is dropped.

## 11. Build order (phased; each phase validated before the next)

Standard per-phase validation gate (run the narrowest relevant subset, then the whole):
`cd backend && go build ./... && go vet ./... && go test ./...` for Go;
`cd frontend && npm run lint && npm run build` for FE; pgTAP via `supabase test db` for
schema. A phase is "done" only when its gate below is green.

- **P0 — Schema + rename.** Forward migration (additive + rename only): rename
  `bulk_import_candidates` → `source_candidates` + generalize (origin, nullable bulk cols,
  unioned statuses, suggested_* cols, conditional trigger/constraints); create
  `script_definitions`; add `pre/post_process_script_key` to both rule tables. Update the
  19 Go SQL sites to the new name. (`extraction_config` is dropped in P1 with the code that
  reads it, to avoid breaking the app between phases.)
  **Validate:** `go build`/`vet`/unit tests green offline; **checkpoint** = apply to linked
  dev + run the dev-only truncate (with approval), then integration + pgTAP green,
  including **bulk + credit-card regression**. *(This is the one destructive step; it does
  not proceed without explicit approval.)*
- **P1 — reconciliation core (additive, non-breaking).** Batch decode
  (`{transactions:[…]}`, ≤50) + accept `script:<key>:v<n>` provenance. (Same-source match
  exclusion moves to P3 with `LoadReconciliationInput`; `applyDeterministicRule`/
  `ExtractionConfig` removal + column drop move to P3 with the worker rework, so nothing
  breaks mid-phase.) **Validate:** `go test ./internal/reconciliation/...`; full Go gate.
- **P2 — script store.** `internal/scriptstore` (Store over pgxpool: LoadActiveScript +
  CRUD: ListScripts/ListVersions/GetVersion/CreateVersion/Activate) + checksum helper +
  tests. (Engine construction + wiring into `cmd/worker` lands in P3 and into `cmd/api` in
  P6, alongside their consumers — an unused engine in `main` would not compile.)
  **Validate:** `scriptstore` unit + integration tests; full Go gate.
- **P3 — worker two-stage parse.** Pre-process stage + N-candidate parse loop +
  post-process stage (**replaces** `applyDeterministicRule`; remove it + `ExtractionConfig`
  + drop the `extraction_config` column in a paired migration) + rule→script resolution
  (**default-key first, then per-rule selection** — OQ3) + same-source match exclusion in
  `LoadReconciliationInput` + per-candidate reconcile + rollup + job payload. **Validate:**
  worker unit tests (pre-apply+fallback, post+provenance+invalid-drop, rollup, empty-array);
  full Go gate; integration against dev.
- **P4 — prompt v3.** Array-output `system_v3.txt` + version bump + prompt tests.
  **Validate:** `go test ./internal/providers/... ./internal/transactionprompt/...`.
- **P5 — candidate UI.** Per-candidate attach/create API + `SourceInspector` candidate
  rows (+ optional script provenance in debug). **Validate:** FE gate; API handler tests.
- **P6 — script management.** API (CRUD + activate + dry-run, dry-run gated — OQ6) +
  `scriptstore` CRUD + Scripts FE page + rule-assignment selects (G6/FR13–15). Behind the
  feature flag. **Validate:** API tests (versioning, one-active, dry-run sandbox); FE gate.
- **P7 — unified pipeline view.** `PipelinePage.tsx` (editable + read-only stages, T8) +
  context resolution + end-to-end dry-run endpoint; **remove/redirect** the three old
  settings pages (T7). **Validate:** FE gate; endpoint tests; manual parity check.
- **P8 — enable + acceptance.** Seed initial pre/post scripts + enable flags; run the §10
  acceptance list (AC1–AC8). **Validate:** end-to-end against dev.

## 12. Risks & mitigations

| Risk | Mitigation |
| --- | --- |
| Generalizing the candidate table breaks bulk. | Conditional constraints/trigger on `batch_id`; P0 gate is bulk regression tests green before any email work. |
| A pre-process script silently degrades LLM input. | Strict-decode output; fallback to original on error; audit records applied-vs-fallback; determinism caveat documented. |
| A post-process script violates an invariant. | Full hardening tail re-runs after the script; violating candidate dropped + audited, never persisted. |
| Intra-email cross-attach (B attaches to A's new txn). | Same-source match exclusion (§4). |
| Committed truncate wiping prod. | Truncate is dev-only and never committed; only forward schema migrations are committed. |
| Scope creep into #4/#5. | NG1/NG2 hold: ingestion chains and bulk's reconcile fn stay separate; T1 (replace vs additive) is an explicit gated decision. |
| Script UI is a code-execution authoring surface; any authenticated user can edit scripts (design-review #5 footgun). | Behind the feature flag; operator-only until a multi-user admin model exists; dry-run + writes go through `requireUser` + service role; source size-bounded; output re-validated on the worker path regardless. |
