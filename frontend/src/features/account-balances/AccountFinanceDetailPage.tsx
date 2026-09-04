import {
  ArrowLeft,
  ArrowRight,
  Check,
  CheckCircle2,
  CircleAlert,
  CreditCard,
  ExternalLink,
  Info,
  Landmark,
  Link2,
  ShieldCheck,
  X,
} from "lucide-react";
import {
  useEffect,
  useRef,
  useState,
} from "react";
import type { Session } from "@supabase/supabase-js";
import type { Account } from "../accounts/model";
import {
  accountTypeName,
  displayDate,
  type AccountFinanceAccountType,
  type AccountFinanceSide,
  type CurrencyAmount,
  type BillView,
  type OpeningBalance,
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
  type CalculationTreatmentState,
  type CreditCardBillDto,
} from "./api";
import { getDocumentEvidence, type BulkEvidenceItemDto } from "../bulk-import/api";
import { getOwnedTransactionCandidate, listTransactionCategories, listTransactionsForAccount } from "../transactions/api";
import type { TransactionListItem } from "../transactions/model";
import "./account-finance-detail.css";
import {
  amountLabel,
  openTrustedEvidenceUrl,
  balanceFromApi,
  billFromApi,
  type FinanceTab,
  type DialogState,
} from "./financeHelpers";
import {
  Dialog,
  BaselineEditor,
  BalanceWorkspace,
  BillList,
  BillDetail,
  LineReviewDialog,
  TransactionTreatmentDialog,
  BillLifecycleDialog,
} from "./financeSections";

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
