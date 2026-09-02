# Wealth Builder

Wealth Builder is a private personal-finance product being built incrementally. Its first completed slice is **Accounts**: an authenticated, user-owned directory of financial accounts. It intentionally does not calculate or store financial values yet.

## Current product scope

### Accounts

Signed-in users can maintain their own account directory with:

- Fixed account types: Bank Account, Brokerage, Digital Wallet, Crypto Wallet, Crypto Exchange, RSU, Credit Card, and Personal Loan.
- Required account name and institution/platform; optional account identification, notes, and custom metadata.
- A searchable, filterable, alphabetically sortable list grouped by Assets and Liabilities.
- Expandable rows that reveal safe custom metadata.
- Create, edit, soft-delete, and restore actions.
- A responsive application shell with sidebar navigation, top bar, and footer. Only Accounts is functional; the remaining navigation entries are placeholders for later product work.

The present release deliberately excludes balances, quantities, currency conversion, opening balances, positions, transactions, totals, charts, market data, financial integrations, and a Go API.

## Architecture

```text
frontend/   React + TypeScript + Vite single-page app
supabase/   Supabase configuration, migrations, and RLS tests
docs/       Product requirements and technical implementation notes
```

The browser app uses `@supabase/supabase-js` directly against Supabase Auth and the Data API. Straightforward account CRUD is protected by row-level security; a Go API is reserved for later complex workflows.

## Data and security

`public.accounts` is the only application table at this stage. Each row belongs to one authenticated user. Row-level security allows users to select, insert, and update only their own accounts; deletion is implemented by updating `deleted_at` rather than removing the row.

Frontend configuration requires only these public values:

```text
VITE_SUPABASE_URL=
VITE_SUPABASE_PUBLISHABLE_KEY=
```

Never put a Supabase secret or service-role key in the frontend.

## Run locally

The frontend connects to the configured remote Supabase project. It does not require a local Supabase stack.

```bash
cd frontend
cp .env.example .env.local
# Set the remote URL and publishable key in .env.local
npm install
npm run dev
```

For a production check:

```bash
cd frontend
npm run build
npm run lint
```

## Documentation

- [Documentation index](docs/README.md)
- [Product overview](docs/product/overview.md)
- [Accounts requirements](docs/features/accounts/README.md)
- [Accounts technical implementation](docs/features/accounts/technical.md)

Future features will be added progressively after their product and technical scope is agreed.
