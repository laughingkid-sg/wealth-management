# Accounts technical implementation

## Boundary

This feature provides a user-owned account directory. It has no balances, currencies, positions, opening records, transactions, valuations, aggregate views, market data, or Go API endpoint.

## Frontend

`frontend/` is a strict TypeScript React and Vite SPA. It uses `@supabase/supabase-js` for email/password Auth and direct Data REST API access to `public.accounts`.

- The application restores the Supabase session at startup and listens for auth-state changes.
- Unauthenticated users see sign-in; no registration UI is rendered.
- The Accounts page queries, inserts, and updates `accounts` directly.
- Account rows are grouped by side and can expand to display metadata as safe text.
- The visible sort options are name A–Z and Z–A. `sort_order` remains an internal stable database value and is not editable in the UI.
- Sidebar entries beyond Accounts are visual placeholders only.

The frontend uses only `VITE_SUPABASE_URL` and `VITE_SUPABASE_PUBLISHABLE_KEY`. Do not expose a secret or service-role key.

## Supabase data and RLS

`public.accounts` is the only application table.

| Column | Notes |
| --- | --- |
| `id`, `user_id` | UUID primary key and required `auth.users(id)` owner. |
| `side`, `account_type` | Constrained Asset/Liability and compatible fixed type. Assets are Bank Account, Brokerage, Digital Wallet, Crypto Wallet, Crypto Exchange, and RSU; liabilities are Credit Card and Personal Loan. |
| `name`, `institution_name` | Required bounded text. |
| `account_identifier`, `notes` | Optional bounded text. |
| `metadata` | JSON object rendered as safe text. |
| `sort_order` | Internal stable ordering value. |
| `deleted_at` | Soft-delete marker. |
| `created_at`, `updated_at` | Server timestamps. |

RLS is enabled. Authenticated users have only `SELECT`, `INSERT`, and `UPDATE` grants, with ownership policies based on `(select auth.uid()) = user_id`. Updates use both `USING` and `WITH CHECK`. There is no delete policy or client-side permanent delete.

Migrations are stored in `supabase/migrations/`; RLS checks are in `supabase/tests/accounts_rls.test.sql`. Apply changes to the configured remote project through the Supabase MCP workflow, never by running a local Supabase stack for this project.

## Verification

- Run `npm run build` and `npm run lint` in `frontend/`.
- Test email/password sign-in against the configured remote project.
- Verify create, edit, metadata expansion, soft-delete, and restore for the signed-in user.
- Check RLS grants, policies, and Supabase security advisors after schema changes.
