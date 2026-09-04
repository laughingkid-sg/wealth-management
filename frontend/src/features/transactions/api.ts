import type { Session } from "@supabase/supabase-js";
import type {
  AccountMatchingKey,
  AccountMatchingKeyInput,
  CursorPage,
  DefaultParserInstructions,
  ExactSourceDebugField,
  GmailConnection,
  GlobalSourceParserRule,
  GlobalSourceParserRuleInput,
  GlobalTransactionSettings,
  InternalTransferInput,
  ManualTransactionInput,
  OwnedAccountOption,
  PromptPreviewInput,
  PromptPreviewResult,
  PromptPreviewSource,
  SourceAttachment,
  SourceParserRule,
  SourceParserRuleInput,
  SourceParseDebug,
  SourceDeletionResult,
  SourceDebugField,
  SourceQueue,
  SourceSummary,
  TransactionCategory,
  TransactionFilters,
  TransactionListItem,
  TransactionPatch,
  TransactionSettings,
  TransactionSyncRun,
} from "./model";
import {
  buildManualDuplicatePreflightParams,
  buildManualTransactionInsertPayload,
} from "./manualTransactionModel";
import {
  TransactionApiError,
  contractError,
  dataRestTransactionSelect,
  enumValue,
  nullableStringValue,
  optionalDate,
  optionalString,
  parseAccountMatchingKey,
  parseDataRestTransaction,
  parseGlobalSourceParserRule,
  parseGlobalTransactionSettings,
  parsePage,
  parsePromptPreviewResult,
  parsePromptPreviewSources,
  parseSourceParseDebug,
  parseSourceParserRule,
  parseSourceSummary,
  parseSyncRunResponse,
  parseTransaction,
  parseTransactionSettings,
  requiredBoolean,
  requiredInteger,
  requiredRecord,
  requiredString,
  sourceDebugFields,
  stringValue,
  unwrapData,
  pageSize,
  request,
  requestDataRest,
  mutateDataRest,
} from "./apiClient";

// Preserve the public surface consumers import from "./api".
export {
  TransactionApiError,
  parseGlobalTransactionSettings,
  parsePromptPreviewResult,
  parsePromptPreviewSources,
  parseSourceParseDebug,
  parseSourceSummary,
  parseSyncRunResponse,
  parseTransactionSettings,
} from "./apiClient";

export interface SanitizedEmail {
  subject: string;
  html: string | null;
  text: string | null;
}


export async function listTransactions(
  session: Session,
  filters: TransactionFilters,
  cursor?: string | null,
  signal?: AbortSignal,
): Promise<CursorPage<TransactionListItem>> {
  const params = new URLSearchParams({ limit: String(pageSize) });
  if (filters.search?.trim()) params.set("search", filters.search.trim());
  if (filters.kind) params.set("kind", filters.kind);
  if (filters.review) params.set("review", filters.review);
  if (cursor) params.set("cursor", cursor);
  const response = await request(session, `/v1/transactions?${params.toString()}`, { signal });
  return parsePage(
    response,
    (item) => parseTransaction(item, true),
    "transactions_page",
  );
}

export async function listOwnedAccounts(
  session: Session,
  signal?: AbortSignal,
): Promise<OwnedAccountOption[]> {
  const params = new URLSearchParams({
    select: "id,name,institution_name",
    deleted_at: "is.null",
    order: "sort_order.asc,name.asc",
  });
  const response = await requestDataRest(session, `accounts?${params.toString()}`, signal);
  if (!Array.isArray(response)) contractError("accounts must be an array");
  return response.map((value, index) => {
    const item = requiredRecord(value, `accounts[${index}]`);
    return {
      id: requiredString(item.id, `accounts[${index}].id`),
      name: requiredString(item.name, `accounts[${index}].name`),
      institution_name: optionalString(
        item.institution_name,
        `accounts[${index}].institution_name`,
      ) ?? "",
    };
  });
}

