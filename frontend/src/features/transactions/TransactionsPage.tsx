import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import type { Session } from "@supabase/supabase-js";
import {
  AlertTriangle,
  CheckCircle2,
  CircleAlert,
  Clock3,
  FileSearch,
  Inbox,
  Link2Off,
  Mail,
  PlusCircle,
  RefreshCw,
  Search,
  SlidersHorizontal,
  X,
} from "lucide-react";
import {
  attachSourceToTransaction,
  createTransactionFromSource,
  getSanitizedEmail,
  getSyncRun,
  getTransactionSources,
  listOwnedAccounts,
  listSources,
  listTransactionsForAccount,
  listTransactions,
  startGmailSync,
  TransactionApiError,
  unmatchSourceLink,
  type SanitizedEmail,
} from "./api";
import {
  formatAmount,
  formatDateTime,
  type OwnedAccountOption,
  type SourceSummary,
  type TransactionFilters,
  type TransactionListItem,
  type TransactionSyncRun,
} from "./model";

type TransactionsView = "transactions" | "review" | "dangling";

function syncDescription(run: TransactionSyncRun): string {
  if (run.status === "queued") return "Your Gmail refresh is queued and will start shortly.";
  if (run.status === "running") {
    return `${run.messages_ingested} of ${run.messages_discovered || "…"} messages stored · ${run.sources_parsed} parsed`;
  }
  if (run.status === "completed") {
    return `${run.transactions_created} transaction${run.transactions_created === 1 ? "" : "s"} created · ${run.sources_review} for review · ${run.sources_dangling} dangling`;
  }
  return run.error_summary || "The refresh could not be completed. Your existing transactions are unchanged.";
}

function sourceTitle(source: SourceSummary): string {
  return source.suggested_title || source.subject || "Untitled transaction evidence";
}

