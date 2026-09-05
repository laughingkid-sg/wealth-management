import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import type { Session } from "@supabase/supabase-js";
import {
  ArrowLeftRight,
  Bug,
  CircleAlert,
  Download,
  ExternalLink,
  FileImage,
  FileText,
  PlusCircle,
  RefreshCw,
  Search,
  Trash2,
  X,
} from "lucide-react";
import { AccessibleDialog } from "./AccessibleDialog";
import {
  attachSourceToTransaction,
  createTransactionFromSource,
  deleteRawSource,
  getExactSourceDebugField,
  getOwnedTransactionCandidate,
  getSanitizedEmail,
  getSourceAttachments,
  getSourceParseDebug,
  listOwnedAccounts,
  listTransactionsForAccount,
  retrySource,
  type SanitizedEmail,
} from "./api";
import { AccountSelect } from "./TransactionForms";
import { SourceCandidateResolver } from "./SourceCandidateResolver";
import {
  formatAmount,
  formatDateTime,
  type ExactSourceDebugField,
  type OwnedAccountOption,
  type SourceAttachment,
  type SourceParseDebug,
  type SourceParseDebugAttempt,
  type SourceDebugField,
  type SourceSummary,
  type TransferSourceRole,
  type TransactionListItem,
} from "./model";
import { mergeCandidateOptions } from "./transactionUiModel";

function sourceTitle(source: SourceSummary): string {
  return source.suggested_title || source.subject || "Untitled transaction evidence";
}

function bytesLabel(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}

function parseStatusLabel(status: SourceSummary["parse_status"]): string {
  if (status === "review_required") return "Needs review";
  if (status === "dangling") return "Unattached";
  if (status === "failed") return "Failed";
  if (status === "parsing") return "Parsing";
  if (status === "pending") return "Pending";
  return "Parsed";
}

function AttachmentPreview({ attachment }: { attachment: SourceAttachment }) {
  if (!attachment.signed_url) {
    return <p className="muted">This attachment is not currently available to view.</p>;
  }
  if (attachment.mime_type === "application/pdf") {
    return (
      <iframe
        className="attachment-preview attachment-pdf"
        referrerPolicy="no-referrer"
        src={attachment.signed_url}
        title={`Preview ${attachment.filename}`}
      />
    );
  }
  if (attachment.mime_type.startsWith("image/") && attachment.mime_type !== "image/heic") {
    return (
      <img
        alt={`Attachment ${attachment.filename}`}
        className="attachment-preview attachment-image"
        referrerPolicy="no-referrer"
        src={attachment.signed_url}
      />
    );
  }
  return <p className="muted">Use “Open” to view this attachment in a compatible application.</p>;
}

function exactDebugKey(attemptID: string, field: SourceDebugField): string {
  return `${attemptID}:${field}`;
}

function DebugValue({
  attemptID,
  exactError,
  exactLoadingKey,
  exactResult,
  field,
  label,
  loadExact,
  releaseExact,
  truncated,
  value,
}: {
  attemptID: string;
  exactError: { key: string; message: string } | null;
  exactLoadingKey: string | null;
  exactResult: ExactSourceDebugField | null;
  field: SourceDebugField;
  label: string;
  loadExact: (attemptID: string, field: SourceDebugField) => void;
  releaseExact: (key: string) => void;
  truncated: boolean;
  value: unknown;
}) {
  const [open, setOpen] = useState(false);
  const key = exactDebugKey(attemptID, field);
  const matchingExactResult = exactResult && exactDebugKey(exactResult.attempt_id, exactResult.field) === key
    ? exactResult
    : null;
  const displayedValue = matchingExactResult ? matchingExactResult.value : value;
  const content = open
    ? typeof displayedValue === "string"
      ? displayedValue
      : JSON.stringify(displayedValue, null, 2)
    : null;
  return (
    <details
      className="source-debug-value"
      onToggle={(event) => {
        const nextOpen = event.currentTarget.open;
        setOpen(nextOpen);
        if (!nextOpen) releaseExact(key);
      }}
    >
      <summary>
        {label}
        {truncated && <span className="source-debug-shortened">Shortened</span>}
      </summary>
      {open && (
        <>
          <pre>{content ?? "Not recorded"}</pre>
          {truncated && (
            <div className="source-debug-exact-action">
              {matchingExactResult ? (
                <>
                  <p role="status">
                    Exact stored value loaded independently (maximum {bytesLabel(matchingExactResult.max_bytes)}).
                  </p>
                  <button className="text-button" onClick={() => releaseExact(key)} type="button">
                    Return to shortened preview
                  </button>
                </>
              ) : (
                <>
                  <p>The preview is shortened. Load only this field to inspect its exact stored text.</p>
                  {exactError?.key === key && <p className="form-error" role="alert">{exactError.message}</p>}
                  <button
                    className="button button-secondary button-compact"
                    disabled={exactLoadingKey === key}
                    onClick={() => loadExact(attemptID, field)}
                    type="button"
                  >
                    {exactLoadingKey === key ? "Loading exact field…" : `Load exact ${label.toLowerCase()}`}
                  </button>
                </>
              )}
            </div>
          )}
        </>
      )}
    </details>
  );
}