export async function listTransactionCategories(
  session: Session,
  signal?: AbortSignal,
): Promise<TransactionCategory[]> {
  const params = new URLSearchParams({
    select: "id,parent_name,name,emoji,sort_order",
    active: "eq.true",
    order: "sort_order.asc",
  });
  const response = await requestDataRest(
    session,
    `transaction_categories?${params.toString()}`,
    signal,
  );
  if (!Array.isArray(response)) contractError("transaction_categories must be an array");
  return response.map((value, index) => {
    const item = requiredRecord(value, `transaction_categories[${index}]`);
    return {
      id: requiredString(item.id, `transaction_categories[${index}].id`),
      parent_name: requiredString(
        item.parent_name,
        `transaction_categories[${index}].parent_name`,
      ),
      name: requiredString(item.name, `transaction_categories[${index}].name`),
      emoji: optionalString(item.emoji, `transaction_categories[${index}].emoji`) ?? "",
      sort_order: requiredInteger(
        item.sort_order,
        `transaction_categories[${index}].sort_order`,
      ),
    };
  });
}

export async function listTransactionsForAccount(
  session: Session,
  accountId: string,
  search: string,
  cursor?: string | null,
  signal?: AbortSignal,
): Promise<CursorPage<TransactionListItem>> {
  const offset = cursor && /^\d+$/.test(cursor) ? Number(cursor) : 0;
  if (!Number.isSafeInteger(offset)) throw new TransactionApiError("Invalid transaction cursor.", 400);
  const params = new URLSearchParams({
    select: dataRestTransactionSelect,
    account_id: `eq.${accountId}`,
    order: "occurred_at.desc",
    limit: String(pageSize + 1),
    offset: String(offset),
  });
  const normalizedSearch = search.trim().replace(/[%*,().]/g, " ");
  if (normalizedSearch) {
    params.set(
      "or",
      `(title.ilike.*${normalizedSearch}*,merchant_name.ilike.*${normalizedSearch}*)`,
    );
  }
  const response = await requestDataRest(session, `transactions?${params.toString()}`, signal);
  if (!Array.isArray(response)) contractError("account transactions must be an array");
  const hasMore = response.length > pageSize;
  return {
    items: response.slice(0, pageSize).map(parseDataRestTransaction),
    next_cursor: hasMore ? String(offset + pageSize) : null,
  };
}

export async function getOwnedTransactionCandidate(
  session: Session,
  transactionId: string,
  accountId?: string | null,
  signal?: AbortSignal,
): Promise<TransactionListItem | null> {
  const params = new URLSearchParams({
    select: dataRestTransactionSelect,
    id: `eq.${transactionId}`,
    limit: "2",
  });
  if (accountId) params.set("account_id", `eq.${accountId}`);
  const response = await requestDataRest(session, `transactions?${params.toString()}`, signal);
  if (!Array.isArray(response)) contractError("recommended transaction must be an array");
  if (response.length > 1) contractError("recommended transaction lookup returned multiple rows");
  return response.length === 0 ? null : parseDataRestTransaction(response[0]);
}

export async function findLikelyManualTransactionDuplicates(
  session: Session,
  input: ManualTransactionInput,
  signal?: AbortSignal,
): Promise<TransactionListItem[]> {
  const occurredAt = new Date(input.occurred_at);
  if (Number.isNaN(occurredAt.getTime())) {
    throw new TransactionApiError("The transaction time is invalid.", 400);
  }
  const params = buildManualDuplicatePreflightParams(input);
  params.set("select", dataRestTransactionSelect);
  const response = await requestDataRest(session, `transactions?${params.toString()}`, signal);
  if (!Array.isArray(response)) contractError("likely duplicate transactions must be an array");
  return response.map(parseDataRestTransaction);
}

export async function createManualTransaction(
  session: Session,
  input: ManualTransactionInput,
): Promise<TransactionListItem> {
  const payload = buildManualTransactionInsertPayload(session.user.id, input);
  const params = new URLSearchParams({ select: dataRestTransactionSelect });
  const response = await mutateDataRest(
    session,
    `transactions?${params.toString()}`,
    payload,
  );
  if (!Array.isArray(response) || response.length !== 1) {
    contractError("manual transaction insert must return exactly one row");
  }
  return parseDataRestTransaction(response[0]);
}

export async function listSources(
  session: Session,
  status: SourceQueue,
  cursor?: string | null,
  signal?: AbortSignal,
): Promise<CursorPage<SourceSummary>> {
  const params = new URLSearchParams({ status, limit: String(pageSize) });
  if (cursor) params.set("cursor", cursor);
  const response = await request(session, `/v1/transactions/sources?${params.toString()}`, {
    signal,
  });
  return parsePage(response, parseSourceSummary, "sources_page");
}

