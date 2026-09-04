import type { Session } from "@supabase/supabase-js";

const apiBaseUrl = (import.meta.env.VITE_API_BASE_URL ?? "/api").replace(/\/$/, "");

export class FinanceApiError extends Error {
  readonly status: number;
  readonly etag: string | null;
  constructor(
    message: string,
    status: number,
    etag: string | null = null,
  ) {
    super(message);
    this.name = "FinanceApiError";
    this.status = status;
    this.etag = etag;
  }
}

export interface MinorAmount {
  currency: string;
  minor_units: string;
}

export interface AccountBalanceDto {
  account_id: string;
  account_name: string;
  state: "unconfigured" | "configured";
  side: "asset" | "liability";
  version: number;
  as_of: string | null;
  opening_balances: MinorAmount[];
  current_balances: MinorAmount[];
}

export interface BalanceRevisionDto {
  id: string;
  version: number;
  balances: MinorAmount[];
  as_of: string;
  correction_reason: string | null;
  changed_at: string;
}

export type CreditCardBillStatus = "review" | "unpaid" | "paid" | "void";
export type CreditCardLineKind = "activity" | "refund" | "fee" | "interest" | "payment" | "summary";
export type CreditCardLineStatus = "pending" | "linked" | "ignored";

export interface CreditCardBillSummaryDto {
  id: string;
  account_id: string;
  period_start: string | null;
  period_end: string | null;
  statement_date: string | null;
  due_date: string | null;
  settlement_currency: string | null;
  amount_due_minor: string | null;
  unresolved_candidate_count: number;
  status: CreditCardBillStatus;
  version: number;
  updated_at: string;
}

export interface CanonicalTransactionDto {
  id: string;
  account_id: string;
  direction: "debit" | "credit";
  original_currency: string;
  original_amount_minor: string;
  occurred_at: string;
}

export interface CreditCardLineDto {
  id: string;
  line_kind: CreditCardLineKind;
  resolution_status: CreditCardLineStatus;
  resolution_reason: string | null;
  link_exception_reason: string | null;
  line_index: number;
  occurred_on: string | null;
  description: string;
  amount_minor: string | null;
  currency: string | null;
  transaction: CanonicalTransactionDto | null;
}

export interface CreditCardEventDto {
  id: string;
  bill_id: string;
  event_type: string;
  reason?: string;
  from_status?: CreditCardBillStatus;
  to_status?: CreditCardBillStatus;
  details: Record<string, string>;
  created_at: string;
}

export interface CreditCardBillDto extends CreditCardBillSummaryDto {
  bulk_document_id: string;
  bulk_attempt_generation: number;
  payoff_transfer_id: string | null;
  payment_candidate_transaction_id: string | null;
  ambiguous_payment_candidates: string[];
  paid_at: string | null;
  void_reason: string | null;
  minimum_payment_minor: string | null;
  previous_balance_minor: string | null;
  evidence_url: string;
  lines: CreditCardLineDto[];
  events: CreditCardEventDto[];
}

export interface CalculationTreatmentDto {
  transaction_id: string;
  spending_basis: "transaction_total" | "line_items" | "exclude";
  source: "default" | "user" | "system";
  reason: string | null;
  immutable: boolean;
  updated_at: string | null;
}

export interface CalculationTreatmentState {
  treatment: CalculationTreatmentDto;
  etag: string;
}

interface CalculationTreatmentResponseDto {
  transaction_id: string;
  spending_basis: "transaction_total" | "line_items" | "exclude";
  source: "default" | "user" | "system";
  reason: string | null;
  immutable?: boolean;
  updated_at: string | null;
}

interface JsonError { error?: string | { message?: string }; message?: string }