export function SourceInspector({
  session,
  source,
  close,
  resolved,
  startTransfer,
}: {
  session: Session;
  source: SourceSummary;
  close: () => void;
  resolved: (message: string) => void;
  startTransfer: (source: SourceSummary, role: TransferSourceRole) => void;
}) {
  const canResolve = source.parse_status === "dangling" || source.parse_status === "review_required";
  const [email, setEmail] = useState<SanitizedEmail | null>(null);
  const [emailError, setEmailError] = useState<string | null>(null);
  const [emailLoading, setEmailLoading] = useState(source.source_type === "gmail_email");
  const [emailReload, setEmailReload] = useState(0);
  const [attachments, setAttachments] = useState<SourceAttachment[]>([]);
  const [attachmentError, setAttachmentError] = useState<string | null>(null);
  const [attachmentsLoading, setAttachmentsLoading] = useState(source.source_type === "gmail_email");
  const [attachmentReload, setAttachmentReload] = useState(0);
  const [preview, setPreview] = useState<SourceAttachment | null>(null);
  const [action, setAction] = useState<"attach" | "create" | "transfer">("attach");
  const [transferSourceRole, setTransferSourceRole] = useState<TransferSourceRole>("debit");
  const [accounts, setAccounts] = useState<OwnedAccountOption[]>([]);
  const [accountsLoading, setAccountsLoading] = useState(
    source.parse_status === "dangling" || source.parse_status === "review_required",
  );
  const [accountsError, setAccountsError] = useState<string | null>(null);
  const [accountsReload, setAccountsReload] = useState(0);
  const [accountId, setAccountId] = useState(source.suggested_account_id ?? "");
  const [candidates, setCandidates] = useState<TransactionListItem[]>([]);
  const [candidateCursor, setCandidateCursor] = useState<string | null>(null);
  const [candidateSearch, setCandidateSearch] = useState("");
  const [candidatesLoading, setCandidatesLoading] = useState(
    canResolve && Boolean(source.suggested_account_id),
  );
  const [candidatesLoadingMore, setCandidatesLoadingMore] = useState(false);
  const [candidateError, setCandidateError] = useState<string | null>(null);
  const [candidateReload, setCandidateReload] = useState(0);
  const [transactionId, setTransactionId] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [retrying, setRetrying] = useState(false);
  const [debugOpen, setDebugOpen] = useState(false);
  const [debug, setDebug] = useState<SourceParseDebug | null>(null);
  const [debugLoading, setDebugLoading] = useState(false);
  const [debugError, setDebugError] = useState<string | null>(null);
  const [exactDebugResult, setExactDebugResult] = useState<ExactSourceDebugField | null>(null);
  const [exactDebugLoadingKey, setExactDebugLoadingKey] = useState<string | null>(null);
  const [exactDebugError, setExactDebugError] = useState<{ key: string; message: string } | null>(null);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const candidateGeneration = useRef(0);
  const candidateLoadMoreController = useRef<AbortController | null>(null);
  const debugController = useRef<AbortController | null>(null);
  const exactDebugController = useRef<{ key: string; controller: AbortController } | null>(null);
  const selectedAccountIsActive = accounts.some(({ id }) => id === accountId);
  const suggestedAccountUnavailable =
    !accountsLoading &&
    !accountsError &&
    Boolean(source.suggested_account_id) &&
    !accounts.some(({ id }) => id === source.suggested_account_id);

  useEffect(() => {
    if (source.source_type !== "gmail_email") return;
    const controller = new AbortController();
    void getSanitizedEmail(session, source.id, controller.signal)
      .then(setEmail)
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setEmailError(error instanceof Error ? error.message : "Couldn’t load the email content.");
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setEmailLoading(false);
      });
    return () => controller.abort();
  }, [emailReload, session, source.id, source.source_type]);

  useEffect(() => {
    if (source.source_type !== "gmail_email") return;
    const controller = new AbortController();
    void getSourceAttachments(session, source.id, controller.signal)
      .then((items) => {
        setAttachments(items);
        setPreview((current) =>
          current ? items.find((item) => item.filename === current.filename) ?? null : null,
        );
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setAttachmentError(
            error instanceof Error ? error.message : "Couldn’t load source attachments.",
          );
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setAttachmentsLoading(false);
      });
    return () => controller.abort();
  }, [attachmentReload, session, source.id, source.source_type]);

  useEffect(() => {
    if (!canResolve) return;
    const controller = new AbortController();
    void listOwnedAccounts(session, controller.signal)
      .then((items) => {
        setAccounts(items);
        setAccountId((current) => {
          if (items.some(({ id }) => id === current)) return current;
          return source.suggested_account_id &&
            items.some(({ id }) => id === source.suggested_account_id)
            ? source.suggested_account_id
            : "";
        });
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setAccountsError(error instanceof Error ? error.message : "Couldn’t load your accounts.");
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setAccountsLoading(false);
      });
    return () => controller.abort();
  }, [accountsReload, canResolve, session, source.suggested_account_id]);

  useEffect(() => {
    const generation = ++candidateGeneration.current;
    candidateLoadMoreController.current?.abort();
    candidateLoadMoreController.current = null;
    if (!canResolve || action !== "attach" || !selectedAccountIsActive) {
      return;
    }
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      setCandidatesLoading(true);
      setCandidateError(null);
      const recommendedID =
        candidateSearch.trim() === "" &&
        source.suggested_transaction_id &&
        (!source.suggested_account_id || source.suggested_account_id === accountId)
          ? source.suggested_transaction_id
          : null;
      void Promise.all([
        listTransactionsForAccount(
          session,
          accountId,
          candidateSearch,
          null,
          controller.signal,
        ),
        recommendedID
          ? getOwnedTransactionCandidate(session, recommendedID, accountId, controller.signal)
          : Promise.resolve(null),
      ])
        .then(([page, recommended]) => {
          if (generation !== candidateGeneration.current) return;
          const options = mergeCandidateOptions(page.items, recommended);
          setCandidates(options);
          setCandidateCursor(page.next_cursor);
          setTransactionId((current) => {
            if (current && options.some(({ id }) => id === current)) return current;
            return recommended?.id ?? "";
          });
        })
        .catch((error: unknown) => {
          if (!controller.signal.aborted && generation === candidateGeneration.current) {
            setCandidateError(
              error instanceof Error
                ? error.message
                : "Couldn’t load transactions for this account.",
            );
          }
        })
        .finally(() => {
          if (!controller.signal.aborted && generation === candidateGeneration.current) {
            setCandidatesLoading(false);
          }
        });
    }, candidateSearch ? 250 : 0);
    return () => {
      candidateGeneration.current += 1;
      window.clearTimeout(timer);
      controller.abort();
      candidateLoadMoreController.current?.abort();
      candidateLoadMoreController.current = null;
    };
  }, [
    accountId,
    action,
    canResolve,
    candidateReload,
    candidateSearch,
    session,
    source.suggested_account_id,
    source.suggested_transaction_id,
    selectedAccountIsActive,
  ]);

  const loadMoreCandidates = useCallback(async () => {
    if (!candidateCursor || candidatesLoadingMore) return;
    candidateLoadMoreController.current?.abort();
    const controller = new AbortController();
    candidateLoadMoreController.current = controller;
    const generation = candidateGeneration.current;
    setCandidatesLoadingMore(true);
    setCandidateError(null);
    try {
      const page = await listTransactionsForAccount(
        session,
        accountId,
        candidateSearch,
        candidateCursor,
        controller.signal,
      );
      if (controller.signal.aborted || generation !== candidateGeneration.current) return;
      setCandidates((current) => {
        const known = new Set(current.map(({ id }) => id));
        return [...current, ...page.items.filter(({ id }) => !known.has(id))];
      });
      setCandidateCursor(page.next_cursor);
    } catch (error: unknown) {
      if (!controller.signal.aborted && generation === candidateGeneration.current) {
        setCandidateError(
          error instanceof Error ? error.message : "Couldn’t load more transactions.",
        );
      }
    } finally {
      if (generation === candidateGeneration.current) setCandidatesLoadingMore(false);
      if (candidateLoadMoreController.current === controller) {
        candidateLoadMoreController.current = null;
      }
    }
  }, [accountId, candidateCursor, candidateSearch, candidatesLoadingMore, session]);

  const selectedCandidateVisible =
    !candidatesLoading &&
    !candidateError &&
    transactionId !== "" &&
    candidates.some(({ id }) => id === transactionId);

  function selectResolution(next: "attach" | "create" | "transfer") {
    setAction(next);
    setTransactionId("");
    setCandidates([]);
    setCandidateCursor(null);
    setCandidateError(null);
    setActionError(null);
    setCandidatesLoadingMore(false);
    setCandidatesLoading(next === "attach" && selectedAccountIsActive);
  }

  async function submitResolution(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setActionError(null);
    if (action === "transfer") {
      startTransfer(source, transferSourceRole);
      close();
      return;
    }
    if (!selectedAccountIsActive) {
      setActionError("Choose an active account before resolving this source.");
      return;
    }
    if (action === "attach" && !selectedCandidateVisible) {
      setActionError("Choose a visible transaction before attaching this evidence.");
      return;
    }
    setSubmitting(true);
    try {
      if (action === "attach") {
        await attachSourceToTransaction(session, source.id, transactionId);
        resolved("Evidence was attached to the selected transaction.");
      } else {
        await createTransactionFromSource(session, source.id, accountId);
        resolved("A transaction was created from this evidence.");
      }
      close();
    } catch (error: unknown) {
      setActionError(error instanceof Error ? error.message : "Couldn’t save this source decision.");
    } finally {
      setSubmitting(false);
    }
  }

  async function retryParsing() {
    setRetrying(true);
    setActionError(null);
    try {
      await retrySource(session, source.id);
      resolved("This source was queued for parsing again.");
      close();
    } catch (error: unknown) {
      setActionError(error instanceof Error ? error.message : "Couldn’t retry this source.");
    } finally {
      setRetrying(false);
    }
  }

  async function deleteSource() {
    setDeleting(true);
    setDeleteError(null);
    try {
      const result = await deleteRawSource(session, source.id);
      resolved(
        result.cleanup_pending
          ? "Raw source deleted. Stored attachment cleanup is queued and will continue in the background."
          : "Raw source deletion completed.",
      );
      close();
    } catch (error: unknown) {
      setDeleteError(error instanceof Error ? error.message : "Couldn’t delete this raw source.");
    } finally {
      setDeleting(false);
    }
  }

  const loadDebug = useCallback(async () => {
    debugController.current?.abort();
    const controller = new AbortController();
    debugController.current = controller;
    setDebugOpen(true);
    setDebugLoading(true);
    setDebugError(null);
    try {
      const result = await getSourceParseDebug(session, source.id, controller.signal);
      if (!controller.signal.aborted) setDebug(result);
    } catch (error: unknown) {
      if (!controller.signal.aborted) {
        setDebugError(error instanceof Error ? error.message : "Couldn’t load parser debug data.");
      }
    } finally {
      if (!controller.signal.aborted) setDebugLoading(false);
      if (debugController.current === controller) debugController.current = null;
    }
  }, [session, source.id]);

  const releaseExactDebug = useCallback((key: string) => {
    if (exactDebugController.current?.key === key) {
      exactDebugController.current.controller.abort();
      exactDebugController.current = null;
    }
    setExactDebugLoadingKey((current) => (current === key ? null : current));
    setExactDebugError((current) => (current?.key === key ? null : current));
    setExactDebugResult((current) =>
      current && exactDebugKey(current.attempt_id, current.field) === key ? null : current,
    );
  }, []);

  const loadExactDebug = useCallback((attemptID: string, field: SourceDebugField) => {
    exactDebugController.current?.controller.abort();
    const key = exactDebugKey(attemptID, field);
    const controller = new AbortController();
    exactDebugController.current = { key, controller };
    // Keep at most one exact field in browser memory. Selecting another field
    // immediately releases the previous potentially large value.
    setExactDebugResult(null);
    setExactDebugLoadingKey(key);
    setExactDebugError(null);
    void getExactSourceDebugField(session, source.id, attemptID, field, controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) setExactDebugResult(result);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          setExactDebugError({
            key,
            message: error instanceof Error ? error.message : "Couldn’t load the exact debug field.",
          });
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setExactDebugLoadingKey(null);
        if (exactDebugController.current?.controller === controller) {
          exactDebugController.current = null;
        }
      });
  }, [session, source.id]);

  useEffect(() => () => {
    debugController.current?.abort();
    exactDebugController.current?.controller.abort();
  }, []);

  function toggleDebug() {
    if (!debug && !debugLoading) {
      void loadDebug();
      return;
    }
    if (debugOpen) {
      exactDebugController.current?.controller.abort();
      exactDebugController.current = null;
      setExactDebugLoadingKey(null);
      setExactDebugError(null);
      setExactDebugResult(null);
    }
    setDebugOpen(!debugOpen);
  }

  function renderDebugValue(
    attempt: SourceParseDebugAttempt,
    field: SourceDebugField,
    label: string,
    value: unknown,
  ) {
    return (
      <DebugValue
        attemptID={attempt.id}
        exactError={exactDebugError}
        exactLoadingKey={exactDebugLoadingKey}
        exactResult={exactDebugResult}
        field={field}
        label={label}
        loadExact={loadExactDebug}
        releaseExact={releaseExactDebug}
        truncated={attempt.truncated_fields.includes(field)}
        value={value}
      />
    );
  }

  return (
    <AccessibleDialog
      className="source-inspector"
      descriptionId="source-inspector-description"
      onClose={close}
      titleId="source-inspector-title"
    >
      <header className="modal-header">
        <div>
          <p className="eyebrow">SOURCE EVIDENCE</p>
          <h2 id="source-inspector-title">{sourceTitle(source)}</h2>
          <p className="muted" id="source-inspector-description">
            {(source.sender || source.provider)} · {formatDateTime(source.received_at)}
          </p>
        </div>
        <button
          aria-label="Close source"
          className="icon-button"
          data-dialog-initial-focus
          onClick={close}
          type="button"
        >
          <X aria-hidden="true" size={18} />
        </button>
      </header>

      <section aria-labelledby="source-parse-facts-title" className="source-parse-facts">
        <div className="section-heading-inline">
          <div>
            <p className="eyebrow">PARSED FACTS</p>
            <h3 id="source-parse-facts-title">What the importer detected</h3>
          </div>
          <button
            aria-controls="source-debug-panel"
            aria-expanded={debugOpen}
            className="button button-secondary button-compact"
            disabled={debugLoading}
            onClick={toggleDebug}
            type="button"
          >
            <Bug aria-hidden="true" size={15} />
            {debugLoading ? "Loading…" : debugOpen ? "Hide debug" : "Debug"}
          </button>
        </div>
        <dl className="source-facts">
          <div><dt>Status</dt><dd>{parseStatusLabel(source.parse_status)}</dd></div>
          <div>
            <dt>Detected amount</dt>
            <dd>
              {source.suggested_amount_minor !== null && source.suggested_currency
                ? formatAmount(source.suggested_amount_minor, source.suggested_currency)
                : "Not detected"}
            </dd>
          </div>
          <div>
            <dt>Detected currency</dt>
            <dd>{source.suggested_currency || "Not detected"}</dd>
          </div>
          <div><dt>Parse confidence</dt><dd>{source.parse_confidence === null ? "Not scored" : `${source.parse_confidence}%`}</dd></div>
          <div>
            <dt>Suggested account</dt>
            <dd>
              {suggestedAccountUnavailable
                ? `${source.suggested_account_name || "Suggested account"} (no longer active)`
                : source.suggested_account_name || (source.suggested_account_id ? "Identified" : "Not identified")}
            </dd>
          </div>
          <div className={source.parse_error || source.reconciliation_reason ? "source-error" : undefined}>
            <dt>Parse context</dt>
            <dd>{source.parse_error || source.reconciliation_reason || "No parser warning was provided."}</dd>
          </div>
        </dl>
      </section>

      {debugOpen && (
        <section aria-labelledby="source-debug-title" className="source-debug" id="source-debug-panel">
          <div className="section-heading-inline">
            <div>
              <p className="eyebrow">OWNER DEBUG</p>
              <h3 id="source-debug-title">Parser attempts</h3>
            </div>
            {debugError && (
              <button className="button button-secondary button-compact" onClick={() => void loadDebug()} type="button">
                <RefreshCw aria-hidden="true" size={15} /> Retry
              </button>
            )}
          </div>
          <p className="source-debug-warning">
            Private diagnostic data can include the complete normalized source and provider payloads.
          </p>
          {debug?.has_more && (
            <p className="source-debug-limit" role="status">
              Only the latest {debug.attempts.length} parser attempts are shown.
            </p>
          )}
          {debug?.truncated && (
            <p className="source-debug-limit" role="status">
              Some large fields were shortened to keep this debug response safe to load.
            </p>
          )}
          {debugLoading ? (
            <p aria-live="polite" className="muted" role="status">Loading parser attempts…</p>
          ) : debugError ? (
            <p className="form-error" role="alert">{debugError}</p>
          ) : debug?.attempts.length === 0 ? (
            <p className="muted" role="status">No parser attempts have been recorded for this source.</p>
          ) : (
            <div className="source-debug-attempts">
              {debug?.attempts.map((attempt, index) => (
                <article className="source-debug-attempt" key={attempt.id}>
                  <header>
                    <div>
                      <strong>Attempt {debug.attempts.length - index}</strong>
                      <p>{formatDateTime(attempt.created_at)} · {attempt.model_name ?? "No model recorded"}</p>
                    </div>
                    <span className={`status-pill ${attempt.validation_status === "valid" ? "transfer" : attempt.validation_status === "pending" ? "review" : "failed"}`}>
                      {attempt.validation_status}
                    </span>
                  </header>
                  <dl className="source-debug-provenance">
                    <div>
                      <dt>Global rule</dt>
                      <dd>{attempt.parser_rule_id ? `${attempt.parser_rule_id} · v${attempt.parser_rule_version}` : "None"}</dd>
                    </div>
                    <div>
                      <dt>Personal rule</dt>
                      <dd>{attempt.user_parser_rule_id ? `${attempt.user_parser_rule_id} · v${attempt.user_parser_rule_version}` : "None"}</dd>
                    </div>
                    <div>
                      <dt>Started</dt>
                      <dd>{attempt.started_at ? formatDateTime(attempt.started_at) : "Not recorded"}</dd>
                    </div>
                    <div>
                      <dt>Completed</dt>
                      <dd>{attempt.completed_at ? formatDateTime(attempt.completed_at) : "Not recorded"}</dd>
                    </div>
                  </dl>
                  {attempt.error_summary && <p className="source-debug-error" role="status"><strong>Error:</strong> {attempt.error_summary}</p>}
                  {attempt.truncated_fields.length > 0 && (
                    <p className="source-debug-truncated" role="status">
                      Shortened fields: {attempt.truncated_fields.join(", ")}
                    </p>
                  )}
                  <div className="source-debug-values">
                    {renderDebugValue(attempt, "assembled_system_prompt", "Assembled system prompt", attempt.assembled_system_prompt)}
                    {renderDebugValue(attempt, "normalized_input", "Normalized input", attempt.normalized_input)}
                    {renderDebugValue(attempt, "prompt_components", "Prompt components", attempt.prompt_components)}
                    {renderDebugValue(attempt, "provider_request", "Provider request", attempt.provider_request)}
                    {renderDebugValue(attempt, "provider_response", "Provider response", attempt.provider_response)}
                    {renderDebugValue(attempt, "model_output", "Raw model output", attempt.model_output)}
                    {renderDebugValue(attempt, "parsed_candidate", "Validated candidate", attempt.parsed_candidate)}
                    {renderDebugValue(attempt, "request_metadata", "Request metadata", attempt.request_metadata)}
                  </div>
                </article>
              ))}
            </div>
          )}
        </section>
      )}

      {(source.reconciliation_reason || source.suggested_category_leaf_name) && (
        <section aria-label="Source recommendation" className="source-recommendation">
          <p className="eyebrow">REVIEW CONTEXT</p>
          <dl>
            {source.reconciliation_reason && (
              <div>
                <dt>Why this needs review</dt>
                <dd>{source.reconciliation_reason}</dd>
              </div>
            )}
            {source.suggested_category_leaf_name && (
              <div>
                <dt>Suggested category</dt>
                <dd>{source.suggested_category_leaf_name}</dd>
              </div>
            )}
          </dl>
        </section>
      )}

      {emailLoading ? (
        <p aria-live="polite" className="source-loading" role="status">Loading sanitized email…</p>
      ) : emailError ? (
        <section className="notice notice-error" role="alert">
          <CircleAlert aria-hidden="true" size={20} />
          <div><strong>Couldn’t load this email.</strong><p>{emailError}</p></div>
          <button className="button button-secondary" onClick={() => { setEmailLoading(true); setEmailError(null); setEmailReload((v) => v + 1); }} type="button">
            <RefreshCw aria-hidden="true" size={16} /> Retry
          </button>
        </section>
      ) : email?.html ? (
        <iframe
          className="source-email-frame"
          referrerPolicy="no-referrer"
          sandbox=""
          srcDoc={email.html}
          title={email.subject}
        />
      ) : email?.text ? (
        <pre className="source-email-text">{email.text}</pre>
      ) : source.source_type !== "gmail_email" ? (
        <p className="source-loading">This source has no email body.</p>
      ) : (
        <p className="source-loading">This email has no stored body. Its attachments and parsed facts remain available below.</p>
      )}

      <section aria-labelledby="source-attachments-title" className="source-attachments">
        <div className="section-heading-inline">
          <div>
            <p className="eyebrow">PRIVATE FILES</p>
            <h3 id="source-attachments-title">Attachments</h3>
          </div>
          {attachmentError && (
            <button className="button button-secondary button-compact" onClick={() => { setAttachmentsLoading(true); setAttachmentError(null); setAttachmentReload((v) => v + 1); }} type="button">
              <RefreshCw aria-hidden="true" size={15} /> Retry
            </button>
          )}
        </div>
        {attachmentsLoading ? (
          <p aria-live="polite" className="muted" role="status">Loading attachments…</p>
        ) : attachmentError ? (
          <p className="form-error" role="alert">{attachmentError}</p>
        ) : attachments.length === 0 ? (
          <p className="muted">No qualifying PDF or image attachments were stored.</p>
        ) : (
          <ul className="attachment-list">
            {attachments.map((attachment) => (
              <li key={`${attachment.filename}-${attachment.byte_size}`}>
                {attachment.mime_type === "application/pdf" ? <FileText aria-hidden="true" size={19} /> : <FileImage aria-hidden="true" size={19} />}
                <div>
                  <strong>{attachment.filename}</strong>
                  <p>{attachment.mime_type} · {bytesLabel(attachment.byte_size)} · {attachment.parse_eligible ? "Used for parsing when available" : "Evidence only"}</p>
                </div>
                {attachment.signed_url ? (
                  <div className="attachment-actions">
                    <button aria-label={`Preview ${attachment.filename}`} className="button button-secondary button-compact" onClick={() => setPreview(attachment)} type="button">View</button>
                    <a aria-label={`Open ${attachment.filename} in a new tab`} className="icon-button" href={attachment.signed_url} rel="noreferrer noopener" target="_blank" title={`Open ${attachment.filename}`}>
                      <ExternalLink aria-hidden="true" size={16} />
                    </a>
                    <a aria-label={`Download ${attachment.filename}`} className="icon-button" download={attachment.filename} href={attachment.signed_url} title={`Download ${attachment.filename}`}>
                      <Download aria-hidden="true" size={16} />
                    </a>
                  </div>
                ) : (
                  <span className="status-pill review">{attachment.storage_status}</span>
                )}
              </li>
            ))}
          </ul>
        )}
        {preview && (
          <div className="attachment-preview-shell">
            <div className="section-heading-inline">
              <strong>{preview.filename}</strong>
              <button aria-label={`Close preview of ${preview.filename}`} className="text-button" onClick={() => setPreview(null)} type="button">Close preview</button>
            </div>
            <AttachmentPreview attachment={preview} />
          </div>
        )}
      </section>

      {source.parse_status === "failed" && (
        <section aria-labelledby="source-retry-title" className="source-resolution">
          <div>
            <p className="eyebrow">PARSING FAILED</p>
            <h3 id="source-retry-title">Retry without fetching Gmail again</h3>
            <p className="muted">{source.parse_error || "The stored source could not be parsed."}</p>
          </div>
          {actionError && <p className="form-error" role="alert">{actionError}</p>}
          <div className="modal-actions">
            <button className="button button-secondary" onClick={close} type="button">Cancel</button>
            <button className="button button-primary" disabled={retrying} onClick={() => void retryParsing()} type="button">
              <RefreshCw aria-hidden="true" className={retrying ? "spin" : undefined} size={17} />
              {retrying ? "Queueing…" : "Retry parsing"}
            </button>
          </div>
        </section>
      )}

      {canResolve && (
        <section aria-labelledby="source-resolution-title" className="source-resolution">
          <div>
            <p className="eyebrow">RESOLVE SOURCE</p>
            <h3 id="source-resolution-title">Choose where this evidence belongs</h3>
            <p className="muted">The server validates ownership and derives canonical fields from the stored, validated source candidate.</p>
          </div>
          {source.source_type === "gmail_email" ? (
            <SourceCandidateResolver session={session} source={source} resolved={resolved} />
          ) : accountsLoading ? (
            <p aria-live="polite" className="muted" role="status">Loading your accounts…</p>
          ) : accountsError ? (
            <section className="notice notice-error" role="alert">
              <CircleAlert aria-hidden="true" size={20} />
              <div><strong>Couldn’t load accounts.</strong><p>{accountsError}</p></div>
              <button className="button button-secondary" onClick={() => { setAccountsLoading(true); setAccountsError(null); setAccountsReload((v) => v + 1); }} type="button">Retry</button>
            </section>
          ) : accounts.length === 0 ? (
            <section className="notice notice-error" role="status">
              <CircleAlert aria-hidden="true" size={20} />
              <div><strong>Add an account first.</strong><p>A transaction needs an active account before this source can be resolved.</p></div>
            </section>
          ) : (
            <form className="resolution-form" onSubmit={(event) => void submitResolution(event)}>
              <fieldset className="resolution-choice">
                <legend>Resolution</legend>
                <label><input checked={action === "attach"} name="source-resolution" onChange={() => selectResolution("attach")} type="radio" value="attach" />Attach to an existing transaction</label>
                <label><input checked={action === "create"} name="source-resolution" onChange={() => selectResolution("create")} type="radio" value="create" />Create a transaction from this source</label>
                <label><input checked={action === "transfer"} name="source-resolution" onChange={() => selectResolution("transfer")} type="radio" value="transfer" />Create an internal transfer</label>
              </fieldset>
              {action !== "transfer" && (
                <label>
                  Account
                  <AccountSelect accounts={accounts} onChange={(value) => { setAccountId(value); setTransactionId(""); setCandidates([]); setCandidateCursor(null); setCandidateError(null); setActionError(null); setCandidatesLoadingMore(false); setCandidatesLoading(Boolean(value) && action === "attach"); }} value={accountId} />
                </label>
              )}
              {action !== "transfer" && suggestedAccountUnavailable && !accountId && (
                <p className="form-error" role="status">The suggested account is no longer active. Choose another account to continue.</p>
              )}
              {action === "transfer" && (
                <fieldset className="resolution-choice transfer-source-role">
                  <legend>Attach this evidence to</legend>
                  <label><input checked={transferSourceRole === "debit"} name="transfer-source-role" onChange={() => setTransferSourceRole("debit")} type="radio" value="debit" />Outgoing debit leg</label>
                  <label><input checked={transferSourceRole === "credit"} name="transfer-source-role" onChange={() => setTransferSourceRole("credit")} type="radio" value="credit" />Incoming credit leg</label>
                  <label><input checked={transferSourceRole === "both"} name="transfer-source-role" onChange={() => setTransferSourceRole("both")} type="radio" value="both" />Both legs</label>
                  {accounts.length < 2 && <p className="form-error" role="status">Add a second active account before creating an internal transfer.</p>}
                </fieldset>
              )}
              {action === "attach" && accountId && (
                <fieldset className="candidate-picker">
                  <legend>Existing transaction</legend>
                  <label className="search-field candidate-search">
                    <Search aria-hidden="true" size={17} />
                    <span className="sr-only">Search transactions in this account</span>
                    <input onChange={(event) => { setCandidateSearch(event.target.value); setTransactionId(""); setCandidates([]); setCandidateCursor(null); setCandidateError(null); setActionError(null); setCandidatesLoadingMore(false); setCandidatesLoading(true); }} placeholder="Search title or merchant" type="search" value={candidateSearch} />
                  </label>
                  {candidatesLoading ? (
                    <p aria-live="polite" className="muted" role="status">Loading transactions…</p>
                  ) : candidateError ? (
                    <div className="inline-error"><p className="form-error" role="alert">{candidateError}</p><button className="text-button" onClick={() => { setCandidatesLoading(true); setCandidateError(null); setCandidateReload((v) => v + 1); }} type="button">Retry</button></div>
                  ) : candidates.length === 0 ? (
                    <p className="muted">No matching transactions in this account.</p>
                  ) : (
                    <div className="candidate-options" role="radiogroup" aria-label="Choose a transaction">
                      {candidates.map((transaction) => (
                        <label className={transactionId === transaction.id ? "selected" : ""} key={transaction.id}>
                          <input checked={transactionId === transaction.id} name="candidate-transaction" onChange={() => setTransactionId(transaction.id)} type="radio" value={transaction.id} />
                          <span>
                            <strong>
                              {transaction.title}
                              {transaction.id === source.suggested_transaction_id && <span className="recommended-badge">Recommended</span>}
                            </strong>
                            <small>{formatAmount(transaction.original_amount_minor, transaction.original_currency)} · {formatDateTime(transaction.occurred_at)}</small>
                          </span>
                        </label>
                      ))}
                    </div>
                  )}
                  {candidateCursor && !candidateError && (
                    <button className="button button-secondary button-compact" disabled={candidatesLoadingMore} onClick={() => void loadMoreCandidates()} type="button">{candidatesLoadingMore ? "Loading…" : "Load more transactions"}</button>
                  )}
                </fieldset>
              )}
              {actionError && <p className="form-error" role="alert">{actionError}</p>}
              <div className="modal-actions">
                <button className="button button-secondary" onClick={close} type="button">Cancel</button>
                <button className="button button-primary" disabled={submitting || (action !== "transfer" && !selectedAccountIsActive) || (action === "attach" && !selectedCandidateVisible) || (action === "transfer" && accounts.length < 2)} type="submit">
                  {action === "transfer" ? <ArrowLeftRight aria-hidden="true" size={17} /> : <PlusCircle aria-hidden="true" size={17} />}
                  {submitting ? "Saving…" : action === "attach" ? "Attach source" : action === "create" ? "Create transaction" : "Continue to transfer"}
                </button>
              </div>
            </form>
          )}
        </section>
      )}

      <section aria-labelledby="source-delete-title" className="source-danger-zone">
        <div>
          <p className="eyebrow">RAW SOURCE</p>
          <h3 id="source-delete-title">Permanently delete this evidence</h3>
          <p className="muted">Use this only when the imported source should not be retained.</p>
        </div>
        {!confirmingDelete ? (
          <button className="button button-secondary" onClick={() => {
            setConfirmingDelete(true);
            setDeleteError(null);
          }} type="button">
            <Trash2 aria-hidden="true" size={17} /> Delete raw source
          </button>
        ) : (
          <div aria-labelledby="source-delete-confirmation-title" className="source-delete-confirmation" role="group">
            <strong id="source-delete-confirmation-title">Delete this raw source permanently?</strong>
            <p>
              Deleting this raw source permanently removes the email record, stored attachments,
              parser attempts/debug data, queued jobs, and evidence links. A transaction remains if
              it has another source or was created/edited by you. An automatically created,
              never-edited transaction is also deleted when this was its last source, including its
              line items. Stored attachment cleanup may continue safely in the background after the
              database record is gone.
            </p>
            {deleteError && <p className="form-error" role="alert">{deleteError}</p>}
            <div className="confirm-actions">
              <button className="button button-secondary" disabled={deleting} onClick={() => {
                setConfirmingDelete(false);
                setDeleteError(null);
              }} type="button">Keep source</button>
              <button className="button button-danger" disabled={deleting} onClick={() => void deleteSource()} type="button">
                <Trash2 aria-hidden="true" size={17} /> {deleting ? "Deleting…" : "Delete permanently"}
              </button>
            </div>
          </div>
        )}
        {deleteError && !confirmingDelete && <p className="form-error" role="alert">{deleteError}</p>}
      </section>
    </AccessibleDialog>
  );
}