export async function getGmailConnection(
  session: Session,
  signal?: AbortSignal,
): Promise<GmailConnection> {
  try {
    const value = requiredRecord(
      unwrapData(
        await request(session, "/v1/transactions/gmail/connection", { signal }),
      ),
      "gmail_connection",
    );
    return {
      connected: requiredBoolean(value.connected, "gmail_connection.connected"),
      status: optionalString(value.status, "gmail_connection.status"),
      email: optionalString(value.email, "gmail_connection.email"),
      last_synced_at: optionalDate(value.last_synced_at, "gmail_connection.last_synced_at"),
      last_error: optionalString(value.last_error, "gmail_connection.last_error"),
    };
  } catch (error) {
    if (error instanceof TransactionApiError && error.status === 404) {
      return { connected: false, status: null, email: null, last_synced_at: null, last_error: null };
    }
    throw error;
  }
}

export async function beginGmailConnection(session: Session): Promise<string> {
  const response = requiredRecord(
    unwrapData(
      await request(session, "/v1/transactions/gmail/connect", { method: "POST" }),
    ),
    "gmail_connect",
  );
  const authorizationURL = requiredString(
    response.authorization_url,
    "gmail_connect.authorization_url",
  );
  let parsed: URL;
  try {
    parsed = new URL(authorizationURL);
  } catch {
    contractError("gmail_connect.authorization_url must be a URL");
  }
  if (parsed.protocol !== "https:") contractError("gmail_connect.authorization_url must use HTTPS");
  return parsed.toString();
}

export async function startGmailSync(session: Session): Promise<TransactionSyncRun> {
  return parseSyncRunResponse(
    await request(session, "/v1/transactions/gmail/sync-runs", { method: "POST" }),
  );
}

export async function getSyncRun(
  session: Session,
  id: string,
  signal?: AbortSignal,
): Promise<TransactionSyncRun> {
  return parseSyncRunResponse(
    await request(session, `/v1/transactions/sync-runs/${encodeURIComponent(id)}`, { signal }),
  );
}

export async function getLatestSyncRun(
  session: Session,
  signal?: AbortSignal,
): Promise<TransactionSyncRun | null> {
  try {
    return parseSyncRunResponse(
      await request(session, "/v1/transactions/sync-runs/latest", { signal }),
    );
  } catch (error) {
    if (error instanceof TransactionApiError && error.status === 404) return null;
    throw error;
  }
}

export async function getSanitizedEmail(
  session: Session,
  sourceId: string,
  signal?: AbortSignal,
): Promise<SanitizedEmail> {
  const response = requiredRecord(
    unwrapData(
      await request(session, `/v1/transactions/sources/${encodeURIComponent(sourceId)}/email`, {
        signal,
      }),
    ),
    "source_email",
  );
  const html = optionalString(response.html, "source_email.html");
  const text = optionalString(response.text, "source_email.text");
  return {
    subject: optionalString(response.subject, "source_email.subject") ?? "Email source",
    html,
    text,
  };
}

function parseAttachment(value: unknown): SourceAttachment {
  const item = requiredRecord(value, "attachment");
  const signedURL = optionalString(item.signed_url, "attachment.signed_url");
  if (signedURL) {
    let parsed: URL;
    try {
      parsed = new URL(signedURL);
    } catch {
      contractError("attachment.signed_url must be a URL");
    }
    const loopbackDevelopmentURL =
      import.meta.env.DEV &&
      parsed.protocol === "http:" &&
      (parsed.hostname === "localhost" ||
        parsed.hostname.endsWith(".localhost") ||
        parsed.hostname === "127.0.0.1" ||
        parsed.hostname === "[::1]");
    if (parsed.protocol !== "https:" && !loopbackDevelopmentURL) {
      contractError("attachment.signed_url uses an unsafe protocol");
    }
  }
  return {
    filename: requiredString(item.filename, "attachment.filename"),
    mime_type: requiredString(item.mime_type, "attachment.mime_type"),
    byte_size: requiredInteger(item.byte_size, "attachment.byte_size"),
    parse_eligible: requiredBoolean(item.parse_eligible, "attachment.parse_eligible"),
    storage_status: requiredString(item.storage_status, "attachment.storage_status"),
    signed_url: signedURL,
  };
}

