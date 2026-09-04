import {
  CalendarDays,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  CreditCard,
  FileText,
  ReceiptText,
  X,
} from "lucide-react";
import { useEffect, useState } from "react";
import type { Session } from "@supabase/supabase-js";
import type { Account } from "../accounts/model";
import { displayDate } from "./viewModel";
import { listCreditCardBills, minorToMajor, type CreditCardBillStatus, type CreditCardBillSummaryDto } from "./api";
import "./credit-card.css";

type CreditCardAccount = Account & { account_type: "credit_card" };

interface CreditCardPageProps {
  accounts: CreditCardAccount[];
  error: string | null;
  loading: boolean;
  onOpenBill: (account: CreditCardAccount, billId: string) => void;
  onOpenBulkImport: () => void;
  onRetry: () => void;
  session: Session;
}

function amountLabel(bill: CreditCardBillSummaryDto): string {
  return `${bill.settlement_currency ?? "—"} ${minorToMajor(bill.amount_due_minor)}`;
}

function statusLabel(status: CreditCardBillStatus): string {
  if (status === "review") return "Needs review";
  if (status === "unpaid") return "Unpaid";
  if (status === "void") return "Void";
  return "Paid";
}

function StatusIcon({ status }: { status: CreditCardBillStatus }) {
  if (status === "paid") return <CheckCircle2 aria-hidden="true" size={14} />;
  if (status === "void") return <X aria-hidden="true" size={14} />;
  return <CircleAlert aria-hidden="true" size={14} />;
}

export function CreditCardPage({
  accounts,
  error,
  loading,
  onOpenBill,
  onOpenBulkImport,
  onRetry,
  session,
}: CreditCardPageProps) {
  const [collapsedAccountIds, setCollapsedAccountIds] = useState<Set<string>>(
    () => new Set(),
  );
  const [billsByAccount, setBillsByAccount] = useState<Record<string, CreditCardBillSummaryDto[]>>({});
  const [billsLoading, setBillsLoading] = useState(true);
  const [billsError, setBillsError] = useState<string | null>(null);
  const [reloadKey, setReloadKey] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    void Promise.resolve().then(() => {
      if (controller.signal.aborted) return [];
      setBillsLoading(true);
      setBillsError(null);
      return Promise.all(accounts.map(async (account) => [account.id, await listCreditCardBills(session, account.id, controller.signal)] as const));
    })
      .then((entries) => setBillsByAccount(Object.fromEntries(entries)))
      .catch((reason: unknown) => {
        if (!controller.signal.aborted) setBillsError(reason instanceof Error ? reason.message : "Credit Card bills could not be loaded.");
      })
      .finally(() => { if (!controller.signal.aborted) setBillsLoading(false); });
    return () => controller.abort();
  }, [accounts, reloadKey, session]);

  const pageError = error ?? billsError;
  const pageLoading = loading || billsLoading;

  return (
    <section aria-labelledby="credit-card-heading" className="ccb-page">
      <header className="page-header ccb-page-header">
        <div>
          <p className="eyebrow">CREDIT CARDS</p>
          <h1 id="credit-card-heading">Credit Card</h1>
          <p className="muted">
            Review every active card and open any reconciled bill.
          </p>
        </div>
        <button className="button button-secondary" onClick={onOpenBulkImport} type="button">
          <ReceiptText aria-hidden="true" size={18} /> Open Bulk Import
        </button>
      </header>

      {pageError && (
        <section className="notice notice-error" role="alert">
          <CircleAlert aria-hidden="true" size={20} />
          <div>
            <strong>Couldn’t load credit cards.</strong>
            <p>{pageError}</p>
          </div>
          <button className="button button-secondary" onClick={() => { setBillsLoading(true); setBillsError(null); setReloadKey((value) => value + 1); onRetry(); }} type="button">
            Retry
          </button>
        </section>
      )}

      {pageLoading ? (
        <section aria-busy="true" aria-label="Loading Credit Card" className="ccb-loading" role="status">
          <span className="sr-only">Loading Credit Card…</span>
          <div className="skeleton-row" />
          <div className="skeleton-row" />
        </section>
      ) : accounts.length === 0 ? (
        <section className="empty-state ccb-empty">
          <CreditCard aria-hidden="true" size={28} />
          <h2>No active credit cards</h2>
          <p>Add or restore a Credit Card account to see its bills here.</p>
        </section>
      ) : (
        <div className="ccb-groups">
          {accounts.map((account) => {
            const billsNewestFirst = [...(billsByAccount[account.id] ?? [])]
              .sort((left, right) => (right.statement_date ?? "").localeCompare(left.statement_date ?? ""));
            return (
            <details
              className="ccb-card-group"
              key={account.id}
              onToggle={(event) => {
                const isOpen = event.currentTarget.open;
                setCollapsedAccountIds((current) => {
                  const next = new Set(current);
                  if (isOpen) next.delete(account.id);
                  else next.add(account.id);
                  return next;
                });
              }}
              open={!collapsedAccountIds.has(account.id)}
            >
              <summary>
                <span className="ccb-card-mark" aria-hidden="true">
                  <CreditCard size={21} />
                </span>
                <span className="ccb-card-identity">
                  <strong>{account.name}</strong>
                  <small>
                    {account.institution_name}
                    {account.account_identifier ? ` · ${account.account_identifier}` : ""}
                  </small>
                </span>
                <span className="ccb-bill-count">
                  {billsNewestFirst.length} {billsNewestFirst.length === 1 ? "bill" : "bills"}
                </span>
                <ChevronDown aria-hidden="true" className="ccb-expand-icon" size={19} />
              </summary>

              <div className="ccb-bill-list">
                {billsNewestFirst.length > 0 ? billsNewestFirst.map((bill) => (
                  <button
                    aria-label={`Open ${displayDate(bill.statement_date ?? "")} bill for ${account.name}`}
                    className="ccb-bill-row"
                    key={bill.id}
                    onClick={() => onOpenBill(account, bill.id)}
                    type="button"
                  >
                    <span className={`ccb-bill-icon ccb-bill-icon-${bill.status}`} aria-hidden="true">
                      <FileText size={20} />
                    </span>
                    <span className="ccb-bill-period">
                      <strong>{displayDate(bill.period_start ?? "")} – {displayDate(bill.period_end ?? "")}</strong>
                      <small>
                        <CalendarDays aria-hidden="true" size={14} /> Statement {displayDate(bill.statement_date ?? "")} · Due {displayDate(bill.due_date ?? "")}{bill.unresolved_candidate_count > 0 ? ` · ${bill.unresolved_candidate_count} unprojected result${bill.unresolved_candidate_count === 1 ? "" : "s"}` : ""}
                      </small>
                    </span>
                    <span className={`ccb-status ccb-status-${bill.status}`}>
                      <StatusIcon status={bill.status} /> {statusLabel(bill.status)}
                    </span>
                    <span className="ccb-amount">
                      <small>Amount due</small>
                      <strong>{amountLabel(bill)}</strong>
                    </span>
                    <ChevronRight aria-hidden="true" className="ccb-row-arrow" size={19} />
                  </button>
                )) : (
                  <div className="ccb-no-bills">
                    <ReceiptText aria-hidden="true" size={20} />
                    <span>
                      <strong>No reconciled bills yet</strong>
                      <small>Process this card’s statement in Bulk Import to create its first bill.</small>
                    </span>
                  </div>
                )}
              </div>
            </details>
            );
          })}
        </div>
      )}
    </section>
  );
}
