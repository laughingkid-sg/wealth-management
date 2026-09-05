# PRD — Transaction Parsing Revamp

> **Date:** 2026-09-05 · **Status:** Draft for review · **Owner:** (maintainer) ·
> **Author:** Claude Code session · **Not yet approved; nothing built.**

## 1. Summary

Revamp the email → transaction pipeline so that:

1. **One email can produce many transactions** (a bank digest, a multi-charge
   receipt, a split payment), each reconciled independently.
2. Both evidence pipelines (Gmail and Bulk Import) **share one candidate model**
   instead of two parallel ones.
3. The already-built **Tengo scripting engine becomes a first-class part of parsing at
   two stages** — **pre-processing the email before the LLM** and **post-processing each
   parsed candidate after it** — letting operator-authored scripts transform inputs and
   outputs under the existing untrusted-output guardrails.

This is a single coherent revamp because all three touch the same seam — the
per-candidate hardening loop in the parse worker — and doing them separately would
mean touching that seam three times and building throwaway structure each time.

## 2. Problem

- **Single-transaction assumption.** The Gmail pipeline is hard-wired one email →
  one candidate → one reconciliation → one transaction. Any email that legitimately
  represents several transactions is either mis-parsed into one, or forced into the
  Review/Failed queue. (`line_items` already model *items within one* transaction;
  this is about genuinely separate transactions.)
- **Two candidate models.** Bulk Import already solved "one source → N candidates →
  per-candidate reconcile → own transaction" (`private.bulk_import_candidates`), but
  the Gmail pipeline inlines a single candidate's state onto the `data_sources` row.
  Building a third, email-only structure would deepen the duplication already flagged
  as design-review finding #4.
- **The scripting engine is inert.** `internal/scriptengine` is built, sandboxed, and
  tested, but no caller uses it. Deterministic post-processing today is a regex-driven
  `applyDeterministicRule`, which design-review #5 flags as an over-built rule layer.

## 3. Goals

- G1 — A single email reliably yields N transactions, each with its own account
  evidence, amount, direction, date, and its own reconciliation outcome.
- G2 — Each parsed transaction reconciles **independently**: one may auto-create while
  another goes to Review and a third dangles.
- G3 — Gmail and Bulk Import write into **one generalized candidate table**
  (`private.source_candidates`), sharing the pure reconciliation core for the parts
  that are genuinely identical.
- G4 — Operator-authored **Tengo scripts run at two parse stages** — a **pre-process**
  transform on the normalized email before the LLM, and a **post-process** transform on
  each parsed candidate after it — versioned and rollback-able, with all input/output
  treated as untrusted.
- G5 — No regression to the security invariants: the model/script never sees account
  metadata; ownership, confidence, and auto-eligibility stay server-derived.
- G6 — A **script management UI** (API-mediated, `requireUser` + service role — never
  direct browser writes to `private`): list scripts by key, view/edit source, create a
  new version, activate/rollback, dry-run a script against sample input, and assign
  pre/post scripts to sender/subject rules.
- G7 — A **unified "Parsing Pipeline" Settings view** that gives the user one global
  place to see and edit the whole pipeline for a given **sender + subject** context, laid
  out in execution order: **Pre-processing → Global Prompt → User Prompt → (LLM) →
  Post-processing**, with a live assembled preview and end-to-end dry-run. This revamps
  and consolidates today's separate user-settings, global-settings, and prompt-preview
  pages into one coherent, stage-based screen.

### Mental model (how Tengo relates to prompts)

Prompt fragments and Tengo scripts are both **selected by the same sender/subject rule
matcher**, but they differ: prompts are *layered text instructions inside* the LLM call;
Tengo scripts are *deterministic code that wraps* the call — a **pre** script cleans the
email before the LLM, a **post** script fixes each candidate after it. A matched rule can
contribute up to three things: `pre_process_script`, `prompt_fragment`, `post_process_script`.
Prompts concatenate across layers; scripts (as designed) **select one per stage** (see T6).

## 4. Non-goals

- NG1 — Merging the two *ingestion/parse chains* (Gmail's 3-stage vs Bulk's 5-stage
  chunk/aggregate). Only the candidate + reconcile layers converge (design-review #4's
  high-risk half stays deferred).