export async function getSourceAttachments(
  session: Session,
  sourceId: string,
  signal?: AbortSignal,
): Promise<SourceAttachment[]> {
  const response = await request(
    session,
    `/v1/transactions/sources/${encodeURIComponent(sourceId)}/attachments`,
    { signal },
  );
  return parsePage(response, parseAttachment, "attachments_page").items;
}

export async function getTransactionSources(
  session: Session,
  transactionId: string,
  signal?: AbortSignal,
): Promise<SourceSummary[]> {
  const response = await request(
    session,
    `/v1/transactions/${encodeURIComponent(transactionId)}/sources`,
    { signal },
  );
  const unwrapped = unwrapData(response);
  const values = Array.isArray(unwrapped)
    ? unwrapped
    : requiredRecord(unwrapped, "transaction_sources").items;
  if (!Array.isArray(values)) contractError("transaction_sources must be an array");
  return values.map(parseSourceSummary);
}

export async function attachSourceToTransaction(
  session: Session,
  sourceId: string,
  transactionId: string,
): Promise<void> {
  await request(session, `/v1/transactions/sources/${encodeURIComponent(sourceId)}/attach`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ transaction_id: transactionId }),
  });
}

export async function createTransactionFromSource(
  session: Session,
  sourceId: string,
  accountId: string,
): Promise<void> {
  await request(
    session,
    `/v1/transactions/sources/${encodeURIComponent(sourceId)}/create-transaction`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ account_id: accountId }),
    },
  );
}

export async function retrySource(session: Session, sourceId: string): Promise<void> {
  await request(session, `/v1/transactions/sources/${encodeURIComponent(sourceId)}/retry`, {
    method: "POST",
  });
}

export async function unmatchSourceLink(session: Session, sourceLinkId: string): Promise<void> {
  await request(
    session,
    `/v1/transactions/source-links/${encodeURIComponent(sourceLinkId)}/unmatch`,
    { method: "POST" },
  );
}

export async function patchTransaction(
  session: Session,
  transactionId: string,
  patch: TransactionPatch,
): Promise<void> {
  await request(session, `/v1/transactions/${encodeURIComponent(transactionId)}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(patch),
  });
}

export async function createInternalTransfer(
  session: Session,
  input: InternalTransferInput,
): Promise<void> {
  await request(session, "/v1/transactions/internal-transfers", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
}

export async function getTransactionSettings(
  session: Session,
  signal?: AbortSignal,
): Promise<TransactionSettings> {
  return parseTransactionSettings(
    await request(session, "/v1/transactions/settings", { signal }),
  );
}

export async function getGlobalTransactionSettings(
  session: Session,
  signal?: AbortSignal,
): Promise<GlobalTransactionSettings> {
  return parseGlobalTransactionSettings(
    await request(session, "/v1/transactions/global-settings", { signal }),
  );
}

export async function createGlobalSourceParserRule(
  session: Session,
  input: GlobalSourceParserRuleInput,
): Promise<GlobalSourceParserRule> {
  return parseGlobalSourceParserRule(
    unwrapData(
      await request(session, "/v1/transactions/global-settings/source-rules", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(input),
      }),
    ),
  );
}

export async function updateGlobalSourceParserRule(
  session: Session,
  ruleId: string,
  expectedVersion: number,
  input: GlobalSourceParserRuleInput,
): Promise<GlobalSourceParserRule> {
  return parseGlobalSourceParserRule(
    unwrapData(
      await request(
        session,
        `/v1/transactions/global-settings/source-rules/${encodeURIComponent(ruleId)}`,
        {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ ...input, expected_version: expectedVersion }),
        },
      ),
    ),
  );
}

export async function listPromptPreviewSources(
  session: Session,
  signal?: AbortSignal,
): Promise<PromptPreviewSource[]> {
  return parsePromptPreviewSources(
    await request(session, "/v1/transactions/prompt-preview/sources", { signal }),
  );
}

export async function buildPromptPreview(
  session: Session,
  input: PromptPreviewInput,
  signal?: AbortSignal,
): Promise<PromptPreviewResult> {
  return parsePromptPreviewResult(
    await request(session, "/v1/transactions/prompt-preview", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
      signal,
    }),
  );
}

export async function putDefaultParserInstructions(
  session: Session,
  defaultInstructions: string,
): Promise<DefaultParserInstructions> {
  const response = requiredRecord(
    unwrapData(
      await request(session, "/v1/transactions/settings/default-instructions", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ default_instructions: defaultInstructions }),
      }),
    ),
    "default_instructions",
  );
  return {
    default_instructions: stringValue(
      response.default_instructions,
      "default_instructions.default_instructions",
    ),
    default_instructions_version: requiredInteger(
      response.default_instructions_version,
      "default_instructions.default_instructions_version",
      1,
    ),
  };
}

export async function createSourceParserRule(
  session: Session,
  input: SourceParserRuleInput,
): Promise<SourceParserRule> {
  return parseSourceParserRule(
    unwrapData(
      await request(session, "/v1/transactions/settings/source-rules", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(input),
      }),
    ),
  );
}

export async function updateSourceParserRule(
  session: Session,
  ruleId: string,
  input: SourceParserRuleInput,
): Promise<SourceParserRule> {
  return parseSourceParserRule(
    unwrapData(
      await request(
        session,
        `/v1/transactions/settings/source-rules/${encodeURIComponent(ruleId)}`,
        {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(input),
        },
      ),
    ),
  );
}

export async function retireSourceParserRule(
  session: Session,
  ruleId: string,
): Promise<void> {
  await request(
    session,
    `/v1/transactions/settings/source-rules/${encodeURIComponent(ruleId)}`,
    { method: "DELETE" },
  );
}

export async function createAccountMatchingKey(
  session: Session,
  input: AccountMatchingKeyInput,
): Promise<AccountMatchingKey> {
  return parseAccountMatchingKey(
    unwrapData(
      await request(session, "/v1/transactions/settings/matching-keys", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(input),
      }),
    ),
  );
}

export async function setAccountMatchingKeyActive(
  session: Session,
  keyId: string,
  active: boolean,
): Promise<AccountMatchingKey> {
  return parseAccountMatchingKey(
    unwrapData(
      await request(
        session,
        `/v1/transactions/settings/matching-keys/${encodeURIComponent(keyId)}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ active }),
        },
      ),
    ),
  );
}

