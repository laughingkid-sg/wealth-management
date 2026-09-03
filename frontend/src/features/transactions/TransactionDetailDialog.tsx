import { useEffect, useRef, useState, type FormEvent } from "react";
import type { Session } from "@supabase/supabase-js";
import {
  ArrowLeftRight,
  CircleAlert,
  FileSearch,
  Link2Off,
  RefreshCw,
  Save,
  X,
} from "lucide-react";
import { AccessibleDialog } from "./AccessibleDialog";
import {
  getTransactionSources,
  listOwnedAccounts,
  listTransactionCategories,
  patchTransaction,
  unmatchSourceLink,
} from "./api";
import {
  AccountSelect,
  CategorySelect,
  LineItemsEditor,
} from "./TransactionForms";
import {
  lineItemsToDrafts,
  parseLineItemDrafts,
  type LineItemDraft,
} from "./transactionFormModel";
import {
  formatAmount,
  formatDateTime,
  isISO4217Currency,
  majorAmountToMinor,
  minorAmountToMajor,
  toDateTimeLocal,
  toRFC3339,
  type OwnedAccountOption,
  type SourceSummary,
  type TransactionCategory,
  type TransactionListItem,
} from "./model";

function sourceTitle(source: SourceSummary): string {
  return source.suggested_title || source.subject || "Untitled transaction evidence";
}

