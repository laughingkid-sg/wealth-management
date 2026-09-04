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
import {
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
  majorToMinor,
  minorToMajor,
  type CalculationTreatmentState,
} from "./api";
import type { TransactionListItem } from "../transactions/model";
import "./account-finance-detail.css";
import {
  amountLabel,
  isExplicitZero,
  statusLabel,
  type DraftAmount,
} from "./financeHelpers";

export function Dialog({
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

export function StatusPill({ status }: { status: BillView["status"] }) {
  return (
    <span className={`abf-status abf-status-${status}`}>
      {status === "paid" ? <CheckCircle2 aria-hidden="true" size={13} /> : status === "void" ? <X aria-hidden="true" size={13} /> : <CircleAlert aria-hidden="true" size={13} />}
      {statusLabel(status)}
    </span>
  );
}

export function BaselineEditor({
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

export function BalanceWorkspace({
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

export function BillList({ bills, onOpen, onOpenBulkImport }: { bills: BillView[]; onOpen: (id: string) => void; onOpenBulkImport?: () => void }) {
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

export function LineStatus({ line }: { line: BillLineView }) {
  if (line.status === "linked") return <span className="abf-line-status abf-line-linked"><Link2 aria-hidden="true" size={13} /> Linked</span>;
  if (line.status === "ignored") return <span className="abf-line-status abf-line-ignored"><Unlink aria-hidden="true" size={13} /> Ignored</span>;
  return <span className="abf-line-status abf-line-pending"><CircleAlert aria-hidden="true" size={13} /> Review</span>;
}

export function BillDetail({
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

export function LineReviewDialog({
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

export function TransactionTreatmentDialog({
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

export function BillLifecycleDialog({
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

