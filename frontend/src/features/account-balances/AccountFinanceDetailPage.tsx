import {
  ArrowLeft,
  ArrowRight,
  BadgeCheck,
  CalendarDays,
  Check,
  CheckCircle2,
  ChevronRight,
  CircleAlert,
  CreditCard,
  ExternalLink,
  FileText,
  History,
  Info,
  Landmark,
  Link2,
  LockKeyhole,
  Plus,
  ReceiptText,
  Search,
  ShieldCheck,
  Sparkles,
  Unlink,
  WalletCards,
  X,
} from "lucide-react";
import {
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import type { Session } from "@supabase/supabase-js";
import type { Account } from "../accounts/model";
import {
  accountTypeName,
  displayDate,
  displayDateTime,
  type AccountFinanceAccountType,
  type AccountFinanceSide,
  type CurrencyAmount,
  type BillView,
  type BillLineView,
  type OpeningBalance,
  type SpendingBasis,
} from "./viewModel";
import {
  confirmPaymentCandidate,
  attachBillLine,
  createBillLineTransaction,
  discardReviewBill,
  getCalculationTreatment,
  getCreditCardBill,
  ignoreBillLine,
  listAccountBalances,
  listCreditCardBills,
  listOpeningBalanceHistory,
  majorToMinor,
  minorToMajor,
  payBillInFull,
  setOpeningBalance,
  selectPaymentCandidate,
  updateCalculationTreatment,
  voidCreditCardBill,
  type AccountBalanceDto,
  type CalculationTreatmentState,
  type CreditCardBillDto,
  type CreditCardBillSummaryDto,
} from "./api";
import { getDocumentEvidence, type BulkEvidenceItemDto } from "../bulk-import/api";
import { getOwnedTransactionCandidate, listTransactionCategories, listTransactionsForAccount } from "../transactions/api";
import type { TransactionListItem } from "../transactions/model";
import "./account-finance-detail.css";

export interface AccountFinanceDetailPageProps {
  accountId: string;
  accountName: string;
  institution: string;
  accountType: AccountFinanceAccountType;
  side: AccountFinanceSide;
  onBack: () => void;
  backLabel?: string;
  initialTab?: "balance" | "bills";
  initialBillId?: string;
  onOpenBulkImport?: () => void;
  session: Session;
  accounts: Account[];
}

type FinanceTab = "balance" | "bills";
type DialogState =
  | { kind: "baseline" }
  | { kind: "line"; lineId: string }
  | { kind: "suggestion" }
  | { kind: "pay" }
  | { kind: "bill-lifecycle" }
  | { kind: "evidence" }
  | { kind: "transaction"; title: string; systemPayoff: boolean; transactionId?: string }
  | null;

interface DraftAmount {
  id: string;
  currency: string;
  amount: string;
}

function amountLabel(currency: string, amount: string): string {
  const match = /^(-?)(\d+)(?:\.(\d{1,2}))?$/.exec(amount.replaceAll(",", ""));
  if (!match) return `${currency} ${amount}`;
  return `${currency} ${match[1]}${BigInt(match[2]).toLocaleString("en-SG")}.${(match[3] ?? "").padEnd(2, "0")}`;
}

function openTrustedEvidenceUrl(rawUrl: string): void {
  const target = new URL(rawUrl);
  const localDevelopment = import.meta.env.DEV
    && target.protocol === "http:"
    && ["localhost", "127.0.0.1", "::1"].includes(target.hostname);
  if (target.protocol !== "https:" && !localDevelopment) {
    throw new Error("The evidence service returned an unsafe URL.");
  }
  window.open(target.toString(), "_blank", "noopener,noreferrer");
}

function balanceFromApi(view: AccountBalanceDto, history: Awaited<ReturnType<typeof listOpeningBalanceHistory>>): OpeningBalance | null {
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

function billFromApi(bill: CreditCardBillSummaryDto | CreditCardBillDto): BillView {
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

function isExplicitZero(amount: string): boolean {
  return BigInt(majorToMinor(amount)) === 0n;
}

function statusLabel(status: BillView["status"]): string {
  if (status === "review") return "Needs review";
  if (status === "unpaid") return "Unpaid";
  if (status === "void") return "Void";
  return "Paid";
}

function Dialog({
  eyebrow,
  title,
  onClose,
  children,
  footer,
  wide = false,
}: {
  eyebrow: string;
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
}) {
  const dialogRef = useRef<HTMLElement>(null);
  const closeRef = useRef(onClose);

  useEffect(() => {
    closeRef.current = onClose;
  }, [onClose]);

  useEffect(() => {
    const previouslyFocused = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const previousBodyOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    const handleKeyboard = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        closeRef.current();
        return;
      }
      if (event.key !== "Tab") return;
      const focusable = dialogRef.current?.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href], summary[tabindex="0"]',
      );
      if (!focusable || focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", handleKeyboard);
    return () => {
      document.removeEventListener("keydown", handleKeyboard);
      document.body.style.overflow = previousBodyOverflow;
      previouslyFocused?.focus();
    };
  }, []);

  return (
    <div className="abf-dialog-backdrop" onMouseDown={(event) => {
      if (event.target === event.currentTarget) onClose();
    }}>
      <section
        aria-labelledby="abf-dialog-title"
        aria-modal="true"
        className={`abf-dialog${wide ? " abf-dialog-wide" : ""}`}
        ref={dialogRef}
        role="dialog"
      >
        <header className="abf-dialog-header">
          <div>
            <p className="abf-eyebrow">{eyebrow}</p>
            <h2 id="abf-dialog-title">{title}</h2>
          </div>
          <button aria-label="Close dialog" autoFocus className="abf-icon-button" onClick={onClose} type="button">
            <X aria-hidden="true" size={19} />
          </button>
        </header>
        <div className="abf-dialog-body">{children}</div>
        {footer && <footer className="abf-dialog-footer">{footer}</footer>}
      </section>
    </div>
  );
}

function StatusPill({ status }: { status: BillView["status"] }) {
  return (
    <span className={`abf-status abf-status-${status}`}>
      {status === "paid" ? <CheckCircle2 aria-hidden="true" size={13} /> : status === "void" ? <X aria-hidden="true" size={13} /> : <CircleAlert aria-hidden="true" size={13} />}
      {statusLabel(status)}
    </span>
  );
}

function BaselineEditor({
  accountType,
  side,
  baseline,
  onClose,
  onSave,
}: {
  accountType: AccountFinanceAccountType;
  side: AccountFinanceSide;
  baseline: OpeningBalance | null;
  onClose: () => void;
  onSave: (amounts: CurrencyAmount[], asOf: string, reason: string) => void;
}) {
  const [amounts, setAmounts] = useState<DraftAmount[]>(() =>
    (baseline?.balances ?? [{ currency: "SGD", amount: "0.00" }]).map((amount, index) => ({
      ...amount,
      id: `draft-${index}-${amount.currency}`,
    })),
  );
  const [asOf, setAsOf] = useState(baseline?.asOf.slice(0, 16) ?? new Date().toISOString().slice(0, 16));
  const [reason, setReason] = useState("");
  const [errors, setErrors] = useState<string[]>([]);
  const [confirming, setConfirming] = useState(false);

  const liability = side === "liability";
  const validate = (): CurrencyAmount[] | null => {
    const nextErrors: string[] = [];
    const currencies = amounts.map((entry) => entry.currency.trim().toUpperCase());
    if (!asOf) nextErrors.push("Choose the date and time represented by this balance.");
    if (amounts.length === 0) nextErrors.push("Keep at least one currency amount.");
    if (currencies.some((currency) => !/^[A-Z]{3}$/.test(currency))) {
      nextErrors.push("Use a three-letter currency code, such as SGD or USD.");
    }
    if (new Set(currencies).size !== currencies.length) {
      nextErrors.push("Each currency can appear only once.");
    }
    if (amounts.some((entry) => !/^-?\d+(\.\d{1,2})?$/.test(entry.amount.trim()))) {
      nextErrors.push("Enter amounts with no more than two decimal places.");
    }
    if (
      accountType !== "bank_account" &&
      amounts.some((entry) => BigInt(majorToMinor(entry.amount)) < 0n)
    ) {
      nextErrors.push(liability ? "Amount owed cannot be negative." : "Only a Bank account can have a negative opening balance.");
    }
    if (baseline && reason.trim().length < 8) {
      nextErrors.push("Add a correction reason of at least 8 characters for the audit history.");
    }
    setErrors(nextErrors);
    if (nextErrors.length > 0) return null;
    return amounts.map((entry) => ({
      currency: entry.currency.trim().toUpperCase(),
      amount: minorToMajor(majorToMinor(entry.amount)),
    }));
  };

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!validate()) return;
    setConfirming(true);
  };

  const save = () => {
    const validated = validate();
    if (!validated) {
      setConfirming(false);
      return;
    }
    onSave(validated, asOf, reason.trim());
  };

  return (
    <Dialog
      eyebrow={baseline ? "AUDITED CORRECTION" : "FINANCIAL BASELINE"}
      title={confirming
        ? baseline ? "Confirm opening balance correction" : "Confirm opening balance"
        : baseline ? "Correct opening balance" : "Set opening balance"}
      onClose={onClose}
      wide
      footer={confirming ? (
        <>
          <button className="abf-button abf-button-secondary" onClick={() => setConfirming(false)} type="button">Back</button>
          <button className="abf-button abf-button-primary" onClick={save} type="button">
            <ShieldCheck aria-hidden="true" size={17} /> {baseline ? "Confirm correction" : "Save opening balance"}
          </button>
        </>
      ) : undefined}
    >
      {confirming ? (
        <div className="abf-confirm-stack">
          <div className="abf-confirm-icon"><ShieldCheck aria-hidden="true" size={24} /></div>
          <p className="abf-lead">
            This replaces the effective baseline. It does not create or edit a transaction.
          </p>
          <dl className="abf-confirm-summary">
            <div><dt>{liability ? "Amount owed" : "Opening amount"}</dt><dd>{amounts.map((entry) => amountLabel(entry.currency.toUpperCase(), entry.amount)).join(" · ")}</dd></div>
            <div><dt>As of</dt><dd>{displayDateTime(asOf)}</dd></div>
            {baseline && <div><dt>Reason</dt><dd>{reason}</dd></div>}
          </dl>
          <div className="abf-note abf-note-neutral">
            <Info aria-hidden="true" size={18} />
            <span>Confirmed transactions strictly after this time will be included in the calculated balance.</span>
          </div>
        </div>
      ) : (
        <form className="abf-balance-form" onSubmit={submit}>
          <div className="abf-note abf-note-neutral">
            <Info aria-hidden="true" size={18} />
            <span>
              {liability
                ? "Enter non-negative amounts owed. A zero is a real balance, not an unset value."
                : accountType === "bank_account"
                  ? "Negative amounts are allowed here to represent an overdraft."
                  : "A zero is a real balance, not an unset value."}
            </span>
          </div>
          <div className="abf-form-heading">
            <div>
              <h3>{liability ? "Opening amount owed" : "Opening amounts"}</h3>
              <p>Keep currencies separate. No exchange-rate conversion is applied.</p>
            </div>
            <button
              className="abf-button abf-button-quiet"
              onClick={() => setAmounts((current) => [...current, { id: `draft-${Date.now()}`, currency: "", amount: "0.00" }])}
              type="button"
            >
              <Plus aria-hidden="true" size={16} /> Add currency
            </button>
          </div>
          <div className="abf-currency-rows">
            {amounts.map((entry, index) => (
              <div className="abf-currency-row" key={entry.id}>
                <label>
                  Currency
                  <input
                    aria-label={`Currency ${index + 1}`}
                    autoCapitalize="characters"
                    maxLength={3}
                    onChange={(event) => setAmounts((current) => current.map((item) => item.id === entry.id ? { ...item, currency: event.target.value.toUpperCase() } : item))}
                    placeholder="SGD"
                    value={entry.currency}
                  />
                </label>
                <label className="abf-amount-field">
                  {liability ? "Amount owed" : "Amount"}
                  <span><span className="abf-input-prefix">{entry.currency || "—"}</span><input inputMode="decimal" onChange={(event) => setAmounts((current) => current.map((item) => item.id === entry.id ? { ...item, amount: event.target.value } : item))} value={entry.amount} /></span>
                </label>
                <button
                  aria-label={`Remove currency row ${index + 1}`}
                  className="abf-icon-button abf-remove-currency"
                  disabled={amounts.length === 1}
                  onClick={() => setAmounts((current) => current.filter((item) => item.id !== entry.id))}
                  type="button"
                >
                  <X aria-hidden="true" size={17} />
                </button>
              </div>
            ))}
          </div>
          <label className="abf-field">
            Balance as of
            <input onChange={(event) => setAsOf(event.target.value)} type="datetime-local" value={asOf} />
            <span>Transactions at this exact time or earlier are assumed to be represented by the baseline.</span>
          </label>
          {baseline && (
            <label className="abf-field">
              Correction reason
              <textarea onChange={(event) => setReason(event.target.value)} placeholder="What did you verify, and why is the baseline changing?" rows={3} value={reason} />
              <span>This will be recorded in immutable history.</span>
            </label>
          )}
          {errors.length > 0 && (
            <div className="abf-note abf-note-error" role="alert">
              <CircleAlert aria-hidden="true" size={18} />
              <div>{errors.map((error) => <p key={error}>{error}</p>)}</div>
            </div>
          )}
          <div className="abf-form-actions">
            <button className="abf-button abf-button-secondary" onClick={onClose} type="button">Cancel</button>
            <button className="abf-button abf-button-primary" type="submit">
              Review {baseline ? "correction" : "opening balance"} <ArrowRight aria-hidden="true" size={17} />
            </button>
          </div>
        </form>
      )}
    </Dialog>
  );
}

