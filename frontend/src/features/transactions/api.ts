import type { Session } from "@supabase/supabase-js";
import type {
  MinorUnitAmount,
  OwnedAccountOption,
  SourceStatus,
  SourceSummary,
  SyncRunStatus,
  TransactionFilters,
  TransactionKind,
  TransactionListItem,
  TransactionReviewStatus,
  TransactionSyncRun,
} from "./model";

export interface SanitizedEmail {
  subject: string;
  html: string;
}

const apiBaseUrl = (import.meta.env.VITE_API_BASE_URL ?? "/api").replace(
  /\/$/,
  "",
);
const supabaseUrl = import.meta.env.VITE_SUPABASE_URL?.replace(/\/$/, "");
const supabasePublishableKey = import.meta.env.VITE_SUPABASE_PUBLISHABLE_KEY;

export class TransactionApiError extends Error {
  readonly status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = "TransactionApiError";
    this.status = status;
  }
}

type JsonRecord = Record<string, unknown>;

function isRecord(value: unknown): value is JsonRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function stringValue(value: unknown, fallback = ""): string {
  return typeof value === "string" ? value : fallback;
}

function nullableString(value: unknown): string | null {
  return typeof value === "string" ? value : null;
}

function nullableNumber(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function nullableMinorUnitAmount(value: unknown): MinorUnitAmount | null {
  if (typeof value === "string" && /^\d+$/.test(value)) return value;
  if (typeof value === "number" && Number.isSafeInteger(value) && value >= 0) {
    return String(value);
  }
  return null;
}

function numberValue(value: unknown, fallback = 0): number {
  return nullableNumber(value) ?? fallback;
}

function enumValue<T extends string>(
  value: unknown,
  accepted: readonly T[],
  fallback: T,
): T {
  return typeof value === "string" && accepted.includes(value as T)
    ? (value as T)
    : fallback;
}

function unwrapData(value: unknown): unknown {
  return isRecord(value) && "data" in value ? value.data : value;
}

function extractItems(value: unknown): unknown[] {
  const unwrapped = unwrapData(value);
  if (Array.isArray(unwrapped)) return unwrapped;
  if (isRecord(unwrapped) && Array.isArray(unwrapped.items)) return unwrapped.items;
  return [];
}

function relatedName(value: unknown): string | null {
  if (isRecord(value)) return nullableString(value.name);
  if (Array.isArray(value) && isRecord(value[0])) return nullableString(value[0].name);
  return null;
}

function parseTransaction(value: unknown): TransactionListItem | null {
  if (!isRecord(value)) return null;
  const id = stringValue(value.id);
  const title = stringValue(value.title);
  const accountId = stringValue(value.account_id);
  const occurredAt = stringValue(value.occurred_at);
  if (!id || !title || !accountId || !occurredAt) return null;
  return {
    id,
    title,
    merchant_name: nullableString(value.merchant_name),
    account_id: accountId,
    account_name:
      stringValue(value.account_name) || relatedName(value.accounts) || "Unlabelled account",
    transaction_kind: enumValue<TransactionKind>(
      value.transaction_kind,
      ["debit", "credit"],
      "debit",
    ),
    original_amount_minor: nullableMinorUnitAmount(value.original_amount_minor) ?? "0",
    original_currency: stringValue(value.original_currency, "SGD"),
    sgd_amount_minor: nullableMinorUnitAmount(value.sgd_amount_minor),
    occurred_at: occurredAt,
    category_name: nullableString(value.category_name) || relatedName(value.transaction_categories),
    review_status: enumValue<TransactionReviewStatus>(
      value.review_status,
      ["confirmed", "review_required", "pending"],
      "pending",
    ),
    source_count: numberValue(value.source_count),
  };
}

function parseSource(value: unknown): SourceSummary | null {
  if (!isRecord(value)) return null;
  const id = stringValue(value.id);
  const receivedAt = stringValue(value.received_at);
  if (!id || !receivedAt) return null;
  return {
    id,
    source_link_id:
      nullableString(value.source_link_id) ||
      nullableString(value.link_id) ||
      nullableString(value.transaction_data_source_id),
    source_type: enumValue(value.source_type, ["gmail_email", "phone_notification"], "gmail_email"),
    provider: stringValue(value.provider, "Gmail"),
    subject: nullableString(value.subject),
    sender: nullableString(value.sender),
    received_at: receivedAt,
    parse_status: enumValue<SourceStatus>(
      value.parse_status,
      ["dangling", "review_required", "parsed", "failed"],
      "review_required",
    ),
    parse_error: nullableString(value.parse_error),
    suggested_title: nullableString(value.suggested_title),
    suggested_amount_minor: nullableMinorUnitAmount(value.suggested_amount_minor),
    suggested_currency: nullableString(value.suggested_currency),
    suggested_account_name: nullableString(value.suggested_account_name),
  };
}

function parseOwnedAccount(value: unknown): OwnedAccountOption | null {
  if (!isRecord(value)) return null;
  const id = stringValue(value.id);
  const name = stringValue(value.name);
  if (!id || !name) return null;
  return {
    id,
    name,
    institution_name: stringValue(value.institution_name),
  };
}

function parseSyncRun(value: unknown): TransactionSyncRun | null {
  const run = unwrapData(value);
  if (!isRecord(run)) return null;
  const id = stringValue(run.id);
  if (!id) return null;
  return {
    id,
    status: enumValue<SyncRunStatus>(
      run.status,
      ["queued", "running", "completed", "failed"],
      "queued",
    ),
    messages_discovered: numberValue(run.messages_discovered),
    messages_ingested: numberValue(run.messages_ingested),
    sources_parsed: numberValue(run.sources_parsed),
    transactions_created: numberValue(run.transactions_created),
    sources_review: numberValue(run.sources_review),
    sources_dangling: numberValue(run.sources_dangling),
    error_summary: nullableString(run.error_summary),
    started_at: nullableString(run.started_at),
    completed_at: nullableString(run.completed_at),
  };
}

async function request(session: Session, path: string, init?: RequestInit): Promise<unknown> {
  const response = await fetch(`${apiBaseUrl}${path}`, {
    ...init,
    headers: {
      Authorization: `Bearer ${session.access_token}`,
      Accept: "application/json",
      ...init?.headers,
    },
  });
  const body: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    const message = isRecord(body) ? stringValue(body.error) || stringValue(body.message) : "";
    throw new TransactionApiError(message || "The request could not be completed.", response.status);
  }
  return body;
}

