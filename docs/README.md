# Documentation index

Documentation is organised by scope so a feature change does not require reading the entire product history.

## Read the smallest relevant layer

| Situation | Read |
| --- | --- |
| Product-wide direction, stack boundaries, or principles | [Product overview](product/overview.md) |
| A change to Accounts behaviour or scope | [Accounts requirements](features/accounts/README.md) |
| Accounts frontend, Supabase, data, RLS, or verification work | [Accounts technical implementation](features/accounts/technical.md) |

## Maintenance rule

When requirements change, update the affected feature's requirements document and its technical document when implementation, data, API, or verification details change. Update the product overview only for genuine cross-feature decisions.

New features belong in `docs/features/<feature>/`. Give each feature its own `README.md` and `technical.md` before implementation begins.