function BalanceWorkspace({
  accountName,
  side,
  baseline,
  onEdit,
}: {
  accountName: string;
  side: AccountFinanceSide;
  baseline: OpeningBalance | null;
  onEdit: () => void;
}) {
  if (!baseline) {
    return (
      <section className="abf-panel abf-baseline-empty" aria-labelledby="opening-balance-heading">
        <div className="abf-empty-orbit" aria-hidden="true"><WalletCards size={30} /></div>
        <p className="abf-eyebrow">OPENING BALANCE</p>
        <h2 id="opening-balance-heading">Opening balance not set</h2>
        <p>
          We won’t invent a zero for {accountName}. Set an amount and cutoff time before a current balance is calculated.
        </p>
        <button className="abf-button abf-button-primary" onClick={onEdit} type="button">
          <Plus aria-hidden="true" size={17} /> Set opening balance
        </button>
        <div className="abf-empty-footnote">
          <Info aria-hidden="true" size={16} /> Explicitly saving {side === "liability" ? "SGD 0.00 owed" : "SGD 0.00"} will be shown as a configured balance.
        </div>
      </section>
    );
  }

  return (
    <div className="abf-balance-layout">
      <section className="abf-panel abf-current-card" aria-labelledby="calculated-balance-heading">
        <div className="abf-section-title-row">
          <div>
            <p className="abf-eyebrow">CALCULATED BALANCE</p>
            <h2 id="calculated-balance-heading">{side === "liability" ? "Amount currently owed" : "Current balance"}</h2>
          </div>
          <span className="abf-live-badge"><span /> Live calculation</span>
        </div>
        <div className="abf-balance-totals">
          {(baseline.currentBalances ?? baseline.balances).map((entry) => {
            return (
              <div className="abf-balance-total" key={entry.currency}>
                <span>{entry.currency}</span>
                <strong>{amountLabel(entry.currency, entry.amount).replace(`${entry.currency} `, "")}</strong>
              </div>
            );
          })}
        </div>
        <div className="abf-calculation-box">
          <div className="abf-calculation-title"><Sparkles aria-hidden="true" size={17} /><strong>How this is calculated</strong></div>
          {baseline.balances.map((entry) => {
            const current = (baseline.currentBalances ?? baseline.balances).find((value) => value.currency === entry.currency)?.amount ?? entry.amount;
            const activityMinor = BigInt(majorToMinor(current)) - BigInt(majorToMinor(entry.amount));
            const activity = minorToMajor(activityMinor.toString());
            return (
              <div className="abf-equation" key={entry.currency}>
                <span>{amountLabel(entry.currency, entry.amount)} opening</span>
                <b>+</b>
                <span>{amountLabel(entry.currency, activity)} net confirmed activity</span>
                <b>=</b>
                <strong>{amountLabel(entry.currency, current)}</strong>
              </div>
            );
          })}
          <p>
            Includes confirmed transactions after {displayDateTime(baseline.asOf)}. Transfers affect this account balance even when excluded from spending.
          </p>
        </div>
      </section>

      <aside className="abf-panel abf-opening-card" aria-labelledby="configured-opening-heading">
        <div className="abf-section-title-row">
          <div>
            <p className="abf-eyebrow">OPENING BALANCE</p>
            <h2 id="configured-opening-heading">Configured baseline</h2>
          </div>
          <BadgeCheck aria-label="Configured" className="abf-configured-icon" size={24} />
        </div>
        <dl className="abf-opening-values">
          {baseline.balances.map((entry) => (
            <div key={entry.currency}>
              <dt>{entry.currency}</dt>
              <dd>{amountLabel(entry.currency, entry.amount).replace(`${entry.currency} `, "")}</dd>
              {isExplicitZero(entry.amount) && <span>Explicit zero</span>}
            </div>
          ))}
        </dl>
        <div className="abf-as-of"><CalendarDays aria-hidden="true" size={16} /><span>As of {displayDateTime(baseline.asOf)}</span></div>
        <button className="abf-button abf-button-secondary abf-full-button" onClick={onEdit} type="button">Correct opening balance</button>
      </aside>

      <section className="abf-panel abf-history-card" aria-labelledby="balance-history-heading">
        <div className="abf-section-title-row">
          <div>
            <p className="abf-eyebrow">AUDIT TRAIL</p>
            <h2 id="balance-history-heading">Opening balance history</h2>
          </div>
          <History aria-hidden="true" size={21} />
        </div>
        <div className="abf-history-list">
          {baseline.history.map((entry) => (
            <details className="abf-history-entry" key={entry.id}>
              <summary>
                <span className="abf-history-dot" />
                <span><strong>{entry.action}</strong><small>{displayDateTime(entry.changedAt)} · {entry.editor}</small></span>
                <ChevronRight aria-hidden="true" size={17} />
              </summary>
              <div className="abf-history-detail">
                {entry.previous && <p><span>Previous</span>{entry.previous.map((amount) => amountLabel(amount.currency, amount.amount)).join(" · ")}</p>}
                <p><span>New baseline</span>{entry.next.map((amount) => amountLabel(amount.currency, amount.amount)).join(" · ")}</p>
                <p><span>Effective as of</span>{displayDateTime(entry.asOf)}</p>
                {entry.reason && <blockquote>“{entry.reason}”</blockquote>}
                <div><LockKeyhole aria-hidden="true" size={14} /> Immutable history record</div>
              </div>
            </details>
          ))}
        </div>
      </section>
    </div>
  );
}

