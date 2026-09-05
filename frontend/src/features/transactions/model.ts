export type TransactionKind = "debit" | "credit";
export type TransactionReviewStatus = "confirmed" | "review_required" | "pending";
export type SourceStatus =
  | "pending"
  | "parsing"
  | "dangling"
  | "review_required"
  | "parsed"
  | "failed";
export type SourceQueue = "dangling" | "review" | "failed";
export type SyncRunStatus = "queued" | "running" | "completed" | "failed";
export type MinorUnitAmount = string;

export type JsonValue =
  | string
  | number
  | boolean
  | null
  | JsonValue[]
  | { [key: string]: JsonValue };

export interface TransactionLineItem {
  schema_version: 1;
  description: string;
  quantity: number;
  unit_price_minor?: MinorUnitAmount;
  line_total_minor?: MinorUnitAmount;
  tax_minor?: MinorUnitAmount;
  discount_minor?: MinorUnitAmount;
  currency: string;
  details: { [key: string]: JsonValue };
}

export interface InternalTransferLink {
  id: string;
  counterpart_transaction_id: string;
  counterpart_title: string | null;
  counterpart_account_name: string | null;
}

export interface TransactionListItem {
  id: string;
  title: string;
  merchant_name: string | null;
  account_id: string;
  account_name: string;
  transaction_kind: TransactionKind;
  original_amount_minor: MinorUnitAmount;
  original_currency: string;
  sgd_amount_minor: MinorUnitAmount | null;
  occurred_at: string;
  category_id: string | null;
  category_name: string | null;
  category_parent_name: string | null;
  line_items: TransactionLineItem[];
  details: { [key: string]: JsonValue };
  user_notes: string | null;
  review_status: TransactionReviewStatus;
  match_confidence: number | null;
  source_count: number;
  transfer_link: InternalTransferLink | null;
}

export interface SourceSummary {
  id: string;
  source_link_id: string | null;
  source_type: "gmail_email" | "phone_notification";
  provider: string;
  subject: string | null;
  sender: string | null;
  received_at: string;
  parse_status: SourceStatus;
  parse_confidence: number | null;
  parse_error: string | null;
  reconciliation_reason: string | null;
  suggested_title: string | null;
  suggested_amount_minor: MinorUnitAmount | null;
  suggested_currency: string | null;
  suggested_account_id: string | null;
  suggested_account_name: string | null;
  suggested_transaction_id: string | null;
  suggested_category_leaf_name: string | null;
}

export interface SourceAttachment {
  filename: string;
  mime_type: string;
  byte_size: number;
  parse_eligible: boolean;
  storage_status: string;
  signed_url: string | null;
}

export interface OwnedAccountOption {
  id: string;
  name: string;
  institution_name: string;
}

export interface TransactionCategory {
  id: string;
  parent_name: string;
  name: string;
  emoji: string;
  sort_order: number;
}

export interface GmailConnection {
  connected: boolean;
  status: string | null;
  email: string | null;
  last_synced_at: string | null;
  last_error: string | null;
}

export interface TransactionSyncRun {
  id: string;
  status: SyncRunStatus;
  messages_discovered: number;
  messages_ingested: number;
  sources_parsed: number;
  sources_failed: number;
  transactions_created: number;
  sources_review: number;
  sources_dangling: number;
  error_summary: string | null;
  started_at: string | null;
  ingestion_completed_at: string | null;
  completed_at: string | null;
}

export interface TransactionFilters {
  search?: string;
  kind?: TransactionKind;
  review?: TransactionReviewStatus;
}

export interface CursorPage<T> {
  items: T[];
  next_cursor: string | null;
}

export interface TransactionPatch {
  title?: string;
  merchant_name?: string | null;
  account_id?: string;
  occurred_at?: string;
  original_amount_minor?: MinorUnitAmount;
  original_currency?: string;
  sgd_amount_minor?: MinorUnitAmount | null;
  category_id?: string | null;
  line_items?: TransactionLineItem[];
  user_notes?: string | null;
}

export interface ManualTransactionInput {
  account_id: string;
  transaction_kind: TransactionKind;
  title: string;
  merchant_name: string | null;
  original_amount_minor: MinorUnitAmount;
  original_currency: string;
  sgd_amount_minor: MinorUnitAmount | null;
  occurred_at: string;
  category_id: string | null;
  line_items: TransactionLineItem[];
  user_notes: string | null;
}

export interface TransferLegInput {
  title: string;
  account_id: string;
  original_amount_minor: MinorUnitAmount;
  original_currency: string;
  sgd_amount_minor: MinorUnitAmount | null;
  occurred_at: string;
  category_id: string | null;
  line_items: TransactionLineItem[];
  source_ids: string[];
}

export interface InternalTransferInput {
  debit: TransferLegInput;
  credit: TransferLegInput;
}

export type TransferSourceRole = "debit" | "credit" | "both";

export type MatchingKeyType = "card_last_four" | "bank_account_suffix";
export type SenderMatchType = "exact" | "domain" | "regex";

