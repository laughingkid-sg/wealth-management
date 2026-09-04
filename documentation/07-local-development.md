# 07 — Local Development

The dev stack runs the **three application processes in Docker Compose** while
pointing at the **hosted** Supabase project. There is intentionally **no local
Supabase** — do not run `supabase start` for normal work.

## Prerequisites

- Docker Desktop (or any Docker with Compose v2).
- The hosted Supabase dev project with migrations applied and a provisioned
  email/password user. (The CLI is already linked to `wealth-management`.)
- Google OAuth credentials with the redirect URI
  `http://localhost:8086/v1/transactions/gmail/oauth/callback` registered **exactly**.
- An Alibaba Cloud Token Plan API key for the parser.
- Go 1.23 and Node (only if you want to run checks outside Docker).

## Environment files (git-ignored)

Create them from the templates if missing:

```bash
cp -n backend/.env.example .env
cp -n frontend/.env.example frontend/.env.local
```

- **Root `.env`** — server-only. Loaded at container runtime by `api` and `worker`.
  Fill in the hosted Supabase pooler URL (`:6543`, `sslmode=require`), service-role
  key, Google client id/secret, base64 32-byte encryption key, and Alibaba key.
  See [03 — Backend](03-backend.md#configuration-contract) for the full contract.
- **`frontend/.env.local`** — public only:
  ```dotenv
  VITE_SUPABASE_URL=https://<project-ref>.supabase.co
  VITE_SUPABASE_PUBLISHABLE_KEY=sb_publishable_...
  ```

> **Never** put `SUPABASE_SERVICE_ROLE_KEY`, `SUPABASE_DB_URL`, the Google secret,
> the encryption key, or the provider key into `frontend/.env.local` or any
> `VITE_*` variable — those ship to the browser.

Compose injects safe, non-secret overrides itself (from `compose.yaml`):

```dotenv
# backend (api + worker)
API_ADDRESS=:8080
FRONTEND_ORIGIN=http://localhost:8085
GOOGLE_OAUTH_REDIRECT_URL=http://localhost:8086/v1/transactions/gmail/oauth/callback
# frontend
VITE_API_BASE_URL=/api
API_PROXY_TARGET=http://api:8080
CHOKIDAR_USEPOLLING=true
```

## Services & ports

| Service | Container process | Internal port | Host access |
| --- | --- | --- | --- |
| `frontend` | Vite dev server + `/api` proxy | 5173 | `http://localhost:8085` |
| `api` | Go HTTP API (Air hot reload) | 8080 | `http://localhost:8086` |
| `worker` | Go worker (Air hot reload) | — | none (background only) |
| Supabase | hosted | — | project URLs in `.env` files |

Host ports bind to `127.0.0.1` only. The frontend `depends_on` the API being
healthy. Both backend services use `restart: unless-stopped`. Source is
bind-mounted; named volumes cache `node_modules` and the Go module/build caches.

The browser calls stay same-origin on `:8085/api/*`; Vite strips `/api` and proxies
to `http://api:8080` over the private Compose network.

The `api`/`worker` and `frontend` dev images are **Alpine-based** to stay small (the
backend compiles Air in a separate build stage; the backend image ships the
attachment-rendering tools `imagemagick`+`imagemagick-heic` and `poppler-utils`). See
[03 — Backend](03-backend.md#system-dependencies-dev-image).

> **Switching an existing checkout from the older Debian images:** run
> `docker compose down --volumes` **once** before rebuilding, so glibc-built frontend
> packages and Go cache entries aren't reused under Alpine's musl. Ordinary `down`/`up`
> afterward preserves the Alpine-compatible caches.

## Everyday commands

```bash
docker compose up -d --build            # build + start everything
docker compose ps                       # health/status (expect frontend/api/worker healthy)
docker compose logs -f frontend api worker   # tail logs (or one service)
docker compose restart api worker       # restart without rebuilding
docker compose down                     # stop + remove containers and network
docker compose down --volumes           # also clear the dev caches (NOT hosted data)
```

Rebuild (`--build`) only when a dependency manifest, Dockerfile, or hot-reload
config changes; otherwise hot reload picks up source edits.

Health checks from the host:

```bash
curl --fail http://localhost:8086/healthz   # API → 204
open http://localhost:8085                    # the app
```

## Optional checks (outside Docker)

```bash
cd backend
go build ./cmd/api ./cmd/worker
go vet ./...
go test ./...

cd ../frontend
npm install
npm run lint
npm run build
```

## Supabase / database workflow

The database is the hosted project. See
[05 — Database & Storage](05-database.md#working-with-the-database-supabase-cli) for
the CLI commands (dump, diff, migration list, pgTAP tests, type generation).

## Troubleshooting

| Symptom | Check |
| --- | --- |
| `docker compose ps` shows a service unhealthy | Tail its log; the worker healthcheck just verifies its binary is running (it has no port). |
| Source edits not reflected | Inspect the service log, then `restart` it; rebuild only on manifest/Dockerfile changes. |
| Gmail `redirect_uri_mismatch` | The Google Cloud client must contain **exactly** `http://localhost:8086/v1/transactions/gmail/oauth/callback` (port 8086, this path). |
| API/worker won't start | Its log names the missing/invalid env var (config fails fast). Don't print secret values. |
| Queued work never advances | Confirm `worker` is healthy; `docker compose logs -f worker`. |
| CLI can't reach `db.<ref>...:5432` | Expected — this host has no IPv6; the CLI auto-falls back to the IPv4 pooler. |
