export type AccountFinanceSide = "asset" | "liability";
export type AccountFinanceAccountType = "bank_account" | "brokerage" | "robo_advisor" | "retirement_account" | "digital_wallet" | "crypto_wallet" | "crypto_exchange" | "rsu" | "credit_card" | "personal_loan" | "other";
export interface CurrencyAmount { currency: string; amount: string }
export interface OpeningBalanceHistoryEntry { id: string; action: "Opening balance set" | "Opening balance corrected"; previous: CurrencyAmount[] | null; next: CurrencyAmount[]; asOf: string; reason: string | null; editor: string; changedAt: string }
export interface OpeningBalance { balances: CurrencyAmount[]; currentBalances?: CurrencyAmount[]; asOf: string; version: number; history: OpeningBalanceHistoryEntry[] }
export type BillStatus = "review" | "unpaid" | "paid" | "void";
export type BillLineStatus = "linked" | "pending" | "ignored";
export type BillLineKind = "activity" | "refund" | "fee" | "payment";
export interface BillLineView { id: string; occurredOn: string; description: string; kind: BillLineKind; amount: string; currency: string; status: BillLineStatus; matchQuality: "exact" | "safe-create" | "ambiguous" | "none"; transactionTitle?: string; resolutionNote?: string }
export interface PaymentView { id: string; bankName: string; paidOn: string; amount: string; currency: string; origin: "existing-transfer" | "completed-suggestion" | "pay-in-full" }
export interface BankDebitSuggestionView { id: string; bankName: string; occurredOn: string; amount: string; currency: string; evidence: string }
export interface BillView { id: string; periodStart: string; periodEnd: string; statementDate: string; dueDate: string; amountDue: string; currency: string; status: BillStatus; evidenceName: string; importedAt: string; unresolvedCandidateCount: number; lines: BillLineView[]; payment: PaymentView | null; bankDebitSuggestion: BankDebitSuggestionView | null }
export type SpendingBasis = "transaction_total" | "line_items" | "exclude";

export const accountTypeName = (accountType: AccountFinanceAccountType): string => ({ bank_account: "Bank account", brokerage: "Brokerage", robo_advisor: "Robo advisor", retirement_account: "Retirement account", digital_wallet: "Digital wallet", crypto_wallet: "Crypto wallet", crypto_exchange: "Crypto exchange", rsu: "RSU", credit_card: "Credit card", personal_loan: "Personal loan", other: "Other account" })[accountType];
export const displayDate = (value: string): string => value ? new Intl.DateTimeFormat("en-SG", { day: "numeric", month: "short", year: "numeric" }).format(new Date(`${value.slice(0, 10)}T00:00:00`)) : "—";
export const displayDateTime = (value: string): string => value ? new Intl.DateTimeFormat("en-SG", { day: "numeric", month: "short", year: "numeric", hour: "numeric", minute: "2-digit" }).format(new Date(value)) : "—";
