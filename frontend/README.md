# Wealth Builder frontend

The frontend is a React, TypeScript, and Vite SPA. It uses the configured hosted Supabase project for the user session, RLS-protected Accounts CRUD, safe transaction reference reads, Realtime sync progress, and the narrowly constrained manual-transaction insert. Gmail, source evidence, attachments, parser configuration, canonical transaction edits, and other privileged or multi-row Transaction workflows go through the Go API.

## Configure and run

```bash
cp .env.example .env.local
# Set VITE_SUPABASE_URL and VITE_SUPABASE_PUBLISHABLE_KEY
npm install
npm run dev
```

Never place a Supabase secret or service-role key in `.env.local`.

## Checks

```bash
npm run build
npm run lint
```

Read the [Accounts feature documentation](../docs/features/accounts/README.md) before changing Accounts behaviour, and its [technical implementation](../docs/features/accounts/technical.md) before changing Supabase or frontend data access. For the Transactions workspace, read the [requirements](../docs/features/transactions/README.md) and [technical implementation](../docs/features/transactions/technical.md) before changing behaviour, API integration, or data access.
