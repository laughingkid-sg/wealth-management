import type { Session } from "@supabase/supabase-js";
import type {
  AccountMatchingKey,
  AccountMatchingKeyInput,
  CursorPage,
  DefaultParserInstructions,
  ExactSourceDebugField,
  GmailConnection,
  InternalTransferInput,
  JsonValue,
  MinorUnitAmount,
  OwnedAccountOption,
  SourceAttachment,
  SourceParserRule,
  SourceParserRuleInput,
  SourceParseDebug,
  SourceParseDebugAttempt,
  SourceDeletionResult,
  SourceDebugField,
  SourceQueue,
  SourceStatus,
  SourceSummary,
  SyncRunStatus,
  TransactionCategory,
  TransactionFilters,
  TransactionKind,
  TransactionLineItem,
  TransactionListItem,
  TransactionPatch,
  TransactionReviewStatus,
  TransactionSettings,
  TransactionSyncRun,
} from "./model";
import { isISO4217Currency } from "./model";

export interface SanitizedEmail {
  subject: string;
  html: string | null;
  text: string | null;
}

const apiBaseUrl = (import.meta.env.VITE_API_BASE_URL ?? "/api").replace(/\/$/, "");
const supabaseUrl = import.meta.env.VITE_SUPABASE_URL?.replace(/\/$/, "");
const supabasePublishableKey = import.meta.env.VITE_SUPABASE_PUBLISHABLE_KEY;
const pageSize = 50;

export class TransactionApiError extends Error {
  readonly status: number;
  readonly code: string | null;

  constructor(message: string, status: number, code: string | null = null) {
    super(message);
    this.name = "TransactionApiError";
    this.status = status;
    this.code = code;
  }
}

type JsonRecord = Record<string, unknown>;

function contractError(message: string): never {
  throw new TransactionApiError(`Invalid transaction service response: ${message}`, 502);
}

function isRecord(value: unknown): value is JsonRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function unwrapData(value: unknown): unknown {
  return isRecord(value) && "data" in value ? value.data : value;
}

function requiredRecord(value: unknown, field: string): JsonRecord {
  if (!isRecord(value)) contractError(`${field} must be an object`);
  return value;
}

function requiredString(value: unknown, field: string): string {
  if (typeof value !== "string" || value.trim() === "") {
    contractError(`${field} must be a non-empty string`);
  }
  return value;
}

function stringValue(value: unknown, field: string): string {
  if (typeof value !== "string") contractError(`${field} must be a string`);
  return value;
}

function optionalString(value: unknown, field: string): string | null {
  if (value === undefined || value === null || value === "") return null;
  if (typeof value !== "string") contractError(`${field} must be a string or null`);
  return value;
}

function nullableStringValue(value: unknown, field: string): string | null {
  if (value === null) return null;
  if (typeof value !== "string") contractError(`${field} must be a string or null`);
  return value;
}

function requiredBoolean(value: unknown, field: string): boolean {
  if (typeof value !== "boolean") contractError(`${field} must be a boolean`);
  return value;
}

const sourceDebugFields = [
  "request_metadata",
  "parsed_candidate",
  "assembled_system_prompt",
  "normalized_input",
  "provider_request",
  "provider_response",
  "model_output",
  "prompt_components",
] as const satisfies readonly SourceDebugField[];

function sourceDebugFieldArray(value: unknown, field: string): SourceDebugField[] {
  if (!Array.isArray(value)) contractError(`${field} must be an array`);
  return value.map((item, index) =>
    enumValue(item, sourceDebugFields, `${field}[${index}]`),
  );
}

function requiredInteger(value: unknown, field: string, minimum = 0): number {
  if (!Number.isSafeInteger(value) || (value as number) < minimum) {
    contractError(`${field} must be a safe integer of at least ${minimum}`);
  }
  return value as number;
}

function optionalInteger(value: unknown, field: string, minimum = 0): number | null {
  if (value === undefined || value === null) return null;
  return requiredInteger(value, field, minimum);
}

