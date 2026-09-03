import { useEffect, useRef, useState, type FormEvent } from "react";
import type { Session } from "@supabase/supabase-js";
import { CircleAlert, RefreshCw, Save, X } from "lucide-react";
import { AccessibleDialog } from "./AccessibleDialog";
import {
  createManualTransaction,
  findLikelyManualTransactionDuplicates,
  listOwnedAccounts,
  listTransactionCategories,
} from "./api";
import { AccountSelect, CategorySelect, LineItemsEditor } from "./TransactionForms";
import {
  emptyManualTransactionDraft,
  validateManualTransactionDraft,
  type ManualTransactionDraft,
} from "./manualTransactionModel";
import {
  formatAmount,
  formatDateTime,
  type OwnedAccountOption,
  type TransactionCategory,
  type TransactionListItem,
} from "./model";

export interface ManualTransactionDialogProps {
  session: Session;
  onClose: () => void;
  onCreated: (transaction: TransactionListItem) => void;
}

export function ManualTransactionDialog({
  session,
  onClose,
  onCreated,
}: ManualTransactionDialogProps) {
  const [draft, setDraft] = useState<ManualTransactionDraft>(emptyManualTransactionDraft);
  const [accounts, setAccounts] = useState<OwnedAccountOption[]>([]);
  const [categories, setCategories] = useState<TransactionCategory[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [reload, setReload] = useState(0);
  const [checking, setChecking] = useState(false);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [duplicates, setDuplicates] = useState<TransactionListItem[]>([]);
  const [approvedDraft, setApprovedDraft] = useState<string | null>(null);
  const operationInFlight = useRef(false);

  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([
      listOwnedAccounts(session, controller.signal),
      listTransactionCategories(session, controller.signal),
    ])
      .then(([accountItems, categoryItems]) => {
        setAccounts(accountItems);
        setCategories(categoryItems);
        setDraft((current) => ({
          ...current,
          accountId: accountItems.some(({ id }) => id === current.accountId)
            ? current.accountId
            : "",
          categoryId: categoryItems.some(({ id }) => id === current.categoryId)
            ? current.categoryId
            : "",
        }));
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setLoadError(
            error instanceof Error ? error.message : "Couldn’t load transaction choices.",
          );
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [reload, session]);

  function update<K extends keyof ManualTransactionDraft>(
    field: K,
    value: ManualTransactionDraft[K],
  ) {
    setDraft((current) => {
      const next = { ...current, [field]: value };
      if (field === "originalAmount" && current.originalCurrency === "SGD") {
        next.sgdAmount = String(value);
      }
      if (field === "originalCurrency" && value === "SGD") {
        next.sgdAmount = current.originalAmount;
      } else if (field === "originalCurrency" && current.originalCurrency === "SGD") {
        next.sgdAmount = "";
      }
      return next;
    });
    setDuplicates([]);
    setApprovedDraft(null);
    setFormError(null);
  }

  async function create(force: boolean) {
    setFormError(null);
    const validated = validateManualTransactionDraft(draft, accounts, categories);
    if (validated.error || !validated.input) {
      setFormError(validated.error);
      return;
    }
    const signature = JSON.stringify(validated.input);
    if (force && approvedDraft !== signature) {
      setDuplicates([]);
      setApprovedDraft(null);
      setFormError("The transaction changed. Check for likely duplicates again.");
      return;
    }
    if (operationInFlight.current) return;
    operationInFlight.current = true;
    let phase: "checking" | "creating" = force ? "creating" : "checking";
    try {
      if (!force) {
        setChecking(true);
        const likely = await findLikelyManualTransactionDuplicates(session, validated.input);
        if (likely.length > 0) {
          setDuplicates(likely);
          setApprovedDraft(signature);
          return;
        }
        setChecking(false);
      }
      phase = "creating";
      setSaving(true);
      const transaction = await createManualTransaction(session, validated.input);
      onCreated(transaction);
      onClose();
    } catch (error: unknown) {
      setFormError(
        error instanceof Error
          ? error.message
          : phase === "checking"
            ? "Couldn’t check for likely duplicates."
            : "Couldn’t create this transaction.",
      );
    } finally {
      operationInFlight.current = false;
      setChecking(false);
      setSaving(false);
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void create(false);
  }

  const busy = checking || saving;
  const disabled = busy || loading || Boolean(loadError);

  return (
    <AccessibleDialog
      className="manual-transaction-dialog"
      descriptionId="manual-transaction-description"
      onClose={onClose}
      titleId="manual-transaction-title"
    >
      <header className="modal-header">
        <div>
          <p className="eyebrow">MANUAL TRANSACTION</p>
          <h2 id="manual-transaction-title">Add a transaction</h2>
          <p className="muted" id="manual-transaction-description">
            Record money in or out. A duplicate check runs before anything is created.
          </p>
        </div>
        <button
          aria-label="Close manual transaction"
          className="icon-button"
          data-dialog-initial-focus
          onClick={onClose}
          type="button"
        >
          <X aria-hidden="true" size={18} />
        </button>
      </header>

      {loading && (
        <p aria-live="polite" className="source-loading" role="status">
          Loading accounts and categories…
        </p>
      )}
      {loadError && (
        <section className="notice notice-error" role="alert">
          <CircleAlert aria-hidden="true" size={20} />
          <div><strong>Couldn’t prepare this transaction.</strong><p>{loadError}</p></div>
          <button
            className="button button-secondary"
            onClick={() => {
              setLoading(true);
              setLoadError(null);
              setReload((value) => value + 1);
            }}
            type="button"
          >
            <RefreshCw aria-hidden="true" size={16} /> Retry
          </button>
        </section>
      )}
      {!loading && !loadError && accounts.length === 0 && (
        <section className="notice notice-error" role="status">
          <CircleAlert aria-hidden="true" size={20} />
          <div>
            <strong>Add an active account first.</strong>
            <p>Every transaction must belong to one of your active Accounts.</p>
          </div>
        </section>
      )}

      <form className="manual-transaction-form" onSubmit={submit}>
        <fieldset disabled={disabled}>
          <legend>Transaction details</legend>
          <div className="form-grid">
            <label>
              Account
              <AccountSelect
                accounts={accounts}
                disabled={loading}
                onChange={(value) => update("accountId", value)}
                value={draft.accountId}
              />
            </label>
            <label>
              Type
              <select
                onChange={(event) => update("kind", event.target.value as "debit" | "credit")}
                value={draft.kind}
              >
                <option value="debit">Debit · money out</option>
                <option value="credit">Credit · money in</option>
              </select>
            </label>
            <label>
              Title
              <input
                maxLength={250}
                onChange={(event) => update("title", event.target.value)}
                required
                value={draft.title}
              />
            </label>
            <label>
              Merchant or payee <span className="optional">(optional)</span>
              <input
                maxLength={250}
                onChange={(event) => update("merchantName", event.target.value)}
                value={draft.merchantName}
              />
            </label>
            <label>
              Date and time
              <input
                onChange={(event) => update("occurredAt", event.target.value)}
                required
                type="datetime-local"
                value={draft.occurredAt}
              />
            </label>
            <label>
              Original amount
              <input
                inputMode="decimal"
                onChange={(event) => update("originalAmount", event.target.value)}
                placeholder="0.00"
                required
                type="text"
                value={draft.originalAmount}
              />
            </label>
            <label>
              Original currency
              <input
                autoCapitalize="characters"
                maxLength={3}
                onChange={(event) => update("originalCurrency", event.target.value.toUpperCase())}
                pattern="[A-Z]{3}"
                required
                value={draft.originalCurrency}
              />
            </label>
            <label>
              SGD amount <span className="optional">({draft.originalCurrency === "SGD" ? "matches original" : "optional"})</span>
              <input
                disabled={draft.originalCurrency === "SGD"}
                inputMode="decimal"
                onChange={(event) => update("sgdAmount", event.target.value)}
                placeholder="0.00"
                type="text"
                value={draft.originalCurrency === "SGD" ? draft.originalAmount : draft.sgdAmount}
              />
            </label>
            <label>
              Category
              <CategorySelect
                categories={categories}
                disabled={loading}
                onChange={(value) => update("categoryId", value)}
                value={draft.categoryId}
              />
            </label>
          </div>
          <label>
            User notes <span className="optional">(optional)</span>
            <textarea
              maxLength={4000}
              onChange={(event) => update("userNotes", event.target.value)}
              rows={3}
              value={draft.userNotes}
            />
          </label>
        </fieldset>

        <LineItemsEditor
          defaultCurrency={draft.originalCurrency}
          disabled={disabled}
          drafts={draft.lineItems}
          onChange={(value) => update("lineItems", value)}
        />

        {duplicates.length > 0 && (
          <section className="notice manual-duplicate-warning" role="alert">
            <CircleAlert aria-hidden="true" size={20} />
            <div>
              <strong>Likely duplicate{duplicates.length === 1 ? "" : "s"} found.</strong>
              <p>These transactions use the same account, type, amount, currency, and a time within 10 minutes.</p>
              <ul>
                {duplicates.map((transaction) => (
                  <li key={transaction.id}>
                    <strong>{transaction.title}</strong>
                    <span>{formatAmount(transaction.original_amount_minor, transaction.original_currency)} · {formatDateTime(transaction.occurred_at)}</span>
                  </li>
                ))}
              </ul>
              <div className="manual-duplicate-actions">
                <button
                  className="button button-danger"
                  disabled={busy}
                  onClick={() => void create(true)}
                  type="button"
                >
                  {saving ? "Creating…" : "Create anyway"}
                </button>
                <button
                  className="button button-secondary"
                  disabled={busy}
                  onClick={() => {
                    setDuplicates([]);
                    setApprovedDraft(null);
                  }}
                  type="button"
                >
                  Review details
                </button>
              </div>
            </div>
          </section>
        )}

        {formError && <p className="form-error" role="alert">{formError}</p>}
        <div className="modal-actions">
          <button className="button button-secondary" onClick={onClose} type="button">Cancel</button>
          <button
            className="button button-primary"
            disabled={disabled || accounts.length === 0 || duplicates.length > 0}
            type="submit"
          >
            <Save aria-hidden="true" size={17} />
            {checking ? "Checking…" : saving ? "Creating…" : "Create transaction"}
          </button>
        </div>
      </form>
    </AccessibleDialog>
  );
}
