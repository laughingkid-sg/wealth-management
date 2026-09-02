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
  account_id?: string;
  occurred_at?: string;
  original_amount_minor?: MinorUnitAmount;
  original_currency?: string;
  sgd_amount_minor?: MinorUnitAmount | null;
  category_id?: string | null;
  line_items?: TransactionLineItem[];
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