export async function deleteRawSource(
  session: Session,
  sourceId: string,
): Promise<SourceDeletionResult> {
  const response = requiredRecord(
    unwrapData(
      await request(session, `/v1/transactions/sources/${encodeURIComponent(sourceId)}`, {
        method: "DELETE",
      }),
    ),
    "source_deletion",
  );
  const status = enumValue(
    response.status,
    ["completed", "cleanup_pending"],
    "source_deletion.status",
  );
  const cleanupPending = requiredBoolean(
    response.cleanup_pending,
    "source_deletion.cleanup_pending",
  );
  if ((status === "cleanup_pending") !== cleanupPending) {
    contractError("source_deletion status and cleanup_pending disagree");
  }
  return { status, cleanup_pending: cleanupPending };
}

export async function getSourceParseDebug(
  session: Session,
  sourceId: string,
  signal?: AbortSignal,
): Promise<SourceParseDebug> {
  return parseSourceParseDebug(
    await request(
      session,
      `/v1/transactions/sources/${encodeURIComponent(sourceId)}/debug`,
      { signal },
    ),
  );
}

export async function getExactSourceDebugField(
  session: Session,
  sourceId: string,
  attemptId: string,
  field: SourceDebugField,
  signal?: AbortSignal,
): Promise<ExactSourceDebugField> {
  const response = requiredRecord(
    unwrapData(
      await request(
        session,
        `/v1/transactions/sources/${encodeURIComponent(sourceId)}/debug/attempts/${encodeURIComponent(attemptId)}/fields/${encodeURIComponent(field)}`,
        { signal },
      ),
    ),
    "source_debug_field",
  );
  const result: ExactSourceDebugField = {
    source_id: requiredString(response.source_id, "source_debug_field.source_id"),
    attempt_id: requiredString(response.attempt_id, "source_debug_field.attempt_id"),
    field: enumValue(response.field, sourceDebugFields, "source_debug_field.field"),
    value: nullableStringValue(response.value, "source_debug_field.value"),
    max_bytes: requiredInteger(response.max_bytes, "source_debug_field.max_bytes", 1),
  };
  if (
    result.source_id !== sourceId ||
    result.attempt_id !== attemptId ||
    result.field !== field
  ) {
    contractError("source_debug_field identity does not match the requested field");
  }
  return result;
}
