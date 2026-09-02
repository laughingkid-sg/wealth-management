import { useEffect, useState, type FormEvent } from "react";
import type { Session } from "@supabase/supabase-js";
import { ArrowLeftRight, CircleAlert, Paperclip, RefreshCw, Save, X } from "lucide-react";
import { AccessibleDialog } from "./AccessibleDialog";
import { createInternalTransfer, listOwnedAccounts, listTransactionCategories } from "./api";
import {
  AccountSelect,
  CategorySelect,
  LineItemsEditor,
} from "./TransactionForms";
import { parseLineItemDrafts, type LineItemDraft } from "./transactionFormModel";
import {
  isISO4217Currency,
  toDateTimeLocal,
  toRFC3339,
  type InternalTransferSourceSeed,
  type OwnedAccountOption,
  type TransactionCategory,
  type TransferLegInput,
} from "./model";
import { sourceIDsForTransferLeg } from "./transactionUiModel";

interface LegDraft {
  title: string;
  accountId: string;
  amount: string;
  currency: string;
  sgdAmount: string;
  occurredAt: string;
  categoryId: string;
  lineItems: LineItemDraft[];
  sourceIds: string[];
}

function newLeg(title: string, sourceIds: string[]): LegDraft {
  return {
    title,
    accountId: "",
    amount: "",
    currency: "SGD",
    sgdAmount: "",
    occurredAt: toDateTimeLocal(new Date().toISOString()),
    categoryId: "",
    lineItems: [],
    sourceIds,
  };
}

function validateLeg(draft: LegDraft, label: string): { value: TransferLegInput | null; error: string | null } {
  const title = draft.title.trim();
  const currency = draft.currency.trim().toUpperCase();
  const occurredAt = toRFC3339(draft.occurredAt);
  if (!title || title.length > 250) return { value: null, error: `${label} title must contain 1 to 250 characters.` };
  if (!draft.accountId) return { value: null, error: `Choose the ${label.toLowerCase()} account.` };
  if (!/^\d+$/.test(draft.amount) || BigInt(draft.amount) === 0n) return { value: null, error: `${label} amount must be a positive minor-unit integer.` };
  if (!isISO4217Currency(currency)) return { value: null, error: `${label} currency must be an ISO 4217 code.` };
  if (draft.sgdAmount && (!/^\d+$/.test(draft.sgdAmount) || BigInt(draft.sgdAmount) === 0n)) return { value: null, error: `${label} SGD amount must be empty or a positive minor-unit integer.` };
  if (!occurredAt) return { value: null, error: `${label} date and time is invalid.` };
  const lines = parseLineItemDrafts(draft.lineItems);
  if (lines.error) return { value: null, error: `${label}: ${lines.error}` };
  return {
    value: {
      title,
      account_id: draft.accountId,
      original_amount_minor: draft.amount,
      original_currency: currency,
      sgd_amount_minor: draft.sgdAmount || null,
      occurred_at: occurredAt,
      category_id: draft.categoryId || null,
      line_items: lines.items,
      source_ids: draft.sourceIds,
    },
    error: null,
  };
}