function BillList({ bills, onOpen, onOpenBulkImport }: { bills: BillView[]; onOpen: (id: string) => void; onOpenBulkImport?: () => void }) {
  const counts = {
    review: bills.filter((bill) => bill.status === "review").length,
    unpaid: bills.filter((bill) => bill.status === "unpaid").length,
    paid: bills.filter((bill) => bill.status === "paid").length,
  };
  const unpaidTotal = minorToMajor(bills
    .filter((bill) => bill.status === "unpaid" && bill.currency === "SGD")
    .reduce((total, bill) => total + BigInt(majorToMinor(bill.amountDue)), 0n).toString());
  return (
    <div className="abf-bills-workspace">
      <section className="abf-bill-overview" aria-label="Bill overview">
        <div><span>Needs review</span><strong>{counts.review}</strong><small>Resolve statement lines</small></div>
        <div><span>Unpaid</span><strong>{counts.unpaid}</strong><small>{amountLabel("SGD", unpaidTotal)} due</small></div>
        <div><span>Paid</span><strong>{counts.paid}</strong><small>Matched exactly</small></div>
      </section>
      <div className="abf-list-heading">
        <div><h2>Credit card bills</h2><p>Created automatically after reconciliation in Bulk Import.</p></div>
        <button className="abf-button abf-button-secondary" onClick={onOpenBulkImport} type="button"><ReceiptText aria-hidden="true" size={17} /> Open Bulk Import</button>
      </div>
      <section className="abf-bill-list" aria-label="Credit card bills">
        {bills.map((bill) => (
          <button className="abf-bill-row" key={bill.id} onClick={() => onOpen(bill.id)} type="button">
            <span className={`abf-bill-icon abf-bill-icon-${bill.status}`} aria-hidden="true"><FileText size={21} /></span>
            <span className="abf-bill-main">
              <span className="abf-bill-heading"><strong>{displayDate(bill.periodStart)} – {displayDate(bill.periodEnd)}</strong><StatusPill status={bill.status} /></span>
              <small>Statement {displayDate(bill.statementDate)} · Due {displayDate(bill.dueDate)}</small>
            </span>
            <span className="abf-bill-amount"><small>Amount due</small><strong>{amountLabel(bill.currency, bill.amountDue)}</strong></span>
            <ChevronRight aria-hidden="true" size={19} />
          </button>
        ))}
      </section>
      <section className="abf-empty-inline">
        <div><ReceiptText aria-hidden="true" size={21} /></div>
        <span><strong>Looking for another bill?</strong><small>Process a Credit Card bill in Bulk Import. There is no separate uploader here.</small></span>
        <button className="abf-text-button" onClick={onOpenBulkImport} type="button">Go to Bulk Import <ArrowRight aria-hidden="true" size={15} /></button>
      </section>
    </div>
  );
}

function LineStatus({ line }: { line: BillLineView }) {
  if (line.status === "linked") return <span className="abf-line-status abf-line-linked"><Link2 aria-hidden="true" size={13} /> Linked</span>;
  if (line.status === "ignored") return <span className="abf-line-status abf-line-ignored"><Unlink aria-hidden="true" size={13} /> Ignored</span>;
  return <span className="abf-line-status abf-line-pending"><CircleAlert aria-hidden="true" size={13} /> Review</span>;
}

