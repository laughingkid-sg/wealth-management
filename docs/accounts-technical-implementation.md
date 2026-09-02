# Accounts — Technical Implementation

## Scope boundary

This implementation creates a user-owned account directory only. The account table stores identity and descriptive information; it stores no balance, asset code, currency, position, opening balance, transaction, or valuation.

Do not create `account_positions`, transaction tables, balance RPCs, market-data adapters, net-worth views, aggregate tables, charts, or a Go API in this slice.

## Repository layout

```text
/frontend
  /src
    /app                 # routing and protected-route boundary
    /components/ui       # locally generated shadcn/ui primitives
    /features/auth       # login form, session hook, logout
    /features/accounts   # list, form, filters, soft delete/restore
    /lib                 # Supabase client, formatting, validation
    /types               # database/domain types
  .env.example
  package.json

/supabase
  config.toml
  /migrations
    <timestamp>_create_accounts.sql
  /tests
    accounts_rls.sql
```

## Authentication

- Enable Supabase email/password sign-in.
- Provision the initial user through Supabase Auth; omit public registration UI.
- Frontend environment variables are `VITE_SUPABASE_URL` and `VITE_SUPABASE_PUBLISHABLE_KEY` only. Never expose a `service_role` or secret key.
- On application start, load the session and subscribe to auth-state changes.
- A protected route allows `/accounts` only with a valid session. Login calls `signInWithPassword`; logout calls `signOut`.

## Data model

`public.accounts` is the only application table for this feature.

| Column | Type | Notes |
| --- | --- | --- |
| `id` | `uuid` | Primary key, generated server-side. |
| `user_id` | `uuid` | Required FK to `auth.users(id)`, indexed. |
| `side` | `text` | `asset` or `liability`. |
| `account_type` | `text` | Fixed set, compatible with `side`: `bank_account`, `brokerage`, `crypto_wallet`, `crypto_exchange`, `rsu`, `credit_card`, `personal_loan`. |
| `name` | `text` | Required user-facing name, 1–100 characters. |
| `institution_name` | `text` | Required provider/platform, 1–100 characters. |
| `account_identifier` | `text` | Optional user-safe reference, maximum 100 characters. |
| `notes` | `text` | Optional plain text, maximum 500 characters. |
| `metadata` | `jsonb` | Optional user-defined JSON object; safe text rendering only. |
| `sort_order` | `integer` | Internal stable ordering value; it is not exposed in the current UI. |
| `deleted_at` | `timestamptz` | Null for active records; implements soft deletion. |
| `created_at`, `updated_at` | `timestamptz` | Server-set timestamps. |

The migration adds checks for non-blank name and institution, lengths, a JSON-object metadata value, and valid side/type combinations. Account type remains constrained text rather than a PostgreSQL enum so an approved future migration can extend the fixed list.

Required indexes:

```sql
create index accounts_user_active_sort_idx
  on public.accounts (user_id, deleted_at, sort_order);

create index accounts_user_side_idx
  on public.accounts (user_id, side)
  where deleted_at is null;
```

## RLS

Enable RLS on `public.accounts`. Every policy targets `authenticated` and combines the role with account ownership. The update policy must include both `USING` and `WITH CHECK`, preventing a caller from changing `user_id`.

```sql
alter table public.accounts enable row level security;

create policy "Users select their own accounts"
on public.accounts for select to authenticated
using ((select auth.uid()) = user_id);

create policy "Users insert their own accounts"
on public.accounts for insert to authenticated
with check ((select auth.uid()) = user_id);

create policy "Users update their own accounts"
on public.accounts for update to authenticated
using ((select auth.uid()) = user_id)
with check ((select auth.uid()) = user_id);
```

The frontend sets `user_id` from the active session when inserting. RLS remains the authority: a user cannot use a crafted request to choose another user's ID. Use `(select auth.uid())` in policies so the function is evaluated once per query rather than per row.

## Frontend

- Scaffold `/frontend` as a strict TypeScript React app using shadcn/ui.
- Use `@supabase/supabase-js` for Auth and Data API calls; it is a client over the Supabase REST API, not a custom CRUD server.
- Generate database TypeScript types after the migration; avoid `any`.
- `AccountsPage`: protected query with loading, empty, error, and no-results states.
- `AccountToolbar`: search, sort, filters, and Add account action. Do not implement Refresh all.
- `AccountFormDialog`: fixed account-type selection, required institution, optional identifier/notes, and custom JSON metadata key/value editor.
- `AccountRow`: name, type, institution/platform, edit/soft-delete/restore actions, and an expandable safe-metadata panel—no monetary display.

All account CRUD requests are direct `accounts` REST requests. The create/edit form never creates a balance or financial record.

## Verification

- Apply the migration to the configured remote Supabase project and generate database types.
- Create two test users and an account for each.
- Assert a user can create, read, update, soft-delete, and restore only their own account.
- Assert cross-user select, insert, update, and delete attempts fail or return no rows, including attempts to reassign `user_id`.
- Unit-test form validation, side/type compatibility, metadata validation, sorting, filtering, and soft-delete behaviour.
- Component-test login, protected routing, empty/error states, and account CRUD interactions.
- Run typecheck, lint, relevant tests, and Supabase database advisors before review.

## Delivery sequence

1. Scaffold `/frontend` and `/supabase`.
2. Configure Supabase Auth and provision the initial user.
3. Create and review the accounts migration, indexes, RLS policies, and RLS tests.
4. Build login and protected routing.
5. Build the account list, create/edit form, filters, sort, and soft delete/restore.
6. Verify RLS isolation and frontend behaviour.

Balances, positions, opening transactions, a transaction ledger, market-data quotes, valuation, net worth, and history charting require a later, separately scoped design.