export interface AccountMatchingKey {
  id: string;
  account_id: string;
  account_name: string;
  key_type: MatchingKeyType;
  display_value: string;
  normalized_value: string;
  active: boolean;
  retired_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface SourceParserRule {
  id: string;
  name: string;
  provider: "gmail";
  sender_match_type: SenderMatchType;
  sender_match_value: string;
  subject_matcher: string | null;
  content_matcher: string | null;
  prompt_fragment: string;
  priority: number;
  active: boolean;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface TransactionSettings {
  default_instructions: string;
  default_instructions_version: number;
  source_rules: SourceParserRule[];
  matching_keys: AccountMatchingKey[];
}

export interface GlobalSourceParserRule {
  id: string;
  name: string;
  provider: "gmail";
  sender_matcher: string | null;
  content_matcher: string | null;
  prompt_fragment: string;
  version: number;
  priority: number;
  active: boolean;
  updated_by_user_id: string | null;
  created_at: string;
  updated_at: string;
}

export interface GlobalTransactionSettings {
  rules: GlobalSourceParserRule[];
}

export interface GlobalSourceParserRuleInput {
  name: string;
  provider: "gmail";
  sender_matcher: string | null;
  content_matcher: string | null;
  prompt_fragment: string;
  priority: number;
  active: boolean;
}

export interface PromptPreviewSource {
  id: string;
  subject: string | null;
  sender: string | null;
  received_at: string;
  parse_status: SourceStatus;
}

export type PromptPreviewMode = "manual" | "automatic";

export interface ManualPromptPreviewInput {
  mode: "manual";
  global_rule_id?: string;
  include_user_default: boolean;
  user_rule_id?: string;
}

export interface AutomaticPromptPreviewInput {
  mode: "automatic";
  data_source_id: string;
}

export type PromptPreviewInput = ManualPromptPreviewInput | AutomaticPromptPreviewInput;

export interface QwenPromptPreviewTextPart {
  type: "text";
  text: "<EMAIL CONTENT OMITTED FROM PREVIEW>";
}

export interface QwenPromptPreviewImagePart {
  type: "image_url";
  image_url: {
    url: "<ELIGIBLE RECEIPT OR INVOICE IMAGE OMITTED FROM PREVIEW>";
  };
}

export interface QwenPromptPreviewRequest {
  model: "qwen3.8-flash";
  messages: [
    { role: "system"; content: string },
    {
      role: "user";
      content: [QwenPromptPreviewTextPart] | [QwenPromptPreviewTextPart, QwenPromptPreviewImagePart];
    },
  ];
  response_format: {
    type: "json_object";
  };
  enable_thinking: false;
}

export interface PromptPreviewResult {
  mode: PromptPreviewMode;
  assembled_system_prompt: string;
  prompt_components: { [key: string]: JsonValue };
  provider_request: QwenPromptPreviewRequest;
  selected_source: PromptPreviewSource | null;
  selection: { [key: string]: JsonValue } | null;
}

export interface DefaultParserInstructions {
  default_instructions: string;
  default_instructions_version: number;
}

export interface AccountMatchingKeyInput {
  account_id: string;
  key_type: MatchingKeyType;
  display_value: string;
}

export interface SourceParserRuleInput {
  name: string;
  provider: "gmail";
  sender_match_type: SenderMatchType;
  sender_match_value: string;
  subject_matcher: string | null;
  content_matcher: string | null;
  prompt_fragment: string;
  priority: number;
  active: boolean;
}

export type ParseValidationStatus = "pending" | "valid" | "invalid" | "failed";
export type SourceDebugField =
  | "request_metadata"
  | "parsed_candidate"
  | "assembled_system_prompt"
  | "normalized_input"
  | "provider_request"
  | "provider_response"
  | "model_output"
  | "prompt_components";

export interface SourceParseDebugAttempt {
  id: string;
  parser_rule_id: string | null;
  parser_rule_version: number | null;
  user_parser_rule_id: string | null;
  user_parser_rule_version: number | null;
  model_name: string | null;
  request_metadata: { [key: string]: JsonValue };
  parsed_candidate: { [key: string]: JsonValue } | null;
  assembled_system_prompt: string | null;
  normalized_input: string | null;
  provider_request: string | null;
  provider_response: string | null;
  model_output: string | null;
  prompt_components: { [key: string]: JsonValue };
  validation_status: ParseValidationStatus;
  error_summary: string | null;
  started_at: string | null;
  completed_at: string | null;
  created_at: string;
  truncated_fields: SourceDebugField[];
}

export interface SourceParseDebug {
  source_id: string;
  attempts: SourceParseDebugAttempt[];
  has_more: boolean;
  truncated: boolean;
}

export interface SourceDeletionResult {
  status: "completed" | "cleanup_pending";
  cleanup_pending: boolean;
}

export interface ExactSourceDebugField {
  source_id: string;
  attempt_id: string;
  field: SourceDebugField;
  value: string | null;
  max_bytes: number;
}

export interface InternalTransferSourceSeed {
  id: string;
  title: string;
  role: TransferSourceRole;
}

const currencyFractionDigits = new Map<string, number>();
const currencyDisplayNames = new Intl.DisplayNames(["en"], {
  type: "currency",
  fallback: "none",
});

export function isISO4217Currency(currency: string): boolean {
  return /^[A-Z]{3}$/.test(currency) && currencyDisplayNames.of(currency) !== undefined;
}

export function fractionDigitsForCurrency(currency: string): number {
  const normalized = currency.toUpperCase();
  const cached = currencyFractionDigits.get(normalized);
  if (cached !== undefined) return cached;
  let digits = 2;
  try {
    digits = new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: normalized,
    }).resolvedOptions().maximumFractionDigits ?? 2;
  } catch {
    // Unknown future currency codes retain the conventional two-decimal display.
  }
  currencyFractionDigits.set(normalized, digits);
  return digits;
}