function BillDetail({
  bill,
  onBack,
  onEvidence,
  onLine,
  onSuggestion,
  onPay,
  onPayment,
  onLifecycle,
  onDiagnostics,
}: {
  bill: BillView;
  onBack: () => void;
  onEvidence: () => void;
  onLine: (id: string) => void;
  onSuggestion: () => void;
  onPay: () => void;
  onPayment: () => void;
  onLifecycle: () => void;
  onDiagnostics?: () => void;
}) {
  const resolvedCount = bill.lines.filter((line) => line.status !== "pending").length;
  const unresolvedLineCount = bill.lines.length - resolvedCount;
  const totalReviewCount = unresolvedLineCount + bill.unresolvedCandidateCount;
  const reviewHeading = totalReviewCount > 0
    ? `${totalReviewCount} imported result${totalReviewCount === 1 ? "" : "s"} need your review`
    : "This bill needs review";
  const reviewExplanation = bill.unresolvedCandidateCount > 0
    ? `${bill.unresolvedCandidateCount} result${bill.unresolvedCandidateCount === 1 ? " could" : "s could"} not be safely projected into a statement line. Inspect or retry the source in Bulk Import.`
    : unresolvedLineCount > 0
      ? "Resolve every ambiguous or missing transaction before payment matching can finish."
      : "No statement line requires action. Inspect the source for a missing header, Account conflict, or payment ambiguity.";
  return (
    <div className="abf-bill-detail">
      <button className="abf-back-link" onClick={onBack} type="button"><ArrowLeft aria-hidden="true" size={17} /> All bills</button>
      <section className="abf-panel abf-bill-hero">
        <div className="abf-bill-hero-main">
          <div className={`abf-bill-icon abf-bill-icon-${bill.status}`}><FileText aria-hidden="true" size={22} /></div>
          <div>
            <div className="abf-title-with-status"><h2>{displayDate(bill.periodStart)} – {displayDate(bill.periodEnd)}</h2><StatusPill status={bill.status} /></div>
            <p>Statement {displayDate(bill.statementDate)} · Due {displayDate(bill.dueDate)}</p>
          </div>
        </div>
        <div className="abf-bill-due"><span>Amount due</span><strong>{amountLabel(bill.currency, bill.amountDue)}</strong>{bill.status === "paid" && <small>Settled in full</small>}</div>
        <button className="abf-evidence-link" onClick={onEvidence} type="button"><FileText aria-hidden="true" size={16} /><span><small>Original evidence</small><strong>{bill.evidenceName}</strong></span><ExternalLink aria-hidden="true" size={15} /></button>
      </section>

      {(bill.status === "review" || bill.status === "unpaid") && (
        <div className="abf-bill-lifecycle-actions">
          <span><Info aria-hidden="true" size={15} /> {bill.status === "review" ? "Discard removes only this review projection; transactions and Bulk evidence stay intact." : "Void keeps this bill as a permanent audit record."}</span>
          <button className="abf-text-button abf-destructive-link" onClick={onLifecycle} type="button">{bill.status === "review" ? "Discard review bill" : "Void bill"}</button>
        </div>
      )}

      {bill.status === "review" && (
        <section className="abf-callout abf-callout-review">
          <CircleAlert aria-hidden="true" size={21} />
          <div><strong>{reviewHeading}</strong><p>{reviewExplanation}</p></div>
          {onDiagnostics && <button className="abf-button abf-button-secondary" onClick={onDiagnostics} type="button">Open Bulk Import diagnostics</button>}
        </section>
      )}

      {(bill.status === "review" || bill.status === "unpaid") && bill.bankDebitSuggestion && (
        <section className="abf-callout abf-callout-suggestion">
          <Sparkles aria-hidden="true" size={22} />
          <div>
            <span className="abf-small-label">POSSIBLE EXISTING PAYMENT</span>
            <strong>{bill.status === "review" ? "Choose the Bank debit that paid this bill." : "We found an exact Bank debit, but its Card credit leg is missing."}</strong>
            <p>{bill.bankDebitSuggestion.bankName} · {displayDate(bill.bankDebitSuggestion.occurredOn)} · {amountLabel(bill.bankDebitSuggestion.currency, bill.bankDebitSuggestion.amount)}</p>
          </div>
          <button className="abf-button abf-button-dark" onClick={onSuggestion} type="button">Review suggestion</button>
        </section>
      )}

      {bill.status === "unpaid" && !bill.bankDebitSuggestion && !bill.payment && (
        <section className="abf-callout abf-callout-unpaid">
          <Search aria-hidden="true" size={21} />
          <div><strong>Payment check complete — no exact transfer found</strong><p>This bill is Unpaid. You can pay it in full below, or leave it open.</p></div>
        </section>
      )}

      {bill.status === "void" && (
        <section className="abf-callout abf-callout-void">
          <LockKeyhole aria-hidden="true" size={21} />
          <div><strong>This bill is void and read-only</strong><p>Its imported evidence and audit record are retained. No payment actions are available.</p></div>
        </section>
      )}

      {bill.payment && (
        <section className="abf-panel abf-payment-card">
          <span className="abf-payment-check"><Check aria-hidden="true" size={18} /></span>
          <div><span className="abf-small-label">EXACT PAYMENT MATCH</span><h3>Paid in full from {bill.payment.bankName}</h3><p>{displayDate(bill.payment.paidOn)} · {amountLabel(bill.payment.currency, bill.payment.amount)} · Bank → Credit Card transfer</p></div>
          <button className="abf-button abf-button-secondary" onClick={onPayment} type="button">View transfer</button>
        </section>
      )}

      <section className="abf-panel abf-lines-card">
        <header className="abf-lines-header">
          <div><p className="abf-eyebrow">RECONCILIATION</p><h2>Statement activity</h2><p>{resolvedCount} of {bill.lines.length} lines resolved</p></div>
          <div className="abf-progress" aria-label={`${resolvedCount} of ${bill.lines.length} lines resolved`}><span style={{ width: `${bill.lines.length ? (resolvedCount / bill.lines.length) * 100 : 0}%` }} /></div>
        </header>
        <div className="abf-line-table" role="table" aria-label="Statement activity">
          <div className="abf-line-table-head" role="row"><span role="columnheader">Date</span><span role="columnheader">Description</span><span role="columnheader">Resolution</span><span role="columnheader">Amount</span><span aria-hidden="true" /></div>
          {bill.lines.map((line) => (
            <div className="abf-line-row" key={line.id} role="row">
              <span data-label="Date" role="cell">{displayDate(line.occurredOn)}</span>
              <span className="abf-line-description" data-label="Description" role="cell"><strong>{line.description}</strong><small>{line.kind === "activity" ? "Card activity" : line.kind === "payment" ? "Payment" : line.kind === "refund" ? "Refund" : "Fee"}</small></span>
              <span data-label="Resolution" role="cell"><LineStatus line={line} />{line.status === "linked" && line.transactionTitle && <small className="abf-linked-title">{line.transactionTitle}</small>}{line.resolutionNote && <small className="abf-linked-title">{line.resolutionNote}</small>}</span>
              <strong className={line.kind === "refund" || line.kind === "payment" ? "abf-credit-amount" : ""} data-label="Amount" role="cell">{line.kind === "refund" || line.kind === "payment" ? "−" : ""}{amountLabel(line.currency, line.amount)}</strong>
              <button className="abf-button abf-button-row" onClick={() => onLine(line.id)} type="button">{line.status === "linked" ? "View" : line.status === "ignored" ? "Details" : line.matchQuality === "safe-create" ? "Create missing" : "Review"}</button>
            </div>
          ))}
        </div>
      </section>

      {bill.status === "unpaid" && (
        <section className="abf-pay-bar">
          <div><CreditCard aria-hidden="true" size={21} /><span><strong>No complete payment is linked yet</strong><small>Pay the exact bill amount from an active Bank account.</small></span></div>
          <button className="abf-button abf-button-primary" onClick={onPay} type="button">Pay {amountLabel(bill.currency, bill.amountDue)} in full</button>
        </section>
      )}
    </div>
  );
}

function LineReviewDialog({
  line,
  onClose,
  onResolve,
  onOpenTransaction,
  candidates,
}: {
  line: BillLineView;
  onClose: () => void;
  onResolve: (resolution: "linked" | "created" | "ignored", reason?: string, transactionId?: string) => void;
  onOpenTransaction: () => void;
  candidates: TransactionListItem[];
}) {
  const [ignoreReason, setIgnoreReason] = useState("");
  const [showIgnore, setShowIgnore] = useState(false);
  return (
    <Dialog eyebrow="BILL LINE" title={line.description} onClose={onClose} wide>
      <div className="abf-line-dialog-summary">
        <div><span>Document date</span><strong>{displayDate(line.occurredOn)}</strong></div>
        <div><span>Document amount</span><strong>{amountLabel(line.currency, line.amount)}</strong></div>
        <div><span>Type</span><strong>{line.kind}</strong></div>
        <div><span>Current status</span><LineStatus line={line} /></div>
      </div>
      {line.status === "linked" && (
        <div className="abf-match-card abf-match-exact">
          <BadgeCheck aria-hidden="true" size={21} />
          <div><span>Exact canonical transaction</span><strong>{line.transactionTitle}</strong><small>Same Card account · amount · currency · document date</small></div>
          <button className="abf-button abf-button-secondary" onClick={onOpenTransaction} type="button">View transaction</button>
        </div>
      )}
      {line.status === "ignored" && (
        <div className="abf-note abf-note-neutral"><Unlink aria-hidden="true" size={18} /><span><strong>This line was ignored.</strong><br />{line.resolutionNote}</span></div>
      )}
      {line.status === "pending" && candidates.length === 0 && (
        <>
          <div className="abf-note abf-note-success"><Sparkles aria-hidden="true" size={18} /><span><strong>Safe missing transaction.</strong><br />No existing Card transaction matches this evidence-backed line.</span></div>
          <div className="abf-resolution-choice">
            <span className="abf-choice-icon"><Plus aria-hidden="true" size={19} /></span>
            <span><strong>Create missing Card transaction</strong><small>Amount, date, direction, and evidence come from the imported line and cannot be changed here.</small></span>
            <button className="abf-button abf-button-primary" onClick={() => onResolve("created")} type="button">Create & link</button>
          </div>
        </>
      )}
      {line.status === "pending" && candidates.length > 0 && (
        <>
          <div className="abf-note abf-note-warning"><CircleAlert aria-hidden="true" size={18} /><span><strong>{candidates.length} possible transaction{candidates.length === 1 ? " was" : "s were"} found.</strong><br />Nothing was linked automatically.</span></div>
          <div className="abf-candidate-list">
            {candidates.map((candidate) => <button key={candidate.id} onClick={() => onResolve("linked", undefined, candidate.id)} type="button"><span><strong>{candidate.title}</strong><small>{displayDate(candidate.occurred_at)} · Card · matching amount</small></span><b>{amountLabel(candidate.original_currency, minorToMajor(candidate.original_amount_minor))}</b><span className="abf-match-score">Review</span></button>)}
          </div>
          <div className="abf-note abf-note-neutral"><Info aria-hidden="true" size={18} /><span>Selecting a match links existing activity. It never creates a duplicate transaction.</span></div>
        </>
      )}
      {line.status === "pending" && (
        <div className="abf-ignore-block">
          {!showIgnore ? (
            <button className="abf-text-button" onClick={() => setShowIgnore(true)} type="button"><Unlink aria-hidden="true" size={15} /> This line should not become a transaction</button>
          ) : (
            <label className="abf-field">Reason for ignoring<textarea autoFocus onChange={(event) => setIgnoreReason(event.target.value)} placeholder="Why does this imported line not represent account activity?" rows={2} value={ignoreReason} /><button className="abf-button abf-button-secondary" disabled={ignoreReason.trim().length < 8} onClick={() => onResolve("ignored", ignoreReason.trim())} type="button">Confirm ignore</button></label>
          )}
        </div>
      )}
    </Dialog>
  );
}