function requiredInt32(value: unknown, field: string): number {
  if (!Number.isSafeInteger(value) || (value as number) < -2147483648 || (value as number) > 2147483647) {
    contractError(`${field} must be a 32-bit integer`);
  }
  return value as number;
}

function optionalPercentage(value: unknown, field: string): number | null {
  const result = optionalInteger(value, field);
  if (result !== null && result > 100) contractError(`${field} must be between 0 and 100`);
  return result;
}

type MinorAmountParser = (
  value: unknown,
  field: string,
  allowZero?: boolean,
) => MinorUnitAmount;

function minorAmount(value: unknown, field: string, allowZero = false): MinorUnitAmount {
  if (typeof value !== "string" || !/^\d+$/.test(value)) {
    contractError(`${field} must be a decimal integer string`);
  }
  if (!allowZero && BigInt(value) === 0n) contractError(`${field} must be positive`);
  return value;
}

function normalizeDataRestMinorAmount(
  value: unknown,
  field: string,
  allowZero = false,
): MinorUnitAmount {
  if (typeof value === "number") {
    if (!Number.isSafeInteger(value) || value < 0) {
      contractError(`${field} from Data REST is outside the safe integer range`);
    }
    return minorAmount(String(value), field, allowZero);
  }
  return minorAmount(value, field, allowZero);
}

function optionalMinorAmount(
  value: unknown,
  field: string,
  allowZero = false,
  parser: MinorAmountParser = minorAmount,
): MinorUnitAmount | null {
  if (value === undefined || value === null || value === "") return null;
  return parser(value, field, allowZero);
}

function requiredDate(value: unknown, field: string): string {
  const result = requiredString(value, field);
  if (Number.isNaN(new Date(result).getTime())) contractError(`${field} must be a valid timestamp`);
  return result;
}

function optionalDate(value: unknown, field: string): string | null {
  const result = optionalString(value, field);
  if (result !== null && Number.isNaN(new Date(result).getTime())) {
    contractError(`${field} must be a valid timestamp or null`);
  }
  return result;
}

function requiredCurrency(value: unknown, field: string): string {
  const result = requiredString(value, field);
  if (!isISO4217Currency(result)) contractError(`${field} must be an uppercase ISO 4217 code`);
  return result;
}

function enumValue<T extends string>(value: unknown, accepted: readonly T[], field: string): T {
  if (typeof value !== "string" || !accepted.includes(value as T)) {
    contractError(`${field} has an unsupported value`);
  }
  return value as T;
}

function jsonValue(value: unknown, field: string): JsonValue {
  if (
    value === null ||
    typeof value === "string" ||
    typeof value === "boolean" ||
    (typeof value === "number" && Number.isFinite(value))
  ) {
    return value;
  }
  if (Array.isArray(value)) return value.map((item, index) => jsonValue(item, `${field}[${index}]`));
  if (isRecord(value)) {
    return Object.fromEntries(
      Object.entries(value).map(([key, item]) => [key, jsonValue(item, `${field}.${key}`)]),
    );
  }
  contractError(`${field} is not valid JSON`);
}

function jsonObject(value: unknown, field: string): { [key: string]: JsonValue } {
  const parsed = jsonValue(value, field);
  if (!isRecord(parsed)) contractError(`${field} must be a JSON object`);
  return parsed as { [key: string]: JsonValue };
}

function optionalJsonObject(
  value: unknown,
  field: string,
): { [key: string]: JsonValue } | null {
  if (value === undefined || value === null) return null;
  return jsonObject(value, field);
}

function relation(value: unknown): JsonRecord | null {
  if (isRecord(value)) return value;
  if (Array.isArray(value) && value.length > 0 && isRecord(value[0])) return value[0];
  return null;
}