- NG2 — Unifying the two *reconcile decision functions*. Bulk's calendar-day +
  credit-card-bill + internal-transfer policy stays as-is; email keeps the 10-minute
  window core.
- NG3 — ~~A management UI or API for scripts.~~ **Superseded — a script management UI is now
  in scope (G6).** CLI seeding remains supported as a secondary path. Note this reverses the
  "Management surface: None for now" row in the scripting-engine design doc.
- NG4 — Preserving historical dev pipeline data (see §9).
- NG5 — Multi-user admin authorization for shared parser rules (design-review #5's
  other half).

## 5. Users & context

Single private user, dev stage, pre-launch. "Operator" (the maintainer) authors
parser scripts directly against the database. No end-user-facing scripting surface.

## 6. Scope decisions

### Locked earlier this effort
| ID | Decision |
| --- | --- |
| S1 | **Separate transactions**, not line items. |
| S2 | **Independent per-transaction** reconciliation. |
| S3 | Scope **C** — converge candidate + reconcile (not full pipeline merge). |
| S4 | **Generalize `bulk_import_candidates` in place** → `source_candidates`. |
| D1 | Converge **storage + email's use of the shared core**; leave bulk's decision fn. |
| D2 | **Drop invalid candidates, keep valid ones**; record each dropped one in the parse audit (source only Failed when the provider call fails or *nothing* usable parsed). |
| D3 | **Empty result → benign `parsed`**, zero candidates, out of all queues. |
| DATA | **Forward migration, no `db reset`.** Keep `public.accounts` + `private.gmail_connections`; drop pipeline data via a **dev-only, uncommitted** truncate; re-sync fresh. |

### New decision this requirement raises
| ID | Question | Recommendation |
| --- | --- | --- |
| **T1** | Regex `applyDeterministicRule` post-process: replace or additive? | **DECIDED — Replace.** Tengo becomes the deterministic field-mutation path; the regex rules' `extraction_config`/`Values` deterministic path is **retired**. Rules keep their *sender/subject matching* and *prompt-fragment* (LLM guidance) roles. Retiring `extraction_config` removes one axis of the over-built rule layer (#5). |
| **T2** | Script stages & keys | **Two stages:** a **pre-process** (email JSON → cleaned email JSON, before the LLM — purpose: strip irrelevant content) and a **post-process** (candidate JSON → candidate JSON, per candidate, after the LLM). Default keys `email_pre_process` / `transaction_post_process`. One active version per key. |
| **T2b** | **Script selection by sender/subject (point 3)** | A **matched sender/subject rule selects which scripts run**, reusing today's `MatchAndApply` (single highest-priority rule by sender/subject/content). Each rule may name a `pre_process_script_key` and/or `post_process_script_key`; when it names none, the stage falls back to the **global default key**. This lets different senders get different cleaning/mutation logic without new matching machinery. |
| **T3** | Feature-flag the consumers | Yes — each stage stays inert unless a script is resolved (via rule or default) AND its flag is on, so the revamp ships dark and stages enable independently. |
| **T4** | Pre-process failure behavior | A pre-process script error/timeout **falls back to the un-transformed normalized content** (recorded in the audit), so a bad script degrades the LLM input, never blocks ingestion. |
| **T5** | Script management UI | **In scope (G6).** API-mediated CRUD + versioning + activate/rollback + **dry-run** + rule assignment. Lives beside the existing parser-rule/settings editor in the `transactions` feature. **Security caveat:** script authoring is a code-execution surface; like the shared-global-rules footgun in design-review #5, any authenticated user could edit scripts until a proper admin model exists — so gate it behind the feature flag and treat it as operator-only until multi-user auth is added. |
| **T6** | Script composition (raised by "similar to prompts?") | **DECIDED — select-one per stage** (highest-priority matching rule, else default). Deterministic, no ordering questions. Chaining deferred. |
| **T7** | Unified pipeline view: consolidate or add? | **DECIDED — Consolidate (full replace).** The stage-based view becomes the primary Settings surface; `transaction-settings`, `transaction-global-settings`, and `transaction-prompt-preview` are **removed/redirected**, their editing folded into the relevant stages and the assembled-preview panel. |
| **T8** | How complete is the view? | **DECIDED — show the full pipeline**, including read-only stages the user can't edit: an **Attachments** indicator on the LLM input, a locked **Server checks** stage (ownership, account-evidence sanitize, auto-eligibility, validation), and a read-only **Reconciliation** stage (resolve account → dedup → create/attach/review/dangling). These make the guarantees visible — scripts can't override them. |

