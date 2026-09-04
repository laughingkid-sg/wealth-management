# Design — Shared Tengo Scripting Engine

> **Date:** 2026-09-05 · **Status:** Engine built and tested; **not yet used by any
> caller**. This is deliberate — the engine ships first as a standalone,
> future-use capability. Consumers (parser-rule post-processing, storage,
> management) are described here as the intended path, not as current behaviour.

## Goal

A single, domain-agnostic engine that runs operator-authored
[Tengo](https://github.com/d5/tengo) scripts with a strict **JSON in → JSON out**
contract, embedded in-process so every service in the Go module (`api`, `worker`)
can share one instance. Scripts and their logic will live in the database; there
is no front end for them.

## Decisions (locked with the maintainer)

| Question | Decision |
| --- | --- |
| Packaging | **Shared `internal/` package**, imported in-process. Tengo is an embedded interpreter, not a separate process — no new deployable, network hop, or infra. |
| First consumer | **Parser-rule / transaction post-processing** — eventually slots in beside `applyDeterministicRule` in `transactionworker`. |
| Relationship to existing rules | **Additive first** (extra post-process stage), keeping today's regex `parserrules`. Consolidation/replacement is a later, separate decision. |
| Authoring & versioning | **Operator-authored, versioned/rollback** from day one. |
| Storage | A table in the **`private` schema** with **no browser grants**. |
| Management surface | **None for now.** Scripts are written to the DB via the **Supabase CLI** (with LLM assistance), not a management API or FE. |

## What exists today (this change)

Package [`backend/internal/scriptengine`](../backend/internal/scriptengine):

| File | Responsibility |
| --- | --- |
| `engine.go` | The `Engine`: compile + sandbox + run. Public API `New(Options)` and `Run(ctx, source, input) (json.RawMessage, error)`. Stateless after construction; safe for concurrent use. |
| `marshal.go` | JSON⇄Tengo conversion, with the int64-preserving number path. |
| `engine_test.go` | 17 table tests (see Testing). |

Dependency added: `github.com/d5/tengo/v2` (pure-Go, MIT, **zero transitive
deps**). This is the one addition to an intentionally minimal dependency set; it
is unavoidable for the feature and carries no supply-chain fan-out.

### Engine API

```go
engine := scriptengine.New(scriptengine.DefaultOptions())
out, err := engine.Run(ctx, source, json.RawMessage(`{"amount_minor":150}`))
```

### Script contract

- The script reads its input from the global variable **`input`** (a map).
- The script **must declare a top-level `output`** with `:=`, set to any
  JSON-serialisable value (map, array, string, number, bool). No top-level
  `output` → `ErrNoOutput`.

```tengo
text := import("text")
output := {
    merchant: text.to_upper(input.merchant),
    amount_minor: input.amount_minor * 2
}
```

### Sandbox (every run)

- **Wall-clock timeout** (`Options.Timeout`, default 250 ms) via `RunContext` —
  aborts infinite loops.
- **Allocation cap** (`MaxAllocs`, default 1,000,000) and **constant-object cap**
  (`MaxConstObjects`) — guard runaway memory.
- **Source-size cap** (`MaxSourceBytes`, default 64 KiB) — rejected before
  compile.
- **Module allowlist** (`Options.Modules`). `os` and `exec` are **always
  stripped**, so filesystem, environment, and process access can never be
  granted — even if a caller passes them. Default allowlist is pure/side-effect
  free: `math, text, times, fmt, json, enum, base64, hex`.
- **Output depth limit** (64) — also defuses a self-referential Tengo map/array.

### Correctness notes baked in

- **Minor units stay int64.** Input JSON is decoded with `json.Number`; integral
  numbers become `tengo.Int` (not `float64`), and `tengo.Int` marshals back as an
  integer. Verified against `2^53 + 1`, which a float64 path would corrupt.
- **Determinism caveat.** `times.now()` (and `rand`, which is not in the default
  allowlist) are nondeterministic. Scripts whose output feeds a replayable
  pipeline (reconciliation) should take any clock from `input` instead. Documented
  in `DefaultOptions`.
- **Output is never trusted.** `Run` returns raw JSON; the consumer must strictly
  decode and validate it — exactly as model/regex-rule output is treated today.

## Intended integration (not built yet)

### 1. Storage — `private.script_definitions` (future migration)

Append-only versions, one active version per key, no browser grants:

```
script_key   text        -- e.g. 'transaction_post_process'
version      int         -- append-only, per key
source       text        -- Tengo source
checksum     text        -- sha256 of source
is_active    boolean     -- exactly one active per key (partial unique index)
notes        text
created_at   timestamptz
primary key (script_key, version)
```

Managed by the service role and seeded via the Supabase CLI. Rollback = flip
`is_active` to a prior version. A future `backend/internal/scriptstore` package
would own this SQL (mirroring the `*store` convention).

### 2. Consumer — parser-rule post-processing (future)

The seam is [`transactionworker/handler.go`](../backend/internal/transactionworker/handler.go)
where `applyDeterministicRule` runs after strict decode and before
re-validation. A future adapter would:

1. Marshal the `reconciliation.Candidate` → JSON.
2. `engine.Run(ctx, activeSource, candidateJSON)`.
3. **Strictly** decode the result back into a `Candidate`.
4. Record `script:<key>:v<n>` field provenance (paralleling today's
   `rule:<id>:v<n>`).
5. Re-run the existing `ValidateParsedResponseAfterRule` + `ValidateCandidate`.

Ownership (`UserID`), `Confidence`, and `AutoEligible` remain server-derived and
are **not** writable by a script.

Wiring: `cmd/api/main.go` and `cmd/worker/main.go` build one `Engine` (cheap,
stateless) and thread it — plus the `scriptstore` — into the handlers that need
it, like the other collaborators.

## Testing

`go test ./internal/scriptengine/` — 17 tests: echo, field transform, **int64
round-trip (2^53+1)**, float preservation, empty input, missing/undefined output,
source-too-large, **timeout aborts infinite loop**, **alloc cap aborts runaway**,
**`os` never importable** (even when allow-listed), allowed-module import, invalid
input JSON, compile error, array output, output-depth limit, and concurrent use.

## Open items for when a consumer is built

- Whether the post-process script eventually **replaces** the regex `parserrules`
  layer or stays additive (ties into design-review #5 — the rule layering is
  already flagged as over-built for one user).
- A feature flag to keep the consumer inert until a real script is seeded.
- pgTAP coverage for `private.script_definitions` (no browser grants, one active
  version per key).
