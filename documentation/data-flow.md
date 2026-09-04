# Data Flow Diagrams

Small, focused diagrams. Each one answers a single question. Start at #1 for the big
picture, then drill into the pipeline you care about.

---

## 1. Components — who talks to whom

```mermaid
flowchart LR
    User([User])
    SPA[React SPA<br/>frontend]
    API[Go API<br/>cmd/api]
    WK[Go Worker<br/>cmd/worker]
    subgraph SB[Hosted Supabase]
      AUTH[Auth]
      PG[(Postgres<br/>public + private)]
      ST[(Storage<br/>attachments)]
      RT[Realtime]
    end
    GM[Gmail API]
    LLM[LLM parser<br/>Qwen]

    User --> SPA
    SPA -->|publishable key + user token| AUTH
    SPA -->|RLS reads / narrow writes| PG
    SPA -->|subscribe progress| RT
    SPA -->|Bearer token, /api| API
    API -->|service role| PG
    API -->|sign URLs| ST
    API -->|OAuth| GM
    WK -->|claim jobs, service role| PG
    WK --> ST
    WK --> GM
    WK --> LLM
```

---

## 2. The two write paths

The browser writes **directly** to Supabase only for a tiny, RLS-guarded set;
everything else goes through the Go API.

```mermaid
flowchart TD
    B([Browser])

    B -->|Supabase client| D{Direct write?}
    D -->|accounts CRUD| PGa[(public.accounts<br/>RLS: owner)]
    D -->|confirmed MANUAL txn only| PGt[(public.transactions<br/>RLS WITH CHECK)]

    B -->|"Bearer token → /api"| API[Go API<br/>requireUser]
    API -->|edits, transfers, source actions| PGt
    API -->|credit card, balances, bulk, rules| PGx[(other tables<br/>public + private)]

    note1[["Everything privileged or<br/>multi-row = Go API"]]
    API -.-> note1
```

---

## 3. Gmail → transaction pipeline

The core flow. The HTTP request returns immediately; the worker does the slow work.

```mermaid
sequenceDiagram
    participant U as Browser
    participant A as Go API
    participant Q as Job queue<br/>(Postgres)
    participant W as Worker
    participant G as Gmail
    participant M as LLM

    U->>A: POST /gmail/sync-runs
    A->>Q: enqueue gmail_ingestion
    A-->>U: 202 (returns now)

    W->>Q: claim gmail_ingestion
    W->>G: fetch labelled messages
    G-->>W: emails + attachments
    W->>Q: save data_sources + enqueue source_parsing

    W->>Q: claim source_parsing
    W->>M: prompt (NO account catalogue)
    M-->>W: parsed candidate
    W->>Q: record audit + enqueue reconciliation

    W->>Q: claim reconciliation
    Note over W: match to account via typed keys + dedup
    W-->>U: progress via Realtime (create / Review / Dangling / Failed)
```

---

## 4. Durable job queue — how async work runs

No broker. Jobs live in Postgres; the worker claims them with a lease.

```mermaid
flowchart LR
    subgraph Producers
      API[Go API]
    end
    Q[(private.transaction_jobs)]
    API -->|enqueue| Q

    subgraph Worker loop
      C[claim one<br/>row lock + lease] --> H[run handler<br/>heartbeat lease]
      H -->|ok| DONE[mark complete]
      H -->|error| RETRY[leave for retry]
      H -->|lease lost| RETRY
      DONE --> P{more?}
      RETRY --> P
      P -->|yes| C
      P -->|no| SLEEP[sleep WORKER_POLL_SECONDS] --> C
    end
    Q --> C
```

---

## 5. Bulk Import pipeline (flag-gated)

Uploaded documents flow into the **same** evidence model as Gmail
(`data_sources`), through a 5-stage job chain.

```mermaid
flowchart LR
    T[Template] --> BA[Batch<br/>snapshot prompt + accounts]
    BA --> UP[Reserve + finalize<br/>signed uploads]
    UP --> SUB[Submit]
    SUB --> P1[prepare]
    P1 --> P2[chunk_parse]
    P2 --> P3[aggregate]
    P3 --> P4[reconcile]
    P4 --> P5[post_process<br/>e.g. credit-card bills]
    P5 --> CAND[Candidates]
    CAND -->|user resolves| TX[(transactions)]
```

---

## 6. Source lifecycle & the three queues

What happens to a piece of evidence (`data_sources.parse_status`), and how the
Review / Dangling / Failed queues arise. User actions come through the Go API.

```mermaid
stateDiagram-v2
    [*] --> pending: ingested
    pending --> parsing: worker claims source_parsing
    parsing --> parsed: matched → transaction
    parsing --> review_required: uncertain match  (Review queue)
    parsing --> dangling: no confident account  (Dangling queue)
    parsing --> failed: parse error  (Failed queue)
    review_required --> parsed: attach / create
    dangling --> parsed: attach / create
    failed --> parsing: retry
    parsed --> dangling: unmatch
    parsed --> [*]: delete → cleanup job
    review_required --> [*]: delete
    dangling --> [*]: delete
    failed --> [*]: delete
```

---

## 7. Credit-card bill reconciliation

A bill (`credit_card_statements.status`) generated from a bulk import, its line
resolution, and payment detection. Lines are `pending → linked | ignored`; a
payment candidate is `suggested → selected → confirmed`.

```mermaid
flowchart TD
    BULK[Bulk import<br/>post_process] --> REVIEW[Statement: review]

    REVIEW --> LINES{Reconcile each line}
    LINES -->|attach to txn| LINKED[line: linked]
    LINES -->|create transaction| LINKED
    LINES -->|ignore| IGN[line: ignored]

    REVIEW --> PC[Payment candidate<br/>suggested → select → confirm]

    LINKED --> UNPAID[Statement: unpaid]
    IGN --> UNPAID
    PC -->|payment matched| PAID[Statement: paid]
    UNPAID -->|payoff in full| PAID

    REVIEW -.->|void| VOID[Statement: void]
    UNPAID -.->|void| VOID
    REVIEW -.->|discard| DEL[(deleted)]
```

---

### Legend

- **RLS** = row-level security (owner-scoped, `auth.uid() = user_id`).
- **Job kinds** (queue): `gmail_ingestion`, `source_parsing`, `reconciliation`,
  `source_attachment_cleanup`, and the five `bulk_*` kinds.
- Diagram source: edit the Mermaid blocks above. See
  [02 — Architecture](02-architecture.md) for the narrative version.
