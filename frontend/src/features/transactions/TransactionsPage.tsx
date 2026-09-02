import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import type { Session } from "@supabase/supabase-js";
import {
  AlertTriangle,
  ArrowLeftRight,
  CheckCircle2,
  CircleAlert,
  Clock3,
  FileSearch,
  Inbox,
  Mail,
  Plus,
  RefreshCw,
  Search,
  SlidersHorizontal,
} from "lucide-react";
import { supabase } from "../../lib/supabase";
import {
  beginGmailConnection,
  getGmailConnection,
  getLatestSyncRun,
  getSyncRun,
  listSources,
  listTransactions,
  startGmailSync,
  TransactionApiError,
} from "./api";
import { InternalTransferDialog } from "./InternalTransferDialog";
import { SourceInspector } from "./SourceInspector";
import { TransactionDetailDialog } from "./TransactionDetailDialog";
import {
  formatAmount,
  formatDateTime,
  type GmailConnection,
  type InternalTransferSourceSeed,
  type SourceQueue,
  type SourceSummary,
  type TransactionFilters,
  type TransactionListItem,
  type TransactionSyncRun,
  type TransferSourceRole,
} from "./model";

type TransactionsView = "transactions" | SourceQueue;

interface SourcePageState {
  items: SourceSummary[];
  nextCursor: string | null;
}

const views: TransactionsView[] = ["transactions", "review", "dangling", "failed"];
const emptySourcePages: Record<SourceQueue, SourcePageState> = {
  review: { items: [], nextCursor: null },
  dangling: { items: [], nextCursor: null },
  failed: { items: [], nextCursor: null },
};

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

function syncDescription(run: TransactionSyncRun): string {
  const failedSuffix = run.sources_failed > 0
    ? ` · ${run.sources_failed} source${run.sources_failed === 1 ? "" : "s"} failed`
    : "";
  if (run.status === "queued") {
    return run.sources_failed > 0
      ? `Your Gmail refresh is queued${failedSuffix}.`
      : "Your Gmail refresh is queued and will start shortly.";
  }
  if (run.status === "running") {
    return `${run.messages_ingested} of ${run.messages_discovered || "…"} messages stored · ${run.sources_parsed} parsed${failedSuffix}`;
  }
  if (run.status === "completed") {
    return `${run.transactions_created} transaction${run.transactions_created === 1 ? "" : "s"} created · ${run.sources_review} for review · ${run.sources_dangling} dangling${failedSuffix}`;
  }
  return `${run.error_summary || "The refresh could not be completed. Stored evidence remains available."}${failedSuffix}`;
}

function sourceTitle(source: SourceSummary): string {
  return source.suggested_title || source.subject || "Untitled transaction evidence";
}

