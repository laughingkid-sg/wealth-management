# 04 — Frontend (React SPA)

Location: `frontend/`. React **19** + TypeScript, built with **Vite 8**. Linting is
**oxlint**. There is **no router library** and **no global state library** — both
are deliberate (see `AGENTS.md`).

## Tooling & scripts

```jsonc
// package.json scripts
"dev":     "vite",              // dev server on :5173 (host-mapped to :8085 in Compose)
"build":   "tsc -b && vite build",
"lint":    "oxlint",
"preview": "vite preview"
```

Key dependencies: `react`, `react-dom`, `@supabase/supabase-js`, `lucide-react`
(icons). TypeScript is used **strictly** — avoid `any`; model API/external data
explicitly. shadcn/ui is referenced in the working agreement but the current code
uses hand-written components + CSS files per feature.

## Directory layout

```text
frontend/src/
├── main.tsx                 React root
├── App.tsx                  Shell: auth gate, sidebar nav, Accounts page, page router
├── App.css / index.css      Global styles
├── lib/
│   ├── supabase.ts          createClient<Database>() from VITE_ env; isSupabaseConfigured
│   └── database.types.ts    Generated Supabase types (Database)
└── features/
    ├── accounts/            model / validation / interactions (Accounts UI lives in App.tsx)
    ├── transactions/        TransactionsPage + dialogs + settings + prompt preview + api.ts
    ├── bulk-import/         BulkImportPage + api.ts + model.ts
    └── account-balances/    AccountFinanceDetailPage + CreditCardPage + api.ts + viewModel.ts
```

Each feature folder keeps its **models/view-models** and its **`api.ts`** client
next to its components, plus a `.css` file. This is the module boundary: work on a
feature by staying inside its folder plus shared `lib/`.

## The app shell (`App.tsx`)

`App.tsx` is large and does several jobs:

- **Auth gate.** On mount it calls `supabase.auth.getSession()` and subscribes to
  `onAuthStateChange`. If not configured → setup screen; if no session →
  `LoginPage` (email/password only, no registration); otherwise the workspace.
- **Navigation.** There is no route library. A `WorkspacePage` union type
  (`accounts | transactions | bulk-import | credit-card | transaction-settings |
  transaction-global-settings | transaction-prompt-preview`) is driven by the
  `?page=` query param via `history.pushState` + `popstate`. `?gmail=` forces the
  transactions page (used by the OAuth return).
- **Accounts UI** is implemented directly in `App.tsx` (`AccountsPage`,
  `AccountForm`). It reads/writes the `accounts` table **directly through
  `supabase-js`** (RLS-guarded): list/order, insert, update, and soft-delete via
  `deleted_at`.
- **Lazy loading.** All other pages are `React.lazy` + `Suspense` code-split
  chunks (transactions, bulk import, credit card, settings, prompt preview,
  account finance detail).

### Sidebar map (what's real vs "Soon")

| Group | Item | State |
| --- | --- | --- |
| Primary | Dashboard | Disabled ("Soon") |
| Primary | **Accounts** | Active |
| Transactions | **Transactions** | Active |
| Transactions | **Credit Card** | Active |
| Transactions | **Bulk Import** | Active (backed by the flag-gated API) |
| Transactions | **Prompt Preview** | Active |
| Transactions | **Global Settings** | Active |
| Transactions | **Settings** | Active |
| Primary | Investments / Goals | Disabled ("Soon") |
| Bottom | AI assistant / Help | Disabled |

## Talking to the backend

Two channels, both authenticated with the Supabase session:

1. **Direct Supabase** (`lib/supabase.ts`): `supabase.from('accounts')...`,
   `supabase.auth...`, `transaction_categories` reads, Realtime subscriptions, and
   the narrow confirmed-manual `transactions` insert. RLS does the authorization.

2. **Go API** via a small `fetch` wrapper in each feature's `api.ts`:
   ```ts
   const apiBaseUrl = (import.meta.env.VITE_API_BASE_URL ?? "/api").replace(/\/$/, "");
   // every request sends:
   headers: { Authorization: `Bearer ${session.access_token}`, Accept: "application/json" }
   ```
   In Docker, `VITE_API_BASE_URL=/api`; the Vite dev server proxies `/api/*` to the
   API service (`API_PROXY_TARGET=http://api:8080`, prefix stripped). So browser
   requests stay **same-origin** and there is no cross-origin API dependency in
   normal use. See `frontend/vite.config.ts`.

## Environment (frontend)

`frontend/.env.local` (git-ignored) must contain **only public** values:

```dotenv
VITE_SUPABASE_URL=https://<project-ref>.supabase.co
VITE_SUPABASE_PUBLISHABLE_KEY=sb_publishable_...
```

Compose injects the non-secret routing values `VITE_API_BASE_URL=/api` and
`API_PROXY_TARGET=http://api:8080`. **Never** put the service-role key, DB URL,
Google secret, encryption key, or provider key in any `VITE_*` variable — they
would ship to the browser.

## UI conventions

- Small, focused, accessible components; semantic HTML; visible loading / empty /
  error states on every data-backed screen (see `AccountsPage` for the pattern:
  skeleton rows, empty state, error notice with retry).
- Responsive layouts (there is a mobile nav with focus trapping in `App.tsx`).
- Money is entered/displayed in **major units** but stored as **minor units**
  (`*_amount_minor` are `bigint`). Keep conversion at the edges.
- Types that mirror the DB live in `lib/database.types.ts` (regenerate with the
  Supabase CLI when the schema changes — see [05 — Database](05-database.md)).