function parseLineItem(
  value: unknown,
  index: number,
  moneyParser: MinorAmountParser = minorAmount,
): TransactionLineItem {
  const item = requiredRecord(value, `line_items[${index}]`);
  if (item.schema_version !== 1) contractError(`line_items[${index}].schema_version must be 1`);
  const details = jsonValue(item.details ?? {}, `line_items[${index}].details`);
  if (!isRecord(details)) contractError(`line_items[${index}].details must be an object`);
  const result: TransactionLineItem = {
    schema_version: 1,
    description: requiredString(item.description, `line_items[${index}].description`),
    quantity: requiredInteger(item.quantity, `line_items[${index}].quantity`, 1),
    currency: requiredCurrency(item.currency, `line_items[${index}].currency`),
    details: details as { [key: string]: JsonValue },
  };
  for (const key of [
    "unit_price_minor",
    "line_total_minor",
    "tax_minor",
    "discount_minor",
  ] as const) {
    const amount = optionalMinorAmount(
      item[key],
      `line_items[${index}].${key}`,
      true,
      moneyParser,
    );
    if (amount !== null) result[key] = amount;
  }
  return result;
}

function parseTransaction(
  value: unknown,
  requireSourceCount = false,
  moneyParser: MinorAmountParser = minorAmount,
): TransactionListItem {
  const item = requiredRecord(value, "transaction");
  const accountRelation = relation(item.accounts);
  const categoryRelation = relation(item.transaction_categories);
  const lineItems = item.line_items ?? [];
  if (!Array.isArray(lineItems)) contractError("transaction.line_items must be an array");

  let transferLink = null;
  const nestedTransfer = relation(item.transfer_link);
  const flatLinkID = optionalString(item.transfer_link_id, "transaction.transfer_link_id");
  if (nestedTransfer || flatLinkID) {
    const transfer = nestedTransfer ?? item;
    transferLink = {
      id: requiredString(
        nestedTransfer ? transfer.id ?? transfer.link_id : flatLinkID,
        "transaction.transfer_link.id",
      ),
      counterpart_transaction_id: requiredString(
        nestedTransfer
          ? transfer.counterpart_transaction_id
          : item.transfer_counterpart_transaction_id,
        "transaction.transfer_link.counterpart_transaction_id",
      ),
      counterpart_title: optionalString(
        nestedTransfer ? transfer.counterpart_title : item.transfer_counterpart_title,
        "transaction.transfer_link.counterpart_title",
      ),
      counterpart_account_name: optionalString(
        nestedTransfer ? transfer.counterpart_account_name : item.transfer_counterpart_account_name,
        "transaction.transfer_link.counterpart_account_name",
      ),
    };
  }

  return {
    id: requiredString(item.id, "transaction.id"),
    title: requiredString(item.title, "transaction.title"),
    merchant_name: optionalString(item.merchant_name, "transaction.merchant_name"),
    account_id: requiredString(item.account_id, "transaction.account_id"),
    account_name: requiredString(
      item.account_name ?? accountRelation?.name,
      "transaction.account_name",
    ),
    transaction_kind: enumValue<TransactionKind>(
      item.transaction_kind,
      ["debit", "credit"],
      "transaction.transaction_kind",
    ),
    original_amount_minor: moneyParser(
      item.original_amount_minor,
      "transaction.original_amount_minor",
    ),
    original_currency: requiredCurrency(
      item.original_currency,
      "transaction.original_currency",
    ),
    sgd_amount_minor: optionalMinorAmount(
      item.sgd_amount_minor,
      "transaction.sgd_amount_minor",
      false,
      moneyParser,
    ),
    occurred_at: requiredDate(item.occurred_at, "transaction.occurred_at"),
    category_id: optionalString(item.category_id, "transaction.category_id"),
    category_name: optionalString(
      item.category_name ?? categoryRelation?.name,
      "transaction.category_name",
    ),
    category_parent_name: optionalString(
      item.category_parent_name ?? categoryRelation?.parent_name,
      "transaction.category_parent_name",
    ),
    line_items: lineItems.map((lineItem, index) => parseLineItem(lineItem, index, moneyParser)),
    review_status: enumValue<TransactionReviewStatus>(
      item.review_status,
      ["confirmed", "review_required", "pending"],
      "transaction.review_status",
    ),
    match_confidence: optionalPercentage(
      item.match_confidence,
      "transaction.match_confidence",
    ),
    source_count: item.source_count === undefined
      ? requireSourceCount
        ? contractError("transaction.source_count is required")
        : 0
      : requiredInteger(item.source_count, "transaction.source_count"),
    transfer_link: transferLink,
  };
}

