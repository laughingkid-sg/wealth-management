# Accounts technical implementation

## Boundary

Accounts owns a user-owned account directory. It does not calculate balances, currencies, positions, opening records, valuations, aggregate views, or market data, and it has no Accounts-specific Go API endpoint. The separate Transactions feature references Accounts but does not change this directory-only responsibility.

## Frontend

`frontend/` is a strict TypeScript React and Vite SPA. It uses `@supabase/supabase-js` for email/password Auth and direct Data REST API access to `public.accounts`.

- The application restores the Supabase session at startup and listens for auth-state changes.
- Unauthenticated users see sign-in; no registration UI is rendered.
- The Accounts page queries, inserts, and updates `accounts` directly.
- Account rows are grouped by side. The complete header surface—icon, text, and whitespace—toggles expansion, while header action buttons remain independent and the expanded detail region does not toggle the row.
- The Account Add/Edit popup handles Escape as a close action only while no save is in progress.
- The visible sort options are name A–Z and Z–A. `sort_order` remains an internal stable database value and is not editable in the UI.
- Other sidebar features are outside the Accounts boundary; Transactions is implemented and documented separately.

Accounts uses `VITE_SUPABASE_URL` and `VITE_SUPABASE_PUBLISHABLE_KEY`. The SPA additionally accepts optional `VITE_API_BASE_URL` for Transactions; see the [Transactions technical implementation](../transactions/technical.md). Do not expose a secret or service-role key in any frontend variable.

## Supabase data and RLS

`public.accounts` is the only table owned by the Accounts feature. [Transactions](../transactions/README.md) owns its own public/private tables and references an active, same-user Account from each canonical transaction; internal transfers use two distinct Accounts and remain a Transactions concern. Their evidence junction ordinarily links one source to one transaction, with the deliberate exception that the same source may support both legs of one internal-transfer pair.

| Column | Notes |
| --- | --- |
| `id`, `user_id` | UUID primary key and required `auth.users(id)` owner. |
| `side`, `account_type` | Constrained Asset/Liability and compatible fixed type. Assets additionally support Robo Advisors (`robo_advisor`), Retirement Account (`retirement_account`), and Others (`other`); liabilities additionally support Others through the same `other` identifier. Existing asset types remain Bank Account, Brokerage, Digital Wallet, Crypto Wallet, Crypto Exchange, and RSU; existing liability types remain Credit Card and Personal Loan. |
| `name`, `institution_name` | Required bounded text. |
| `account_identifier`, `notes` | Optional bounded text. |
| `metadata` | JSON object rendered as safe text. |
| `sort_order` | Internal stable ordering value. |
| `deleted_at` | Soft-delete marker. |
| `created_at`, `updated_at` | Server timestamps. |

RLS is enabled. Authenticated users have only `SELECT`, `INSERT`, and `UPDATE` grants, with ownership policies based on `(select auth.uid()) = user_id`. Updates use both `USING` and `WITH CHECK`. There is no delete policy or client-side permanent delete.

Migrations are stored in `supabase/migrations/`; RLS checks are in `supabase/tests/accounts_rls.test.sql`. Apply changes to the configured remote project through the Supabase MCP workflow, never by running a local Supabase stack for this project.

## Verification

For future changes, run `npm run build` and `npm run lint` in `frontend/`, exercise email/password sign-in and Account CRUD against the configured hosted project, and rerun the RLS/grant tests and Supabase advisors after schema changes.

At this release checkpoint, frontend lint/build and the relevant automated RLS/owner coverage pass. The signed-out application was exercised at desktop and mobile sizes, including accessibility controls, with no console warnings. Authenticated Account CRUD was not manually rerun because neither available browser had an application session and no login credentials were supplied. The hosted project also contains only one user, so live second-user isolation could not be attempted; ownership remains covered by the RLS/owner test suite. These two environment-limited checks are not claimed as live passes.