function SourceInspector({
  session,
  source,
  close,
  resolved,
}: {
  session: Session;
  source: SourceSummary;
  close: () => void;
  resolved: (message: string) => void;
}) {
  const [email, setEmail] = useState<SanitizedEmail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [action, setAction] = useState<"attach" | "create">("attach");
  const [accounts, setAccounts] = useState<OwnedAccountOption[]>([]);
  const [accountsLoading, setAccountsLoading] = useState(true);
  const [accountsError, setAccountsError] = useState<string | null>(null);
  const [accountId, setAccountId] = useState("");
  const [candidates, setCandidates] = useState<TransactionListItem[]>([]);
  const [candidatesLoading, setCandidatesLoading] = useState(false);
  const [candidateError, setCandidateError] = useState<string | null>(null);
  const [transactionId, setTransactionId] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void getSanitizedEmail(session, source.id)
      .then((content) => {
        if (!cancelled) setEmail(content);
      })
      .catch((requestError: unknown) => {
        if (!cancelled) {
          setError(
            requestError instanceof Error
              ? requestError.message
              : "Couldn’t load the email content.",
          );
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [session, source.id]);

  useEffect(() => {
    let cancelled = false;
    void listOwnedAccounts(session)
      .then((items) => {
        if (!cancelled) setAccounts(items);
      })
      .catch((requestError: unknown) => {
        if (!cancelled) {
          setAccountsError(
            requestError instanceof Error ? requestError.message : "Couldn’t load your accounts.",
          );
        }
      })
      .finally(() => {
        if (!cancelled) setAccountsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [session]);

  useEffect(() => {
    if (!accountId) {
      return;
    }
    let cancelled = false;
    void listTransactionsForAccount(session, accountId)
      .then((items) => {
        if (!cancelled) setCandidates(items);
      })
      .catch((requestError: unknown) => {
        if (!cancelled) {
          setCandidateError(
            requestError instanceof Error
              ? requestError.message
              : "Couldn’t load transactions for this account.",
          );
        }
      })
      .finally(() => {
        if (!cancelled) setCandidatesLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [accountId, session]);

  async function submitResolution(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!accountId || (action === "attach" && !transactionId)) return;
    setSubmitting(true);
    setActionError(null);
    try {
      if (action === "attach") {
        await attachSourceToTransaction(session, source.id, transactionId);
        resolved(`Evidence was attached to the selected transaction.`);
      } else {
        await createTransactionFromSource(session, source.id, accountId);
        resolved(`A transaction was created from this evidence.`);
      }
      close();
    } catch (requestError: unknown) {
      setActionError(
        requestError instanceof Error
          ? requestError.message
          : "Couldn’t save this source decision.",
      );
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={close}>
      <section
        className="modal source-inspector"
        role="dialog"
        aria-modal="true"
        aria-labelledby="source-inspector-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="modal-header">
          <div>
            <p className="eyebrow">SOURCE EVIDENCE</p>
            <h2 id="source-inspector-title">{sourceTitle(source)}</h2>
            <p className="muted">{source.sender ?? source.provider}</p>
          </div>
          <button className="icon-button" onClick={close} type="button" aria-label="Close source">
            <X size={18} />
          </button>
        </header>
        {loading && <div className="source-loading">Loading sanitized email…</div>}
        {error && (
          <section className="notice notice-error" role="alert">
            <CircleAlert size={20} />
            <div>
              <strong>Couldn’t load this email.</strong>
              <p>{error}</p>
            </div>
          </section>
        )}
        {email && (
          <iframe
            className="source-email-frame"
            title={email.subject}
            sandbox=""
            referrerPolicy="no-referrer"
            srcDoc={email.html}
          />
        )}
        <section className="source-resolution" aria-labelledby="source-resolution-title">
          <div>
            <p className="eyebrow">RESOLVE SOURCE</p>
            <h3 id="source-resolution-title">Choose where this evidence belongs</h3>
            <p className="muted">
              Select one of your active accounts. The server validates ownership and uses the
              source candidate; this form never sends parsed amounts or titles.
            </p>
          </div>
          {accountsLoading ? (
            <p className="muted" aria-live="polite">Loading your accounts…</p>
          ) : accountsError ? (
            <section className="notice notice-error" role="alert">
              <CircleAlert size={20} />
              <div>
                <strong>Couldn’t load accounts.</strong>
                <p>{accountsError}</p>
              </div>
            </section>
          ) : accounts.length === 0 ? (
            <section className="notice notice-error" role="status">
              <CircleAlert size={20} />
              <div>
                <strong>Add an account first.</strong>
                <p>A transaction needs an active account before this source can be resolved.</p>
              </div>
            </section>
          ) : (
            <form className="resolution-form" onSubmit={(event) => void submitResolution(event)}>
              <fieldset className="resolution-choice">
                <legend>Resolution</legend>
                <label>
                  <input
                    checked={action === "attach"}
                    name="source-resolution"
                    onChange={() => setAction("attach")}
                    type="radio"
                    value="attach"
                  />
                  Attach to an existing transaction
                </label>
                <label>
                  <input
                    checked={action === "create"}
                    name="source-resolution"
                    onChange={() => setAction("create")}
                    type="radio"
                    value="create"
                  />
                  Create a transaction from this source
                </label>
              </fieldset>
              <label>
                Account
                <select
                  onChange={(event) => {
                    const nextAccountId = event.target.value;
                    setAccountId(nextAccountId);
                    setCandidates([]);
                    setCandidateError(null);
                    setTransactionId("");
                    setCandidatesLoading(Boolean(nextAccountId));
                  }}
                  required
                  value={accountId}
                >
                  <option value="">Choose an account</option>
                  {accounts.map((account) => (
                    <option key={account.id} value={account.id}>
                      {account.name}{account.institution_name ? ` · ${account.institution_name}` : ""}
                    </option>
                  ))}
                </select>
              </label>
              {action === "attach" && accountId && (
                <label>
                  Transaction
                  <select
                    disabled={candidatesLoading || Boolean(candidateError)}
                    onChange={(event) => setTransactionId(event.target.value)}
                    required
                    value={transactionId}
                  >
                    <option value="">
                      {candidatesLoading
                        ? "Loading transactions…"
                        : candidates.length === 0
                          ? "No transactions in this account"
                          : "Choose a transaction"}
                    </option>
                    {candidates.map((transaction) => (
                      <option key={transaction.id} value={transaction.id}>
                        {transaction.title} · {formatAmount(transaction.original_amount_minor, transaction.original_currency)} · {formatDateTime(transaction.occurred_at)}
                      </option>
                    ))}
                  </select>
                </label>
              )}
              {candidateError && (
                <p className="form-error" role="alert">{candidateError}</p>
              )}
              {actionError && (
                <p className="form-error" role="alert">{actionError}</p>
              )}
              <div className="modal-actions">
                <button className="button button-secondary" onClick={close} type="button">
                  Cancel
                </button>
                <button
                  className="button button-primary"
                  disabled={
                    submitting ||
                    !accountId ||
                    (action === "attach" && (!transactionId || candidatesLoading))
                  }
                  type="submit"
                >
                  <PlusCircle size={17} />
                  {submitting
                    ? "Saving…"
                    : action === "attach"
                      ? "Attach source"
                      : "Create transaction"}
                </button>
              </div>
            </form>
          )}
        </section>
      </section>
    </div>
  );
}

function TransactionEvidenceDialog({
  session,
  transaction,
  close,
  unmatched,
}: {
  session: Session;
  transaction: TransactionListItem;
  close: () => void;
  unmatched: (message: string) => void;
}) {
  const [sources, setSources] = useState<SourceSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [unmatchingId, setUnmatchingId] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    void getTransactionSources(session, transaction.id)
      .then((items) => {
        if (!cancelled) setSources(items);
      })
      .catch((requestError: unknown) => {
        if (!cancelled) {
          setError(
            requestError instanceof Error
              ? requestError.message
              : "Couldn’t load this transaction’s evidence.",
          );
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [session, transaction.id]);

  async function unmatch(source: SourceSummary) {
    if (!source.source_link_id) return;
    setUnmatchingId(source.source_link_id);
    setError(null);
    try {
      await unmatchSourceLink(session, source.source_link_id);
      setSources((current) => current.filter((item) => item.source_link_id !== source.source_link_id));
      unmatched("Evidence was unmatched and retained for review.");
    } catch (requestError: unknown) {
      setError(
        requestError instanceof Error ? requestError.message : "Couldn’t unmatch this evidence.",
      );
    } finally {
      setUnmatchingId(null);
    }
  }

  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={close}>
      <section
        className="modal evidence-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="transaction-evidence-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="modal-header">
          <div>
            <p className="eyebrow">TRANSACTION EVIDENCE</p>
            <h2 id="transaction-evidence-title">{transaction.title}</h2>
            <p className="muted">Unmatching keeps the source and its audit history for review.</p>
          </div>
          <button className="icon-button" onClick={close} type="button" aria-label="Close evidence">
            <X size={18} />
          </button>
        </header>
        {loading ? (
          <p className="source-loading" aria-live="polite">Loading attached evidence…</p>
        ) : error ? (
          <section className="notice notice-error" role="alert">
            <CircleAlert size={20} />
            <div>
              <strong>Couldn’t update transaction evidence.</strong>
              <p>{error}</p>
            </div>
          </section>
        ) : sources.length === 0 ? (
          <p className="source-loading">There is no active evidence attached to this transaction.</p>
        ) : (
          <ul className="evidence-list">
            {sources.map((source) => (
              <li key={source.source_link_id ?? source.id}>
                <div>
                  <strong>{sourceTitle(source)}</strong>
                  <p>{source.sender ?? source.provider} · {formatDateTime(source.received_at)}</p>
                </div>
                <button
                  className="button button-secondary"
                  disabled={!source.source_link_id || unmatchingId === source.source_link_id}
                  onClick={() => void unmatch(source)}
                  type="button"
                >
                  <Link2Off size={17} />
                  {unmatchingId === source.source_link_id ? "Unmatching…" : "Unmatch"}
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function TransactionRows({
  items,
  inspectEvidence,
}: {
  items: TransactionListItem[];
  inspectEvidence: (transaction: TransactionListItem) => void;
}) {
  return (
    <section className="transaction-list" aria-label="Transactions">
      {items.map((transaction) => (
        <article className="transaction-row" key={transaction.id}>
          <div className={`transaction-kind ${transaction.transaction_kind}`} aria-hidden="true">
            {transaction.transaction_kind === "debit" ? "−" : "+"}
          </div>
          <div className="transaction-identity">
            <h2>{transaction.title}</h2>
            <p>
              {transaction.account_name} · {formatDateTime(transaction.occurred_at)}
              {transaction.source_count ? ` · ${transaction.source_count} source${transaction.source_count === 1 ? "" : "s"}` : ""}
            </p>
          </div>
          <div className="transaction-meta">
            {transaction.category_name && <span>{transaction.category_name}</span>}
            {transaction.review_status !== "confirmed" && (
              <span className="status-pill review">Needs review</span>
            )}
          </div>
          <button
            className="button button-secondary button-compact"
            onClick={() => inspectEvidence(transaction)}
            type="button"
          >
            Evidence{transaction.source_count ? ` (${transaction.source_count})` : ""}
          </button>
          <strong className={`transaction-amount ${transaction.transaction_kind}`}>
            {transaction.transaction_kind === "debit" ? "−" : "+"}
            {formatAmount(transaction.original_amount_minor, transaction.original_currency)}
          </strong>
        </article>
      ))}
    </section>
  );
}

function SourceCards({
  sources,
  view,
  inspect,
}: {
  sources: SourceSummary[];
  view: Exclude<TransactionsView, "transactions">;
  inspect: (source: SourceSummary) => void;
}) {
  return (
    <section className="source-card-list" aria-label={view === "review" ? "Sources needing review" : "Dangling sources"}>
      {sources.map((source) => (
        <article className="source-card" key={source.id}>
          <div className="source-card-icon" aria-hidden="true">
            {source.parse_status === "failed" ? <AlertTriangle size={20} /> : <Mail size={20} />}
          </div>
          <div className="source-card-main">
            <div className="source-card-heading">
              <h2>{sourceTitle(source)}</h2>
              <span className={`status-pill ${view}`}>{view === "review" ? "Review" : "Unattached"}</span>
            </div>
            <p>
              {source.sender ?? source.provider} · {formatDateTime(source.received_at)}
            </p>
            <dl className="source-facts">
              {source.suggested_account_name && (
                <div>
                  <dt>Suggested account</dt>
                  <dd>{source.suggested_account_name}</dd>
                </div>
              )}
              {source.suggested_amount_minor !== null && source.suggested_currency && (
                <div>
                  <dt>Detected amount</dt>
                  <dd>{formatAmount(source.suggested_amount_minor, source.suggested_currency)}</dd>
                </div>
              )}
              {source.parse_error && (
                <div className="source-error">
                  <dt>Needs attention</dt>
                  <dd>{source.parse_error}</dd>
                </div>
              )}
            </dl>
          </div>
          <button className="button button-secondary" onClick={() => inspect(source)} type="button">
            <FileSearch size={17} /> Inspect & resolve
          </button>
        </article>
      ))}
    </section>
  );
}

export function TransactionsPage({ session }: { session: Session }) {
  const [view, setView] = useState<TransactionsView>("transactions");
  const [transactions, setTransactions] = useState<TransactionListItem[]>([]);
  const [sources, setSources] = useState<SourceSummary[]>([]);
  const [filters, setFilters] = useState<TransactionFilters>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [syncRun, setSyncRun] = useState<TransactionSyncRun | null>(null);
  const [startingSync, setStartingSync] = useState(false);
  const [inspecting, setInspecting] = useState<SourceSummary | null>(null);
  const [evidenceTransaction, setEvidenceTransaction] = useState<TransactionListItem | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      if (view === "transactions") {
        setTransactions(await listTransactions(session, filters));
      } else {
        setSources(await listSources(session, view));
      }
    } catch (requestError: unknown) {
      setError(
        requestError instanceof Error
          ? requestError.message
          : "Couldn’t load transaction information.",
      );
    } finally {
      setLoading(false);
    }
  }, [filters, session, view]);

  useEffect(() => {
    const timer = window.setTimeout(() => void load(), view === "transactions" && filters.search ? 250 : 0);
    return () => window.clearTimeout(timer);
  }, [filters, load, view]);

  useEffect(() => {
    if (!syncRun || (syncRun.status !== "queued" && syncRun.status !== "running")) return;
    const timer = window.setInterval(() => {
      void getSyncRun(session, syncRun.id)
        .then((nextRun) => {
          setSyncRun(nextRun);
          if (nextRun.status === "completed") void load();
        })
        .catch((requestError: unknown) => {
          setSyncRun((current) =>
            current
              ? {
                  ...current,
                  status: "failed",
                  error_summary: requestError instanceof Error ? requestError.message : "Couldn’t refresh sync status.",
                }
              : current,
          );
        });
    }, 2500);
    return () => window.clearInterval(timer);
  }, [load, session, syncRun]);

  const activeCount = view === "transactions" ? transactions.length : sources.length;
  const hasFilters = Boolean(filters.search || filters.kind || filters.review);
  const syncBusy = startingSync || syncRun?.status === "queued" || syncRun?.status === "running";
  const heading = view === "transactions" ? "Transactions" : view === "review" ? "Review sources" : "Dangling sources";
  const description =
    view === "transactions"
      ? "Account-linked activity created from your transaction evidence."
      : view === "review"
        ? "Evidence that needs a quick decision before it can update your records."
        : "Evidence without a reliable account match. Attach it or create a transaction when ready.";

  const emptyCopy = useMemo(() => {
    if (view === "transactions") {
      return hasFilters
        ? ["No transactions match these filters", "Try a different search or clear the active filters."]
        : ["No transactions yet", "Refresh your odin-finance Gmail label to import the latest five messages."];
    }
    return view === "review"
      ? ["Nothing needs review", "Low-confidence sources will appear here without changing a transaction automatically."]
      : ["No dangling sources", "Sources that cannot identify an account will appear here for you to resolve."];
  }, [hasFilters, view]);

  async function triggerSync() {
    setStartingSync(true);
    setError(null);
    try {
      setSyncRun(await startGmailSync(session));
    } catch (requestError: unknown) {
      setError(
        requestError instanceof TransactionApiError && requestError.status === 409
          ? "A Gmail refresh is already in progress."
          : requestError instanceof Error
            ? requestError.message
            : "Couldn’t start the Gmail refresh.",
      );
    } finally {
      setStartingSync(false);
    }
  }

  function clearFilters() {
    setFilters({});
  }

  function showSuccess(message: string) {
    setSuccess(message);
    void load();
  }

  return (
    <>
      <header className="page-header transactions-header">
        <div>
          <p className="eyebrow">TRANSACTION ACTIVITY</p>
          <h1>{heading}</h1>
          <p className="muted">{description}</p>
        </div>
        <button className="button button-primary" disabled={syncBusy} onClick={() => void triggerSync()} type="button">
          <RefreshCw className={syncBusy ? "spin" : undefined} size={18} />
          {startingSync ? "Starting refresh…" : syncBusy ? "Refreshing Gmail…" : "Refresh Gmail"}
        </button>
      </header>

      {syncRun && (
        <section className={`sync-status ${syncRun.status}`} aria-live="polite">
          {syncRun.status === "completed" ? <CheckCircle2 size={21} /> : syncRun.status === "failed" ? <CircleAlert size={21} /> : <Clock3 size={21} />}
          <div>
            <strong>
              {syncRun.status === "completed" ? "Gmail refresh complete" : syncRun.status === "failed" ? "Gmail refresh needs attention" : "Refreshing odin-finance"}
            </strong>
            <p>{syncDescription(syncRun)}</p>
          </div>
        </section>
      )}

      <div className="transaction-tabs" role="tablist" aria-label="Transaction areas">
        {(["transactions", "review", "dangling"] as const).map((item) => (
          <button
            aria-selected={view === item}
            className={view === item ? "active" : ""}
            key={item}
            onClick={() => setView(item)}
            role="tab"
            type="button"
          >
            {item === "transactions" ? "Transactions" : item === "review" ? "Review" : "Dangling"}
          </button>
        ))}
      </div>

      {view === "transactions" && (
        <section className="toolbar" aria-label="Transaction filters">
          <label className="search-field">
            <Search size={18} />
            <span className="sr-only">Search transactions</span>
            <input
              value={filters.search ?? ""}
              onChange={(event) => setFilters((current) => ({ ...current, search: event.target.value || undefined }))}
              placeholder="Search merchant or title"
            />
          </label>
          <label className="select-field">
            <SlidersHorizontal size={17} />
            <span className="sr-only">Transaction kind</span>
            <select
              value={filters.kind ?? ""}
              onChange={(event) => setFilters((current) => ({ ...current, kind: event.target.value === "" ? undefined : event.target.value as "debit" | "credit" }))}
            >
              <option value="">All flow</option>
              <option value="debit">Money out</option>
              <option value="credit">Money in</option>
            </select>
          </label>
          <label className="select-field">
            <span className="sr-only">Review state</span>
            <select
              value={filters.review ?? ""}
              onChange={(event) => setFilters((current) => ({ ...current, review: event.target.value === "" ? undefined : event.target.value as "confirmed" | "review_required" | "pending" }))}
            >
              <option value="">All states</option>
              <option value="confirmed">Confirmed</option>
              <option value="review_required">Needs review</option>
              <option value="pending">Processing</option>
            </select>
          </label>
        </section>
      )}

      {hasFilters && view === "transactions" && (
        <button className="text-button clear-filters" onClick={clearFilters} type="button">Clear filters</button>
      )}

      {error && (
        <section className="notice notice-error" role="alert">
          <CircleAlert size={20} />
          <div>
            <strong>Couldn’t load transactions.</strong>
            <p>{error}</p>
          </div>
          <button className="button button-secondary" onClick={() => void load()} type="button">Retry</button>
        </section>
      )}

      {success && (
        <section className="notice notice-success" role="status">
          <CheckCircle2 size={20} />
          <div>
            <strong>Saved</strong>
            <p>{success}</p>
          </div>
          <button className="button button-secondary" onClick={() => setSuccess(null)} type="button">
            Dismiss
          </button>
        </section>
      )}

      {loading ? (
        <section className="transaction-panel" aria-label="Loading transactions">
          <div className="skeleton-row" />
          <div className="skeleton-row" />
          <div className="skeleton-row" />
        </section>
      ) : activeCount === 0 ? (
        <section className="empty-state transaction-empty">
          <Inbox size={28} aria-hidden="true" />
          <h2>{emptyCopy[0]}</h2>
          <p>{emptyCopy[1]}</p>
          {view === "transactions" && !hasFilters && (
            <button className="button button-primary" disabled={syncBusy} onClick={() => void triggerSync()} type="button">
              <RefreshCw size={18} /> Refresh Gmail
            </button>
          )}
          {hasFilters && <button className="button button-secondary" onClick={clearFilters} type="button">Clear filters</button>}
        </section>
      ) : view === "transactions" ? (
        <TransactionRows inspectEvidence={setEvidenceTransaction} items={transactions} />
      ) : (
        <SourceCards inspect={setInspecting} sources={sources} view={view} />
      )}

      {inspecting && (
        <SourceInspector
          close={() => setInspecting(null)}
          resolved={showSuccess}
          session={session}
          source={inspecting}
        />
      )}
      {evidenceTransaction && (
        <TransactionEvidenceDialog
          close={() => setEvidenceTransaction(null)}
          session={session}
          transaction={evidenceTransaction}
          unmatched={showSuccess}
        />
      )}
    </>
  );
}