function parseDataRestTransaction(value: unknown): TransactionListItem {
  return parseTransaction(value, false, normalizeDataRestMinorAmount);
}

export function parseSourceSummary(value: unknown): SourceSummary {
  const item = requiredRecord(value, "source");
  return {
    id: requiredString(item.id, "source.id"),
    source_link_id: optionalString(
      item.source_link_id ?? item.link_id ?? item.transaction_data_source_id,
      "source.source_link_id",
    ),
    source_type: enumValue(
      item.source_type,
      ["gmail_email", "phone_notification"],
      "source.source_type",
    ),
    provider: requiredString(item.provider, "source.provider"),
    subject: optionalString(item.subject, "source.subject"),
    sender: optionalString(item.sender, "source.sender"),
    received_at: requiredDate(item.received_at, "source.received_at"),
    parse_status: enumValue<SourceStatus>(
      item.parse_status,
      ["pending", "parsing", "parsed", "review_required", "dangling", "failed"],
      "source.parse_status",
    ),
    parse_confidence: optionalPercentage(item.parse_confidence, "source.parse_confidence"),
    parse_error: optionalString(item.parse_error, "source.parse_error"),
    reconciliation_reason: optionalString(
      item.reconciliation_reason,
      "source.reconciliation_reason",
    ),
    suggested_title: optionalString(item.suggested_title, "source.suggested_title"),
    suggested_amount_minor: optionalMinorAmount(
      item.suggested_amount_minor,
      "source.suggested_amount_minor",
    ),
    suggested_currency:
      item.suggested_currency === undefined || item.suggested_currency === null
        ? null
        : requiredCurrency(item.suggested_currency, "source.suggested_currency"),
    suggested_account_id: optionalString(
      item.suggested_account_id,
      "source.suggested_account_id",
    ),
    suggested_account_name: optionalString(
      item.suggested_account_name,
      "source.suggested_account_name",
    ),
    suggested_transaction_id: optionalString(
      item.suggested_transaction_id,
      "source.suggested_transaction_id",
    ),
    suggested_category_leaf_name: optionalString(
      item.suggested_category_leaf_name,
      "source.suggested_category_leaf_name",
    ),
  };
}

export function parseSyncRunResponse(value: unknown): TransactionSyncRun {
  const run = requiredRecord(unwrapData(value), "sync_run");
  return {
    id: requiredString(run.id, "sync_run.id"),
    status: enumValue<SyncRunStatus>(
      run.status,
      ["queued", "running", "completed", "failed"],
      "sync_run.status",
    ),
    messages_discovered: requiredInteger(run.messages_discovered, "sync_run.messages_discovered"),
    messages_ingested: requiredInteger(run.messages_ingested, "sync_run.messages_ingested"),
    sources_parsed: requiredInteger(run.sources_parsed, "sync_run.sources_parsed"),
    sources_failed: requiredInteger(run.sources_failed, "sync_run.sources_failed"),
    transactions_created: requiredInteger(
      run.transactions_created,
      "sync_run.transactions_created",
    ),
    sources_review: requiredInteger(run.sources_review, "sync_run.sources_review"),
    sources_dangling: requiredInteger(run.sources_dangling, "sync_run.sources_dangling"),
    error_summary: optionalString(run.error_summary, "sync_run.error_summary"),
    started_at: optionalDate(run.started_at, "sync_run.started_at"),
    ingestion_completed_at: optionalDate(
      run.ingestion_completed_at,
      "sync_run.ingestion_completed_at",
    ),
    completed_at: optionalDate(run.completed_at, "sync_run.completed_at"),
  };
}

