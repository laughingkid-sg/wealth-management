export type TransactionKind = "debit" | "credit";
export type TransactionReviewStatus = "confirmed" | "review_required" | "pending";
export type SourceStatus = "dangling" | "review_required" | "parsed" | "failed";
export type SyncRunStatus = "queued" | "running" | "completed" | "failed";
export type MinorUnitAmount = string;

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
  category_name: string | null;
  review_status: TransactionReviewStatus;
  source_count: number;
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
  parse_error: string | null;
  suggested_title: string | null;
  suggested_amount_minor: MinorUnitAmount | null;
  suggested_currency: string | null;
  suggested_account_name: string | null;
}

export interface OwnedAccountOption {
  id: string;
  name: string;
  institution_name: string;
}

export interface TransactionSyncRun {
  id: string;
  status: SyncRunStatus;
  messages_discovered: number;
  messages_ingested: number;
  sources_parsed: number;
  transactions_created: number;
  sources_review: number;
  sources_dangling: number;
  error_summary: string | null;
  started_at: string | null;
  completed_at: string | null;
}

export interface TransactionFilters {
  search?: string;
  kind?: TransactionKind;
  review?: TransactionReviewStatus;
}

export function formatAmount(minor: MinorUnitAmount, currency: string): string {
  const value = BigInt(minor);
  const whole = value / 100n;
  const fraction = (value % 100n).toString().padStart(2, "0");
  const formattedWhole = new Intl.NumberFormat(undefined, {
    maximumFractionDigits: 0,
  }).format(whole);
  return `${currency} ${formattedWhole}.${fraction}`;
}

export function formatDateTime(value: string): string {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}
