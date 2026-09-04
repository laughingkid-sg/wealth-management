# 11 — Deployment & CI/CD

> **Status:** implementation guide for the **first** production deployment. No
> production infrastructure exists yet. This supersedes the earlier
> `docs/deployment-plan.md` proposal — corrections from a code review are folded in
> (see the call-outs), and it is aligned to the current Alpine dev image.

Read [02 — Architecture](02-architecture.md) and [08 — Security](08-security.md)
first; this page assumes that model.

## 1. What gets deployed

| Component | Artifact | Notes |
| --- | --- | --- |
| **SPA** | Static Vite build (`frontend/dist/`) | React 19 + TS. Talks to Supabase directly and to the Go API via `/api/*`. |
| **Go API** | Binary `cmd/api` | The only process serving HTTP. Needs ingress. |
| **Go worker** | Binary `cmd/worker` | Background only, no ports. Drains the Postgres job queue; calls Gmail, the LLM, and Storage. |
| **Supabase** | Hosted **production** project | Postgres 17, Auth, Realtime, Storage bucket `transaction-attachments` (private). |

**Non-negotiable constraints** (enforced in `backend/internal/config`, fail-fast on
boot — see [03 — Backend](03-backend.md#configuration-contract)):

- DB access **only** via the Supabase **transaction pooler**, port **6543**,
  `sslmode=require`.
- Outside `APP_ENV=development`, `SUPABASE_URL`, `FRONTEND_ORIGIN`, and
  `GOOGLE_OAUTH_REDIRECT_URL` must be **https**.
- `GOOGLE_TEST_REFRESH_TOKEN` is **rejected** in production — never set it.
- `TRANSACTION_TOKEN_ENCRYPTION_KEY` = base64-encoded **32 bytes**.
- Secrets never appear in `VITE_*` variables (they ship to the browser).
- The **worker** needs `poppler-utils` + `imagemagick` (+ `imagemagick-heic`) at
  runtime: it rasterises PDF attachments and converts images (incl. HEIC) to
  model-safe PNGs before sending evidence to the LLM. On Linux the code calls
  ImageMagick `magick` (`transactionworker.imageConversionCommand` switches on
  `runtime.GOOS`).

## 2. Target topology (recommended: single VM)

A small VM (Hetzner/DigitalOcean/Linode; 2 vCPU / 4 GB is ample for a private
single-user app) running Docker Compose behind Caddy:

```text
Internet ──► Caddy (TLS :443)
              ├── /api/*  ──► Go API (:8080, internal only)   [proxied, prefix stripped]
              └── /*      ──► static SPA (dist/, SPA fallback) [everything else]
                 Go worker (no ingress)
                 └── outbound: Supabase pooler · Gmail API · Alibaba LLM · Storage
```

- Caddy terminates TLS, serves the static bundle, and proxies `/api/*` to the API.
  Same-origin means **no CORS**, and `VITE_API_BASE_URL=/api` works unchanged.
- The worker is a sibling container with no exposed port.
- The browser also needs ordinary outbound HTTPS/WSS to the Supabase project (Auth,
  data, Realtime) — nothing to host.

> A cross-origin split (Go services on Fly/Render, SPA on Cloudflare Pages/Netlify)
> is **not** drop-in — see [§12](#12-cross-origin-alternative-needs-code). The
> single-VM layout avoids that entirely and is cheaper for a private app.

## 3. Production images (gap: they don't exist yet)

Only `backend/Dockerfile.dev` exists (Alpine + Air, source-mounted, for local dev).
Add a production `backend/Dockerfile` — one image, two entrypoints, mirroring how
the dev stack reuses one image:

```dockerfile
# ---- build ----
FROM golang:1.23.12-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

# ---- runtime ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates curl imagemagick imagemagick-heic poppler-utils procps \
 && adduser -D -u 10001 app
COPY --from=build /out/api /usr/local/bin/api
COPY --from=build /out/worker /usr/local/bin/worker
USER app
ENTRYPOINT ["/usr/local/bin/api"]   # worker service overrides the command
```

- Same `apk` runtime set as the dev image (Alpine v7 ImageMagick provides `magick`;
  `imagemagick-heic` adds HEIC). `curl` for the API healthcheck, `procps` for the
  worker's `pgrep`.
- Non-root (`app`). The `api` container healthchecks `GET /healthz`; the `worker`
  container overrides the command to `/usr/local/bin/worker` and healthchecks
  `pgrep -x worker` (see [§9](#9-operations) for why that's weak).

The frontend is a **static build** (below) served by Caddy — no runtime image
needed. Build it with `node:22-alpine` (matches the dev frontend image) in CI.

### `compose.prod.yaml` (sketch)

```yaml
services:
  api:
    image: ghcr.io/<owner>/<repo>/backend:${IMAGE_TAG}
    command: ["/usr/local/bin/api"]
    env_file: [/srv/wealth-builder/api.env]   # server-only secrets, chmod 600, NOT in the image
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "curl", "-fsS", "http://localhost:8080/healthz"]
      interval: 10s
      timeout: 3s
      retries: 5
      start_period: 20s
    networks: [app]
  worker:
    image: ghcr.io/<owner>/<repo>/backend:${IMAGE_TAG}
    command: ["/usr/local/bin/worker"]
    env_file: [/srv/wealth-builder/api.env]
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "pgrep", "-x", "worker"]
      interval: 10s
      timeout: 3s
      retries: 5
    networks: [app]
  caddy:
    image: caddy:2
    ports: ["443:443", "80:80"]
    volumes:
      - /srv/wealth-builder/Caddyfile:/etc/caddy/Caddyfile:ro
      - /srv/wealth-builder/dist:/srv/dist:ro
      - caddy_data:/data
    depends_on: { api: { condition: service_healthy } }
    networks: [app]
networks: { app: {} }
volumes: { caddy_data: {} }
```

Runtime secrets live in `/srv/wealth-builder/api.env` **on the VM** (chmod 600),
never in the image and never in GitHub — CI only ships the image tag and the SPA.

### Caddyfile — note the OAuth callback

```text
<domain> {
    encode zstd gzip
    handle_path /api/* {
        reverse_proxy api:8080
    }
    handle {
        root * /srv/dist
        try_files {path} /index.html
        file_server
    }
    header {
        Strict-Transport-Security "max-age=31536000; includeSubDomains"
        # Optional CSP — must allow the Supabase origins the browser talks to:
        # Content-Security-Policy "default-src 'self'; connect-src 'self' https://<ref>.supabase.co wss://<ref>.supabase.co; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'"
    }
}
```

> 🔴 **OAuth callback must go through `/api`.** The Gmail callback is a *server*
> endpoint (`GET /v1/transactions/gmail/oauth/callback`). Google redirects the
> browser straight to `GOOGLE_OAUTH_REDIRECT_URL`; if that URL isn't proxied to the
> API it lands on the SPA fallback and Gmail connect silently fails. So set the
> redirect to **`https://<domain>/api/v1/transactions/gmail/oauth/callback`** (and
> register exactly that in Google Cloud). Caddy's `handle_path /api/*` strips `/api`,
> so the API still sees `/v1/...`. The API's `X-Frame-Options: DENY` and
> `X-Content-Type-Options: nosniff` are set in code; HSTS/CSP are added at the proxy.

## 4. Environment configuration

**Server-only** (`api.env` on the VM; injected into both containers):

| Variable | Value / notes |
| --- | --- |
| `APP_ENV` | `production` |
| `API_ADDRESS` | `:8080` |
| `SUPABASE_URL` | prod project URL (https) |
| `SUPABASE_DB_URL` | prod pooler URL, `:6543`, `sslmode=require`. **Add `pool_max_conns`** — see below. |
| `SUPABASE_SERVICE_ROLE_KEY` | prod service-role key |
| `GOOGLE_OAUTH_CLIENT_ID` / `_SECRET` | prod OAuth client |
| `GOOGLE_OAUTH_REDIRECT_URL` | `https://<domain>/api/v1/transactions/gmail/oauth/callback` |
| `TRANSACTION_TOKEN_ENCRYPTION_KEY` | base64 32-byte key |
| `ALIBABA_TOKEN_PLAN_API_KEY` (+ optional `_BASE_URL`, `_MODEL`) | LLM provider |
| `FRONTEND_ORIGIN` | `https://<domain>` |
| `GMAIL_SYNC_LABEL` | default `odin-finance` (or your label) |
| `GMAIL_INITIAL_BACKFILL_MAX_MESSAGES` | default `5` (allowed 1–100) |
| `WORKER_POLL_SECONDS`, `OUTBOUND_HTTP_TIMEOUT_SECONDS` | defaults fine (5 / 20) |
| `BULK_IMPORT_ENABLED` | `false` initially; flip to `true` only when wanted |
| Do **not** set | `GOOGLE_TEST_REFRESH_TOKEN` (boot fails in production if set) |

> 🟡 **Bound the pgx pool.** `api` and `worker` each open their own `pgxpool`
> against the shared transaction pooler, which has a client-connection cap. Cap each
> service by appending `&pool_max_conns=N` (e.g. `10`) to `SUPABASE_DB_URL` so the
> two processes (and any extra workers) stay under the pooler limit.

**Frontend** (baked into the build at compile time — never a secret):

```dotenv
VITE_SUPABASE_URL=https://<prod-ref>.supabase.co
VITE_SUPABASE_PUBLISHABLE_KEY=sb_publishable_...
VITE_API_BASE_URL=/api
```

Confirm no server secret ever lands in a `VITE_*` variable or in `dist/`.

## 5. Prerequisites (first deploy)

1. **Production Supabase project** — create it (region near the VM). Record: project
   URL, pooler string (`:6543`, `sslmode=require`), service-role key, publishable
   key. Set **Auth → Site URL** to the SPA URL and add it to redirect allow-list.
   Confirm SMTP for email auth, and that Realtime publishes `transaction_sync_runs`.
2. **Apply schema** (see [§7](#7-cicd-github-actions) for automating this):
   ```bash
   supabase link --project-ref <prod-ref>
   supabase db push --dry-run    # review first
   supabase db push              # applies supabase/migrations in order
   ```
   Then spot-check RLS on prod (RLS on for every browser-reachable table; the
   `private` schema has no browser grants). **Do not run the pgTAP suite against
   prod** — run `supabase test db` against a Supabase branch/staging project.
3. **Google OAuth client (production)** — redirect URI registered **exactly** as
   `https://<domain>/api/v1/transactions/gmail/oauth/callback` (note the `/api`). If
   the consent screen stays in "Testing", refresh tokens expire in 7 days — publish
   the app or accept periodic re-consent.
4. **Alibaba Cloud Token Plan API key** for the configured region endpoint.
5. **Domain + DNS** (`app.example.com`) pointed at the VM.
6. **Encryption key** — generate once, store in a password manager, back it up:
   ```bash
   openssl rand -base64 32
   ```
   ⚠️ Losing it makes stored Gmail refresh tokens unrecoverable (users must
   reconnect Gmail).

## 6. Build the SPA

```bash
cd frontend && npm ci && npm run lint && npm run build   # emits dist/
```

Serve `dist/` with SPA fallback (`try_files … /index.html`) and long-cache the
hashed assets. In production this is produced by CI (below), not on the VM.

## 7. CI/CD (GitHub Actions)

Two workflows. **CI** runs on every PR and on `main`; **Deploy** runs on `main`
(and `workflow_dispatch`) and ships to the VM.

### `.github/workflows/ci.yml`

```yaml
name: CI
on:
  pull_request:
  push: { branches: [main] }
jobs:
  backend:
    runs-on: ubuntu-latest
    defaults: { run: { working-directory: backend } }
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.23.12', cache-dependency-path: backend/go.sum }
      - run: go build ./cmd/api ./cmd/worker
      - run: go vet ./...
      - run: go test ./...   # DB integration tests self-skip unless TRANSACTIONSTORE_TEST_DB_URL is set
  frontend:
    runs-on: ubuntu-latest
    defaults: { run: { working-directory: frontend } }
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with: { node-version: '22', cache: npm, cache-dependency-path: frontend/package-lock.json }
      - run: npm ci
      - run: npm run lint
      - run: npm run build
        env:
          VITE_SUPABASE_URL: ${{ vars.VITE_SUPABASE_URL }}
          VITE_SUPABASE_PUBLISHABLE_KEY: ${{ vars.VITE_SUPABASE_PUBLISHABLE_KEY }}
          VITE_API_BASE_URL: /api
```

> The `transactionstore` integration tests **skip** when `TRANSACTIONSTORE_TEST_DB_URL`
> is unset, so `go test ./...` is green in CI with no database. For full coverage,
> add a `postgres:17` service, apply `supabase/migrations`, and set that env var.

### `.github/workflows/deploy.yml`

```yaml
name: Deploy
on:
  push: { branches: [main] }
  workflow_dispatch:
permissions: { contents: read, packages: write }
concurrency: { group: deploy, cancel-in-progress: false }
jobs:
  migrate:                       # DB first — API/worker assume the current schema
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: supabase/setup-cli@v1
      - env:
          SUPABASE_ACCESS_TOKEN: ${{ secrets.SUPABASE_ACCESS_TOKEN }}
          SUPABASE_DB_PASSWORD: ${{ secrets.SUPABASE_DB_PASSWORD }}
        run: |
          supabase link --project-ref ${{ vars.SUPABASE_PROJECT_REF }}
          supabase db push --dry-run
          supabase db push
  image:
    needs: migrate
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: docker/login-action@v3
        with: { registry: ghcr.io, username: ${{ github.actor }}, password: ${{ secrets.GITHUB_TOKEN }} }
      - uses: docker/build-push-action@v6
        with:
          context: ./backend
          file: ./backend/Dockerfile
          push: true
          tags: |
            ghcr.io/${{ github.repository }}/backend:${{ github.sha }}
            ghcr.io/${{ github.repository }}/backend:latest
  frontend:
    needs: migrate
    runs-on: ubuntu-latest
    defaults: { run: { working-directory: frontend } }
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with: { node-version: '22', cache: npm, cache-dependency-path: frontend/package-lock.json }
      - run: npm ci && npm run build
        env:
          VITE_SUPABASE_URL: ${{ vars.VITE_SUPABASE_URL }}
          VITE_SUPABASE_PUBLISHABLE_KEY: ${{ vars.VITE_SUPABASE_PUBLISHABLE_KEY }}
          VITE_API_BASE_URL: /api
      - uses: actions/upload-artifact@v4
        with: { name: dist, path: frontend/dist }
  ship:
    needs: [image, frontend]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/download-artifact@v4
        with: { name: dist, path: dist }
      - name: rsync SPA + roll services
        env:
          SSH_KEY: ${{ secrets.DEPLOY_SSH_KEY }}
          HOST: ${{ secrets.DEPLOY_HOST }}
          USER: ${{ secrets.DEPLOY_USER }}
        run: |
          install -m600 /dev/stdin key <<< "$SSH_KEY"
          SSH="ssh -i key -o StrictHostKeyChecking=accept-new"
          rsync -az --delete -e "$SSH" dist/ "$USER@$HOST:/srv/wealth-builder/dist/"
          $SSH "$USER@$HOST" \
            "cd /srv/wealth-builder && IMAGE_TAG=${{ github.sha }} docker compose pull && IMAGE_TAG=${{ github.sha }} docker compose up -d"
```

**Secrets / vars to configure in the repo:**

| Kind | Name | Purpose |
| --- | --- | --- |
| var | `VITE_SUPABASE_URL`, `VITE_SUPABASE_PUBLISHABLE_KEY`, `SUPABASE_PROJECT_REF` | public build/link values |
| secret | `SUPABASE_ACCESS_TOKEN`, `SUPABASE_DB_PASSWORD` | run migrations from CI |
| secret | `DEPLOY_SSH_KEY`, `DEPLOY_HOST`, `DEPLOY_USER` | ship to the VM |

Runtime server secrets (`SUPABASE_SERVICE_ROLE_KEY`, Google secret, encryption key,
LLM key) stay in `/srv/wealth-builder/api.env` **on the VM** — deliberately not in
GitHub. The image is tagged by commit SHA, so rollback = redeploy a prior tag.

## 8. Deployment order (and why)

1. **Migrations** first (the `migrate` job) — API and worker assume the current
   schema. Migrations today are additive; a future breaking change must be split
   into **expand → deploy → contract**.
2. **Worker** — safe to start before the API; it just polls the queue.
3. **API** — verify `GET /healthz` → `204`.
4. **SPA** — last; it's what users hit.

## 9. Post-deploy verification

- [ ] `https://<domain>/` loads the SPA; `https://<domain>/api/healthz` → `204`.
- [ ] Sign in via Supabase Auth works.
- [ ] Accounts CRUD works (browser direct-write path).
- [ ] Manual transaction insert works.
- [ ] Gmail connect: OAuth completes at the `/api/...` callback; encrypted token stored.
- [ ] Trigger a sync run: Realtime progress updates (polling `GET /sync-runs/latest`
      also works); ingest → parse → reconcile completes; Review queue renders evidence.
- [ ] Worker logs show a clean startup (config fails fast — clean boot = env contract passed).
- [ ] RLS spot-check: signed-in user cannot read another user's rows; `private` schema
      is unreachable from the browser.
- [ ] If enabled: bulk import upload → parse → candidates end-to-end.

## 10. Rollback

- **SPA:** immutable `dist/` per build; roll back by re-shipping a previous artifact
  (keep the last N on the VM, or re-run Deploy at an older SHA).
- **API/worker:** images tagged by SHA; `IMAGE_TAG=<old-sha> docker compose up -d`.
  Single-instance restarts take seconds.
- **Database:** migrations run before code. Schema rollback only via a **new forward
  migration** (never hand-edit the remote schema).
- **Secrets:** treat a suspected leak as an incident — rotate the service-role key,
  OAuth secret, LLM key, and (worst case) the encryption key (forces Gmail reconnects).

## 11. Operations

- **Backups:** enable Supabase PITR/daily backups on prod. Back up the **encryption
  key** out-of-band — it is not recoverable from Supabase.
- **Monitoring:** `restart: unless-stopped`; API `/healthz` and worker `pgrep`
  healthchecks; ship `docker logs` to a collector (Loki/Vector). 🟡 The worker
  `pgrep` check only proves the process exists, not that it's draining the queue —
  the real signal is a **business alert on `private.transaction_jobs` queue depth and
  stuck leases**. Treat that as required, not optional.
- **Log hygiene:** the app handles email + financial data. `config.Config` is never
  logged (enforced); keep job-failure logs free of PII/secret values.
- **Scaling:** not needed for a private app, but multiple workers are safe (atomic
  claim). If you scale out, keep total pooler connections under the cap (`pool_max_conns`).
- **Secrets rotation:** service-role key, DB pooler password, OAuth secret, and LLM
  key are plain env vars in `api.env` — rotate by editing it and
  `docker compose up -d` the two services.

## 12. Cross-origin alternative (needs code)

If you split hosting (Go services on Fly/Render, SPA on Pages/Netlify), the SPA's
`fetch` to the API becomes **cross-origin**. 🔴 The Go API has **no CORS
middleware** today — `FRONTEND_ORIGIN` is used only for the OAuth redirect target,
not for CORS. So this path is **not config-only**: it requires adding CORS handling
(allow-list `FRONTEND_ORIGIN`, handle `OPTIONS` preflight, allow the `Authorization`
header) in `backend/cmd/api`, plus setting `VITE_API_BASE_URL` to the API's absolute
URL. The single-VM, same-origin layout in [§2](#2-target-topology-recommended-single-vm)
avoids all of this and is recommended.

## 13. Open work items (repo work before first deploy)

1. Production `backend/Dockerfile` (multi-stage Alpine, one image / two entrypoints) — [§3](#3-production-images-gap-they-dont-exist-yet).
2. `compose.prod.yaml` + `Caddyfile` + `api.env` on the VM — [§3](#3-production-images-gap-they-dont-exist-yet).
3. `.github/workflows/ci.yml` and `deploy.yml`; configure the repo secrets/vars — [§7](#7-cicd-github-actions).
4. Register the production domain and the exact Google redirect URI (**with `/api`**).
5. (Optional) add CORS middleware **only** if you choose the cross-origin split.

## Suggested first step

Pick the VM host and create the production Supabase project — every other step hangs
off those two. Then land the four repo artifacts in item 1–3 as a single PR.
