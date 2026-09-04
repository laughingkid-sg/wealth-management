# 08 — Security Model

This is a finance app handling email evidence, attachments, and OAuth tokens. The
security posture is central to the design; preserve it in every change.

## Secret inventory (where secrets may and may not live)

| Secret | Lives in | Must NEVER appear in |
| --- | --- | --- |
| `SUPABASE_SERVICE_ROLE_KEY` | root `.env` (api + worker) | frontend, any `VITE_*`, logs, responses |
| `SUPABASE_DB_URL` (pooler creds) | root `.env` | frontend, `VITE_*`, logs |
| `GOOGLE_OAUTH_CLIENT_SECRET` | root `.env` | frontend, `VITE_*`, logs |
| `TRANSACTION_TOKEN_ENCRYPTION_KEY` (32B) | root `.env` | frontend, `VITE_*`, logs |
| `ALIBABA_TOKEN_PLAN_API_KEY` | root `.env` | frontend, `VITE_*`, logs |
| Gmail **refresh token** | `private.gmail_connections` **encrypted** (`bytea`) | anywhere in plaintext |
| OAuth PKCE verifier | `private.gmail_oauth_states` **encrypted** | anywhere in plaintext |

The browser holds **only** `VITE_SUPABASE_URL` and `VITE_SUPABASE_PUBLISHABLE_KEY`
(both public) plus the signed-in user's access token.

`config.Config` is deliberately **never logged or marshalled**.

## Layered defenses

1. **RLS everywhere browser-reachable.** Every `public` table restricts rows to
   `auth.uid() = user_id`. Policies enforce **ownership**, not just
   `TO authenticated`. See [05 — Database](05-database.md#row-level-security).

2. **Private schema is unreachable from the browser.** Sensitive data (raw
   evidence, tokens, jobs, audit, matching keys, credit-card/bulk internals) lives
   in `private`, which has RLS enabled and **no grants** to `anon`/`authenticated`.

3. **The single narrow browser write.** Browsers can insert into
   `public.transactions` only when the row is a *confirmed manual* transaction they
   own (enforced by RLS `WITH CHECK`). Every other write goes through the Go API.

4. **Server verifies identity.** The API validates the Supabase access token and
   derives the user id server-side; it never trusts a client-supplied `user_id`.

5. **Pooler + TLS pinned.** `config` refuses any DB URL that isn't the
   `*.pooler.supabase.com:6543` transaction pooler with `sslmode=require`.

6. **Token encryption.** Gmail refresh tokens and PKCE verifiers are AES-encrypted
   (`internal/secret`) before storage. No Gmail password is ever accepted.

7. **Single-use, expiring OAuth state.** `gmail_oauth_states` stores only digests +
   encrypted verifier; raw state values are never persisted, and rows expire and are
   consumed once.

8. **Untrusted content is sanitised & validated.** Stored email HTML is sanitised
   (`emailcontent` / bluemonday) before display, and the private original HTML is
   not returned by the API. LLM output and source content are treated as untrusted
   and validated by the server **and** by DB CHECK constraints (amounts, currency
   regex, jsonb shapes, status enums).

9. **The model never sees the account catalogue.** Account metadata and matching
   keys are **not** sent to the LLM; matching happens server-side using typed keys.

10. **Private Storage + signed URLs.** The `transaction-attachments` bucket is
    private, 5 MiB/file, PDF/image MIME only. Browser access is blocked by a
    restrictive `storage.objects` policy; files are reached via short-lived,
    ownership-checked **signed URLs** minted by the API. Browser uploads are allowed
    only against a reserved, unexpired object path for a `draft` batch.

11. **Response hygiene.** Handlers never surface DB errors or secrets. Security
    headers (`nosniff`, `X-Frame-Options: DENY`) are set on every response.

## Known, intentional posture (dev phase)

- **Global transaction rules are shared** and, in this dev phase, any authenticated
  user may edit them through the Go API. An admin-only authorization model is
  **deferred** — do not assume it exists.
- `user_metadata` is user-controlled and must **not** be used for authorization.
- The hosted project is a **development** environment; breaking schema changes are
  allowed within agreed scope, but **auth users are preserved** unless explicitly
  named for deletion.

## When you change security-relevant code

- Adding a browser-reachable table → enable RLS + write an ownership policy + add a
  pgTAP test.
- Adding a privileged action → put it behind the Go API with `requireUser`; validate
  input at the boundary; consider idempotency (`api_idempotency_records`).
- Never widen Storage access; keep the private-bucket + signed-URL pattern.
- Load the Supabase Postgres best-practices skill before changing DB objects, and
  review generated migrations/policies before applying.