export function TransactionDetailDialog({
  session,
  transaction,
  close,
  inspectSource,
  saved,
}: {
  session: Session;
  transaction: TransactionListItem;
  close: () => void;
  inspectSource: (source: SourceSummary) => void;
  saved: (message: string) => void;
}) {
  const [title, setTitle] = useState(transaction.title);
  const [merchantName, setMerchantName] = useState(transaction.merchant_name ?? "");
  const [accountId, setAccountId] = useState(transaction.account_id);
  const [occurredAt, setOccurredAt] = useState(toDateTimeLocal(transaction.occurred_at));
  const [originalAmount, setOriginalAmount] = useState(
    minorAmountToMajor(transaction.original_amount_minor, transaction.original_currency),
  );
  const [originalCurrency, setOriginalCurrency] = useState(transaction.original_currency);
  const [sgdAmount, setSgdAmount] = useState(
    transaction.sgd_amount_minor
      ? minorAmountToMajor(transaction.sgd_amount_minor, "SGD")
      : "",
  );
  const [categoryId, setCategoryId] = useState(transaction.category_id ?? "");
  const [userNotes, setUserNotes] = useState(transaction.user_notes ?? "");
  const [lineItems, setLineItems] = useState<LineItemDraft[]>(
    lineItemsToDrafts(transaction.line_items),
  );
  const [accounts, setAccounts] = useState<OwnedAccountOption[]>([]);
  const [categories, setCategories] = useState<TransactionCategory[]>([]);
  const [sources, setSources] = useState<SourceSummary[]>([]);
  const [referenceLoading, setReferenceLoading] = useState(true);
  const [referenceError, setReferenceError] = useState<string | null>(null);
  const [referenceReload, setReferenceReload] = useState(0);
  const [evidenceLoading, setEvidenceLoading] = useState(true);
  const [evidenceError, setEvidenceError] = useState<string | null>(null);
  const [evidenceSuccess, setEvidenceSuccess] = useState<string | null>(null);
  const [evidenceReload, setEvidenceReload] = useState(0);
  const [confirmUnmatchId, setConfirmUnmatchId] = useState<string | null>(null);
  const [unmatchingId, setUnmatchingId] = useState<string | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const confirmUnmatchButtonRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([
      listOwnedAccounts(session, controller.signal),
      listTransactionCategories(session, controller.signal),
    ])
      .then(([accountItems, categoryItems]) => {
        setAccounts(accountItems);
        setCategories(categoryItems);
        setAccountId((current) =>
          accountItems.some(({ id }) => id === current) ? current : "",
        );
        setCategoryId((current) =>
          !current || categoryItems.some(({ id }) => id === current) ? current : "",
        );
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setReferenceError(
            error instanceof Error ? error.message : "Couldn’t load transaction choices.",
          );
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setReferenceLoading(false);
      });
    return () => controller.abort();
  }, [referenceReload, session]);

  useEffect(() => {
    const controller = new AbortController();
    void getTransactionSources(session, transaction.id, controller.signal)
      .then(setSources)
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setEvidenceError(
            error instanceof Error ? error.message : "Couldn’t load supporting evidence.",
          );
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setEvidenceLoading(false);
      });
    return () => controller.abort();
  }, [evidenceReload, session, transaction.id]);

  useEffect(() => {
    if (!confirmUnmatchId) return;
    const frame = window.requestAnimationFrame(() => confirmUnmatchButtonRef.current?.focus());
    return () => window.cancelAnimationFrame(frame);
  }, [confirmUnmatchId]);

  const selectedAccountIsActive = accounts.some(({ id }) => id === accountId);
  const selectedCategoryIsActive =
    categoryId === "" || categories.some(({ id }) => id === categoryId);
  const originalAccountUnavailable =
    !referenceLoading &&
    !referenceError &&
    !accounts.some(({ id }) => id === transaction.account_id);
  const originalCategoryUnavailable =
    !referenceLoading &&
    !referenceError &&
    Boolean(transaction.category_id) &&
    !categories.some(({ id }) => id === transaction.category_id);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFormError(null);
    const normalizedTitle = title.trim();
    const normalizedMerchantName = merchantName.trim();
    const normalizedCurrency = originalCurrency.trim().toUpperCase();
    const timestamp = toRFC3339(occurredAt);
    const parsedLines = parseLineItemDrafts(lineItems);
    if (!normalizedTitle || normalizedTitle.length > 250) {
      setFormError("Title must contain 1 to 250 characters.");
      return;
    }
    if (!selectedAccountIsActive) {
      setFormError("Choose an active account.");
      return;
    }
    if (!timestamp) {
      setFormError("Enter a valid transaction date and time.");
      return;
    }
    if (!isISO4217Currency(normalizedCurrency)) {
      setFormError("Original currency must be an ISO 4217 code.");
      return;
    }
    if (!selectedCategoryIsActive) {
      setFormError("Choose an active category or leave the transaction uncategorized.");
      return;
    }
    if (normalizedMerchantName.length > 250) {
      setFormError("Merchant or payee must contain at most 250 characters.");
      return;
    }
    const normalizedUserNotes = userNotes.trim();
    if ([...normalizedUserNotes].length > 4000) {
      setFormError("User notes must contain at most 4,000 characters.");
      return;
    }
    let originalAmountMinor: string;
    try {
      originalAmountMinor = majorAmountToMinor(originalAmount, normalizedCurrency);
    } catch (error) {
      setFormError(`Original amount: ${error instanceof Error ? error.message : "Enter a valid amount."}`);
      return;
    }
    let sgdAmountMinor: string | null = null;
    if (normalizedCurrency === "SGD") {
      sgdAmountMinor = originalAmountMinor;
    } else if (sgdAmount.trim()) {
      try {
        sgdAmountMinor = majorAmountToMinor(sgdAmount, "SGD");
      } catch (error) {
        setFormError(`SGD amount: ${error instanceof Error ? error.message : "Enter a valid amount."}`);
        return;
      }
    }
    if (parsedLines.error) {
      setFormError(parsedLines.error);
      return;
    }
    setSaving(true);
    try {
      await patchTransaction(session, transaction.id, {
        title: normalizedTitle,
        merchant_name: normalizedMerchantName || null,
        account_id: accountId,
        occurred_at: timestamp,
        original_amount_minor: originalAmountMinor,
        original_currency: normalizedCurrency,
        sgd_amount_minor: sgdAmountMinor,
        category_id: categoryId || null,
        line_items: parsedLines.items,
        user_notes: normalizedUserNotes || null,
      });
      saved("Transaction changes were saved.");
      close();
    } catch (error: unknown) {
      setFormError(error instanceof Error ? error.message : "Couldn’t save this transaction.");
    } finally {
      setSaving(false);
    }
  }

  async function unmatch(source: SourceSummary) {
    if (!source.source_link_id) return;
    setUnmatchingId(source.source_link_id);
    setEvidenceError(null);
    setEvidenceSuccess(null);
    try {
      await unmatchSourceLink(session, source.source_link_id);
      setSources((current) => current.filter(({ source_link_id }) => source_link_id !== source.source_link_id));
      setConfirmUnmatchId(null);
      setEvidenceSuccess("Evidence was unmatched and remains available for review or reattachment.");
      saved("Evidence was unmatched and is available for review or reattachment.");
    } catch (error: unknown) {
      setEvidenceError(error instanceof Error ? error.message : "Couldn’t unmatch this evidence.");
    } finally {
      setUnmatchingId(null);
    }
  }

  return (
    <AccessibleDialog
      className="transaction-detail-dialog"
      descriptionId="transaction-detail-description"
      onClose={close}
      titleId="transaction-detail-title"
    >
      <header className="modal-header">
        <div>
          <p className="eyebrow">TRANSACTION DETAIL</p>
          <h2 id="transaction-detail-title">{transaction.title}</h2>
          <p className="muted" id="transaction-detail-description">
            {transaction.transaction_kind === "debit" ? "Money out" : "Money in"} · {formatDateTime(transaction.occurred_at)}
          </p>
        </div>
        <button aria-label="Close transaction detail" className="icon-button" data-dialog-initial-focus onClick={close} type="button">
          <X aria-hidden="true" size={18} />
        </button>
      </header>

      <div className="transaction-summary-strip">
        <div><span>Original amount</span><strong>{formatAmount(transaction.original_amount_minor, transaction.original_currency)}</strong></div>
        <div><span>SGD evidence</span><strong>{transaction.sgd_amount_minor ? formatAmount(transaction.sgd_amount_minor, "SGD") : "Not supplied"}</strong></div>
        <div><span>Review state</span><strong>{transaction.review_status === "confirmed" ? "Confirmed" : transaction.review_status === "review_required" ? "Needs review" : "Processing"}</strong></div>
        <div><span>Match confidence</span><strong>{transaction.match_confidence === null ? "Not scored" : `${transaction.match_confidence}%`}</strong></div>
      </div>

      {transaction.transfer_link && (
        <section className="transfer-link-summary" aria-label="Internal transfer link">
          <ArrowLeftRight aria-hidden="true" size={20} />
          <div><strong>Internal transfer</strong><p>Paired with {transaction.transfer_link.counterpart_title || "the other leg"}{transaction.transfer_link.counterpart_account_name ? ` in ${transaction.transfer_link.counterpart_account_name}` : ""}.</p></div>
        </section>
      )}

      {referenceError && (
        <section className="notice notice-error" role="alert">
          <CircleAlert aria-hidden="true" size={20} />
          <div><strong>Couldn’t load edit choices.</strong><p>{referenceError}</p></div>
          <button className="button button-secondary" onClick={() => { setReferenceLoading(true); setReferenceError(null); setReferenceReload((value) => value + 1); }} type="button"><RefreshCw aria-hidden="true" size={16} /> Retry</button>
        </section>
      )}

      {!referenceLoading && !referenceError && (originalAccountUnavailable || originalCategoryUnavailable) && (
        <section className="notice reference-warning" role="status">
          <CircleAlert aria-hidden="true" size={20} />
          <div>
            <strong>Some saved choices are no longer active.</strong>
            <p>
              {originalAccountUnavailable
                ? "Choose a replacement active account before saving. "
                : ""}
              {originalCategoryUnavailable
                ? "The inactive category was cleared; choose a replacement or leave this uncategorized."
                : ""}
            </p>
          </div>
        </section>
      )}

      <form className="transaction-edit-form" onSubmit={(event) => void submit(event)}>
        <fieldset disabled={saving || referenceLoading || Boolean(referenceError)}>
          <legend>Canonical fields</legend>
          <div className="form-grid">
            <label>Title<input maxLength={250} onChange={(event) => setTitle(event.target.value)} required value={title} /></label>
            <label>Merchant or payee <span className="optional">(optional)</span><input maxLength={250} onChange={(event) => setMerchantName(event.target.value)} value={merchantName} /></label>
            <label>Account<AccountSelect accounts={accounts} disabled={referenceLoading} onChange={setAccountId} value={accountId} /></label>
            <label>Date and time<input onChange={(event) => setOccurredAt(event.target.value)} required type="datetime-local" value={occurredAt} /></label>
            <label>Original amount<input inputMode="decimal" onChange={(event) => { const value = event.target.value; setOriginalAmount(value); if (originalCurrency === "SGD") setSgdAmount(value); }} placeholder="0.00" required type="text" value={originalAmount} /></label>
            <label>Original currency<input autoCapitalize="characters" maxLength={3} onChange={(event) => { const value = event.target.value.toUpperCase(); if (value === "SGD") setSgdAmount(originalAmount); else if (originalCurrency === "SGD") setSgdAmount(""); setOriginalCurrency(value); }} pattern="[A-Z]{3}" required value={originalCurrency} /></label>
            <label>SGD amount <span className="optional">({originalCurrency === "SGD" ? "matches original" : "optional"})</span><input disabled={originalCurrency === "SGD"} inputMode="decimal" onChange={(event) => setSgdAmount(event.target.value)} placeholder="0.00" type="text" value={originalCurrency === "SGD" ? originalAmount : sgdAmount} /></label>
            <label>Category<CategorySelect categories={categories} disabled={referenceLoading} onChange={setCategoryId} value={categoryId} /></label>
          </div>
          <label>User notes <span className="optional">(optional)</span><textarea maxLength={4000} onChange={(event) => setUserNotes(event.target.value)} rows={3} value={userNotes} /></label>
        </fieldset>
        <LineItemsEditor defaultCurrency={originalCurrency} disabled={saving} drafts={lineItems} onChange={setLineItems} />
        {formError && <p className="form-error" role="alert">{formError}</p>}
        <div className="modal-actions">
          <button className="button button-secondary" onClick={close} type="button">Cancel</button>
          <button className="button button-primary" disabled={saving || referenceLoading || Boolean(referenceError) || !selectedAccountIsActive || !selectedCategoryIsActive} type="submit"><Save aria-hidden="true" size={17} />{saving ? "Saving…" : "Save changes"}</button>
        </div>
      </form>

      <section aria-labelledby="transaction-evidence-title" className="transaction-evidence-section">
        <div className="section-heading-inline">
          <div><p className="eyebrow">SUPPORTING EVIDENCE</p><h3 id="transaction-evidence-title">{sources.length} active source{sources.length === 1 ? "" : "s"}</h3></div>
          {evidenceError && <button className="button button-secondary button-compact" onClick={() => { setEvidenceLoading(true); setEvidenceError(null); setEvidenceReload((value) => value + 1); }} type="button"><RefreshCw aria-hidden="true" size={15} /> Retry</button>}
        </div>
        {evidenceLoading ? (
          <p aria-live="polite" className="source-loading" role="status">Loading attached evidence…</p>
        ) : evidenceError ? (
          <p className="form-error" role="alert">{evidenceError}</p>
        ) : sources.length === 0 ? (
          <p className="muted">There is no active evidence attached to this transaction.</p>
        ) : (
          <ul className="evidence-list">
            {sources.map((source) => (
              <li key={source.source_link_id ?? source.id}>
                <div><strong>{sourceTitle(source)}</strong><p>{source.sender || source.provider} · {formatDateTime(source.received_at)}</p></div>
                <div className="evidence-actions">
                  <button aria-label={`Inspect evidence ${sourceTitle(source)} received ${formatDateTime(source.received_at)}`} className="button button-secondary button-compact" onClick={() => inspectSource(source)} type="button"><FileSearch aria-hidden="true" size={16} /> Inspect</button>
                  {confirmUnmatchId === source.source_link_id ? (
                    <span className="confirm-actions">
                      <span className="sr-only" role="status">Confirm unmatching {sourceTitle(source)}.</span>
                      <button aria-label={`Confirm unmatch ${sourceTitle(source)}`} className="button button-danger button-compact" disabled={unmatchingId === source.source_link_id} onClick={() => void unmatch(source)} ref={confirmUnmatchButtonRef} type="button">{unmatchingId === source.source_link_id ? "Unmatching…" : "Confirm unmatch"}</button>
                      <button aria-label={`Cancel unmatch ${sourceTitle(source)}`} className="text-button" onClick={() => setConfirmUnmatchId(null)} type="button">Cancel</button>
                    </span>
                  ) : (
                    <button aria-label={`Unmatch evidence ${sourceTitle(source)}`} className="button button-secondary button-compact" disabled={!source.source_link_id} onClick={() => setConfirmUnmatchId(source.source_link_id)} type="button"><Link2Off aria-hidden="true" size={16} /> Unmatch</button>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
        {evidenceSuccess && <p className="form-success" role="status">{evidenceSuccess}</p>}
      </section>
    </AccessibleDialog>
  );
}