function parseAccountMatchingKey(value: unknown, field = "matching_key"): AccountMatchingKey {
  const item = requiredRecord(value, field);
  return {
    id: requiredString(item.id, `${field}.id`),
    account_id: requiredString(item.account_id, `${field}.account_id`),
    account_name: requiredString(item.account_name, `${field}.account_name`),
    key_type: enumValue(item.key_type, ["card_last_four", "bank_account_suffix"], `${field}.key_type`),
    display_value: requiredString(item.display_value, `${field}.display_value`),
    normalized_value: requiredString(item.normalized_value, `${field}.normalized_value`),
    active: requiredBoolean(item.active, `${field}.active`),
    retired_at: optionalDate(item.retired_at, `${field}.retired_at`),
    created_at: requiredDate(item.created_at, `${field}.created_at`),
    updated_at: requiredDate(item.updated_at, `${field}.updated_at`),
  };
}

function parseSourceParserRule(value: unknown, field = "source_rule"): SourceParserRule {
  const item = requiredRecord(value, field);
  return {
    id: requiredString(item.id, `${field}.id`),
    name: requiredString(item.name, `${field}.name`),
    provider: enumValue(item.provider, ["gmail"], `${field}.provider`),
    sender_match_type: enumValue(
      item.sender_match_type,
      ["exact", "domain", "regex"],
      `${field}.sender_match_type`,
    ),
    sender_match_value: requiredString(item.sender_match_value, `${field}.sender_match_value`),
    subject_matcher: optionalString(item.subject_matcher, `${field}.subject_matcher`),
    content_matcher: optionalString(item.content_matcher, `${field}.content_matcher`),
    prompt_fragment: stringValue(item.prompt_fragment, `${field}.prompt_fragment`),
    priority: requiredInt32(item.priority, `${field}.priority`),
    active: requiredBoolean(item.active, `${field}.active`),
    version: requiredInteger(item.version, `${field}.version`, 1),
    created_at: requiredDate(item.created_at, `${field}.created_at`),
    updated_at: requiredDate(item.updated_at, `${field}.updated_at`),
  };
}

export function parseTransactionSettings(value: unknown): TransactionSettings {
  const settings = requiredRecord(unwrapData(value), "transaction_settings");
  if (!Array.isArray(settings.source_rules)) {
    contractError("transaction_settings.source_rules must be an array");
  }
  if (!Array.isArray(settings.matching_keys)) {
    contractError("transaction_settings.matching_keys must be an array");
  }
  return {
    default_instructions: stringValue(
      settings.default_instructions,
      "transaction_settings.default_instructions",
    ),
    default_instructions_version: requiredInteger(
      settings.default_instructions_version,
      "transaction_settings.default_instructions_version",
    ),
    source_rules: settings.source_rules.map((item, index) =>
      parseSourceParserRule(item, `transaction_settings.source_rules[${index}]`),
    ),
    matching_keys: settings.matching_keys.map((item, index) =>
      parseAccountMatchingKey(item, `transaction_settings.matching_keys[${index}]`),
    ),
  };
}

function parseSourceDebugAttempt(value: unknown, index: number): SourceParseDebugAttempt {
  const field = `source_debug.attempts[${index}]`;
  const item = requiredRecord(value, field);
  return {
    id: requiredString(item.id, `${field}.id`),
    parser_rule_id: optionalString(item.parser_rule_id, `${field}.parser_rule_id`),
    parser_rule_version: optionalInteger(
      item.parser_rule_version,
      `${field}.parser_rule_version`,
      1,
    ),
    user_parser_rule_id: optionalString(
      item.user_parser_rule_id,
      `${field}.user_parser_rule_id`,
    ),
    user_parser_rule_version: optionalInteger(
      item.user_parser_rule_version,
      `${field}.user_parser_rule_version`,
      1,
    ),
    model_name: optionalString(item.model_name, `${field}.model_name`),
    request_metadata: jsonObject(item.request_metadata, `${field}.request_metadata`),
    parsed_candidate: optionalJsonObject(item.parsed_candidate, `${field}.parsed_candidate`),
    assembled_system_prompt: optionalString(
      item.assembled_system_prompt,
      `${field}.assembled_system_prompt`,
    ),
    normalized_input: optionalString(item.normalized_input, `${field}.normalized_input`),
    provider_request: optionalString(item.provider_request, `${field}.provider_request`),
    provider_response: optionalString(item.provider_response, `${field}.provider_response`),
    model_output: optionalString(item.model_output, `${field}.model_output`),
    prompt_components: jsonObject(item.prompt_components, `${field}.prompt_components`),
    validation_status: enumValue(
      item.validation_status,
      ["pending", "valid", "invalid", "failed"],
      `${field}.validation_status`,
    ),
    error_summary: optionalString(item.error_summary, `${field}.error_summary`),
    started_at: optionalDate(item.started_at, `${field}.started_at`),
    completed_at: optionalDate(item.completed_at, `${field}.completed_at`),
    created_at: requiredDate(item.created_at, `${field}.created_at`),
    truncated_fields: sourceDebugFieldArray(
      item.truncated_fields,
      `${field}.truncated_fields`,
    ),
  };
}

