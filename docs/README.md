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

## Feature status

| Feature | Status |
| --- | --- |
| Accounts | Delivered as the account directory used by other finance features; current automated and signed-out-browser verification is recorded in its technical document. |
| Transactions | Implemented and release-verified on `codex/feat-transaction`; hosted migration, live provider/Storage, replay-idempotency, database, Go, and frontend checks pass. Two environment-limited manual checks are explicitly recorded in the Transactions technical document. |

## Maintenance rule

When requirements change, update the affected feature's requirements document and its technical document when implementation, data, API, or verification details change. Update the product overview only for genuine cross-feature decisions.

New features belong in `docs/features/<feature>/`. Give each feature its own `README.md` and `technical.md`, then keep both current through implementation and verification.