function TransactionTreatmentDialog({
  title,
  systemPayoff,
  treatmentState,
  onClose,
  onSaved,
}: {
  title: string;
  systemPayoff: boolean;
  treatmentState: CalculationTreatmentState | null;
  onClose: () => void;
  onSaved: (basis: SpendingBasis, reason: string) => void;
}) {
  const loading = !systemPayoff && treatmentState === null;
  const locked = systemPayoff || treatmentState?.treatment.immutable === true;
  const [basis, setBasis] = useState<SpendingBasis>(systemPayoff ? "exclude" : treatmentState?.treatment.spending_basis ?? "transaction_total");
  const [reason, setReason] = useState("");
  const options: { value: SpendingBasis; label: string; description: string }[] = [
    { value: "transaction_total", label: "Transaction total", description: "Count the canonical amount once." },
    { value: "line_items", label: "Line items", description: "Replace the header with complete itemized amounts." },
    { value: "exclude", label: "Exclude from spending", description: "Count no amount in spending reports." },
  ];
  return (
    <Dialog
      eyebrow={locked ? "SYSTEM TREATMENT" : "TRANSACTION DETAIL"}
      title={title}
      onClose={onClose}
      wide
      footer={!locked && !loading ? <><button className="abf-button abf-button-secondary" onClick={onClose} type="button">Cancel</button><button className="abf-button abf-button-primary" disabled={reason.trim().length < 8} onClick={() => onSaved(basis, reason.trim())} type="button">Save treatment</button></> : <button className="abf-button abf-button-primary" onClick={onClose} type="button">Done</button>}
    >
      {loading && <div className="abf-note abf-note-neutral"><Info aria-hidden="true" size={18} /><span><strong>Loading calculation treatment…</strong></span></div>}
      {locked && (
        <div className="abf-note abf-note-success"><ShieldCheck aria-hidden="true" size={19} /><span><strong>Payoff protection is on.</strong><br />This Bank → Credit Card transfer is excluded so paying the bill never counts as new spending.</span></div>
      )}
      {!locked && treatmentState?.treatment.source === "default" && (
        <div className="abf-note abf-note-neutral"><Info aria-hidden="true" size={18} /><span><strong>Using the default treatment.</strong><br />Saving creates a user override while leaving the transaction and its evidence unchanged.</span></div>
      )}
      {!locked && treatmentState?.treatment.source === "user" && (
        <div className="abf-note abf-note-neutral"><Info aria-hidden="true" size={18} /><span><strong>User override is active.</strong><br />{treatmentState.treatment.reason ?? "No saved reason was returned."}</span></div>
      )}
      <fieldset className="abf-treatment-options" disabled={locked || loading}>
        <legend>Spending treatment</legend>
        {options.map((option) => (
          <label className={basis === option.value ? "abf-treatment-selected" : ""} key={option.value}>
            <input checked={basis === option.value} name="spending-treatment" onChange={() => setBasis(option.value)} type="radio" value={option.value} />
            <span><strong>{option.label}</strong><small>{option.description}</small></span>
            {basis === option.value && <Check aria-hidden="true" size={16} />}
          </label>
        ))}
      </fieldset>
      {basis === "line_items" && !locked && (
        <div className="abf-note abf-note-success"><CheckCircle2 aria-hidden="true" size={18} /><span>The API will verify that every line item has a complete amount in the transaction’s original currency.</span></div>
      )}
      {!locked && (
        <label className="abf-field">Reason for change<textarea onChange={(event) => setReason(event.target.value)} placeholder="Explain why this treatment represents spending accurately" rows={3} value={reason} /><span>Required for the audit history. Raw transaction evidence is never changed.</span></label>
      )}
      <div className="abf-balance-spending-split">
        <div><Landmark aria-hidden="true" size={18} /><span><strong>Account balance</strong><small>Still includes this transaction</small></span></div>
        <div><ReceiptText aria-hidden="true" size={18} /><span><strong>Spending</strong><small>{basis === "exclude" ? "Counts SGD 0.00" : basis === "line_items" ? "Uses reconciled line items" : "Uses the transaction total"}</small></span></div>
      </div>
    </Dialog>
  );
}

function BillLifecycleDialog({
  bill,
  onClose,
  onConfirm,
}: {
  bill: BillView;
  onClose: () => void;
  onConfirm: (reason: string) => void;
}) {
  const isDiscard = bill.status === "review";
  const [reason, setReason] = useState("");
  return (
    <Dialog
      eyebrow={isDiscard ? "DISCARD REVIEW BILL" : "VOID BILL"}
      title={isDiscard ? "Discard this review projection?" : "Void this unpaid bill?"}
      onClose={onClose}
      footer={(
        <>
          <button className="abf-button abf-button-secondary" onClick={onClose} type="button">Cancel</button>
          <button className="abf-button abf-button-danger" disabled={!isDiscard && reason.trim().length < 8} onClick={() => onConfirm(reason.trim())} type="button">
            {isDiscard ? "Discard review bill" : "Confirm void"}
          </button>
        </>
      )}
      wide
    >
      <div className="abf-note abf-note-warning">
        <CircleAlert aria-hidden="true" size={18} />
        <span>
          {isDiscard
            ? "Only the bill projection and its projected lines are removed. Canonical transactions, the Bulk Import document, and uploaded evidence are not deleted."
            : "A void bill remains in the list as a read-only audit record. It cannot be paid after it is voided."}
        </span>
      </div>
      <dl className="abf-confirm-summary abf-lifecycle-summary">
        <div><dt>Billing period</dt><dd>{displayDate(bill.periodStart)} – {displayDate(bill.periodEnd)}</dd></div>
        <div><dt>Amount due</dt><dd>{amountLabel(bill.currency, bill.amountDue)}</dd></div>
        <div><dt>Evidence</dt><dd>{bill.evidenceName}</dd></div>
      </dl>
      {!isDiscard && (
        <label className="abf-field">Reason for voiding<textarea autoFocus onChange={(event) => setReason(event.target.value)} placeholder="Why should this bill no longer be payable?" rows={3} value={reason} /><span>Required and retained in the audit record.</span></label>
      )}
    </Dialog>
  );
}