export function parseSourceParseDebug(value: unknown): SourceParseDebug {
  const debug = requiredRecord(unwrapData(value), "source_debug");
  if (!Array.isArray(debug.attempts)) contractError("source_debug.attempts must be an array");
  return {
    source_id: requiredString(debug.source_id, "source_debug.source_id"),
    attempts: debug.attempts.map(parseSourceDebugAttempt),
    has_more: requiredBoolean(debug.has_more, "source_debug.has_more"),
    truncated: requiredBoolean(debug.truncated, "source_debug.truncated"),
  };
}

function parsePage<T>(value: unknown, parser: (item: unknown) => T, field: string): CursorPage<T> {
  const unwrapped = unwrapData(value);
  if (Array.isArray(unwrapped)) {
    return { items: unwrapped.map(parser), next_cursor: null };
  }
  const page = requiredRecord(unwrapped, field);
  if (!Array.isArray(page.items)) contractError(`${field}.items must be an array`);
  return {
    items: page.items.map(parser),
    next_cursor: optionalString(page.next_cursor, `${field}.next_cursor`),
  };
}

async function request(
  session: Session,
  path: string,
  init?: RequestInit,
): Promise<unknown> {
  const response = await fetch(`${apiBaseUrl}${path}`, {
    ...init,
    headers: {
      Authorization: `Bearer ${session.access_token}`,
      Accept: "application/json",
      ...init?.headers,
    },
  });
  const body: unknown = response.status === 204 ? null : await response.json().catch(() => null);
  if (!response.ok) {
    const error = isRecord(body) ? body : null;
    const message =
      (typeof error?.error === "string" && error.error) ||
      (typeof error?.message === "string" && error.message) ||
      "The request could not be completed.";
    const code = typeof error?.code === "string" ? error.code : null;
    throw new TransactionApiError(message, response.status, code);
  }
  return body;
}

async function requestDataRest(
  session: Session,
  path: string,
  signal?: AbortSignal,
): Promise<unknown> {
  if (!supabaseUrl || !supabasePublishableKey) {
    throw new TransactionApiError("Supabase is not configured in this frontend.", 500);
  }
  const response = await fetch(`${supabaseUrl}/rest/v1/${path}`, {
    signal,
    headers: {
      Authorization: `Bearer ${session.access_token}`,
      apikey: supabasePublishableKey,
      Accept: "application/json",
    },
  });
  const body: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    throw new TransactionApiError("Transaction reference data could not be read.", response.status);
  }
  return body;
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
    select:
      "id,title,merchant_name,account_id,transaction_kind,original_amount_minor,original_currency,sgd_amount_minor,occurred_at,category_id,line_items,review_status,match_confidence,accounts(name),transaction_categories(name,parent_name)",
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
    select:
      "id,title,merchant_name,account_id,transaction_kind,original_amount_minor,original_currency,sgd_amount_minor,occurred_at,category_id,line_items,review_status,match_confidence,accounts(name),transaction_categories(name,parent_name)",
    id: `eq.${transactionId}`,
    limit: "2",
  });
  if (accountId) params.set("account_id", `eq.${accountId}`);
  const response = await requestDataRest(session, `transactions?${params.toString()}`, signal);
  if (!Array.isArray(response)) contractError("recommended transaction must be an array");
  if (response.length > 1) contractError("recommended transaction lookup returned multiple rows");
  return response.length === 0 ? null : parseDataRestTransaction(response[0]);
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
