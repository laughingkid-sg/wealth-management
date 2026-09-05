import { useCallback, useEffect, useMemo, useState } from "react";
import type { Session } from "@supabase/supabase-js";
import { CircleAlert, Link2, Plus, RefreshCw } from "lucide-react";
import {
  attachCandidateToTransaction,
  createTransactionFromCandidate,
  listOwnedAccounts,
  listSourceCandidates,
  listTransactions,
  TransactionApiError,
} from "./api";
import {
  formatAmount,
  formatDateTime,
  type OwnedAccountOption,
  type SourceCandidate,
  type SourceSummary,
  type TransactionListItem,
} from "./model";
import "./parsing-pipeline.css";

function candidateResolved(status: string): boolean {
  return status === "created" || status === "attached";
}

function statusLabel(status: string): string {
  switch (status) {
    case "created":
      return "Created";
    case "attached":
      return "Attached";
    case "review_required":
      return "Needs review";
    case "dangling":
      return "Unattached";
    case "pending_reconciliation":
      return "Reconciling…";
    default:
      return status;
  }
}

function CandidateRow({
  session,
  sourceId,
  candidate,
  accounts,
  onResolved,
}: {
  session: Session;
  sourceId: string;
  candidate: SourceCandidate;
  accounts: OwnedAccountOption[];
  onResolved: (message: string) => void;
}) {
  const [mode, setMode] = useState<"create" | "attach">("create");
  const [accountId, setAccountId] = useState(candidate.suggested_account_id ?? "");
  const [search, setSearch] = useState("");
  const [matches, setMatches] = useState<TransactionListItem[]>([]);
  const [searching, setSearching] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const report = useCallback((cause: unknown) => {
    if (cause instanceof TransactionApiError) setError(cause.message);
    else if (cause instanceof Error) setError(cause.message);
    else setError("Something went wrong.");
  }, []);

  async function handleCreate() {
    if (accountId === "") {
      setError("Choose an account first.");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      await createTransactionFromCandidate(session, sourceId, candidate.id, accountId);
      onResolved("Transaction created from candidate.");
    } catch (cause) {
      report(cause);
    } finally {
      setSubmitting(false);
    }
  }

  async function handleSearch() {
    setSearching(true);
    setError(null);
    try {
      const page = await listTransactions(session, { search, kind: candidate.transaction_kind });
      setMatches(page.items.slice(0, 8));
    } catch (cause) {
      report(cause);
    } finally {
      setSearching(false);
    }
  }

  async function handleAttach(transactionId: string) {
    setSubmitting(true);
    setError(null);
    try {
      await attachCandidateToTransaction(session, sourceId, candidate.id, transactionId);
      onResolved("Candidate attached to transaction.");
    } catch (cause) {
      report(cause);
    } finally {
      setSubmitting(false);
    }
  }

  const resolved = candidateResolved(candidate.status);

  return (
    <li className="candidate-row">
      <div className="candidate-summary">
        <span className="candidate-title">{candidate.title || "Untitled"}</span>
        <span className="muted">
          {candidate.transaction_kind} · {formatAmount(String(candidate.original_amount_minor), candidate.original_currency)} · {formatDateTime(candidate.occurred_at)}
        </span>
        <span className={`badge${resolved ? " success" : ""}`}>{statusLabel(candidate.status)}</span>
      </div>
      {candidate.reconciliation_reason && <p className="muted">{candidate.reconciliation_reason}</p>}

      {!resolved && candidate.status !== "pending_reconciliation" && (
        <div className="candidate-actions">
          <div className="segmented" role="tablist" aria-label="Resolution mode">
            <button type="button" className={mode === "create" ? "active" : ""} onClick={() => setMode("create")}>
              Create
            </button>
            <button type="button" className={mode === "attach" ? "active" : ""} onClick={() => setMode("attach")}>
              Attach
            </button>
          </div>

          {mode === "create" ? (
            <div className="candidate-create">
              <select value={accountId} onChange={(event) => setAccountId(event.target.value)} disabled={submitting} aria-label="Account">
                <option value="">Select an account…</option>
                {accounts.map((account) => (
                  <option key={account.id} value={account.id}>
                    {account.name}
                    {account.institution_name ? ` · ${account.institution_name}` : ""}
                  </option>
                ))}
              </select>
              <button type="button" className="button button-primary" onClick={() => void handleCreate()} disabled={submitting}>
                <Plus aria-hidden size={16} /> Create transaction
              </button>
            </div>
          ) : (
            <div className="candidate-attach">
              <div className="candidate-search">
                <input
                  type="search"
                  value={search}
                  placeholder="Search transactions…"
                  onChange={(event) => setSearch(event.target.value)}
                  disabled={submitting}
                />
                <button type="button" className="button button-secondary" onClick={() => void handleSearch()} disabled={searching || submitting}>
                  Search
                </button>
              </div>
              {matches.length > 0 && (
                <ul className="candidate-matches">
                  {matches.map((match) => (
                    <li key={match.id}>
                      <span>{match.title}</span>
                      <span className="muted">{formatAmount(match.original_amount_minor, match.original_currency)} · {formatDateTime(match.occurred_at)}</span>
                      <button type="button" className="button button-secondary" onClick={() => void handleAttach(match.id)} disabled={submitting}>
                        <Link2 aria-hidden size={14} /> Attach
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </div>
      )}

      {error && (
        <p className="form-error" role="alert">
          <CircleAlert aria-hidden size={14} /> {error}
        </p>
      )}
    </li>
  );
}

export function SourceCandidateResolver({
  session,
  source,
  resolved,
}: {
  session: Session;
  source: SourceSummary;
  resolved: (message: string) => void;
}) {
  const [candidates, setCandidates] = useState<SourceCandidate[]>([]);
  const [accounts, setAccounts] = useState<OwnedAccountOption[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    Promise.all([
      listSourceCandidates(session, source.id, controller.signal),
      listOwnedAccounts(session, controller.signal),
    ])
      .then(([loadedCandidates, loadedAccounts]) => {
        if (controller.signal.aborted) return;
        setCandidates(loadedCandidates);
        setAccounts(loadedAccounts);
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        setError(cause instanceof Error ? cause.message : "Couldn’t load candidates.");
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [session, source.id, reload]);

  const pending = useMemo(
    () => candidates.filter((candidate) => !candidateResolved(candidate.status)).length,
    [candidates],
  );

  function handleResolved(message: string) {
    resolved(message);
    setReload((value) => value + 1);
  }

  if (loading) return <p>Loading candidates…</p>;
  if (error) {
    return (
      <p className="form-error" role="alert">
        <CircleAlert aria-hidden size={16} /> {error}
      </p>
    );
  }
  if (candidates.length === 0) {
    return <p className="empty-state">This email parsed into no transactions.</p>;
  }

  return (
    <div className="candidate-resolver">
      <div className="candidate-resolver-header">
        <h3>{candidates.length} candidate{candidates.length === 1 ? "" : "s"} · {pending} unresolved</h3>
        <button type="button" className="button button-secondary" onClick={() => setReload((value) => value + 1)}>
          <RefreshCw aria-hidden size={16} /> Refresh
        </button>
      </div>
      <ul className="candidate-list">
        {candidates.map((candidate) => (
          <CandidateRow
            key={candidate.id}
            session={session}
            sourceId={source.id}
            candidate={candidate}
            accounts={accounts}
            onResolved={handleResolved}
          />
        ))}
      </ul>
    </div>
  );
}
