import {
  type BillView,
  type OpeningBalance,
} from "./viewModel";
import {
  listOpeningBalanceHistory,
  majorToMinor,
  minorToMajor,
  type AccountBalanceDto,
  type CreditCardBillDto,
  type CreditCardBillSummaryDto,
} from "./api";

export type FinanceTab = "balance" | "bills";
export type DialogState =
  | { kind: "baseline" }
  | { kind: "line"; lineId: string }
  | { kind: "suggestion" }
  | { kind: "pay" }
  | { kind: "bill-lifecycle" }
  | { kind: "evidence" }
  | { kind: "transaction"; title: string; systemPayoff: boolean; transactionId?: string }
  | null;

export interface DraftAmount {
  id: string;
  currency: string;
  amount: string;
}

export function amountLabel(currency: string, amount: string): string {
  const match = /^(-?)(\d+)(?:\.(\d{1,2}))?$/.exec(amount.replaceAll(",", ""));
  if (!match) return `${currency} ${amount}`;
  return `${currency} ${match[1]}${BigInt(match[2]).toLocaleString("en-SG")}.${(match[3] ?? "").padEnd(2, "0")}`;
}

export function openTrustedEvidenceUrl(rawUrl: string): void {
  const target = new URL(rawUrl);
  const localDevelopment = import.meta.env.DEV
    && target.protocol === "http:"
    && ["localhost", "127.0.0.1", "::1"].includes(target.hostname);
  if (target.protocol !== "https:" && !localDevelopment) {
    throw new Error("The evidence service returned an unsafe URL.");
  }
  window.open(target.toString(), "_blank", "noopener,noreferrer");
}

export function balanceFromApi(view: AccountBalanceDto, history: Awaited<ReturnType<typeof listOpeningBalanceHistory>>): OpeningBalance | null {
  if (view.state === "unconfigured" || !view.as_of) return null;
  const revisions = [...history].sort((left, right) => right.version - left.version);
  return {
    balances: view.opening_balances.map((amount) => ({ currency: amount.currency, amount: minorToMajor(amount.minor_units) })),
    currentBalances: view.current_balances.map((amount) => ({ currency: amount.currency, amount: minorToMajor(amount.minor_units) })),
    asOf: view.as_of,
    version: view.version,
    history: revisions.map((revision, index) => ({
      id: revision.id,
      action: revision.version === 1 ? "Opening balance set" : "Opening balance corrected",
      previous: revisions[index + 1]?.balances.map((amount) => ({ currency: amount.currency, amount: minorToMajor(amount.minor_units) })) ?? null,
      next: revision.balances.map((amount) => ({ currency: amount.currency, amount: minorToMajor(amount.minor_units) })),
      asOf: revision.as_of,
      reason: revision.correction_reason,
      editor: "You",
      changedAt: revision.changed_at,
    })),
  };
}

export function billFromApi(bill: CreditCardBillSummaryDto | CreditCardBillDto): BillView {
  const detail = "lines" in bill ? bill : null;
  const candidateId = detail?.payment_candidate_transaction_id ?? detail?.ambiguous_payment_candidates[0] ?? null;
  return {
    id: bill.id,
    periodStart: bill.period_start ?? "",
    periodEnd: bill.period_end ?? "",
    statementDate: bill.statement_date ?? "",
    dueDate: bill.due_date ?? "",
    amountDue: minorToMajor(bill.amount_due_minor),
    currency: bill.settlement_currency ?? "—",
    status: bill.status,
    evidenceName: detail?.bulk_document_id ?? "Bulk Import evidence",
    importedAt: bill.updated_at,
    unresolvedCandidateCount: bill.unresolved_candidate_count,
    lines: detail?.lines.filter((line) => line.line_kind !== "summary").map((line) => ({
      id: line.id,
      occurredOn: line.occurred_on ?? "",
      description: line.description,
      kind: line.line_kind === "interest" ? "fee" : line.line_kind === "summary" ? "activity" : line.line_kind,
      amount: minorToMajor(line.amount_minor),
      currency: line.currency ?? bill.settlement_currency ?? "—",
      status: line.resolution_status,
      matchQuality: line.transaction ? "exact" : "safe-create",
      transactionTitle: line.transaction ? line.description : undefined,
      resolutionNote: line.resolution_reason ?? line.link_exception_reason ?? undefined,
    })) ?? [],
    payment: detail?.payoff_transfer_id ? {
      id: detail.payoff_transfer_id,
      bankName: "Linked bank account",
      paidOn: detail.paid_at ?? bill.updated_at,
      amount: minorToMajor(bill.amount_due_minor),
      currency: bill.settlement_currency ?? "—",
      origin: "existing-transfer",
    } : null,
    bankDebitSuggestion: candidateId ? {
      id: candidateId,
      bankName: "Matched bank transaction",
      occurredOn: bill.due_date ?? bill.statement_date ?? "",
      amount: minorToMajor(bill.amount_due_minor),
      currency: bill.settlement_currency ?? "—",
      evidence: "Exact amount and currency candidate",
    } : null,
  };
}

export function isExplicitZero(amount: string): boolean {
  return BigInt(majorToMinor(amount)) === 0n;
}

export function statusLabel(status: BillView["status"]): string {
  if (status === "review") return "Needs review";
  if (status === "unpaid") return "Unpaid";
  if (status === "void") return "Void";
  return "Paid";
}