function TransactionRows({
  items,
  inspect,
}: {
  items: TransactionListItem[];
  inspect: (transaction: TransactionListItem) => void;
}) {
  return (
    <section aria-label="Transactions" className="transaction-list">
      {items.map((transaction) => (
        <article className="transaction-row" key={transaction.id}>
          <div aria-hidden="true" className={`transaction-kind ${transaction.transaction_kind}`}>
            {transaction.transaction_kind === "debit" ? "−" : "+"}
          </div>
          <div className="transaction-identity">
            <h2>{transaction.title}</h2>
            <p>{transaction.account_name} · {formatDateTime(transaction.occurred_at)} · {transaction.source_count} source{transaction.source_count === 1 ? "" : "s"}</p>
          </div>
          <div className="transaction-meta">
            {transaction.category_name && <span>{transaction.category_name}</span>}
            {transaction.transfer_link && <span className="status-pill transfer"><ArrowLeftRight aria-hidden="true" size={12} /> Transfer</span>}
            {transaction.review_status !== "confirmed" && <span className="status-pill review">{transaction.review_status === "pending" ? "Processing" : "Needs review"}</span>}
          </div>
          <button aria-label={`View details and evidence for ${transaction.title}`} className="button button-secondary button-compact transaction-detail-button" onClick={() => inspect(transaction)} type="button">
            <FileSearch aria-hidden="true" size={16} /> Details
          </button>
          <div className="transaction-values">
            <strong className={`transaction-amount ${transaction.transaction_kind}`}>
              <span className="sr-only">{transaction.transaction_kind === "debit" ? "Money out" : "Money in"}: </span>
              {transaction.transaction_kind === "debit" ? "−" : "+"}{formatAmount(transaction.original_amount_minor, transaction.original_currency)}
            </strong>
            {transaction.sgd_amount_minor && transaction.original_currency !== "SGD" && <small>{formatAmount(transaction.sgd_amount_minor, "SGD")}</small>}
          </div>
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
  view: SourceQueue;
  inspect: (source: SourceSummary) => void;
}) {
  const statusLabel = view === "review" ? "Review" : view === "failed" ? "Failed" : "Unattached";
  return (
    <section aria-label={view === "review" ? "Sources needing review" : view === "failed" ? "Sources with parsing failures" : "Dangling sources"} className="source-card-list">
      {sources.map((source) => (
        <article className="source-card" key={source.id}>
          <div aria-hidden="true" className="source-card-icon">
            {view === "failed" ? <AlertTriangle size={20} /> : <Mail size={20} />}
          </div>
          <div className="source-card-main">
            <div className="source-card-heading">
              <h2>{sourceTitle(source)}</h2>
              <span className={`status-pill ${view}`}>{statusLabel}</span>
            </div>
            <p>{source.sender || source.provider} · {formatDateTime(source.received_at)}</p>
            <dl className="source-facts">
              {source.suggested_account_name && <div><dt>Suggested account</dt><dd>{source.suggested_account_name}</dd></div>}
              {source.suggested_amount_minor !== null && source.suggested_currency && <div><dt>Detected amount</dt><dd>{formatAmount(source.suggested_amount_minor, source.suggested_currency)}</dd></div>}
              {source.parse_confidence !== null && <div><dt>Parse confidence</dt><dd>{source.parse_confidence}%</dd></div>}
              {source.parse_error && <div className="source-error"><dt>Needs attention</dt><dd>{source.parse_error}</dd></div>}
            </dl>
          </div>
          <button aria-label={`${view === "failed" ? "Inspect and retry" : "Inspect and resolve"} ${sourceTitle(source)} received ${formatDateTime(source.received_at)}`} className="button button-secondary" onClick={() => inspect(source)} type="button">
            {view === "failed" ? <RefreshCw aria-hidden="true" size={17} /> : <FileSearch aria-hidden="true" size={17} />}
            {view === "failed" ? "Inspect & retry" : "Inspect & resolve"}
          </button>
        </article>
      ))}
    </section>
  );
}

export function TransactionsPage({ session }: { session: Session }) {
  const gmailCallbackResult = useMemo(
    () => new URL(window.location.href).searchParams.get("gmail"),
    [],
  );
  const [view, setView] = useState<TransactionsView>("transactions");
  const [transactions, setTransactions] = useState<TransactionListItem[]>([]);
  const [transactionCursor, setTransactionCursor] = useState<string | null>(null);
  const [sourcePages, setSourcePages] = useState<Record<SourceQueue, SourcePageState>>(emptySourcePages);
  const [filters, setFilters] = useState<TransactionFilters>({});
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [dataError, setDataError] = useState<string | null>(null);
  const [loadMoreError, setLoadMoreError] = useState<string | null>(null);
  const [connection, setConnection] = useState<GmailConnection | null>(null);
  const [connectionLoading, setConnectionLoading] = useState(true);
  const [connectionError, setConnectionError] = useState<string | null>(null);
  const [oauthError, setOAuthError] = useState<string | null>(() =>
    gmailCallbackResult && gmailCallbackResult !== "connected"
      ? "Gmail connection was not completed. Please try again."
      : null,
  );
  const [connecting, setConnecting] = useState(false);
  const [syncRun, setSyncRun] = useState<TransactionSyncRun | null>(null);
  const [syncRestoring, setSyncRestoring] = useState(true);
  const [syncRestoreReload, setSyncRestoreReload] = useState(0);
  const [syncActionError, setSyncActionError] = useState<string | null>(null);
  const [monitorError, setMonitorError] = useState<string | null>(null);
  const [monitorStopped, setMonitorStopped] = useState(false);
  const [monitorResumeKey, setMonitorResumeKey] = useState(0);
  const [startingSync, setStartingSync] = useState(false);
  const [inspecting, setInspecting] = useState<SourceSummary | null>(null);
  const [detailTransaction, setDetailTransaction] = useState<TransactionListItem | null>(null);
  const [creatingTransfer, setCreatingTransfer] = useState(false);
  const [transferSourceSeed, setTransferSourceSeed] = useState<InternalTransferSourceSeed | null>(null);
  const [success, setSuccess] = useState<string | null>(() =>
    gmailCallbackResult === "connected"
      ? "Gmail was connected. You can refresh odin-finance now."
      : null,
  );
  const loadGeneration = useRef(0);
  const transactionCursorRef = useRef<string | null>(null);
  const sourceCursorRef = useRef<Record<SourceQueue, string | null>>({
    review: null,
    dangling: null,
    failed: null,
  });
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);

  const loadData = useCallback(async (append: boolean, signal?: AbortSignal) => {
    const generation = ++loadGeneration.current;
    const targetView = view;
    if (append) {
      setLoadingMore(true);
      setLoadMoreError(null);
    } else {
      setLoadingMore(false);
      setLoading(true);
      setDataError(null);
      setLoadMoreError(null);
      if (targetView === "transactions") {
        setTransactions([]);
        setTransactionCursor(null);
        transactionCursorRef.current = null;
      } else {
        setSourcePages((current) => ({ ...current, [targetView]: { items: [], nextCursor: null } }));
        sourceCursorRef.current[targetView] = null;
      }
    }
    try {
      if (targetView === "transactions") {
        const page = await listTransactions(
          session,
          filters,
          append ? transactionCursorRef.current : null,
          signal,
        );
        if (generation !== loadGeneration.current) return;
        setTransactions((current) => {
          if (!append) return page.items;
          const known = new Set(current.map(({ id }) => id));
          return [...current, ...page.items.filter(({ id }) => !known.has(id))];
        });
        setTransactionCursor(page.next_cursor);
        transactionCursorRef.current = page.next_cursor;
      } else {
        const cursor = append ? sourceCursorRef.current[targetView] : null;
        const page = await listSources(session, targetView, cursor, signal);
        if (generation !== loadGeneration.current) return;
        setSourcePages((current) => {
          const existing = append ? current[targetView].items : [];
          const known = new Set(existing.map(({ id }) => id));
          return {
            ...current,
            [targetView]: {
              items: [...existing, ...page.items.filter(({ id }) => !known.has(id))],
              nextCursor: page.next_cursor,
            },
          };
        });
        sourceCursorRef.current[targetView] = page.next_cursor;
      }
    } catch (error: unknown) {
      if (isAbortError(error) || generation !== loadGeneration.current) return;
      const message = error instanceof Error ? error.message : "Couldn’t load transaction information.";
      if (append) setLoadMoreError(message);
      else setDataError(message);
    } finally {
      if (generation === loadGeneration.current) {
        if (append) setLoadingMore(false);
        else setLoading(false);
      }
    }
  }, [filters, session, view]);

  useEffect(() => {
    const controller = new AbortController();
    const timer = window.setTimeout(
      () => void loadData(false, controller.signal),
      view === "transactions" && filters.search ? 250 : 0,
    );
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [filters.search, loadData, view]);

  const loadConnection = useCallback(async (signal?: AbortSignal) => {
    try {
      setConnection(await getGmailConnection(session, signal));
    } catch (error: unknown) {
      if (!isAbortError(error)) {
        setConnectionError(error instanceof Error ? error.message : "Couldn’t check Gmail connection.");
      }
    } finally {
      if (!signal?.aborted) setConnectionLoading(false);
    }
  }, [session]);

  useEffect(() => {
    const controller = new AbortController();
    const timer = window.setTimeout(() => void loadConnection(controller.signal), 0);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [loadConnection]);

  useEffect(() => {
    if (!gmailCallbackResult) return;
    const url = new URL(window.location.href);
    url.searchParams.delete("gmail");
    url.searchParams.set("page", "transactions");
    window.history.replaceState({}, "", url);
  }, [gmailCallbackResult]);

  useEffect(() => {
    const controller = new AbortController();
    void getLatestSyncRun(session, controller.signal)
      .then((run) => {
        if (!controller.signal.aborted) setSyncRun(run);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted && !isAbortError(error)) {
          setMonitorError(error instanceof Error ? error.message : "Couldn’t restore the latest Gmail refresh.");
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setSyncRestoring(false);
      });
    return () => controller.abort();
  }, [session, syncRestoreReload]);

  const activeSyncID = syncRun?.status === "queued" || syncRun?.status === "running" ? syncRun.id : null;
  useEffect(() => {
    if (!activeSyncID) return;
    let cancelled = false;
    let finished = false;
    let realtimeReady = false;
    let attempts = 0;
    let timer: number | undefined;
    let checking = false;
    const controller = new AbortController();

    const applyRun = (next: TransactionSyncRun) => {
      if (cancelled) return;
      setSyncRun(next);
      setMonitorError(null);
      if (next.status === "completed" || next.status === "failed") {
        finished = true;
        void loadData(false);
        void loadConnection();
      }
    };

    const check = async () => {
      if (cancelled || finished || checking) return;
      if (timer !== undefined) {
        window.clearTimeout(timer);
        timer = undefined;
      }
      if (attempts >= 40) {
        setMonitorStopped(true);
        setMonitorError("Live monitoring paused after repeated checks. The server-side refresh continues safely.");
        return;
      }
      checking = true;
      attempts += 1;
      try {
        applyRun(await getSyncRun(session, activeSyncID, controller.signal));
      } catch (error: unknown) {
        if (!controller.signal.aborted) {
          setMonitorError(error instanceof Error ? error.message : "Couldn’t refresh sync progress.");
        }
      } finally {
        checking = false;
        if (!cancelled && !finished) {
          timer = window.setTimeout(() => void check(), realtimeReady ? 10_000 : 3_000);
        }
      }
    };

    const channel = supabase
      .channel(`transaction-sync-${activeSyncID}`)
      .on(
        "postgres_changes",
        { event: "UPDATE", schema: "public", table: "transaction_sync_runs", filter: `id=eq.${activeSyncID}` },
        () => void check(),
      )
      .subscribe((status) => {
        realtimeReady = status === "SUBSCRIBED";
        if (status === "CHANNEL_ERROR" || status === "TIMED_OUT") {
          setMonitorError("Live updates are unavailable; progress is continuing with secure polling.");
        }
      });
    void check();
    return () => {
      cancelled = true;
      controller.abort();
      if (timer !== undefined) window.clearTimeout(timer);
      void supabase.removeChannel(channel);
    };
  }, [activeSyncID, loadConnection, loadData, monitorResumeKey, session]);

  const activeSources = view === "transactions" ? [] : sourcePages[view].items;
  const activeCount = view === "transactions" ? transactions.length : activeSources.length;
  const nextCursor = view === "transactions" ? transactionCursor : sourcePages[view].nextCursor;
  const hasFilters = Boolean(filters.search || filters.kind || filters.review);
  const syncBusy = syncRestoring || startingSync || syncRun?.status === "queued" || syncRun?.status === "running";
  const syncRestoreFailed = !syncRestoring && !syncRun && Boolean(monitorError);
  const heading = view === "transactions" ? "Transactions" : view === "review" ? "Review sources" : view === "dangling" ? "Dangling sources" : "Failed sources";
  const description = view === "transactions"
    ? "Account-linked activity created from your transaction evidence."
    : view === "review"
      ? "Low-confidence evidence awaiting your decision."
      : view === "dangling"
        ? "Evidence without a reliable account match."
        : "Stored evidence that could not be parsed and can be retried without fetching Gmail again.";

  const emptyCopy = useMemo<[string, string]>(() => {
    if (view === "transactions") {
      return hasFilters
        ? ["No transactions match these filters", "Try a different search or clear the active filters."]
        : ["No transactions yet", connection?.connected ? "Refresh odin-finance to import your latest evidence." : "Connect Gmail to begin importing finance-labelled evidence."];
    }
    if (view === "review") return ["Nothing needs review", "Low-confidence sources will wait here without changing a transaction automatically."];
    if (view === "dangling") return ["No dangling sources", "Sources that cannot identify an account will wait here for resolution."];
    return ["No failed sources", "Parsing failures will remain visible here until you retry them."];
  }, [connection?.connected, hasFilters, view]);

  function selectView(next: TransactionsView) {
    if (next === view) return;
    setDataError(null);
    setLoadMoreError(null);
    setLoading(true);
    setView(next);
  }

  function onTabKeyDown(event: ReactKeyboardEvent<HTMLButtonElement>, index: number) {
    let target = index;
    if (event.key === "ArrowRight") target = (index + 1) % views.length;
    else if (event.key === "ArrowLeft") target = (index - 1 + views.length) % views.length;
    else if (event.key === "Home") target = 0;
    else if (event.key === "End") target = views.length - 1;
    else return;
    event.preventDefault();
    selectView(views[target]);
    tabRefs.current[target]?.focus();
  }

  async function connectGmail() {
    setConnecting(true);
    setConnectionError(null);
    setOAuthError(null);
    setSyncActionError(null);
    try {
      window.location.assign(await beginGmailConnection(session));
    } catch (error: unknown) {
      setConnectionError(error instanceof Error ? error.message : "Couldn’t begin Gmail connection.");
      setConnecting(false);
    }
  }

  async function triggerSync() {
    setStartingSync(true);
    setMonitorStopped(false);
    setSyncActionError(null);
    setMonitorError(null);
    try {
      setSyncRun(await startGmailSync(session));
    } catch (error: unknown) {
      const message = error instanceof Error ? error.message : "Couldn’t start the Gmail refresh.";
      if (error instanceof TransactionApiError && error.status === 409 && /connect gmail/i.test(message)) {
        setSyncActionError(message);
        setConnection((current) => ({
          connected: false,
          status: current?.status ?? null,
          email: current?.email ?? null,
          last_synced_at: current?.last_synced_at ?? null,
          last_error: message,
        }));
      } else if (error instanceof TransactionApiError && error.status === 409) {
        try {
          const latest = await getLatestSyncRun(session);
          if (latest) {
            setSyncRun(latest);
            setMonitorError(null);
            setMonitorStopped(false);
            setSyncActionError(null);
          } else {
            setSyncRun(null);
            setSyncActionError(null);
            setMonitorError("A Gmail refresh is already active, but its progress was not available yet.");
          }
        } catch (restoreError: unknown) {
          setSyncRun(null);
          setSyncActionError(null);
          setMonitorError(
            restoreError instanceof Error
              ? `A Gmail refresh may already be running, but its progress could not be restored: ${restoreError.message}`
              : message,
          );
        }
      } else {
        setSyncActionError(message);
      }
    } finally {
      setStartingSync(false);
    }
  }

  function retrySyncRestore() {
    setMonitorError(null);
    setSyncRestoring(true);
    setSyncRestoreReload((value) => value + 1);
  }

  function clearFilters() {
    setFilters({});
  }

  function showSuccess(message: string) {
    setSuccess(message);
    void loadData(false);
  }

  function startSourceTransfer(source: SourceSummary, role: TransferSourceRole) {
    setTransferSourceSeed({ id: source.id, title: sourceTitle(source), role });
    setCreatingTransfer(true);
  }

  function closeTransfer() {
    setCreatingTransfer(false);
    setTransferSourceSeed(null);
  }

  return (
    <>
      <header className="page-header transactions-header">
        <div>
          <p className="eyebrow">TRANSACTION ACTIVITY</p>
          <h1>{heading}</h1>
          <p className="muted">{description}</p>
        </div>
        <div className="transaction-header-actions">
          <button className="button button-secondary" onClick={() => { setTransferSourceSeed(null); setCreatingTransfer(true); }} type="button"><Plus aria-hidden="true" size={17} /> Internal transfer</button>
          {connection?.connected ? (
            <button className="button button-primary" disabled={syncBusy} onClick={() => void triggerSync()} type="button">
              <RefreshCw aria-hidden="true" className={syncBusy ? "spin" : undefined} size={18} />
              {startingSync ? "Starting…" : syncBusy ? "Refreshing Gmail…" : "Refresh Gmail"}
            </button>
          ) : (
            <button className="button button-primary" disabled={connecting || connectionLoading} onClick={() => void connectGmail()} type="button">
              <Mail aria-hidden="true" size={18} />{connecting ? "Opening Google…" : connectionLoading ? "Checking Gmail…" : "Connect Gmail"}
            </button>
          )}
        </div>
      </header>

      <section aria-label="Gmail connection" className={`gmail-connection ${connection?.connected ? "connected" : "disconnected"}`}>
        <Mail aria-hidden="true" size={18} />
        <div>
          <strong>{connectionLoading ? "Checking Gmail connection…" : connection?.connected ? `Gmail connected${connection.email ? ` as ${connection.email}` : ""}` : "Gmail is not connected"}</strong>
          <p>{connection?.last_synced_at ? `Last refreshed ${formatDateTime(connection.last_synced_at)}` : connection?.connected ? "Ready to read odin-finance." : "Connect with read-only access to import odin-finance evidence."}</p>
        </div>
        {connectionError && <button className="text-button" onClick={() => { setConnectionLoading(true); setConnectionError(null); void loadConnection(); }} type="button">Retry status</button>}
      </section>
      {(connectionError || oauthError) && <p className="form-error page-inline-error" role="alert">{connectionError || oauthError}</p>}

      {(syncRestoring || syncRun || monitorError) && (
        <section aria-live="polite" className={`sync-status ${syncRestoreFailed ? "failed" : syncRun?.status ?? "queued"}`} role="status">
          {syncRestoreFailed || syncRun?.status === "failed" ? <CircleAlert aria-hidden="true" size={21} /> : syncRestoring ? <Clock3 aria-hidden="true" size={21} /> : syncRun?.status === "completed" ? <CheckCircle2 aria-hidden="true" size={21} /> : <Clock3 aria-hidden="true" size={21} />}
          <div>
            <strong>{syncRestoreFailed ? "Couldn’t restore Gmail refresh progress" : syncRestoring ? "Restoring latest Gmail refresh" : syncRun?.status === "completed" ? "Gmail refresh complete" : syncRun?.status === "failed" ? "Gmail refresh needs attention" : "Refreshing odin-finance"}</strong>
            <p>{syncRun ? syncDescription(syncRun) : syncRestoreFailed ? "Retry the owner-scoped progress check before starting another refresh." : "Checking safe progress…"}</p>
            {monitorError && <p className="sync-monitor-error">{monitorError}</p>}
          </div>
          {syncRestoreFailed ? <button className="button button-secondary button-compact" onClick={retrySyncRestore} type="button">Retry progress</button> : monitorStopped ? <button className="button button-secondary button-compact" onClick={() => { setMonitorStopped(false); setMonitorResumeKey((value) => value + 1); }} type="button">Resume progress</button> : null}
        </section>
      )}
      {syncActionError && (
        <section className="notice notice-error" role="alert">
          <CircleAlert aria-hidden="true" size={20} />
          <div><strong>Couldn’t start Gmail refresh.</strong><p>{syncActionError}</p></div>
          {/connect gmail/i.test(syncActionError) ? <button className="button button-secondary" onClick={() => void connectGmail()} type="button">Connect Gmail</button> : <button className="button button-secondary" onClick={() => void triggerSync()} type="button">Try again</button>}
        </section>
      )}

      <div aria-label="Transaction areas" className="transaction-tabs" role="tablist">
        {views.map((item, index) => (
          <button
            aria-controls={`transaction-panel-${item}`}
            aria-selected={view === item}
            className={view === item ? "active" : ""}
            id={`transaction-tab-${item}`}
            key={item}
            onClick={() => selectView(item)}
            onKeyDown={(event) => onTabKeyDown(event, index)}
            ref={(element) => { tabRefs.current[index] = element; }}
            role="tab"
            tabIndex={view === item ? 0 : -1}
            type="button"
          >
            {item === "transactions" ? "Transactions" : item === "review" ? "Review" : item === "dangling" ? "Dangling" : "Failed"}
          </button>
        ))}
      </div>

      {views.map((item) => (
        <section
          aria-labelledby={`transaction-tab-${item}`}
          hidden={view !== item}
          id={`transaction-panel-${item}`}
          key={item}
          role="tabpanel"
          tabIndex={view === item ? 0 : -1}
        >
          {view === item && (
            <>
        {view === "transactions" && (
          <section aria-label="Transaction filters" className="toolbar">
            <label className="search-field"><Search aria-hidden="true" size={18} /><span className="sr-only">Search transactions</span><input onChange={(event) => setFilters((current) => ({ ...current, search: event.target.value || undefined }))} placeholder="Search merchant or title" type="search" value={filters.search ?? ""} /></label>
            <label className="select-field"><SlidersHorizontal aria-hidden="true" size={17} /><span className="sr-only">Transaction kind</span><select onChange={(event) => setFilters((current) => ({ ...current, kind: event.target.value === "" ? undefined : event.target.value as "debit" | "credit" }))} value={filters.kind ?? ""}><option value="">All flow</option><option value="debit">Money out</option><option value="credit">Money in</option></select></label>
            <label className="select-field"><span className="sr-only">Review state</span><select onChange={(event) => setFilters((current) => ({ ...current, review: event.target.value === "" ? undefined : event.target.value as "confirmed" | "review_required" | "pending" }))} value={filters.review ?? ""}><option value="">All states</option><option value="confirmed">Confirmed</option><option value="review_required">Needs review</option><option value="pending">Processing</option></select></label>
          </section>
        )}
        {hasFilters && view === "transactions" && <button className="text-button clear-filters" onClick={clearFilters} type="button">Clear filters</button>}

        {dataError && (
          <section className="notice notice-error" role="alert">
            <CircleAlert aria-hidden="true" size={20} />
            <div><strong>Couldn’t load {view === "transactions" ? "transactions" : `${view} sources`}.</strong><p>{dataError}</p></div>
            <button className="button button-secondary" onClick={() => void loadData(false)} type="button">Retry</button>
          </section>
        )}
        {success && (
          <section className="notice notice-success" role="status">
            <CheckCircle2 aria-hidden="true" size={20} />
            <div><strong>Saved</strong><p>{success}</p></div>
            <button className="button button-secondary" onClick={() => setSuccess(null)} type="button">Dismiss</button>
          </section>
        )}

        {loading ? (
          <section aria-busy="true" aria-label={`Loading ${view}`} className="transaction-panel" role="status">
            <span className="sr-only">Loading {view}…</span><div className="skeleton-row" /><div className="skeleton-row" /><div className="skeleton-row" />
          </section>
        ) : dataError ? null : activeCount === 0 ? (
          <section className="empty-state transaction-empty">
            <Inbox aria-hidden="true" size={28} /><h2>{emptyCopy[0]}</h2><p>{emptyCopy[1]}</p>
            {view === "transactions" && !hasFilters && (connection?.connected ? <button className="button button-primary" disabled={syncBusy} onClick={() => void triggerSync()} type="button"><RefreshCw aria-hidden="true" size={18} /> Refresh Gmail</button> : <button className="button button-primary" disabled={connecting || connectionLoading} onClick={() => void connectGmail()} type="button"><Mail aria-hidden="true" size={18} /> Connect Gmail</button>)}
            {hasFilters && <button className="button button-secondary" onClick={clearFilters} type="button">Clear filters</button>}
          </section>
        ) : view === "transactions" ? (
          <TransactionRows inspect={setDetailTransaction} items={transactions} />
        ) : (
          <SourceCards inspect={setInspecting} sources={activeSources} view={view} />
        )}

        {nextCursor && !loading && !dataError && (
          <div className="load-more-row"><button className="button button-secondary" disabled={loadingMore} onClick={() => void loadData(true)} type="button">{loadingMore ? "Loading…" : `Load more ${view === "transactions" ? "transactions" : "sources"}`}</button></div>
        )}
        {loadMoreError && <div className="inline-error load-more-error"><p className="form-error" role="alert">{loadMoreError}</p><button className="text-button" onClick={() => void loadData(true)} type="button">Retry</button></div>}
            </>
          )}
        </section>
      ))}

      {inspecting && <SourceInspector close={() => setInspecting(null)} resolved={showSuccess} session={session} source={inspecting} startTransfer={startSourceTransfer} />}
      {detailTransaction && (
        <TransactionDetailDialog
          close={() => setDetailTransaction(null)}
          inspectSource={(source) => { setDetailTransaction(null); setInspecting(source); }}
          saved={showSuccess}
          session={session}
          transaction={detailTransaction}
        />
      )}
      {creatingTransfer && <InternalTransferDialog close={closeTransfer} initialSource={transferSourceSeed ?? undefined} saved={showSuccess} session={session} />}
    </>
  );
}