async function requestDataRest(session: Session, path: string): Promise<unknown> {
  if (!supabaseUrl || !supabasePublishableKey) {
    throw new TransactionApiError("Supabase is not configured in this frontend.", 500);
  }
  const response = await fetch(`${supabaseUrl}/rest/v1/${path}`, {
    headers: {
      Authorization: `Bearer ${session.access_token}`,
      apikey: supabasePublishableKey,
      Accept: "application/json",
    },
  });
  const body: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    throw new TransactionApiError("Transactions could not be read from the database.", response.status);
  }
  return body;
}

export async function listTransactions(
  session: Session,
  filters: TransactionFilters,
): Promise<TransactionListItem[]> {
  const params = new URLSearchParams();
  params.set(
    "select",
    "id,title,merchant_name,account_id,transaction_kind,original_amount_minor,original_currency,sgd_amount_minor,occurred_at,review_status,accounts(name),transaction_categories(name)",
  );
  if (filters.kind) params.set("transaction_kind", `eq.${filters.kind}`);
  if (filters.review) params.set("review_status", `eq.${filters.review}`);
  params.set("order", "occurred_at.desc");
  params.set("limit", "100");
  const response = await requestDataRest(session, `transactions?${params.toString()}`);
  const search = filters.search?.trim().toLocaleLowerCase();
  return extractItems(response)
    .map(parseTransaction)
    .filter((item): item is TransactionListItem => item !== null)
    .filter(
      (item) =>
        !search ||
        item.title.toLocaleLowerCase().includes(search) ||
        item.merchant_name?.toLocaleLowerCase().includes(search),
    );
}

export async function listOwnedAccounts(session: Session): Promise<OwnedAccountOption[]> {
  const params = new URLSearchParams({
    select: "id,name,institution_name",
    deleted_at: "is.null",
    order: "sort_order.asc,name.asc",
  });
  const response = await requestDataRest(session, `accounts?${params.toString()}`);
  return extractItems(response)
    .map(parseOwnedAccount)
    .filter((item): item is OwnedAccountOption => item !== null);
}

export async function listTransactionsForAccount(
  session: Session,
  accountId: string,
): Promise<TransactionListItem[]> {
  const params = new URLSearchParams({
    select:
      "id,title,merchant_name,account_id,transaction_kind,original_amount_minor,original_currency,sgd_amount_minor,occurred_at,review_status,accounts(name),transaction_categories(name)",
    account_id: `eq.${accountId}`,
    order: "occurred_at.desc",
    limit: "100",
  });
  const response = await requestDataRest(session, `transactions?${params.toString()}`);
  return extractItems(response)
    .map(parseTransaction)
    .filter((item): item is TransactionListItem => item !== null);
}

export async function listSources(
  session: Session,
  status: "dangling" | "review",
): Promise<SourceSummary[]> {
  const response = await request(session, `/v1/transactions/sources?status=${status}`);
  return extractItems(response)
    .map(parseSource)
    .filter((item): item is SourceSummary => item !== null);
}

export async function startGmailSync(session: Session): Promise<TransactionSyncRun> {
  const response = await request(session, "/v1/transactions/gmail/sync-runs", {
    method: "POST",
  });
  const run = parseSyncRun(response);
  if (!run) throw new TransactionApiError("The sync service returned an invalid response.", 502);
  return run;
}

export async function getSyncRun(session: Session, id: string): Promise<TransactionSyncRun> {
  const response = await request(session, `/v1/transactions/sync-runs/${encodeURIComponent(id)}`);
  const run = parseSyncRun(response);
  if (!run) throw new TransactionApiError("The sync service returned an invalid response.", 502);
  return run;
}

export async function getSanitizedEmail(
  session: Session,
  sourceId: string,
): Promise<SanitizedEmail> {
  const response = unwrapData(
    await request(session, `/v1/transactions/sources/${encodeURIComponent(sourceId)}/email`),
  );
  if (!isRecord(response)) {
    throw new TransactionApiError("The source service returned an invalid response.", 502);
  }
  const html = stringValue(response.html);
  if (!html) throw new TransactionApiError("This source does not contain displayable email content.", 404);
  return { subject: stringValue(response.subject, "Email source"), html };
}

export async function getTransactionSources(
  session: Session,
  transactionId: string,
): Promise<SourceSummary[]> {
  const response = await request(
    session,
    `/v1/transactions/${encodeURIComponent(transactionId)}/sources`,
  );
  return extractItems(response)
    .map(parseSource)
    .filter((item): item is SourceSummary => item !== null);
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

export async function unmatchSourceLink(session: Session, sourceLinkId: string): Promise<void> {
  await request(
    session,
    `/v1/transactions/source-links/${encodeURIComponent(sourceLinkId)}/unmatch`,
    { method: "POST" },
  );
}