export function InternalTransferDialog({
  session,
  close,
  saved,
  initialSource,
}: {
  session: Session;
  close: () => void;
  saved: (message: string) => void;
  initialSource?: InternalTransferSourceSeed;
}) {
  const [debit, setDebit] = useState<LegDraft>(() =>
    newLeg("Transfer out", sourceIDsForTransferLeg(initialSource, "debit")),
  );
  const [credit, setCredit] = useState<LegDraft>(() =>
    newLeg("Transfer in", sourceIDsForTransferLeg(initialSource, "credit")),
  );
  const [accounts, setAccounts] = useState<OwnedAccountOption[]>([]);
  const [categories, setCategories] = useState<TransactionCategory[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [reload, setReload] = useState(0);
  const [formError, setFormError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const sameAccount = debit.accountId !== "" && debit.accountId === credit.accountId;

  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([
      listOwnedAccounts(session, controller.signal),
      listTransactionCategories(session, controller.signal),
    ])
      .then(([accountItems, categoryItems]) => {
        setAccounts(accountItems);
        setCategories(categoryItems);
        const transferCategory = categoryItems.find(
          ({ parent_name, name }) => parent_name === "Transfers" && name === "Transfer",
        );
        const reconcileLeg = (current: LegDraft): LegDraft => ({
          ...current,
          accountId: accountItems.some(({ id }) => id === current.accountId)
            ? current.accountId
            : "",
          categoryId: categoryItems.some(({ id }) => id === current.categoryId)
            ? current.categoryId
            : transferCategory?.id ?? "",
        });
        setDebit(reconcileLeg);
        setCredit(reconcileLeg);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setLoadError(error instanceof Error ? error.message : "Couldn’t load transfer choices.");
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [reload, session]);

  function legFields(
    legend: string,
    kind: "debit" | "credit",
    draft: LegDraft,
    setDraft: (updater: (current: LegDraft) => LegDraft) => void,
  ) {
    const update = <K extends keyof LegDraft>(field: K, value: LegDraft[K]) =>
      setDraft((current) => ({ ...current, [field]: value }));
    return (
      <fieldset className={`transfer-leg ${kind}`} disabled={saving || loading || Boolean(loadError)}>
        <legend>{legend}</legend>
        {initialSource && draft.sourceIds.includes(initialSource.id) && (
          <p className="transfer-evidence-seed">
            <Paperclip aria-hidden="true" size={15} />
            Evidence: {initialSource.title}
          </p>
        )}
        <div className="form-grid">
          <label>Title<input maxLength={250} onChange={(event) => update("title", event.target.value)} required value={draft.title} /></label>
          <label>Account<AccountSelect accounts={accounts} disabled={loading} excludedId={kind === "debit" ? credit.accountId : debit.accountId} onChange={(value) => update("accountId", value)} value={draft.accountId} /></label>
          <label>Amount <span className="optional">(minor units)</span><input inputMode="numeric" min="1" onChange={(event) => update("amount", event.target.value)} required type="number" value={draft.amount} /></label>
          <label>Currency<input autoCapitalize="characters" maxLength={3} onChange={(event) => update("currency", event.target.value.toUpperCase())} pattern="[A-Z]{3}" required value={draft.currency} /></label>
          <label>SGD amount <span className="optional">(minor units, optional)</span><input inputMode="numeric" min="1" onChange={(event) => update("sgdAmount", event.target.value)} type="number" value={draft.sgdAmount} /></label>
          <label>Date and time<input onChange={(event) => update("occurredAt", event.target.value)} required type="datetime-local" value={draft.occurredAt} /></label>
          <label>Category<CategorySelect categories={categories} disabled={loading} onChange={(value) => update("categoryId", value)} value={draft.categoryId} /></label>
        </div>
        <details className="transfer-line-items">
          <summary>Line items ({draft.lineItems.length})</summary>
          <LineItemsEditor defaultCurrency={draft.currency} disabled={saving} drafts={draft.lineItems} onChange={(value) => update("lineItems", value)} />
        </details>
      </fieldset>
    );
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFormError(null);
    if (sameAccount) return;
    if (
      !accounts.some(({ id }) => id === debit.accountId) ||
      !accounts.some(({ id }) => id === credit.accountId)
    ) {
      setFormError("Choose two active accounts before creating this transfer.");
      return;
    }
    if (
      (debit.categoryId && !categories.some(({ id }) => id === debit.categoryId)) ||
      (credit.categoryId && !categories.some(({ id }) => id === credit.categoryId))
    ) {
      setFormError("Choose active categories or leave both legs uncategorized.");
      return;
    }
    const outgoing = validateLeg(debit, "Outgoing leg");
    if (outgoing.error || !outgoing.value) {
      setFormError(outgoing.error);
      return;
    }
    const incoming = validateLeg(credit, "Incoming leg");
    if (incoming.error || !incoming.value) {
      setFormError(incoming.error);
      return;
    }
    setSaving(true);
    try {
      await createInternalTransfer(session, { debit: outgoing.value, credit: incoming.value });
      saved("Internal transfer and both account-linked legs were created.");
      close();
    } catch (error: unknown) {
      setFormError(error instanceof Error ? error.message : "Couldn’t create this transfer.");
    } finally {
      setSaving(false);
    }
  }

  return (
    <AccessibleDialog className="internal-transfer-dialog" descriptionId="internal-transfer-description" onClose={close} titleId="internal-transfer-title">
      <header className="modal-header">
        <div>
          <p className="eyebrow">INTERNAL TRANSFER</p>
          <h2 id="internal-transfer-title">Create both transfer legs</h2>
          <p className="muted" id="internal-transfer-description">One outgoing debit and one incoming credit will be created atomically. Each leg must use a different account.</p>
        </div>
        <button aria-label="Close internal transfer" className="icon-button" data-dialog-initial-focus onClick={close} type="button"><X aria-hidden="true" size={18} /></button>
      </header>

      <div className="transfer-flow" aria-hidden="true"><span>Money out</span><ArrowLeftRight size={22} /><span>Money in</span></div>
      {loading && <p aria-live="polite" className="source-loading" role="status">Loading accounts and categories…</p>}
      {loadError && (
        <section className="notice notice-error" role="alert">
          <CircleAlert aria-hidden="true" size={20} />
          <div><strong>Couldn’t prepare this transfer.</strong><p>{loadError}</p></div>
          <button className="button button-secondary" onClick={() => { setLoading(true); setLoadError(null); setReload((value) => value + 1); }} type="button"><RefreshCw aria-hidden="true" size={16} /> Retry</button>
        </section>
      )}
      {!loading && !loadError && accounts.length < 2 && (
        <section className="notice notice-error" role="status"><CircleAlert aria-hidden="true" size={20} /><div><strong>Add another active account.</strong><p>An internal transfer needs two different active accounts.</p></div></section>
      )}
      <form className="transfer-form" onSubmit={(event) => void submit(event)}>
        {legFields("Outgoing debit leg", "debit", debit, setDebit)}
        {legFields("Incoming credit leg", "credit", credit, setCredit)}
        {sameAccount && <p className="form-error" role="alert">Choose two different accounts for an internal transfer.</p>}
        {formError && <p className="form-error" role="alert">{formError}</p>}
        <div className="modal-actions">
          <button className="button button-secondary" onClick={close} type="button">Cancel</button>
          <button className="button button-primary" disabled={saving || loading || Boolean(loadError) || accounts.length < 2 || sameAccount} type="submit"><Save aria-hidden="true" size={17} />{saving ? "Creating…" : "Create transfer"}</button>
        </div>
      </form>
    </AccessibleDialog>
  );
}
