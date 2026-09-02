import { useCallback, useEffect, useMemo, useState } from "react";
import type { Session } from "@supabase/supabase-js";
import {
  AlertTriangle,
  CheckCircle2,
  CircleAlert,
  Clock3,
  FileSearch,
  Inbox,
  Mail,
  RefreshCw,
  Search,
  SlidersHorizontal,
  X,
} from "lucide-react";
import {
  getSanitizedEmail,
  getSyncRun,
  listSources,
  listTransactions,
  startGmailSync,
  TransactionApiError,
  type SanitizedEmail,
} from "./api";
import {
  formatAmount,
  formatDateTime,
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
}: {
  session: Session;
  source: SourceSummary;
  close: () => void;
}) {
  const [email, setEmail] = useState<SanitizedEmail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

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
      </section>
    </div>
  );
}

function TransactionRows({ items }: { items: TransactionListItem[] }) {
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
            <FileSearch size={17} /> Inspect source
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
        <TransactionRows items={transactions} />
      ) : (
        <SourceCards inspect={setInspecting} sources={sources} view={view} />
      )}

      {inspecting && <SourceInspector close={() => setInspecting(null)} session={session} source={inspecting} />}
    </>
  );
}