async function request<T>(session: Session, path: string, init: RequestInit = {}): Promise<{ data: T; etag: string | null }> {
  const response = await fetch(`${apiBaseUrl}${path}`, {
    ...init,
    headers: {
      Accept: "application/json",
      Authorization: `Bearer ${session.access_token}`,
      ...init.headers,
    },
  });
  const data: unknown = response.status === 204 ? null : await response.json().catch(() => null);
  if (!response.ok) {
    const body = data && typeof data === "object" ? data as JsonError : null;
    const message = typeof body?.error === "string"
      ? body.error
      : typeof body?.error === "object" && typeof body.error.message === "string"
        ? body.error.message
        : typeof body?.message === "string" ? body.message : "The request could not be completed.";
    throw new FinanceApiError(message, response.status, response.headers.get("ETag"));
  }
  return { data: data as T, etag: response.headers.get("ETag") };
}

function json(method: string, body: object): RequestInit {
  return { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
}

export async function listAccountBalances(session: Session, signal?: AbortSignal): Promise<AccountBalanceDto[]> {
  return (await request<{ accounts: AccountBalanceDto[] }>(session, "/v1/accounts/balances", { signal })).data.accounts;
}

export async function setOpeningBalance(
  session: Session,
  accountId: string,
  input: { balances: Record<string, string>; as_of: string; expected_version: number; correction_reason: string | null },
  idempotencyKey: string,
): Promise<AccountBalanceDto> {
  return (await request<AccountBalanceDto>(session, `/v1/accounts/${encodeURIComponent(accountId)}/opening-balance`, {
    ...json("PUT", input),
    headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
  })).data;
}

export async function listOpeningBalanceHistory(session: Session, accountId: string, signal?: AbortSignal): Promise<BalanceRevisionDto[]> {
  return (await request<{ revisions: BalanceRevisionDto[] }>(session, `/v1/accounts/${encodeURIComponent(accountId)}/opening-balance/history`, { signal })).data.revisions;
}

export async function listCreditCardBills(session: Session, accountId: string, signal?: AbortSignal): Promise<CreditCardBillSummaryDto[]> {
  const bills: CreditCardBillSummaryDto[] = [];
  const seenCursors = new Set<string>();
  let cursor: string | null = null;
  for (let pageNumber = 0; pageNumber < 20; pageNumber += 1) {
    const suffix: string = cursor ? `&cursor=${encodeURIComponent(cursor)}` : "";
    const page: { bills: CreditCardBillSummaryDto[]; next_cursor: string | null } = (await request<{ bills: CreditCardBillSummaryDto[]; next_cursor: string | null }>(session, `/v1/accounts/${encodeURIComponent(accountId)}/credit-card-statements?limit=100${suffix}`, { signal })).data;
    bills.push(...page.bills);
    if (!page.next_cursor) return bills;
    if (seenCursors.has(page.next_cursor)) throw new FinanceApiError("Credit Card history returned a repeated cursor.", 502);
    seenCursors.add(page.next_cursor);
    cursor = page.next_cursor;
  }
  throw new FinanceApiError("Credit Card history is larger than the supported 2,000-bill view.", 422);
}

export async function getCreditCardBill(session: Session, billId: string, signal?: AbortSignal): Promise<CreditCardBillDto> {
  return (await request<CreditCardBillDto>(session, `/v1/credit-card-statements/${encodeURIComponent(billId)}`, { signal })).data;
}

function billMutation(
  session: Session,
  billId: string,
  path: string,
  version: number,
  body: object,
  idempotencyKey?: string,
): Promise<{ data: CreditCardBillDto; etag: string | null }> {
  return request<CreditCardBillDto>(session, `/v1/credit-card-statements/${encodeURIComponent(billId)}${path}`, {
    ...json("POST", body),
    headers: {
      "Content-Type": "application/json",
      "If-Match": `"v-${version}"`,
      ...(idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {}),
    },
  });
}

export async function attachBillLine(session: Session, bill: CreditCardBillDto, lineId: string, transactionId: string, linkExceptionReason: string | null): Promise<CreditCardBillDto> {
  return (await billMutation(session, bill.id, `/lines/${encodeURIComponent(lineId)}/attach`, bill.version, { transaction_id: transactionId, link_exception_reason: linkExceptionReason })).data;
}

export async function createBillLineTransaction(session: Session, bill: CreditCardBillDto, lineId: string, categoryId: string, idempotencyKey: string): Promise<CreditCardBillDto> {
  return (await billMutation(session, bill.id, `/lines/${encodeURIComponent(lineId)}/create-transaction`, bill.version, { category_id: categoryId }, idempotencyKey)).data;
}

export async function ignoreBillLine(session: Session, bill: CreditCardBillDto, lineId: string, reason: string): Promise<CreditCardBillDto> {
  return (await billMutation(session, bill.id, `/lines/${encodeURIComponent(lineId)}/ignore`, bill.version, { reason })).data;
}

export async function selectPaymentCandidate(session: Session, bill: CreditCardBillDto, transactionId: string, idempotencyKey: string): Promise<CreditCardBillDto> {
  return (await billMutation(session, bill.id, "/payment-candidate/select", bill.version, { bank_transaction_id: transactionId }, idempotencyKey)).data;
}

export async function confirmPaymentCandidate(session: Session, bill: CreditCardBillDto, transactionId: string, idempotencyKey: string): Promise<CreditCardBillDto> {
  return (await billMutation(session, bill.id, "/payment-candidate/confirm", bill.version, { bank_transaction_id: transactionId }, idempotencyKey)).data;
}

export async function payBillInFull(session: Session, bill: CreditCardBillDto, bankAccountId: string, idempotencyKey: string): Promise<CreditCardBillDto> {
  return (await billMutation(session, bill.id, "/payoff", bill.version, { bank_account_id: bankAccountId }, idempotencyKey)).data;
}

export async function voidCreditCardBill(session: Session, bill: CreditCardBillDto, reason: string, idempotencyKey: string): Promise<CreditCardBillDto> {
  return (await billMutation(session, bill.id, "/void", bill.version, { reason }, idempotencyKey)).data;
}

export async function discardReviewBill(session: Session, bill: CreditCardBillDto, idempotencyKey: string): Promise<void> {
  await request<null>(session, `/v1/credit-card-statements/${encodeURIComponent(bill.id)}`, {
    method: "DELETE",
    headers: { "If-Match": `"v-${bill.version}"`, "Idempotency-Key": idempotencyKey },
  });
}

export async function getCalculationTreatment(session: Session, transactionId: string, signal?: AbortSignal): Promise<CalculationTreatmentState> {
  const response = await request<CalculationTreatmentResponseDto>(session, `/v1/transaction-calculation-treatments/${encodeURIComponent(transactionId)}`, { signal });
  return { treatment: { ...response.data, immutable: response.data.immutable ?? response.data.source === "system" }, etag: response.etag ?? '"t-0"' };
}

export async function updateCalculationTreatment(
  session: Session,
  transactionId: string,
  spendingBasis: "transaction_total" | "line_items" | "exclude",
  reason: string,
  etag = '"t-0"',
): Promise<CalculationTreatmentState> {
  const response = await request<CalculationTreatmentResponseDto>(session, `/v1/transaction-calculation-treatments/${encodeURIComponent(transactionId)}`, {
    ...json("PUT", { spending_basis: spendingBasis, reason }),
    headers: { "Content-Type": "application/json", "If-Match": etag },
  });
  return { treatment: { ...response.data, immutable: response.data.immutable ?? response.data.source === "system" }, etag: response.etag ?? etag };
}

export function majorToMinor(value: string): string {
  const normalized = value.trim().replaceAll(",", "");
  const match = /^(-?)(\d+)(?:\.(\d{1,2}))?$/.exec(normalized);
  if (!match) throw new Error("Enter a monetary amount with no more than two decimal places.");
  const sign = match[1] === "-" ? -1n : 1n;
  return (sign * (BigInt(match[2]) * 100n + BigInt((match[3] ?? "").padEnd(2, "0") || "0"))).toString();
}

export function minorToMajor(value: string | null): string {
  if (value === null || !/^-?\d+$/.test(value)) return "—";
  const amount = BigInt(value);
  const negative = amount < 0n;
  const absolute = negative ? -amount : amount;
  const whole = absolute / 100n;
  const fraction = (absolute % 100n).toString().padStart(2, "0");
  return `${negative ? "-" : ""}${whole.toString()}.${fraction}`;
}