## 7. Functional requirements

- FR1 — The parser returns an **array** of `{candidate, evidence}` objects; the worker
  persists one `source_candidates` row per valid candidate.
- FR2 — Each candidate enqueues its **own reconciliation job**; outcomes
  (create/attach/review/dangling) are recorded per candidate.
- FR3 — `data_sources.parse_status` is a **rollup** over its candidates:
  `failed > review_required > dangling > parsed` (all linked, or zero candidates).
- FR4 — A candidate cannot auto-attach to a transaction created from the **same email**
  (same-source match exclusion).
- FR5 — Invalid candidates are dropped but **traced** in `source_parse_attempts`
  (count + bounded per-candidate error). Zero valid + ≥1 invalid ⇒ source Failed.
- FR6 — Empty `transactions: []` ⇒ source `parsed`, zero candidates, absent from queues.
- FR7 — **Pre-process (clean before LLM):** before assembling the LLM prompt, the
  resolved pre-process script runs on the normalized email (subject, sender, text,
  received_at, attachment *metadata* only): email JSON → `engine.Run` → strict-decode →
  the cleaned content becomes the model input and the audit `normalized_input`. Purpose is
  to strip irrelevant content so the LLM sees only what matters. On error/timeout, fall
  back to the un-transformed content (T4). Evidence source-paths reference the content the
  model actually saw.
- FR8 — **Post-process:** after LLM decode + hardening and **before** re-validation, the
  resolved post-process script runs **per candidate**: candidate → JSON → `engine.Run` →
  strict-decode → `script:<key>:v<n>` provenance → existing sanitize/auto-eligible/
  validate re-runs. This is the sole deterministic-mutation path (T1 replace).
- FR9 — **Script selection:** the pre/post scripts for an email are resolved from the
  single highest-priority matching sender/subject rule (`MatchAndApply`); a rule that
  names no script for a stage falls back to that stage's global default key. No match ⇒
  global default (or skip if none). Selection is recorded in the audit.
- FR10 — Scripts are **versioned**; exactly one active version per key; rollback by
  flipping active. Seeded via Supabase CLI. The retired regex `extraction_config`
  deterministic path is removed.
- FR11 — Bulk Import continues to function unchanged against the renamed candidate table.
- FR12 — Manual queue actions (attach / create) operate **per candidate**; retry stays
  source-level. The source inspector lists candidates individually.
- FR13 — **Script management UI** (API-mediated): list scripts by key with the active
  version; view a version's source; create a new version; activate/rollback a version;
  edit notes. All via Go API (`requireUser`, service role); no direct browser writes to
  `private`.
- FR14 — **Dry-run:** the UI can run a draft script against a sample input JSON and show
  the output (or sandboxed error) without persisting — for authoring/verification.
- FR15 — **Rule assignment UI:** the existing parser-rule editor can set a rule's
  `pre_process_script_key` / `post_process_script_key`, and shows the resolved effective
  scripts.
- FR16 — **Unified Parsing Pipeline view:** a Settings screen with (a) a **context
  selector** (enter/pick a sender + subject, or a recent source, or a specific rule) that
  resolves which layers apply, and (b) an ordered, editable stage stack — **Pre-processing**
  (resolved pre-script), **Global Prompt** (immutable platform text + matched global rule
  fragment), **User Prompt** (user default + matched user source-rule fragment), **LLM**
  (model marker), **Post-processing** (resolved post-script) — plus a **live assembled
  prompt preview** and an **end-to-end dry-run** (pre-script → assembled prompt → post-script)
  against a sample/recent email. Each stage links to its editor (script version/activate,
  fragment edit, default edit). Read-only items (platform prompt) are clearly marked.
  The view also shows the **non-editable** stages for completeness (T8): **Attachments**
  on the LLM input, a locked **Server checks** stage, and a read-only **Reconciliation**
  stage — so the full email → transaction flow is visible in one place.

