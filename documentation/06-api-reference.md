# 06 — API Reference (Go HTTP API)

Base path (internal): the API serves at `:8080`. The browser reaches it at
**`/api`** (Vite/proxy strips the prefix in dev). So a route documented here as
`GET /v1/transactions` is called from the SPA as `GET /api/v1/transactions`.

## Conventions

- **Auth:** every route except the two below is wrapped in `requireUser`, which
  requires `Authorization: Bearer <supabase access token>`; the user id is derived
  server-side. Unauthenticated → `401`.
  - Exceptions: `GET /healthz` (no auth) and `GET /v1/transactions/gmail/oauth/callback`
    (browser redirect target from Google; validates its own single-use state).
- **Responses:** JSON. Errors do not leak DB internals or secrets.
- **Security headers:** `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`
  on all responses.
- **Feature flag:** all `…/bulk-import/…` and `…/bulk-attempts…` routes exist only
  when `BULK_IMPORT_ENABLED=true`.

Route source of truth: the `Register(mux, verifier)` method in each feature's
`http.go` (`transactions`, `accountbalances`, `creditcard`, `bulkimport`).

---

## Health

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/healthz` | Liveness. Returns `204`. Used by the Compose healthcheck. |

## Gmail connection & sync

| Method | Path | Handler | Purpose |
| --- | --- | --- | --- |
| POST | `/v1/transactions/gmail/connect` | `beginGmailConnection` | Start the Gmail OAuth (PKCE) flow; returns the Google consent URL. |
| GET | `/v1/transactions/gmail/oauth/callback` | `completeGmailConnection` | OAuth return target (no `requireUser`); consumes single-use state, stores encrypted refresh token. |
| GET | `/v1/transactions/gmail/connection` | `getGmailConnection` | Current Gmail connection status for the user. |
| POST | `/v1/transactions/gmail/sync-runs` | `createSyncRun` | Create a sync run + enqueue a `gmail_ingestion` job; returns immediately. |
| GET | `/v1/transactions/sync-runs/latest` | `getLatestSyncRun` | Latest sync run (polling fallback for Realtime). |
| GET | `/v1/transactions/sync-runs/{id}` | `getSyncRun` | A specific sync run. |

## Transactions & evidence

| Method | Path | Handler | Purpose |
| --- | --- | --- | --- |
| GET | `/v1/transactions` | `listTransactions` | List transactions (filters + cursor pagination). |
| GET | `/v1/transactions/` (subtree) | `transactionSubroute` | Transaction detail and its source subtree (narrow handler that disambiguates `/{id}` vs `/sync-runs/{id}`). |
| PATCH | `/v1/transactions/{id}` | `patchTransaction` | Canonical edit of a transaction. |
| POST | `/v1/transactions/internal-transfers` | `createInternalTransfer` | Create an atomic internal transfer (paired debit+credit linked via `transaction_links`). |
| GET | `/v1/transactions/sources` | `listSources` | List evidence sources (Review / Dangling / Failed queues). |
| GET | `/v1/transactions/sources/{id}/email` | `getSourceEmail` | Sanitised email body for a source. |
| GET | `/v1/transactions/sources/{id}/attachments` | `listSourceAttachments` | Attachment list + signed URLs. |
| GET | `/v1/transactions/sources/{id}/debug` | `getSourceDebug` | Parse audit/debug summary (attempts). |
| GET | `/v1/transactions/sources/{id}/debug/attempts/{attempt_id}/fields/{field}` | `getSourceDebugField` | One large debug field (prompt / request / response / model output). |
| POST | `/v1/transactions/sources/{id}/attach` | `attachSource` | Attach a source to an existing transaction. |
| POST | `/v1/transactions/sources/{id}/create-transaction` | `createTransactionFromSource` | Create a new transaction from a source. |
| POST | `/v1/transactions/sources/{id}/retry` | `retrySourceParse` | Re-enqueue parsing for a source. |
| DELETE | `/v1/transactions/sources/{id}` | `deleteSource` | Deliberate raw-source deletion (enqueues `source_attachment_cleanup`). |
| POST | `/v1/transactions/source-links/{id}/unmatch` | `unmatchSourceLink` | Detach a source from its transaction. |

## Transaction settings (per-user)

| Method | Path | Handler | Purpose |
| --- | --- | --- | --- |
| GET | `/v1/transactions/settings` | `getTransactionSettings` | User parser settings + rules + matching keys. |
| PUT | `/v1/transactions/settings/default-instructions` | `putDefaultInstructions` | Set the user's default parser instructions. |
| POST | `/v1/transactions/settings/source-rules` | `createSourceRule` | Create a user source-parser rule. |
| PUT | `/v1/transactions/settings/source-rules/{id}` | `updateSourceRule` | Update a user source rule. |
| DELETE | `/v1/transactions/settings/source-rules/{id}` | `deleteSourceRule` | Delete a user source rule. |
| POST | `/v1/transactions/settings/matching-keys` | `createMatchingKey` | Add a typed account matching key. |
| PATCH | `/v1/transactions/settings/matching-keys/{id}` | `patchMatchingKey` | Update a matching key. |

## Global settings (shared) & prompt preview

| Method | Path | Handler | Purpose |
| --- | --- | --- | --- |
| GET | `/v1/transactions/global-settings` | `getGlobalSettings` | Shared global parser settings/rules. |
| POST | `/v1/transactions/global-settings/source-rules` | `createGlobalSourceRule` | Create a shared global source rule. |
| PUT | `/v1/transactions/global-settings/source-rules/{id}` | `updateGlobalSourceRule` | Update a shared global source rule. |
| GET | `/v1/transactions/prompt-preview/sources` | `listPromptPreviewSources` | Sources selectable for prompt preview. |
| POST | `/v1/transactions/prompt-preview` | `previewPrompt` | Side-effect-free assembly of the system prompt + provider request template. |

> Global rules are shared across users. During this dev phase any authenticated user
> can edit them; an admin-only model is deferred.

## Account balances & treatments

| Method | Path | Handler | Purpose |
| --- | --- | --- | --- |
| GET | `/v1/accounts/balances` | `listBalances` | Balances across accounts. |
| PUT | `/v1/accounts/{id}/opening-balance` | `setOpeningBalance` | Set/revise an account's opening balance (versioned). |
| GET | `/v1/accounts/{id}/opening-balance/history` | `listOpeningBalanceHistory` | Opening-balance revision history. |
| GET | `/v1/transaction-calculation-treatments/{id}` | `getTreatment` | Get a transaction's spending treatment. |
| PUT | `/v1/transaction-calculation-treatments/{id}` | `setTreatment` | Set a transaction's spending treatment. |

## Credit-card statements (bills)

| Method | Path | Handler | Purpose |
| --- | --- | --- | --- |
| GET | `/v1/accounts/{account_id}/credit-card-statements` | `listBills` | List bills for a credit-card account. |
| GET | `/v1/credit-card-statements/{id}` | `getBill` | Bill detail (header + lines + candidates). |
| PATCH | `/v1/credit-card-statements/{id}` | `correctHeader` | Correct bill header fields. |
| POST | `/v1/credit-card-statements/{id}/lines/{line_id}/attach` | `attachLine` | Attach a line to a transaction. |
| POST | `/v1/credit-card-statements/{id}/lines/{line_id}/create-transaction` | `createLineTransaction` | Create a transaction from a line. |
| POST | `/v1/credit-card-statements/{id}/lines/{line_id}/ignore` | `ignoreLine` | Ignore a line. |
| POST | `/v1/credit-card-statements/{id}/payment-candidate/select` | `selectPaymentCandidate` | Select a detected payment candidate. |
| POST | `/v1/credit-card-statements/{id}/payment-candidate/confirm` | `confirmPaymentCandidate` | Confirm the payment candidate. |
| POST | `/v1/credit-card-statements/{id}/payoff` | `payInFull` | Record a pay-in-full payoff. |
| POST | `/v1/credit-card-statements/{id}/void` | `voidBill` | Void a bill. |
| DELETE | `/v1/credit-card-statements/{id}` | `discardBill` | Discard a bill. |

## Bulk import (only when `BULK_IMPORT_ENABLED=true`)

### Templates
| Method | Path | Handler |
| --- | --- | --- |
| GET / POST | `/v1/transactions/bulk-import/templates` | `templates` |
| PATCH | `/v1/transactions/bulk-import/templates/{id}` | `templateUpdate` |
| POST | `/v1/transactions/bulk-import/templates/{id}/archive` | `templateArchive(true)` |
| POST | `/v1/transactions/bulk-import/templates/{id}/restore` | `templateArchive(false)` |

### Batches, files, documents
| Method | Path | Handler |
| --- | --- | --- |
| GET / POST | `/v1/transactions/bulk-import/batches` | `batches` |
| GET / DELETE | `/v1/transactions/bulk-import/batches/{id}` | `batch` |
| POST | `/v1/transactions/bulk-import/batches/{id}/files/reservations` | `reserveFile` |
| POST | `/v1/transactions/bulk-import/batches/{id}/files/{file_id}/finalize` | `finalizeFile` |
| PUT | `/v1/transactions/bulk-import/batches/{id}/documents` | `replaceDocuments` |
| POST | `/v1/transactions/bulk-import/batches/{id}/submit` | `batchAction("submit")` |
| POST | `/v1/transactions/bulk-import/batches/{id}/cancel` | `batchAction("cancel")` |
| GET | `/v1/transactions/bulk-import/batches/{id}/candidates` | `candidates` |
| POST | `/v1/transactions/bulk-import/candidates/{id}/resolve` | `resolveCandidate` |
| POST | `/v1/transactions/bulk-import/documents/{id}/retry` | `retryDocument` |
| DELETE | `/v1/transactions/bulk-import/documents/{id}` | `deleteDocument` |

### Preview, debug, evidence
| Method | Path | Handler |
| --- | --- | --- |
| POST | `/v1/transactions/bulk-import/prompt-preview` | `promptPreview` |
| GET | `/v1/transactions/sources/{id}/debug/bulk-attempts` | `debugAttempts` |
| GET | `/v1/transactions/sources/{id}/debug/bulk-attempts/{attempt_id}/fields/{field}` | `debugAttemptField` |
| GET | `/v1/bulk-import/documents/{id}` | `documentEvidence` |

---

For request/response **shapes**, read the handler in the relevant `http.go` and the
matching TypeScript client in `frontend/src/features/<feature>/api.ts` — the two
are kept in sync and together define the contract.