export function AccountFinanceDetailPage({
  accountId,
  accountName,
  institution,
  accountType,
  side,
  onBack,
  backLabel = "Back to accounts",
  initialTab = "balance",
  initialBillId,
  onOpenBulkImport,
  session,
  accounts,
}: AccountFinanceDetailPageProps) {
  const isCreditCard = accountType === "credit_card";
  const [tab, setTab] = useState<FinanceTab>((initialTab === "bills" || initialBillId) && isCreditCard ? "bills" : "balance");
  const [baseline, setBaseline] = useState<OpeningBalance | null>(null);
  const [bills, setBills] = useState<BillView[]>([]);
  const [billDetails, setBillDetails] = useState<Record<string, CreditCardBillDto>>({});
  const [selectedBillId, setSelectedBillId] = useState<string | null>(isCreditCard ? initialBillId ?? null : null);
  const [dialog, setDialog] = useState<DialogState>(null);
  const [toast, setToast] = useState<string | null>(null);
  const bankAccounts = accounts.filter((account) => account.account_type === "bank_account" && !account.deleted_at);
  const [bankAccountId, setBankAccountId] = useState(bankAccounts[0]?.id ?? "");
  const [loading, setLoading] = useState(true);
  const [requestError, setRequestError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [uncategorizedId, setUncategorizedId] = useState<string | null>(null);
  const [lineCandidates, setLineCandidates] = useState<TransactionListItem[]>([]);
  const [paymentCandidates, setPaymentCandidates] = useState<TransactionListItem[]>([]);
  const [selectedPaymentCandidateId, setSelectedPaymentCandidateId] = useState("");
  const [evidenceItems, setEvidenceItems] = useState<BulkEvidenceItemDto[]>([]);
  const [evidenceLoading, setEvidenceLoading] = useState(false);
  const [evidenceError, setEvidenceError] = useState<string | null>(null);
  const [treatmentState, setTreatmentState] = useState<CalculationTreatmentState | null>(null);
  const idempotencyKeys = useRef(new Map<string, string>());

  const idempotencyKeyFor = (fingerprint: string): string => {
    const existing = idempotencyKeys.current.get(fingerprint);
    if (existing) return existing;
    const created = crypto.randomUUID();
    idempotencyKeys.current.set(fingerprint, created);
    return created;
  };

  const completeIdempotentMutation = (fingerprint: string) => {
    idempotencyKeys.current.delete(fingerprint);
  };

  const selectedBill = bills.find((bill) => bill.id === selectedBillId) ?? null;
  const selectedLine = dialog?.kind === "line" && selectedBill
    ? selectedBill.lines.find((line) => line.id === dialog.lineId) ?? null
    : null;

  const load = async (signal?: AbortSignal) => {
    setLoading(true);
    setRequestError(null);
    try {
      const [balanceViews, history, billSummaries, categories] = await Promise.all([
        listAccountBalances(session, signal),
        listOpeningBalanceHistory(session, accountId, signal),
        isCreditCard ? listCreditCardBills(session, accountId, signal) : Promise.resolve([]),
        listTransactionCategories(session, signal),
      ]);
      const balance = balanceViews.find((item) => item.account_id === accountId);
      if (!balance) throw new Error("This Account is unavailable.");
      setBaseline(balanceFromApi(balance, history));
      setBills(billSummaries.map(billFromApi));
      setUncategorizedId(categories.find((category) => category.name === "Uncategorized")?.id ?? categories[0]?.id ?? null);
      if (selectedBillId) {
        const detail = await getCreditCardBill(session, selectedBillId, signal);
        setBillDetails((current) => ({ ...current, [detail.id]: detail }));
        setBills((current) => current.map((item) => item.id === detail.id ? billFromApi(detail) : item));
      }
    } catch (reason) {
      if (!signal?.aborted) setRequestError(reason instanceof Error ? reason.message : "Account finances could not be loaded.");
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  };

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  // load is intentionally scoped to this Account and session.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [accountId, session]);

  useEffect(() => {
    if (!selectedBillId || billDetails[selectedBillId]) return;
    const controller = new AbortController();
    setLoading(true);
    void getCreditCardBill(session, selectedBillId, controller.signal)
      .then((detail) => {
        setBillDetails((current) => ({ ...current, [detail.id]: detail }));
        setBills((current) => current.map((item) => item.id === detail.id ? billFromApi(detail) : item));
      })
      .catch((reason: unknown) => { if (!controller.signal.aborted) setRequestError(reason instanceof Error ? reason.message : "The bill could not be loaded."); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [billDetails, selectedBillId, session]);

  useEffect(() => {
    if (!toast) return;
    const timeout = window.setTimeout(() => setToast(null), 4200);
    return () => window.clearTimeout(timeout);
  }, [toast]);

  const saveBaseline = async (amounts: CurrencyAmount[], asOf: string, reason: string) => {
    setSaving(true);
    setRequestError(null);
    const input = {
      balances: Object.fromEntries(amounts.map((amount) => [amount.currency, majorToMinor(amount.amount)])),
      as_of: new Date(asOf).toISOString(),
      expected_version: baseline?.version ?? 0,
      correction_reason: baseline ? reason : null,
    };
    const fingerprint = `opening-balance:${accountId}:${JSON.stringify(input)}`;
    try {
      const view = await setOpeningBalance(session, accountId, input, idempotencyKeyFor(fingerprint));
      const history = await listOpeningBalanceHistory(session, accountId);
      completeIdempotentMutation(fingerprint);
      setBaseline(balanceFromApi(view, history));
      setDialog(null);
      setToast(baseline ? "Opening balance corrected and recorded in immutable history." : "Opening balance saved. A calculated balance is now available.");
    } catch (reasonValue) {
      setRequestError(reasonValue instanceof Error ? reasonValue.message : "The opening balance could not be saved.");
    } finally {
      setSaving(false);
    }
  };

  const replaceBill = (detail: CreditCardBillDto) => {
    setBillDetails((current) => ({ ...current, [detail.id]: detail }));
    setBills((current) => current.map((bill) => bill.id === detail.id ? billFromApi(detail) : bill));
  };

  const resolveLine = async (resolution: "linked" | "created" | "ignored", reason?: string, transactionId?: string) => {
    if (!selectedLine || !selectedBillId) return;
    const detail = billDetails[selectedBillId];
    if (!detail) return;
    setSaving(true);
    setRequestError(null);
    const fingerprint = resolution === "created" && uncategorizedId
      ? `bill-line-create:${detail.id}:${selectedLine.id}:${uncategorizedId}`
      : null;
    try {
      const updated = resolution === "linked"
        ? transactionId ? await attachBillLine(session, detail, selectedLine.id, transactionId, null) : (() => { throw new Error("Choose a transaction to attach."); })()
        : resolution === "created"
        ? uncategorizedId && fingerprint ? await createBillLineTransaction(session, detail, selectedLine.id, uncategorizedId, idempotencyKeyFor(fingerprint)) : (() => { throw new Error("No transaction category is available."); })()
        : await ignoreBillLine(session, detail, selectedLine.id, reason ?? "Reviewed as non-transaction statement information.");
      if (fingerprint) completeIdempotentMutation(fingerprint);
      replaceBill(updated);
      setDialog(null);
      setToast(resolution === "created" ? "Missing Card transaction created and linked." : resolution === "linked" ? "Existing Card transaction linked." : "Bill line ignored with an audit reason.");
    } catch (reasonValue) {
      setRequestError(reasonValue instanceof Error ? reasonValue.message : "The bill line could not be resolved.");
    } finally {
      setSaving(false);
    }
  };

  const openLine = async (lineId: string) => {
    setLineCandidates([]);
    const detail = selectedBillId ? billDetails[selectedBillId] : null;
    const line = detail?.lines.find((item) => item.id === lineId);
    if (line?.resolution_status === "pending" && line.amount_minor && line.currency && line.occurred_on) {
      try {
        const transactions: TransactionListItem[] = [];
        let cursor: string | null = null;
        do {
          const page = await listTransactionsForAccount(session, accountId, "", cursor);
          transactions.push(...page.items);
          cursor = page.next_cursor;
        } while (cursor);
        const expectedDirection = line.line_kind === "refund" || line.line_kind === "payment" ? "credit" : "debit";
        const expectedAmount = BigInt(line.amount_minor) < 0n ? -BigInt(line.amount_minor) : BigInt(line.amount_minor);
        setLineCandidates(transactions.filter((candidate) => {
          const candidateAmount = BigInt(candidate.original_amount_minor);
          return candidate.transaction_kind === expectedDirection && candidate.original_currency === line.currency && candidateAmount === expectedAmount && candidate.occurred_at.slice(0, 10) === line.occurred_on;
        }));
      } catch (reason) { setRequestError(reason instanceof Error ? reason.message : "Matching transactions could not be loaded."); }
    }
    setDialog({ kind: "line", lineId });
  };

  const openPaymentSuggestion = async () => {
    if (!selectedBillId) return;
    const detail = billDetails[selectedBillId];
    if (!detail) return;
    const ids = detail.payment_candidate_transaction_id ? [detail.payment_candidate_transaction_id] : detail.ambiguous_payment_candidates;
    setSelectedPaymentCandidateId(detail.payment_candidate_transaction_id ?? "");
    try {
      const values = await Promise.all(ids.map((id) => getOwnedTransactionCandidate(session, id)));
      const found = values.filter((value): value is TransactionListItem => value !== null);
      setPaymentCandidates(found);
      if (found.length === 1) setSelectedPaymentCandidateId(found[0].id);
      setDialog({ kind: "suggestion" });
    } catch (reason) {
      setRequestError(reason instanceof Error ? reason.message : "Payment candidates could not be loaded.");
    }
  };

  const openBillEvidence = async () => {
    if (!selectedBillId) return;
    const detail = billDetails[selectedBillId];
    if (!detail) return;
    setEvidenceItems([]);
    setEvidenceError(null);
    setEvidenceLoading(true);
    setDialog({ kind: "evidence" });
    try {
      setEvidenceItems(await getDocumentEvidence(session, detail.bulk_document_id));
    } catch (reason) {
      setEvidenceError(reason instanceof Error ? reason.message : "Evidence could not be loaded.");
    } finally {
      setEvidenceLoading(false);
    }
  };

  const openTransactionTreatment = async (title: string, transactionId: string) => {
    setTreatmentState(null);
    setDialog({ kind: "transaction", title, systemPayoff: false, transactionId });
    try {
      setTreatmentState(await getCalculationTreatment(session, transactionId));
    } catch (reason) {
      setRequestError(reason instanceof Error ? reason.message : "Calculation treatment could not be loaded.");
      setDialog(null);
    }
  };

  const completePayment = async (origin: "completed-suggestion" | "pay-in-full") => {
    if (!selectedBillId) return;
    const detail = billDetails[selectedBillId];
    if (!detail) return;
    setSaving(true);
    setRequestError(null);
    try {
      const candidateId = detail.payment_candidate_transaction_id ?? selectedPaymentCandidateId;
      let selectedDetail = detail;
      if (origin === "completed-suggestion" && !detail.payment_candidate_transaction_id && candidateId) {
        const selectFingerprint = `bill-payment-select:${detail.id}:${candidateId}`;
        selectedDetail = await selectPaymentCandidate(session, detail, candidateId, idempotencyKeyFor(selectFingerprint));
        completeIdempotentMutation(selectFingerprint);
        replaceBill(selectedDetail);
      }
      const confirmFingerprint = candidateId ? `bill-payment-confirm:${detail.id}:${candidateId}` : null;
      const payoffFingerprint = bankAccountId ? `bill-payoff:${detail.id}:${bankAccountId}` : null;
      const updated = origin === "completed-suggestion"
        ? candidateId && confirmFingerprint ? await confirmPaymentCandidate(session, selectedDetail, candidateId, idempotencyKeyFor(confirmFingerprint)) : (() => { throw new Error("No payment candidate is selected."); })()
        : bankAccountId && payoffFingerprint ? await payBillInFull(session, detail, bankAccountId, idempotencyKeyFor(payoffFingerprint)) : (() => { throw new Error("Choose a Bank account."); })();
      if (origin === "completed-suggestion" && confirmFingerprint) completeIdempotentMutation(confirmFingerprint);
      if (origin === "pay-in-full" && payoffFingerprint) completeIdempotentMutation(payoffFingerprint);
      replaceBill(updated);
      setDialog(null);
      setToast(origin === "completed-suggestion" ? "Payment completed with the existing Bank debit." : "Bill paid in full with one atomic Bank → Credit Card transfer.");
    } catch (reasonValue) {
      setRequestError(reasonValue instanceof Error ? reasonValue.message : "The payment could not be completed.");
    } finally {
      setSaving(false);
    }
  };

  const showTab = (nextTab: FinanceTab) => {
    setTab(nextTab);
    if (nextTab !== "bills") setSelectedBillId(null);
  };

  const applyBillLifecycleAction = async (reason: string) => {
    if (!selectedBillId) return;
    const detail = billDetails[selectedBillId];
    if (!detail) return;
    setSaving(true);
    const fingerprint = detail.status === "review"
      ? `bill-discard:${detail.id}`
      : `bill-void:${detail.id}:${reason}`;
    try {
      if (detail.status === "review") {
        await discardReviewBill(session, detail, idempotencyKeyFor(fingerprint));
        completeIdempotentMutation(fingerprint);
        setBills((current) => current.filter((bill) => bill.id !== detail.id));
        setSelectedBillId(null);
        setDialog(null);
        setToast("Review bill projection discarded. Bulk evidence and canonical transactions were retained.");
      } else if (detail.status === "unpaid") {
        const updated = await voidCreditCardBill(session, detail, reason, idempotencyKeyFor(fingerprint));
        completeIdempotentMutation(fingerprint);
        replaceBill(updated);
        setDialog(null);
        setToast("Bill voided and retained in the audit history.");
      }
    } catch (reasonValue) {
      setRequestError(reasonValue instanceof Error ? reasonValue.message : "The bill could not be updated.");
    } finally {
      setSaving(false);
    }
  };

  return (
    <section aria-label={`${accountName} finance details`} className="abf-page">
      <header className="abf-page-header">
        <button className="abf-icon-button abf-page-back" aria-label={backLabel} onClick={onBack} title={backLabel} type="button"><ArrowLeft aria-hidden="true" size={20} /></button>
        <div className="abf-account-mark" aria-hidden="true">{isCreditCard ? <CreditCard size={24} /> : <Landmark size={24} />}</div>
        <div className="abf-account-heading">
          <p className="abf-eyebrow">{side === "liability" ? "LIABILITY" : "ASSET"} · {accountTypeName(accountType).toUpperCase()}</p>
          <h1>{accountName}</h1>
          <p>{institution}</p>
        </div>
      </header>

      <nav aria-label="Account finance sections" className="abf-tabs" role="tablist">
        <button aria-selected={tab === "balance"} onClick={() => showTab("balance")} role="tab" type="button">Balance</button>
        {isCreditCard && <button aria-selected={tab === "bills"} onClick={() => showTab("bills")} role="tab" type="button">Bills <span>{bills.filter((bill) => bill.status === "review" || bill.status === "unpaid").length}</span></button>}
      </nav>

      <div className="abf-content">
        {requestError ? (
          <section className="notice notice-error" role="alert"><CircleAlert aria-hidden="true" size={20} /><div><strong>Couldn’t load account finance data.</strong><p>{requestError}</p></div><button className="button button-secondary" onClick={() => void load()} type="button">Retry</button></section>
        ) : loading ? (
          <section aria-busy="true" aria-label="Loading account finance data" className="account-section" role="status"><div className="skeleton-row" /><div className="skeleton-row" /></section>
        ) : tab === "balance" ? (
          <BalanceWorkspace accountName={accountName} baseline={baseline} onEdit={() => setDialog({ kind: "baseline" })} side={side} />
        ) : selectedBill ? (
          <BillDetail
            bill={selectedBill}
            onBack={() => setSelectedBillId(null)}
            onEvidence={() => void openBillEvidence()}
            onDiagnostics={onOpenBulkImport}
            onLine={(lineId) => void openLine(lineId)}
            onLifecycle={() => setDialog({ kind: "bill-lifecycle" })}
            onPay={() => setDialog({ kind: "pay" })}
            onPayment={() => { setTreatmentState(null); setDialog({ kind: "transaction", title: "Card payoff transfer", systemPayoff: true }); }}
            onSuggestion={() => void openPaymentSuggestion()}
          />
        ) : (
          <BillList bills={bills} onOpen={setSelectedBillId} onOpenBulkImport={onOpenBulkImport} />
        )}
      </div>

      {dialog?.kind === "baseline" && (
        <BaselineEditor accountType={accountType} baseline={baseline} onClose={() => setDialog(null)} onSave={saveBaseline} side={side} />
      )}
      {dialog?.kind === "line" && selectedLine && (
        <LineReviewDialog
          line={selectedLine}
          candidates={lineCandidates}
          onClose={() => setDialog(null)}
          onOpenTransaction={() => {
            const transactionId = billDetails[selectedBillId ?? ""]?.lines.find((line) => line.id === selectedLine.id)?.transaction?.id;
            if (transactionId) void openTransactionTreatment(selectedLine.transactionTitle ?? selectedLine.description, transactionId);
            else setRequestError("The linked transaction identifier is unavailable.");
          }}
          onResolve={resolveLine}
        />
      )}
      {dialog?.kind === "evidence" && (
        <Dialog eyebrow="ORIGINAL EVIDENCE" title={selectedBill?.evidenceName ?? "Credit Card bill evidence"} onClose={() => setDialog(null)} wide>
          {evidenceLoading ? (
            <div className="abf-note abf-note-neutral"><Info aria-hidden="true" size={18} /><span><strong>Creating short-lived evidence links…</strong></span></div>
          ) : evidenceError ? (
            <div className="abf-note abf-note-warning" role="alert"><CircleAlert aria-hidden="true" size={18} /><span><strong>Evidence could not be loaded.</strong><br />{evidenceError}</span><button className="abf-button abf-button-secondary" onClick={() => void openBillEvidence()} type="button">Retry</button></div>
          ) : evidenceItems.length ? (
            <div className="abf-candidate-list">
              {evidenceItems.map((item) => (
                <button key={item.id} onClick={() => openTrustedEvidenceUrl(item.signed_url)} type="button">
                  <span><strong>{item.filename}</strong><small>{item.mime_type} · {(item.byte_size / 1_000_000).toFixed(2)} MB</small></span>
                  <ExternalLink aria-hidden="true" size={17} />
                </button>
              ))}
            </div>
          ) : (
            <div className="abf-note abf-note-neutral"><Info aria-hidden="true" size={18} /><span><strong>No evidence files are available for this bill.</strong></span></div>
          )}
          <div className="abf-note abf-note-neutral"><ShieldCheck aria-hidden="true" size={18} /><span>Links are owner-checked and expire after five minutes.</span></div>
        </Dialog>
      )}
      {dialog?.kind === "suggestion" && selectedBill?.bankDebitSuggestion && (
        <Dialog
          eyebrow="PAYMENT SUGGESTION"
          title="Complete the missing Card leg?"
          onClose={() => setDialog(null)}
          footer={<><button className="abf-button abf-button-secondary" disabled={saving} onClick={() => setDialog(null)} type="button">Not this payment</button><button className="abf-button abf-button-primary" disabled={saving || !selectedPaymentCandidateId} onClick={() => void completePayment("completed-suggestion")} type="button"><Link2 aria-hidden="true" size={17} /> {saving ? "Completing…" : "Confirm & complete payment"}</button></>}
          wide
        >
          {paymentCandidates.length > 1 && (
            <fieldset className="abf-treatment-options">
              <legend>Choose the matching Bank debit</legend>
              {paymentCandidates.map((candidate) => (
                <label className={selectedPaymentCandidateId === candidate.id ? "abf-treatment-selected" : ""} key={candidate.id}>
                  <input checked={selectedPaymentCandidateId === candidate.id} name="payment-candidate" onChange={() => setSelectedPaymentCandidateId(candidate.id)} type="radio" />
                  <span><strong>{candidate.title}</strong><small>{displayDate(candidate.occurred_at)} · {amountLabel(candidate.original_currency, minorToMajor(candidate.original_amount_minor))}</small></span>
                  {selectedPaymentCandidateId === candidate.id && <Check aria-hidden="true" size={16} />}
                </label>
              ))}
            </fieldset>
          )}
          <div className="abf-payment-flow">
            <div className="abf-payment-node"><Landmark aria-hidden="true" size={20} /><span><strong>{selectedBill.bankDebitSuggestion.bankName}</strong><small>Existing debit · {displayDate(selectedBill.bankDebitSuggestion.occurredOn)}</small></span><b>−{amountLabel(selectedBill.currency, selectedBill.amountDue)}</b></div>
            <div className="abf-payment-connector"><ArrowRight aria-hidden="true" size={18} /><span>Create only this missing leg</span></div>
            <div className="abf-payment-node abf-payment-node-new"><CreditCard aria-hidden="true" size={20} /><span><strong>{accountName}</strong><small>New Card credit · linked atomically</small></span><b>+{amountLabel(selectedBill.currency, selectedBill.amountDue)}</b></div>
          </div>
          <div className="abf-note abf-note-warning"><CircleAlert aria-hidden="true" size={18} /><span>Confirm only if the Bank debit paid this exact bill. The existing Bank debit will be kept; a second debit will not be created.</span></div>
          <div className="abf-note abf-note-success"><ShieldCheck aria-hidden="true" size={18} /><span>The linked payoff will affect both account balances and remain excluded from spending.</span></div>
        </Dialog>
      )}
      {dialog?.kind === "pay" && selectedBill && (
        <Dialog
          eyebrow="PAY BILL IN FULL"
          title={`Pay ${amountLabel(selectedBill.currency, selectedBill.amountDue)}`}
          onClose={() => setDialog(null)}
          footer={<><button className="abf-button abf-button-secondary" disabled={saving} onClick={() => setDialog(null)} type="button">Cancel</button><button className="abf-button abf-button-primary" disabled={saving || !bankAccountId} onClick={() => void completePayment("pay-in-full")} type="button">{saving ? "Paying…" : "Confirm payment"}</button></>}
          wide
        >
          <label className="abf-field">Pay from<select onChange={(event) => setBankAccountId(event.target.value)} value={bankAccountId}><option value="">Choose a Bank account</option>{bankAccounts.map((account) => <option key={account.id} value={account.id}>{account.institution_name} · {account.name}</option>)}</select><span>Only active Bank accounts are shown. The API validates currency support.</span></label>
          <div className="abf-pay-summary">
            <div><span>From</span><strong>{bankAccounts.find((account) => account.id === bankAccountId)?.name ?? "Choose an account"}</strong></div>
            <ArrowRight aria-hidden="true" size={20} />
            <div><span>To</span><strong>{accountName}</strong></div>
            <div className="abf-pay-summary-amount"><span>Exact payoff</span><strong>{amountLabel(selectedBill.currency, selectedBill.amountDue)}</strong></div>
          </div>
          <div className="abf-note abf-note-success"><ShieldCheck aria-hidden="true" size={18} /><span>One atomic transfer creates both account legs. The payment cannot partially complete and will be excluded from spending.</span></div>
        </Dialog>
      )}
      {dialog?.kind === "transaction" && (
        <TransactionTreatmentDialog key={`${dialog.transactionId ?? "system"}:${treatmentState?.etag ?? "loading"}`} systemPayoff={dialog.systemPayoff} treatmentState={treatmentState} onClose={() => setDialog(null)} onSaved={(basis, reason) => {
          if (!dialog.transactionId) {
            setRequestError("The linked transaction identifier is unavailable.");
            return;
          }
          setSaving(true);
          void updateCalculationTreatment(session, dialog.transactionId, basis, reason, treatmentState?.etag ?? '"t-0"')
            .then((updated) => { setTreatmentState(updated); setDialog(null); setToast("Spending treatment updated. Account balance and evidence were unchanged."); })
            .catch((cause: unknown) => setRequestError(cause instanceof Error ? cause.message : "The spending treatment could not be saved."))
            .finally(() => setSaving(false));
        }} title={dialog.title} />
      )}
      {dialog?.kind === "bill-lifecycle" && selectedBill && (selectedBill.status === "review" || selectedBill.status === "unpaid") && (
        <BillLifecycleDialog bill={selectedBill} onClose={() => setDialog(null)} onConfirm={(reason) => void applyBillLifecycleAction(reason)} />
      )}

      {toast && <div className="abf-toast" role="status"><CheckCircle2 aria-hidden="true" size={18} /><span>{toast}</span><button aria-label="Dismiss notification" onClick={() => setToast(null)} type="button"><X aria-hidden="true" size={15} /></button></div>}
    </section>
  );
}