## 8. Non-functional requirements

- NFR1 (Security) — The model and any script **never** receive account metadata or
  matching keys. Ownership (`UserID`), `Confidence`, and `AutoEligible` are
  server-derived after the script and **not** script-writable. `os`/`exec` remain
  unreachable (already enforced by the engine).
- NFR2 (Correctness) — Money stays `int64` minor units end-to-end (the engine already
  preserves this). Reconciliation remains **replayable**; scripts must take any clock
  from `input`, never `times.now()`.
- NFR3 (Isolation) — A script runs under the engine's sandbox: 250 ms timeout, alloc
  cap, source-size cap. A script failure fails **only that candidate**, recorded in the
  audit; it never aborts the whole source or the worker.
- NFR4 (Bounds) — Per-email transaction cap (proposed 50) to bound abuse.
- NFR5 (No regression) — Bulk Import + credit-card reconciliation pgTAP and Go tests
  stay green after the table rename/generalization (hard gate before building the email
  path).
- NFR6 (Prod-safety) — Schema changes ship as forward migrations; the dev data wipe is
  never a committed migration.

## 9. Data handling & rollout

- Preserve `public.accounts` and `private.gmail_connections` (and account matching
  keys, parser config, categories) — untouched by the migration.
- Drop existing pipeline data (sources, parse attempts, candidates, transactions and
  dependents) via a **one-off dev-only** truncate at the P0 checkpoint; re-sync
  repopulates through the new pipeline.
- Ship behind a feature flag so the engine stage is inert until a script is seeded.

## 10. Success criteria (acceptance)

- AC1 — A test digest email with 3 distinct charges produces 3 transactions/candidates
  with independent outcomes.
- AC2 — A single itemized receipt still produces **one** transaction with line items
  (no over-splitting).
- AC3 — A non-transaction email lands as benign `parsed`, in no queue.
- AC4 — A seeded **post-process** script measurably transforms a candidate field, with
  `script:<key>:v<n>` provenance recorded and the candidate still passing validation;
  a script that violates an invariant is rejected by re-validation, not persisted.
- AC5 — A seeded **pre-process** script measurably transforms the email content the LLM
  receives (verified via the audit `normalized_input`); a failing pre-process script
  falls back to un-transformed content and ingestion still succeeds.
- AC6 — Bulk Import + credit-card flows unchanged (tests green).
- AC7 — No account metadata reaches the model or either script (verified by prompt/input
  assembly tests).
- AC8 — From the UI, an operator can create a new script version, dry-run it against
  sample input, activate it, roll back to a prior version, and assign it to a sender/subject
  rule — all via the Go API, with no browser write to the `private` schema.

## 11. Open questions

- ~~OQ1 (T1)~~ — **Decided: Replace.** Regex `extraction_config` deterministic path retired.
- ~~OQ2~~ — **Decided:** per-email transaction cap = 50.
- ~~OQ3~~ — **Decided:** default-key script path first (P3), per-rule selection immediately
  after (same matcher, small increment).
- ~~OQ5 (T6)~~ — **Decided: select-one per stage** (highest-priority matching rule, else
  default). Deterministic, no ordering questions. Chaining can be revisited later if needed.
- ~~OQ6~~ — **Decided: yes** — dry-run stays flag/operator-gated even in single-user dev
  (it executes sandboxed code on request).

**All decisions are now locked; no open questions remain.**
- OQ4 — Should the pre-process script also receive rendered **attachment text/OCR**, or
  metadata only? **Decided: metadata only** initially; attachment bytes stay in the
  worker's attachment path, not the script sandbox.

## 12. Out of scope (this requirement)

Ingestion-chain merge (NG1), reconcile-function unification (NG2), multi-user admin auth
(NG5), Dashboard/Investments/Goals features. (Script management UI is now **in** scope —
G6/FR13–15.)