// PostgREST serializes PostgreSQL bigint columns as JSON numbers in returned
// representations. Keep browser-created amounts in the lossless JSON integer
// range until that read contract returns minor units as strings.
const maxSafeMinorUnitAmount = BigInt(Number.MAX_SAFE_INTEGER);

function normalizedCurrencyFractionDigits(currency: string): number {
  const normalized = currency.trim().toUpperCase();
  if (!isISO4217Currency(normalized)) {
    throw new Error("Currency must be a valid three-letter ISO 4217 code.");
  }
  return fractionDigitsForCurrency(normalized);
}

export function majorAmountToMinor(
  major: string,
  currency: string,
  allowZero = false,
): MinorUnitAmount {
  const digits = normalizedCurrencyFractionDigits(currency);
  const normalized = major.trim();
  if (normalized.length > 64) {
    throw new Error("Amount is too large to transfer safely through the browser.");
  }
  if (!/^\d+(?:\.\d+)?$/.test(normalized)) {
    throw new Error("Use digits and an optional decimal point without grouping separators or exponents.");
  }
  const [whole, suppliedFraction = ""] = normalized.split(".");
  if (suppliedFraction.length > digits) {
    throw new Error(
      `${currency.trim().toUpperCase()} supports at most ${digits} decimal place${digits === 1 ? "" : "s"}.`,
    );
  }
  const fraction = suppliedFraction.padEnd(digits, "0");
  const minor = BigInt(whole) * 10n ** BigInt(digits) + BigInt(fraction || "0");
  if ((!allowZero && minor === 0n) || minor < 0n) {
    throw new Error(allowZero ? "Amount cannot be negative." : "Amount must be greater than zero.");
  }
  if (minor > maxSafeMinorUnitAmount) {
    throw new Error("Amount is too large to transfer safely through the browser.");
  }
  return minor.toString();
}

export function minorAmountToMajor(
  minor: MinorUnitAmount,
  currency: string,
): string {
  const digits = normalizedCurrencyFractionDigits(currency);
  if (!/^\d+$/.test(minor)) {
    throw new Error("Minor-unit amount must be a non-negative integer string.");
  }
  if (minor.length > 19) {
    throw new Error("Minor-unit amount is outside the supported database range.");
  }
  const value = BigInt(minor);
  if (digits === 0) return value.toString();
  const divisor = 10n ** BigInt(digits);
  return `${value / divisor}.${(value % divisor).toString().padStart(digits, "0")}`;
}

export function formatAmount(minor: MinorUnitAmount, currency: string): string {
  if (!/^\d+$/.test(minor)) throw new Error("Invalid minor-unit amount");
  const normalizedCurrency = currency.toUpperCase();
  const digits = fractionDigitsForCurrency(normalizedCurrency);
  const divisor = 10n ** BigInt(digits);
  const value = BigInt(minor);
  const whole = value / divisor;
  const fraction = (value % divisor).toString().padStart(digits, "0");
  const formattedWhole = new Intl.NumberFormat(undefined, {
    maximumFractionDigits: 0,
  }).format(whole);
  return `${normalizedCurrency} ${formattedWhole}${digits ? `.${fraction}` : ""}`;
}

export function formatDateTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "Invalid date";
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}

export function toDateTimeLocal(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const component = (part: number) => String(part).padStart(2, "0");
  return `${date.getFullYear()}-${component(date.getMonth() + 1)}-${component(date.getDate())}T${component(date.getHours())}:${component(date.getMinutes())}`;
}

export function toRFC3339(value: string): string | null {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date.toISOString();
}

export interface ScriptSummary {
  script_key: string;
  active_version: number;
  version_count: number;
}

export interface ScriptVersion {
  script_key: string;
  version: number;
  source: string;
  checksum: string;
  is_active: boolean;
  notes: string;
  created_at: string;
  updated_at: string;
}

export interface SourceCandidate {
  id: string;
  output_ordinal: number;
  status: string;
  transaction_kind: TransactionKind;
  title: string;
  merchant_name: string;
  original_amount_minor: number;
  original_currency: string;
  occurred_at: string;
  suggested_account_id: string | null;
  suggested_transaction_id: string | null;
  reconciliation_reason: string;
  match_confidence: number | null;
  transaction_id: string | null;
}
