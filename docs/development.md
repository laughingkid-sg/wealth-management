# Local development orchestration

The development Compose stack keeps the three application processes running with automatic restarts and source hot reload. Supabase remains hosted: `compose.yaml` does not create Postgres, Auth, Storage, Realtime, or another Supabase service.

## Services and ports

| Service | Container process | Host access | Configuration |
| --- | --- | --- | --- |
| `frontend` | Vite development server and same-origin API proxy | `http://localhost:8085` | `frontend/.env.local` plus Compose proxy overrides |
| `api` | Go HTTP API with hot reload | `http://localhost:8086` | root `.env` plus safe Compose development overrides |
| `worker` | Go asynchronous worker with hot reload | No exposed port | root `.env` |
| Hosted Supabase | Remote managed services | Project URLs in the environment files | Not managed by Compose |

The frontend container listens on port `5173` internally and the API listens on `8080` internally. Only their host mappings use `8085` and `8086`. Browser API calls use same-origin `/api`; Vite removes that prefix and proxies the request to the API service at `http://api:8080` over the private Compose network. The worker connects outbound to the hosted Supabase transaction pooler, Storage, Gmail, and model provider; it does not serve a public endpoint.

All application services use `restart: unless-stopped`. Frontend startup waits for the API health check. The worker is an independent queue consumer and starts automatically with the stack.

## Configuration

Create ignored local files if necessary:

```bash
cp -n backend/.env.example .env
cp -n frontend/.env.example frontend/.env.local
```

The root `.env` is server-only and is loaded at runtime by `api` and `worker`. It must contain the hosted Supabase transaction-pooler URL on port `6543` with `sslmode=require`, the service-role key, Google credentials, the token-encryption key, and model-provider settings documented in `backend/.env.example`.

Compose safely overrides these local routing values:

```dotenv
API_ADDRESS=:8080
FRONTEND_ORIGIN=http://localhost:8085
GOOGLE_OAUTH_REDIRECT_URL=http://localhost:8086/v1/transactions/gmail/oauth/callback
```

Register that exact Google callback URI in Google Cloud before testing Gmail connection. A callback for port `8080`, a different hostname, or a different path will produce `redirect_uri_mismatch`.

The frontend file must contain only public browser configuration:

```dotenv
VITE_SUPABASE_URL=https://your-project-ref.supabase.co
VITE_SUPABASE_PUBLISHABLE_KEY=sb_publishable_replace_me
```

Compose adds these non-secret routing values:

```dotenv
VITE_API_BASE_URL=/api
API_PROXY_TARGET=http://api:8080
```

`API_PROXY_TARGET` is read only by the Vite development server. Outside Docker it defaults safely to `http://localhost:8080`, and may be overridden when a host-run API uses another port. Do not place `SUPABASE_SERVICE_ROLE_KEY`, `SUPABASE_DB_URL`, provider credentials, encryption keys, or Google client secrets in `frontend/.env.local` or any `VITE_*` variable.

## Commands

Build and start all services in the background:

```bash
docker compose up -d --build
```

Inspect health and restart state:

```bash
docker compose ps
```

Follow every application log, or one service only:

```bash
docker compose logs -f frontend api worker
docker compose logs -f worker
```

Restart processes without rebuilding images:

```bash
docker compose restart api worker
```

Rebuild after dependency or Dockerfile changes:

```bash
docker compose up -d --build
```

Stop and remove application containers and the development network:

```bash
docker compose down
```

Source files stay on the host. Named volumes retain frontend dependencies and Go download/build caches between ordinary `down` and `up` operations. To clear only those Docker-managed development caches, use `docker compose down --volumes`; this does not alter hosted Supabase data.

## Health and troubleshooting

- `docker compose ps` should show `frontend`, `api`, and `worker` as healthy. The worker check verifies that its queue-consumer binary is running, even though the worker exposes no port.
- Open the application at `http://localhost:8085`; its `/api` requests should remain on that origin and be forwarded internally by Vite, so normal browser use does not depend on cross-origin API access.
- `curl --fail http://localhost:8086/healthz` checks the API from the host.
- If source changes are not reflected, inspect the relevant log, then restart that service. Rebuild only when a dependency manifest, Dockerfile, or hot-reload configuration changes.
- If Gmail connection reports a redirect mismatch, confirm the Google Cloud client contains the exact `http://localhost:8086/v1/transactions/gmail/oauth/callback` URI.
- If API or worker startup fails, inspect their logs for the name of a missing or invalid environment variable. Do not print credential values into logs or support output.
- If queued work does not advance, confirm the `worker` service is healthy and inspect `docker compose logs -f worker`.
- Do not run `supabase start` for this workflow. Schema and Storage operations target the configured hosted development project.
