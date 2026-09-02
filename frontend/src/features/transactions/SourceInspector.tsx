import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import type { Session } from "@supabase/supabase-js";
import {
  ArrowLeftRight,
  CircleAlert,
  Download,
  ExternalLink,
  FileImage,
  FileText,
  PlusCircle,
  RefreshCw,
  Search,
  X,
} from "lucide-react";
import { AccessibleDialog } from "./AccessibleDialog";
import {
  attachSourceToTransaction,
  createTransactionFromSource,
  getOwnedTransactionCandidate,
  getSanitizedEmail,
  getSourceAttachments,
  listOwnedAccounts,
  listTransactionsForAccount,
  retrySource,
  type SanitizedEmail,
} from "./api";
import { AccountSelect } from "./TransactionForms";
import {
  formatAmount,
  formatDateTime,
  type OwnedAccountOption,
  type SourceAttachment,
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
  const candidateGeneration = useRef(0);
  const candidateLoadMoreController = useRef<AbortController | null>(null);
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
        <p className="eyebrow">PARSED FACTS</p>
        <h3 id="source-parse-facts-title">What the importer detected</h3>
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
          {accountsLoading ? (
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
    </AccessibleDialog>
  );
}
