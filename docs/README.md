# Documentation index

Documentation is organised by scope so a feature change does not require reading the entire product history.

## Read the smallest relevant layer

| Situation | Read |
| --- | --- |
| Product-wide direction, stack boundaries, or principles | [Product overview](product/overview.md) |
| A change to Accounts behaviour or scope | [Accounts requirements](features/accounts/README.md) |
| Accounts frontend, Supabase, data, RLS, or verification work | [Accounts technical implementation](features/accounts/technical.md) |
| A change to Transactions behaviour or scope | [Transactions requirements](features/transactions/README.md) |
| Transactions ingestion, parsing, matching, data, API, or verification work | [Transactions technical implementation](features/transactions/technical.md) |
| A change to uploaded-document transaction extraction or batch processing | [Bulk Insert requirements](features/bulk-insert/README.md) |
| Bulk Insert storage, parsing, jobs, reconciliation, API, or UI implementation | [Bulk Insert technical design](features/bulk-insert/technical.md) |
| A change to Account balances, spending treatments, or Credit Card bills | [Credit Card requirements](features/account-balances/README.md) |
| Credit Card balance, bill workflow, data, API, or verification work | [Credit Card technical design](features/account-balances/technical.md) |

## Feature status

| Feature | Status |
| --- | --- |
| Accounts | Delivered as the account directory used by other finance features; current automated and signed-out-browser verification is recorded in its technical document. |
| Transactions | Delivered. Gmail/manual transaction flows remain implemented, and the shared evidence model now also supports uploaded Bulk Import documents. |
| Bulk Insert | Delivered end to end in the React SPA, authenticated Go API, asynchronous Go worker, hosted Postgres, private Storage, and live LLM integration. An isolated hosted synthetic run verified signed upload integrity, one created candidate, evidence/Debug access, and complete fixture cleanup. |
| Credit Card | Delivered with Account opening balances, spending treatments, Bulk-generated Credit Card bills, reconciliation, payment detection, and payoff workflows. |

## Maintenance rule

When requirements change, update the affected feature's requirements document and its technical document when implementation, data, API, or verification details change. Update the product overview only for genuine cross-feature decisions.

New features belong in `docs/features/<feature>/`. Give each feature its own `README.md` and `technical.md`, then keep both current through implementation and verification.
