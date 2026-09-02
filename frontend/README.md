# Wealth Builder frontend

The frontend is a React, TypeScript, and Vite SPA. It connects directly to the configured hosted Supabase project for the user session and simple RLS-protected Accounts CRUD.

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

Read the [Accounts feature documentation](../docs/features/accounts/README.md) before changing Accounts behaviour, and its [technical implementation](../docs/features/accounts/technical.md) before changing Supabase or frontend data access.
