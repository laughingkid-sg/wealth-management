import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
  type DragEvent,
  type FormEvent,
  type ReactNode,
} from "react";
import type { Session } from "@supabase/supabase-js";
import { createPortal } from "react-dom";
import {
  Archive,
  ArchiveRestore,
  ArrowDown,
  ArrowRight,
  ArrowUp,
  Check,
  CheckCircle2,
  ChevronDown,
  CircleAlert,
  Clock3,
  CreditCard,
  FileImage,
  FileSearch,
  FileText,
  FolderPlus,
  History,
  Layers3,
  LoaderCircle,
  Pencil,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  ShieldCheck,
  Sparkles,
  Trash2,
  Upload,
  X,
} from "lucide-react";
import {
  documentTypeLabel,
  documentTypeOptions,
  formatFileSize,
  totalCandidates,
  type BatchGroupResult,
  type BatchStatus,
  type DocumentGroup,
  type DocumentType,
  type DraftFile,
  type ImportTemplate,
  type ImportBatch,
} from "./model";
import type { Account } from "../accounts/model";
import {
  cancelBatch as cancelBulkBatch,
  createBatch,
  createTemplate,
  deleteBatch as deleteBulkBatch,
  deleteDocument,
  finalizeFile,
  getBatch,
  getDebugAttemptField,
  getDocumentEvidence,
  listBatches,
  listCandidates,
  listDebugAttempts,
  listTemplates,
  previewPrompt,
  replaceDocumentLayout,
  reserveFile,
  resolveCandidate,
  retryDocument,
  setTemplateArchived,
  sha256Hex,
  submitBatch,
  updateTemplate,
  uploadReservedFile,
  BulkApiError,
  type BulkBatchDto,
  type BulkCandidateDto,
  type BulkDocumentDto,
  type BulkEvidenceItemDto,
  type BulkReservationDto,
  type BulkTemplateDto,
} from "./api";
import "./BulkImportPage.css";

const fileLimit = 20;
const fileSizeLimit = 5 * 1024 * 1024;
const batchSizeLimit = 50 * 1024 * 1024;
const acceptedExtensions = ["bmp", "jpeg", "jpg", "png", "tiff", "tif", "webp", "heic", "pdf"];
const acceptedMimes = new Set([
  "application/pdf",
  "image/bmp",
  "image/jpeg",
  "image/png",
  "image/tiff",
  "image/webp",
  "image/heic",
]);
const mimeByExtension: Record<string, string> = { bmp: "image/bmp", jpeg: "image/jpeg", jpg: "image/jpeg", png: "image/png", tiff: "image/tiff", tif: "image/tiff", webp: "image/webp", heic: "image/heic", pdf: "application/pdf" };

type PageTab = "new" | "history";
type HistoryFilter = "all" | BatchStatus;
interface UploadRecovery {
  batchId: string;
  reservations: Record<string, BulkReservationDto>;
  finalizedDraftFileIds: string[];
}
function templateFromApi(template: BulkTemplateDto): ImportTemplate {
  return { id: template.id, title: template.title, documentType: template.document_type, prompt: template.parsing_prompt, accountIds: template.accounts.map((account) => account.account_id), archived: Boolean(template.archived_at), lastUsed: new Date(template.updated_at).toLocaleString("en-SG") };
}

function documentStatus(status: BulkDocumentDto["status"]): BatchGroupResult["status"] {
  switch (status) {
    case "preparing": case "parsing": case "aggregating": case "reconciling": return "processing";
    case "draft": case "queued": return "queued";
    case "completed_with_errors": case "failed": return "failed";
    case "completed": return "completed";
    case "cancelled": return "cancelled";
  }
}

function batchFromApi(batch: BulkBatchDto): ImportBatch {
  const status: BatchStatus = batch.status === "running" ? "processing" : batch.status === "cancelling" ? "cancel_requested" : batch.status;
  const documents = batch.documents ?? [];
  const totalDocuments = documents.length || batch.counters.documents;
  const terminalDocuments = documents.length
    ? documents.filter((document) => ["completed", "completed_with_errors", "failed", "cancelled"].includes(document.status)).length
    : ["completed", "completed_with_errors", "failed", "cancelled"].includes(status) ? totalDocuments : 0;
  const progress = totalDocuments ? Math.round((terminalDocuments / totalDocuments) * 100) : status === "completed" ? 100 : 0;
  return {
    id: batch.id,
    name: batch.title_snapshot,
    createdAt: new Date(batch.created_at).toLocaleString("en-SG"),
    templateTitle: batch.title_snapshot,
    documentType: batch.document_type_snapshot,
    accountIds: batch.accounts.map((account) => account.account_id),
    accountNames: batch.accounts.map((account) => [account.institution_name, account.name].filter(Boolean).join(" · ")),
    status,
    progress,
    processedGroups: terminalDocuments,
    totalGroups: totalDocuments,
    candidates: { created: batch.counters.created, matched: batch.counters.attached + batch.counters.duplicates, review: batch.counters.review, failed: batch.counters.failed },
    groups: documents.map((document, index) => ({
      id: document.id,
      name: document.display_label || `Document ${document.sort_order + 1}`,
      fileSummary: document.files.map((file) => file.display_filename).join(" + ") || "Evidence unavailable",
      status: documentStatus(document.status),
      candidates: document.created_count !== undefined
        ? { created: document.created_count, matched: (document.attached_count ?? 0) + (document.duplicate_count ?? 0), review: document.review_count ?? 0, failed: document.failed_count ?? 0 }
        : index === 0
          ? { created: batch.counters.created, matched: batch.counters.attached + batch.counters.duplicates, review: batch.counters.review, failed: batch.counters.failed }
          : { created: 0, matched: 0, review: 0, failed: document.status === "failed" ? 1 : 0 },
      message: document.status === "failed" ? batch.error_summary ?? "Document processing failed." : document.status === "completed" ? "Document processing completed." : `Document is ${document.status}.`,
      attempt: document.attempt_generation,
      dataSourceId: document.data_source_id ?? undefined,
      bill: document.specialized_result?.kind === "credit_card_bill" ? {
        id: document.specialized_result.resource_id,
        accountId: batch.accounts[0]?.account_id,
        cardName: batch.accounts[0] ? [batch.accounts[0].institution_name, batch.accounts[0].name].filter(Boolean).join(" · ") : "Credit Card",
        statementPeriod: "Open the bill for statement details",
        dueDate: "See Credit Card",
        amountDue: "See Credit Card",
        status: "Review",
      } : undefined,
    })),
  };
}

function statusLabel(status: BatchStatus): string {
  const labels: Record<BatchStatus, string> = {
    draft: "Draft",
    queued: "Queued",
    processing: "Processing",
    cancel_requested: "Cancelling",
    completed: "Completed",
    completed_with_errors: "Completed with errors",
    failed: "Failed",
    cancelled: "Cancelled",
  };
  return labels[status];
}

function statusIcon(status: BatchStatus, size = 15) {
  if (status === "draft" || status === "queued") return <Clock3 aria-hidden="true" size={size} />;
  if (status === "processing" || status === "cancel_requested") {
    return <LoaderCircle aria-hidden="true" className="bi-spin" size={size} />;
  }
  if (status === "completed") return <CheckCircle2 aria-hidden="true" size={size} />;
  if (status === "completed_with_errors" || status === "failed") {
    return <CircleAlert aria-hidden="true" size={size} />;
  }
  return <X aria-hidden="true" size={size} />;
}

function Dialog({
  titleId,
  descriptionId,
  className = "",
  close,
  children,
}: {
  titleId: string;
  descriptionId?: string;
  className?: string;
  close: () => void;
  children: ReactNode;
}) {
  const dialogRef = useRef<HTMLElement>(null);
  const closeRef = useRef(close);

  useEffect(() => {
    closeRef.current = close;
  }, [close]);

  useEffect(() => {
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const previousOverflow = document.body.style.overflow;
    const app = document.querySelector<HTMLElement>(".app-layout");
    const previousInert = app?.inert ?? false;
    if (app) app.inert = true;
    document.body.style.overflow = "hidden";
    const frame = window.requestAnimationFrame(() => {
      (dialogRef.current?.querySelector<HTMLElement>("[data-initial-focus]") ?? dialogRef.current)?.focus();
    });
    const focusableSelector = "a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex='-1'])";

    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.preventDefault();
        closeRef.current();
        return;
      }
      if (event.key !== "Tab" || !dialogRef.current) return;
      const focusable = [...dialogRef.current.querySelectorAll<HTMLElement>(focusableSelector)]
        .filter((element) => element.getClientRects().length > 0);
      if (!focusable.length) {
        event.preventDefault();
        dialogRef.current.focus();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }

    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      window.cancelAnimationFrame(frame);
      document.body.style.overflow = previousOverflow;
      if (app) app.inert = previousInert;
      previousFocus?.focus();
    };
  }, []);

  return createPortal(
    <div className="bi-dialog-backdrop" onMouseDown={(event) => event.target === event.currentTarget && close()}>
      <section
        aria-describedby={descriptionId}
        aria-labelledby={titleId}
        aria-modal="true"
        className={`bi-dialog ${className}`.trim()}
        ref={dialogRef}
        role="dialog"
        tabIndex={-1}
      >
        {children}
      </section>
    </div>,
    document.body,
  );
}

function DialogHeader({ titleId, eyebrow, title, close }: { titleId: string; eyebrow: string; title: string; close: () => void }) {
  return (
    <header className="bi-dialog-header">
      <div>
        <p className="bi-kicker">{eyebrow}</p>
        <h2 id={titleId}>{title}</h2>
      </div>
      <button aria-label="Close dialog" className="bi-icon-button" onClick={close} type="button"><X size={19} /></button>
    </header>
  );
}

function TemplateDialog({
  template,
  templates,
  close,
  save,
  accounts,
}: {
  template: ImportTemplate | null;
  templates: ImportTemplate[];
  close: () => void;
  save: (template: ImportTemplate) => Promise<void>;
  accounts: Account[];
}) {
  const [title, setTitle] = useState(template?.title ?? "");
  const [documentType, setDocumentType] = useState<DocumentType>(template?.documentType ?? "physical_receipt");
  const [prompt, setPrompt] = useState(template?.prompt ?? "");
  const [accountIds, setAccountIds] = useState<string[]>(template?.accountIds ?? []);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const isCardBill = documentType === "credit_card_bill";
  const availableAccounts = isCardBill
    ? accounts.filter((account) => account.account_type === "credit_card")
    : accounts;

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const normalizedTitle = title.trim();
    const nextErrors: Record<string, string> = {};
    if (!normalizedTitle) nextErrors.title = "Add a clear template title.";
    if (templates.some((item) => item.id !== template?.id && item.title.toLowerCase() === normalizedTitle.toLowerCase())) {
      nextErrors.title = "That title is already used by an active or archived template.";
    }
    if (!prompt.trim()) nextErrors.prompt = "Add guidance for interpreting this document.";
    if (prompt.length > 8_000) nextErrors.prompt = "Keep guidance to 8,000 characters or fewer.";
    if (isCardBill && accountIds.length !== 1) nextErrors.accounts = "Choose exactly one Credit Card Account.";
    if (!isCardBill && accountIds.length < 1) nextErrors.accounts = "Choose at least one Account.";
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length) return;
    void save({
      id: template?.id ?? `template-${Date.now()}`,
      title: normalizedTitle,
      documentType,
      prompt: prompt.trim(),
      accountIds,
      archived: template?.archived ?? false,
      lastUsed: template?.lastUsed ?? "Never used",
    });
  }

  function toggleAccount(id: string) {
    if (isCardBill) {
      setAccountIds([id]);
      return;
    }
    setAccountIds((current) => current.includes(id) ? current.filter((value) => value !== id) : [...current, id]);
  }

  return (
    <Dialog close={close} descriptionId="bi-template-description" titleId="bi-template-title">
      <DialogHeader close={close} eyebrow="BULK IMPORT TEMPLATE" title={template ? "Edit template" : "Create a template"} titleId="bi-template-title" />
      <p className="bi-dialog-copy" id="bi-template-description">
        Save only interpretation guidance here. Transaction facts will always come from the uploaded document.
      </p>
      <form className="bi-template-form" onSubmit={submit}>
        <div className="bi-form-grid">
          <label>
            Template title
            <input aria-describedby={errors.title ? "bi-title-error" : undefined} autoFocus data-initial-focus maxLength={80} onChange={(event) => setTitle(event.target.value)} placeholder="e.g. DBS monthly card statement" value={title} />
            {errors.title && <small className="bi-field-error" id="bi-title-error">{errors.title}</small>}
          </label>
          <label>
            Document type
            <span className="bi-select-wrap">
              <select onChange={(event) => {
                const nextType = event.target.value as DocumentType;
                setDocumentType(nextType);
                if (nextType === "credit_card_bill") {
                  const cardId = accountIds.find((id) => accounts.some((account) => account.id === id && account.account_type === "credit_card"));
                  setAccountIds(cardId ? [cardId] : []);
                }
              }} value={documentType}>
                {documentTypeOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
              </select>
              <ChevronDown aria-hidden="true" size={16} />
            </span>
          </label>
        </div>
        <label>
          Parsing guidance
          <textarea aria-describedby={errors.prompt ? "bi-prompt-error" : "bi-prompt-help"} maxLength={8_001} onChange={(event) => setPrompt(event.target.value)} placeholder="Describe date formats, table structure, debit and credit notation, or refund conventions…" rows={5} value={prompt} />
          <span className="bi-field-meta" id="bi-prompt-help"><span>Guidance cannot invent missing transaction details.</span><span>{prompt.length.toLocaleString()} / 8,000</span></span>
          {errors.prompt && <small className="bi-field-error" id="bi-prompt-error">{errors.prompt}</small>}
        </label>
        <fieldset className="bi-account-fieldset">
          <legend>{isCardBill ? "Credit Card Account" : "Default Accounts"}</legend>
          <p>{isCardBill ? "One bill belongs to exactly one active Credit Card Account." : "These are preselected for each new batch and can be overridden."}</p>
          <div className="bi-account-options">
            {availableAccounts.map((account) => {
              const checked = accountIds.includes(account.id);
              return (
                <label className={`bi-account-option ${checked ? "is-selected" : ""}`} key={account.id}>
                  <input checked={checked} name={isCardBill ? "template-card" : undefined} onChange={() => toggleAccount(account.id)} type={isCardBill ? "radio" : "checkbox"} />
                  <span className="bi-account-mark">{checked && <Check aria-hidden="true" size={14} />}</span>
                  <span><strong>{account.name}</strong><small>{account.institution_name} · {account.account_type.replaceAll("_", " ")}{account.account_identifier ? ` · ${account.account_identifier}` : ""}</small></span>
                </label>
              );
            })}
          </div>
          {errors.accounts && <small className="bi-field-error">{errors.accounts}</small>}
        </fieldset>
        <footer className="bi-dialog-actions">
          <button className="button button-secondary" onClick={close} type="button">Cancel</button>
          <button className="button button-primary" type="submit">{template ? "Save changes" : "Create template"}</button>
        </footer>
      </form>
    </Dialog>
  );
}

function CandidateSummary({ group }: { group: BatchGroupResult }) {
  const counts = group.candidates;
  if (group.status === "failed" || group.status === "cancelled" || !totalCandidates(counts)) return null;
  return (
    <div aria-label={`${totalCandidates(counts)} candidate outcomes`} className="bi-outcome-row">
      {counts.created > 0 && <span className="bi-outcome is-created"><Check size={13} />{counts.created} created</span>}
      {counts.matched > 0 && <span className="bi-outcome is-matched"><Layers3 size={13} />{counts.matched} matched</span>}
      {counts.review > 0 && <span className="bi-outcome is-review"><CircleAlert size={13} />{counts.review} review</span>}
      {counts.failed > 0 && <span className="bi-outcome is-failed"><X size={13} />{counts.failed} failed</span>}
    </div>
  );
}

function BillResultCard({ bill, openBill }: { bill: NonNullable<BatchGroupResult["bill"]>; openBill: () => void }) {
  return (
    <aside className="bi-bill-result">
      <div className="bi-bill-icon"><CreditCard aria-hidden="true" size={20} /></div>
      <div className="bi-bill-main">
        <div className="bi-bill-title-row">
          <div><p>Credit Card bill created</p><strong>{bill.cardName}</strong></div>
          <span className={`bi-bill-status is-${bill.status.toLowerCase()}`}>{bill.status}</span>
        </div>
        <dl>
          <div><dt>Statement period</dt><dd>{bill.statementPeriod}</dd></div>
          <div><dt>Due</dt><dd>{bill.dueDate}</dd></div>
          <div><dt>Amount due</dt><dd>{bill.amountDue}</dd></div>
        </dl>
      </div>
      <button aria-label={`View Credit Card bill ${bill.id}`} className="button button-secondary" onClick={openBill} type="button">View {bill.id} <ArrowRight size={16} /></button>
    </aside>
  );
}

export function BulkImportPage({
  onOpenCreditCardBill,
  session,
  accounts,
}: {
  onOpenCreditCardBill?: (billId: string, accountId: string) => void;
  session: Session;
  accounts: Account[];
}) {
  const [tab, setTab] = useState<PageTab>("new");
  const [templates, setTemplates] = useState<ImportTemplate[]>([]);
  const [serverTemplates, setServerTemplates] = useState<Record<string, BulkTemplateDto>>({});
  const [selectedTemplateId, setSelectedTemplateId] = useState("");
  const [batchAccountIds, setBatchAccountIds] = useState<string[]>([]);
  const [templateDialog, setTemplateDialog] = useState<{ template: ImportTemplate | null } | null>(null);
  const [archiveTarget, setArchiveTarget] = useState<ImportTemplate | null>(null);
  const [showArchived, setShowArchived] = useState(false);
  const [groups, setGroups] = useState<DocumentGroup[]>([]);
  const [fileErrors, setFileErrors] = useState<string[]>([]);
  const [duplicateConfirmed, setDuplicateConfirmed] = useState(false);
  const [duplicateRequired, setDuplicateRequired] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [currentBatch, setCurrentBatch] = useState<ImportBatch | null>(null);
  const [history, setHistory] = useState<ImportBatch[]>([]);
  const [historyFilter, setHistoryFilter] = useState<HistoryFilter>("all");
  const [historySearch, setHistorySearch] = useState("");
  const [selectedBatchId, setSelectedBatchId] = useState("");
  const [evidenceGroup, setEvidenceGroup] = useState<BatchGroupResult | null>(null);
  const [evidenceItems, setEvidenceItems] = useState<BulkEvidenceItemDto[]>([]);
  const [evidenceLoading, setEvidenceLoading] = useState(false);
  const [evidenceError, setEvidenceError] = useState<string | null>(null);
  const [debugGroup, setDebugGroup] = useState<BatchGroupResult | null>(null);
  const [reviewGroup, setReviewGroup] = useState<BatchGroupResult | null>(null);
  const [reviewAccountId, setReviewAccountId] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<BatchGroupResult | null>(null);
  const [reorderAnnouncement, setReorderAnnouncement] = useState("");
  const [toast, setToast] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [pageError, setPageError] = useState<string | null>(null);
  const [debugPayload, setDebugPayload] = useState<unknown>(null);
  const [reviewCandidates, setReviewCandidates] = useState<BulkCandidateDto[]>([]);
  const [promptPreviewPayload, setPromptPreviewPayload] = useState<unknown>(null);
  const [uploadRecovery, setUploadRecovery] = useState<UploadRecovery | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const selectedTemplate = templates.find((template) => template.id === selectedTemplateId) ?? null;
  const isCardBill = selectedTemplate?.documentType === "credit_card_bill";
  const totalFiles = groups.reduce((sum, group) => sum + group.files.length, 0);
  const totalBytes = groups.flatMap((group) => group.files).reduce((sum, file) => sum + file.size, 0);
  const isBatchActive = currentBatch && ["queued", "processing", "cancel_requested"].includes(currentBatch.status);
  const inputLocked = Boolean(isBatchActive || uploadRecovery);

  const filteredHistory = useMemo(() => history.filter((batch) => {
    if (historyFilter !== "all" && batch.status !== historyFilter) return false;
    const query = historySearch.trim().toLowerCase();
    return !query || `${batch.name} ${batch.templateTitle} ${batch.id}`.toLowerCase().includes(query);
  }), [history, historyFilter, historySearch]);

  const selectedBatch = currentBatch?.id === selectedBatchId
    ? currentBatch
    : history.find((batch) => batch.id === selectedBatchId) ?? filteredHistory[0] ?? null;

  useEffect(() => {
    if (!toast) return;
    const timer = window.setTimeout(() => setToast(null), 2_800);
    return () => window.clearTimeout(timer);
  }, [toast]);

  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([listTemplates(session, true, controller.signal), listBatches(session, controller.signal)])
      .then(([templateItems, batches]) => {
        const mappedTemplates = templateItems.map(templateFromApi);
        setTemplates(mappedTemplates);
        setServerTemplates(Object.fromEntries(templateItems.map((item) => [item.id, item])));
        const active = mappedTemplates.find((item) => !item.archived);
        if (active) {
          setSelectedTemplateId((current) => current || active.id);
          setBatchAccountIds((current) => current.length ? current : active.accountIds);
        }
        const mappedBatches = batches.map(batchFromApi);
        setHistory(mappedBatches);
        const activeBatch = mappedBatches.find((batch) => ["queued", "processing", "cancel_requested"].includes(batch.status));
        const detailTarget = activeBatch ?? mappedBatches[0];
        setSelectedBatchId((current) => current || detailTarget?.id || "");
        if (detailTarget) void getBatch(session, detailTarget.id, controller.signal).then((detail) => {
          const mapped = batchFromApi(detail);
          setHistory((items) => items.map((item) => item.id === mapped.id ? mapped : item));
          if (["queued", "processing", "cancel_requested"].includes(mapped.status)) setCurrentBatch(mapped);
        }).catch((reason: unknown) => { if (!controller.signal.aborted) setPageError(reason instanceof Error ? reason.message : "Batch detail could not be loaded."); });
      })
      .catch((reason: unknown) => { if (!controller.signal.aborted) setPageError(reason instanceof Error ? reason.message : "Bulk Import could not be loaded."); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [session]);

  useEffect(() => {
    if (!currentBatch || !["queued", "processing", "cancel_requested"].includes(currentBatch.status)) return;
    const timer = window.setInterval(() => {
      void getBatch(session, currentBatch.id).then((next) => {
        const mapped = batchFromApi(next);
        setCurrentBatch(mapped);
        setHistory((items) => items.some((item) => item.id === mapped.id) ? items.map((item) => item.id === mapped.id ? mapped : item) : [mapped, ...items]);
      }).catch((reason: unknown) => setPageError(reason instanceof Error ? reason.message : "Batch progress could not be refreshed."));
    }, 2000);
    return () => window.clearInterval(timer);
  }, [currentBatch, session]);

  function chooseTemplate(template: ImportTemplate) {
    if (template.archived || inputLocked) return;
    setSelectedTemplateId(template.id);
    setBatchAccountIds(template.accountIds);
    setDuplicateConfirmed(false);
    setDuplicateRequired(false);
  }

  async function saveTemplate(nextTemplate: ImportTemplate) {
    setBusy(true);
    setPageError(null);
    try {
      const input = { title: nextTemplate.title, document_type: nextTemplate.documentType, parsing_prompt: nextTemplate.prompt, account_ids: nextTemplate.accountIds };
      const existing = templateDialog?.template ? serverTemplates[templateDialog.template.id] : null;
      const saved = existing ? await updateTemplate(session, existing, input) : await createTemplate(session, input);
      const mapped = templateFromApi(saved);
      setServerTemplates((items) => ({ ...items, [saved.id]: saved }));
      setTemplates((items) => items.some((item) => item.id === mapped.id) ? items.map((item) => item.id === mapped.id ? mapped : item) : [mapped, ...items]);
      setSelectedTemplateId(mapped.id);
      setBatchAccountIds(mapped.accountIds);
      setTemplateDialog(null);
      setToast(existing ? "Template changes saved for future batches." : "Template created and selected.");
    } catch (reason) {
      setPageError(reason instanceof Error ? reason.message : "The template could not be saved.");
    } finally { setBusy(false); }
  }

  async function toggleArchived(template: ImportTemplate) {
    const nextArchived = !template.archived;
    setBusy(true);
    try {
      const saved = await setTemplateArchived(session, template.id, nextArchived);
      const mapped = templateFromApi(saved);
      setServerTemplates((items) => ({ ...items, [saved.id]: saved }));
      setTemplates((items) => items.map((item) => item.id === template.id ? mapped : item));
      if (nextArchived && selectedTemplateId === template.id) {
        const fallback = templates.find((item) => item.id !== template.id && !item.archived);
        if (fallback) {
          setSelectedTemplateId(fallback.id);
          setBatchAccountIds(fallback.accountIds);
        }
      }
      setArchiveTarget(null);
      setToast(nextArchived ? "Template archived. Historical batches keep their snapshot." : "Template restored.");
    } catch (reason) { setPageError(reason instanceof Error ? reason.message : "The template could not be updated."); }
    finally { setBusy(false); }
  }

  function toggleBatchAccount(id: string) {
    if (!selectedTemplate || inputLocked) return;
    if (isCardBill) {
      setBatchAccountIds([id]);
      return;
    }
    setBatchAccountIds((ids) => ids.includes(id) ? ids.filter((value) => value !== id) : [...ids, id]);
  }

  function addFiles(fileList: FileList | File[]) {
    if (inputLocked) return;
    const files = Array.from(fileList);
    const errors: string[] = [];
    const accepted: DraftFile[] = [];
    let nextTotalBytes = totalBytes;
    let nextCount = totalFiles;

    files.forEach((file, index) => {
      const extension = file.name.split(".").pop()?.toLowerCase() ?? "";
      const normalizedMime = acceptedMimes.has(file.type) ? file.type : mimeByExtension[extension];
      const supported = Boolean(normalizedMime) && acceptedExtensions.includes(extension);
      if (!supported) {
        errors.push(`${file.name}: use a PDF, BMP, JPEG, PNG, TIFF, WEBP, or HEIC file.`);
        return;
      }
      if (file.size > fileSizeLimit) {
        errors.push(`${file.name}: ${formatFileSize(file.size)} exceeds the 5 MB per-file limit.`);
        return;
      }
      if (nextCount >= fileLimit) {
        errors.push(`${file.name}: this batch already has the maximum 20 files.`);
        return;
      }
      if (nextTotalBytes + file.size > batchSizeLimit) {
        errors.push(`${file.name}: adding it would exceed the 50 MB batch limit.`);
        return;
      }
      nextCount += 1;
      nextTotalBytes += file.size;
      accepted.push({
        id: `file-${Date.now()}-${index}`,
        name: file.name,
        mimeType: normalizedMime,
        size: file.size,
        kind: extension === "pdf" || file.type === "application/pdf" ? "pdf" : "image",
        duplicate: false,
        previewTone: (["orange", "blue", "green", "violet"] as const)[index % 4],
        raw: file,
      });
    });

    if (accepted.length) {
      setGroups((current) => [
        ...current,
        ...accepted.map((file, index) => ({
          id: `group-${Date.now()}-${index}`,
          label: file.kind === "pdf" ? "PDF document" : "Image document",
          files: [file],
        })),
      ]);
      setDuplicateConfirmed(false);
      setDuplicateRequired(false);
    }
    setFileErrors(errors);
  }

  function moveGroup(index: number, direction: -1 | 1) {
    setGroups((current) => {
      const target = index + direction;
      if (target < 0 || target >= current.length) return current;
      const next = [...current];
      [next[index], next[target]] = [next[target], next[index]];
      return next;
    });
    setReorderAnnouncement(`Document group moved to position ${index + direction + 1} of ${groups.length}.`);
  }

  function moveFile(groupId: string, index: number, direction: -1 | 1) {
    setGroups((current) => current.map((group) => {
      if (group.id !== groupId) return group;
      const target = index + direction;
      if (target < 0 || target >= group.files.length) return group;
      const files = [...group.files];
      [files[index], files[target]] = [files[target], files[index]];
      return { ...group, files };
    }));
    const group = groups.find((item) => item.id === groupId);
    setReorderAnnouncement(`${group?.files[index].name ?? "Image"} moved to page ${index + direction + 1} of ${group?.files.length ?? 1}.`);
  }

  function groupImages() {
    const imageGroups = groups.filter((group) => group.files.every((file) => file.kind === "image"));
    if (imageGroups.length < 2) {
      setToast("Add at least two separate image documents to group them.");
      return;
    }
    const imageIds = new Set(imageGroups.map((group) => group.id));
    const merged: DocumentGroup = {
      id: `group-images-${Date.now()}`,
      label: `Grouped images · ${imageGroups.flatMap((group) => group.files).length} pages`,
      files: imageGroups.flatMap((group) => group.files),
    };
    const firstIndex = groups.findIndex((group) => imageIds.has(group.id));
    const remaining = groups.filter((group) => !imageIds.has(group.id));
    remaining.splice(firstIndex, 0, merged);
    setGroups(remaining);
    setToast("Images grouped in their current order.");
  }

  function ungroupImages(groupId: string) {
    setGroups((current) => current.flatMap((group) => group.id === groupId
      ? group.files.map((file, index) => ({ id: `group-${Date.now()}-${index}`, label: "Image document", files: [file] }))
      : [group]));
  }

  function removeFile(groupId: string, fileId: string) {
    setGroups((current) => current.flatMap((group) => {
      if (group.id !== groupId) return [group];
      const files = group.files.filter((file) => file.id !== fileId);
      return files.length ? [{ ...group, files }] : [];
    }));
    setFileErrors([]);
  }

  async function startBatch() {
    const validation: string[] = [];
    if (!selectedTemplate) validation.push("Select an active template.");
    if (!batchAccountIds.length) validation.push("Select at least one Account.");
    if (isCardBill && batchAccountIds.length !== 1) validation.push("Choose exactly one Credit Card Account.");
    if (!totalFiles) validation.push("Add at least one document.");
    if (duplicateRequired && !duplicateConfirmed) validation.push("Confirm the possible duplicate document before processing.");
    if (validation.length) {
      setFileErrors(validation);
      return;
    }
    if (!selectedTemplate) return;
    setBusy(true);
    setPageError(null);
    let recovery = uploadRecovery;
    try {
      let serverBatch: BulkBatchDto;
      if (recovery) {
        serverBatch = await getBatch(session, recovery.batchId);
        if (serverBatch.status !== "draft") throw new Error("This recoverable upload is no longer a draft. Delete it or refresh the page before trying again.");
      } else {
        serverBatch = await createBatch(session, selectedTemplate.id, batchAccountIds);
        recovery = { batchId: serverBatch.id, reservations: {}, finalizedDraftFileIds: [] };
        setUploadRecovery(recovery);
        const mappedDraft = batchFromApi(serverBatch);
        setCurrentBatch(mappedDraft);
        setHistory((items) => [mappedDraft, ...items.filter((item) => item.id !== mappedDraft.id)]);
        setSelectedBatchId(mappedDraft.id);
      }
      const previouslyReserved = new Set(Object.keys(recovery.reservations));
      const reserved = new Map<string, BulkReservationDto>(Object.entries(recovery.reservations));
      const finalized = new Set(recovery.finalizedDraftFileIds);
      const retainRecovery = () => setUploadRecovery({
        batchId: serverBatch.id,
        reservations: Object.fromEntries(reserved),
        finalizedDraftFileIds: [...finalized],
      });
      for (const group of groups) {
        for (const draft of group.files) {
          if (!draft.raw) throw new Error(`${draft.name} must be chosen again before upload.`);
          let reservation = reserved.get(draft.id);
          if (!reservation) {
            reservation = await reserveFile(session, serverBatch.id, {
              filename: draft.name,
              mime_type: draft.mimeType,
              byte_size: draft.size,
              sha256: await sha256Hex(draft.raw),
              intentional_duplicate: duplicateConfirmed,
            });
            reserved.set(draft.id, reservation);
            retainRecovery();
          }
          if (!finalized.has(draft.id)) {
            if (previouslyReserved.has(draft.id)) {
              try {
                await finalizeFile(session, serverBatch.id, reservation.file.id);
              } catch {
                await uploadReservedFile(reservation, draft.raw);
                await finalizeFile(session, serverBatch.id, reservation.file.id);
              }
            } else {
              await uploadReservedFile(reservation, draft.raw);
              await finalizeFile(session, serverBatch.id, reservation.file.id);
            }
            finalized.add(draft.id);
            retainRecovery();
          }
        }
      }
      serverBatch = await replaceDocumentLayout(session, serverBatch.id, groups.map((group) => {
        const files = group.files.map((file) => reserved.get(file.id)?.file).filter((file): file is NonNullable<typeof file> => Boolean(file));
        if (!files[0]) throw new Error("An uploaded document is missing its server reservation.");
        return { id: files[0].document_id, label: group.label, file_ids: files.map((file) => file.id) };
      }));
      serverBatch = await submitBatch(session, serverBatch.id);
      const mapped = batchFromApi(serverBatch);
      setUploadRecovery(null);
      setCurrentBatch(mapped);
      setHistory((items) => [mapped, ...items.filter((item) => item.id !== mapped.id)]);
      setSelectedBatchId(mapped.id);
      setFileErrors([]);
      setDuplicateRequired(false);
      setDuplicateConfirmed(false);
      setToast("Batch queued with an immutable template and Account snapshot.");
    } catch (reason) {
      const duplicateConflict = reason instanceof BulkApiError
        && reason.status === 409
        && (reason.code === "duplicate_file" || /duplicate|same file|checksum/i.test(reason.message));
      if (duplicateConflict && !duplicateConfirmed) {
        setDuplicateRequired(true);
        setPageError("This file was already uploaded. Confirm “Import anyway” only if you want to process another copy.");
      } else {
        setPageError(reason instanceof Error ? reason.message : "The batch could not be submitted.");
      }
      if (recovery?.batchId) {
        try {
          const draft = batchFromApi(await getBatch(session, recovery.batchId));
          setCurrentBatch(draft);
          setHistory((items) => items.some((item) => item.id === draft.id) ? items.map((item) => item.id === draft.id ? draft : item) : [draft, ...items]);
        } catch {
          // Keep the original upload error; the retained recovery state can be retried or deleted.
        }
      }
    } finally { setBusy(false); }
  }

  async function cancelBatch() {
    if (!currentBatch) return;
    try {
      const mapped = batchFromApi(await cancelBulkBatch(session, currentBatch.id));
      setCurrentBatch(mapped);
      setHistory((items) => items.map((item) => item.id === mapped.id ? mapped : item));
      setToast("Cancellation requested.");
    } catch (reason) { setPageError(reason instanceof Error ? reason.message : "Cancellation could not be requested."); }
  }

  async function retryGroup(batchId: string, groupId: string) {
    try {
      await retryDocument(session, groupId);
      const mapped = batchFromApi(await getBatch(session, batchId));
      setHistory((items) => items.map((item) => item.id === mapped.id ? mapped : item));
      if (currentBatch?.id === mapped.id) setCurrentBatch(mapped);
      setToast("Retry started. Successful groups will not run again.");
    } catch (reason) { setPageError(reason instanceof Error ? reason.message : "The document could not be retried."); }
  }

  function openCreditCardBill(bill: NonNullable<BatchGroupResult["bill"]>) {
    if (onOpenCreditCardBill) {
      if (bill.accountId) onOpenCreditCardBill(bill.id, bill.accountId);
      else setPageError("The Credit Card Account is missing from this bill result.");
      return;
    }
    setPageError("Credit Card navigation is unavailable.");
  }

  async function deleteEvidence(groupId: string) {
    try {
      await deleteDocument(session, groupId);
      const remove = (batch: ImportBatch): ImportBatch => ({ ...batch, groups: batch.groups.filter((group) => group.id !== groupId), totalGroups: Math.max(0, batch.totalGroups - 1) });
      setHistory((items) => items.map(remove));
      setCurrentBatch((batch) => batch ? remove(batch) : null);
      setDeleteTarget(null);
      setToast("Eligible evidence and its parse history were deleted.");
    } catch (reason) { setPageError(reason instanceof Error ? reason.message : "The evidence could not be deleted."); }
  }

  async function removeBatch(batchId: string) {
    try {
      await deleteBulkBatch(session, batchId);
      setHistory((items) => items.filter((item) => item.id !== batchId));
      if (currentBatch?.id === batchId) setCurrentBatch(null);
      if (uploadRecovery?.batchId === batchId) setUploadRecovery(null);
      setSelectedBatchId((current) => current === batchId ? "" : current);
      setToast("Draft or cancelled batch deleted.");
    } catch (reason) { setPageError(reason instanceof Error ? reason.message : "The batch could not be deleted."); }
  }

  async function openDebug(group: BatchGroupResult) {
    setDebugGroup(group);
    setDebugPayload(null);
    if (!group.dataSourceId) {
      setDebugPayload({ message: "No parse attempt exists for this document yet." });
      return;
    }
    try {
      const attempts = await listDebugAttempts(session, group.dataSourceId);
      const hydrated = await Promise.all(attempts.map(async (attempt) => {
        const result: Record<string, unknown> = { ...attempt };
        await Promise.all(attempt.truncated_fields.map(async (field) => {
          const fullField = await getDebugAttemptField(session, group.dataSourceId!, attempt.id, field);
          result[field] = fullField.value;
        }));
        return result;
      }));
      setDebugPayload(hydrated);
    }
    catch (reason) { setDebugPayload({ error: reason instanceof Error ? reason.message : "Debug attempts could not be loaded." }); }
  }

  async function openEvidence(group: BatchGroupResult) {
    setEvidenceGroup(group);
    setEvidenceItems([]);
    setEvidenceError(null);
    setEvidenceLoading(true);
    try { setEvidenceItems(await getDocumentEvidence(session, group.id)); }
    catch (reason) { setEvidenceError(reason instanceof Error ? reason.message : "Evidence links could not be created."); }
    finally { setEvidenceLoading(false); }
  }

  async function openReview(group: BatchGroupResult, batchId: string) {
    setReviewGroup(group);
    setReviewCandidates([]);
    const batch = currentBatch?.id === batchId ? currentBatch : history.find((item) => item.id === batchId);
    setReviewAccountId(batch?.accountIds.length === 1 ? batch.accountIds[0] : "");
    try {
      const candidates = await listCandidates(session, batchId);
      setReviewCandidates(candidates.filter((candidate) => candidate.document_id === group.id && candidate.status === "review_required"));
    } catch (reason) { setPageError(reason instanceof Error ? reason.message : "Review candidates could not be loaded."); }
  }

  async function selectHistoryBatch(batchId: string) {
    setSelectedBatchId(batchId);
    try {
      const mapped = batchFromApi(await getBatch(session, batchId));
      setHistory((items) => items.map((item) => item.id === mapped.id ? mapped : item));
    } catch (reason) { setPageError(reason instanceof Error ? reason.message : "Batch detail could not be loaded."); }
  }

  async function resolveReviewCandidate(candidate: BulkCandidateDto) {
    try {
      const updated = candidate.account_id
        ? await resolveCandidate(session, candidate.id, { action: "create", expected_generation: candidate.attempt_generation })
        : reviewAccountId
          ? await resolveCandidate(session, candidate.id, { action: "set_account", account_id: reviewAccountId, expected_generation: candidate.attempt_generation })
          : (() => { throw new Error("Choose an Account before resolving this candidate."); })();
      setReviewCandidates((items) => items.map((item) => item.id === updated.id ? updated : item).filter((item) => item.status === "review_required"));
      setToast(candidate.account_id ? "Transaction created from the reviewed candidate." : "Candidate Account updated. Review it once more before creating.");
    } catch (reason) { setPageError(reason instanceof Error ? reason.message : "The candidate could not be resolved."); }
  }

  async function openPromptPreview() {
    if (!selectedTemplate) return;
    setPromptPreviewPayload({ status: "Loading…" });
    try { setPromptPreviewPayload(await previewPrompt(session, selectedTemplate.id, batchAccountIds)); }
    catch (reason) { setPromptPreviewPayload({ error: reason instanceof Error ? reason.message : "Prompt preview could not be loaded." }); }
  }

  const activeTemplates = templates.filter((template) => !template.archived);
  const archivedTemplates = templates.filter((template) => template.archived);
  const batchAccountChoices = isCardBill ? accounts.filter((account) => account.account_type === "credit_card") : accounts;

  return (
    <section aria-labelledby="bi-page-title" className="bi-page">
      <header className="bi-page-header">
        <div>
          <p className="bi-kicker">TRANSACTIONS</p>
          <h1 id="bi-page-title">Bulk Import</h1>
          <p>Turn documents into tidy transactions. Every upload keeps its evidence, and only uncertain details wait for review.</p>
        </div>
      </header>

      {pageError && <div className="bi-inline-alert is-error" role="alert"><CircleAlert size={18} /><div><strong>Bulk Import needs attention</strong><p>{pageError}</p></div><button aria-label="Dismiss error" className="bi-icon-button" onClick={() => setPageError(null)} type="button"><X size={16} /></button></div>}
      {loading && <div aria-busy="true" className="bi-panel" role="status"><strong>Loading Bulk Import…</strong></div>}

      <div className="bi-page-controls">
        <nav aria-label="Bulk Import views" className="bi-page-tabs">
          <button aria-current={tab === "new" ? "page" : undefined} className={tab === "new" ? "is-active" : ""} onClick={() => setTab("new")} type="button"><Sparkles aria-hidden="true" size={17} /> New import</button>
          <button aria-current={tab === "history" ? "page" : undefined} className={tab === "history" ? "is-active" : ""} onClick={() => setTab("history")} type="button"><History aria-hidden="true" size={17} /> Batch history <span>{history.length}</span></button>
        </nav>
        <button className="button button-primary bi-header-action" onClick={() => { setTab("new"); setTemplateDialog({ template: null }); }} type="button">
          <Plus aria-hidden="true" size={18} /> New template
        </button>
      </div>

      {tab === "new" ? (
        <div className="bi-new-layout">
          <section className="bi-main-column">
            <article className="bi-panel bi-template-panel">
              <div className="bi-section-heading">
                <span className="bi-step">1</span>
                <div><h2>Choose a template</h2><p>Saved guidance and default Accounts for this document format.</p></div>
                <button className="button button-secondary bi-compact" disabled={!selectedTemplate} onClick={() => void openPromptPreview()} type="button"><FileText size={16} /> Preview prompt</button>
                <button className="button button-secondary bi-compact" onClick={() => setTemplateDialog({ template: null })} type="button"><Plus size={16} /> Create</button>
              </div>
              <div className="bi-template-grid">
                {activeTemplates.map((template) => {
                  const selected = template.id === selectedTemplateId;
                  return (
                    <article className={`bi-template-card ${selected ? "is-selected" : ""}`} key={template.id}>
                      <button aria-label={`Use ${template.title}`} aria-pressed={selected} className="bi-template-select" disabled={inputLocked} onClick={() => chooseTemplate(template)} type="button">
                        <span className="bi-template-check">{selected && <Check aria-hidden="true" size={15} />}</span>
                        <span className="bi-template-body">
                          <span className="bi-template-kind">{template.documentType === "credit_card_bill" && <CreditCard size={14} />}{documentTypeLabel(template.documentType)}</span>
                          <strong>{template.title}</strong>
                          <small>{template.accountIds.length} default Account{template.accountIds.length === 1 ? "" : "s"} · {template.lastUsed}</small>
                        </span>
                      </button>
                      <div className="bi-template-actions">
                        <button aria-label={`Edit ${template.title}`} className="bi-icon-button" disabled={inputLocked} onClick={() => setTemplateDialog({ template })} type="button"><Pencil size={15} /></button>
                        <button aria-label={`Archive ${template.title}`} className="bi-icon-button" disabled={inputLocked} onClick={() => setArchiveTarget(template)} type="button"><Archive size={15} /></button>
                      </div>
                    </article>
                  );
                })}
                {activeTemplates.length === 0 && <div className="bi-template-empty"><ArchiveRestore size={20} /><div><strong>No active templates</strong><p>Create a new template or restore one from the archive.</p></div><button className="button button-secondary bi-compact" onClick={() => setTemplateDialog({ template: null })} type="button">Create template</button></div>}
              </div>
              {archivedTemplates.length > 0 && (
                <div className="bi-archive-row">
                  <button aria-expanded={showArchived} className="bi-text-button" onClick={() => setShowArchived((value) => !value)} type="button"><Archive size={15} /> {showArchived ? "Hide" : "Show"} archived templates ({archivedTemplates.length})</button>
                  {showArchived && <div className="bi-archived-list">{archivedTemplates.map((template) => <div key={template.id}><span><strong>{template.title}</strong><small>{documentTypeLabel(template.documentType)} · {template.lastUsed}</small></span><button className="button button-secondary bi-compact" disabled={busy} onClick={() => void toggleArchived(template)} type="button"><ArchiveRestore size={15} /> Restore</button></div>)}</div>}
                </div>
              )}
            </article>

            <article className="bi-panel">
              <div className="bi-section-heading">
                <span className="bi-step">2</span>
                <div><h2>Choose Accounts</h2><p>{isCardBill ? "This bill will belong to exactly one Credit Card Account." : "Override the template defaults for this batch only."}</p></div>
                <span className="bi-local-badge">This batch only</span>
              </div>
              <div className="bi-batch-accounts">
                {batchAccountChoices.map((account) => {
                  const checked = batchAccountIds.includes(account.id);
                  return (
                    <label className={`bi-batch-account ${checked ? "is-selected" : ""}`} key={account.id}>
                      <input checked={checked} disabled={inputLocked} name={isCardBill ? "batch-credit-card" : undefined} onChange={() => toggleBatchAccount(account.id)} type={isCardBill ? "radio" : "checkbox"} />
                      <span className="bi-account-mark">{checked && <Check size={14} />}</span>
                      <span className="bi-account-logo">{account.institution_name.slice(0, 1)}</span>
                      <span><strong>{account.name}</strong><small>{account.institution_name} · {account.account_type.replaceAll("_", " ")}</small></span>
                    </label>
                  );
                })}
              </div>
              {selectedTemplate && !inputLocked && JSON.stringify(batchAccountIds) !== JSON.stringify(selectedTemplate.accountIds) && (
                <button className="bi-text-button bi-reset-accounts" onClick={() => setBatchAccountIds(selectedTemplate.accountIds)} type="button"><RotateCcw size={14} /> Reset to template defaults</button>
              )}
            </article>

            <article className="bi-panel">
              <div className="bi-section-heading">
                <span className="bi-step">3</span>
                <div><h2>Add documents</h2><p>PDFs stay separate. Related images can be grouped and ordered.</p></div>
                <span className="bi-file-count">{totalFiles} / 20 files</span>
              </div>
              <input accept=".pdf,.bmp,.jpg,.jpeg,.png,.tif,.tiff,.webp,.heic" aria-label="Choose PDF or image documents to import" className="sr-only" disabled={inputLocked} multiple onChange={(event: ChangeEvent<HTMLInputElement>) => { if (event.target.files) addFiles(event.target.files); event.target.value = ""; }} ref={fileInputRef} type="file" />
              <div
                className={`bi-dropzone ${dragging ? "is-dragging" : ""} ${inputLocked ? "is-disabled" : ""}`}
                onDragEnter={(event: DragEvent<HTMLDivElement>) => { event.preventDefault(); if (!inputLocked) setDragging(true); }}
                onDragLeave={(event: DragEvent<HTMLDivElement>) => { event.preventDefault(); if (!event.currentTarget.contains(event.relatedTarget as Node)) setDragging(false); }}
                onDragOver={(event: DragEvent<HTMLDivElement>) => event.preventDefault()}
                onDrop={(event: DragEvent<HTMLDivElement>) => { event.preventDefault(); setDragging(false); addFiles(event.dataTransfer.files); }}
              >
                <span className="bi-upload-icon"><Upload aria-hidden="true" size={23} /></span>
                <div><strong>Drop PDFs or images here</strong><p>5 MB each · 50 MB per batch · up to 50 pages per document</p></div>
                <button className="button button-secondary" disabled={inputLocked} onClick={() => fileInputRef.current?.click()} type="button">Choose files</button>
              </div>
              {fileErrors.length > 0 && <div className="bi-inline-alert is-error" role="alert"><CircleAlert size={18} /><div><strong>Check this draft</strong>{fileErrors.map((error) => <p key={error}>{error}</p>)}</div><button aria-label="Dismiss file errors" className="bi-icon-button" onClick={() => setFileErrors([])} type="button"><X size={16} /></button></div>}
              {duplicateRequired && (
                <div className="bi-inline-alert is-warning">
                  <CircleAlert size={19} />
                  <div><strong>A file may have appeared in an earlier batch</strong><p>Remove it, or intentionally continue with this upload.</p></div>
                  <label className="bi-confirm-check"><input checked={duplicateConfirmed} disabled={Boolean(isBatchActive)} onChange={(event) => setDuplicateConfirmed(event.target.checked)} type="checkbox" /><span>{duplicateConfirmed ? <Check size={14} /> : null}</span>Import anyway</label>
                </div>
              )}
              {uploadRecovery && (
                <div className="bi-inline-alert is-warning" role="status">
                  <RotateCcw size={19} />
                  <div><strong>Upload paused safely</strong><p>{uploadRecovery.finalizedDraftFileIds.length} of {totalFiles} files are finalized. Resume with the same files, or delete the draft batch below to clean up.</p></div>
                </div>
              )}

              {groups.length > 0 ? (
                <div className="bi-document-area">
                  <div className="bi-document-toolbar"><div><strong>{groups.length} document group{groups.length === 1 ? "" : "s"}</strong><span>{formatFileSize(totalBytes)} total</span></div><button className="button button-secondary bi-compact" disabled={inputLocked} onClick={groupImages} type="button"><FolderPlus size={16} /> Group loose images</button></div>
                  <div className="bi-group-list">
                    {groups.map((group, groupIndex) => (
                      <article className="bi-document-group" key={group.id}>
                        <header>
                          <span className="bi-group-number">{String(groupIndex + 1).padStart(2, "0")}</span>
                          <div><strong>{group.label}</strong><small>{group.files.length} file{group.files.length === 1 ? "" : "s"} · processed independently</small></div>
                          <div className="bi-reorder-actions">
                            <button aria-label={`Move document ${groupIndex + 1} up`} className="bi-icon-button" disabled={inputLocked || groupIndex === 0} onClick={() => moveGroup(groupIndex, -1)} type="button"><ArrowUp size={15} /></button>
                            <button aria-label={`Move document ${groupIndex + 1} down`} className="bi-icon-button" disabled={inputLocked || groupIndex === groups.length - 1} onClick={() => moveGroup(groupIndex, 1)} type="button"><ArrowDown size={15} /></button>
                          </div>
                        </header>
                        <div className="bi-file-list">
                          {group.files.map((file, fileIndex) => (
                            <div className="bi-file-row" key={file.id}>
                              <span className={`bi-file-preview is-${file.previewTone}`}>{file.kind === "pdf" ? <FileText size={19} /> : <FileImage size={19} />}<small>{file.kind === "pdf" ? "PDF" : fileIndex + 1}</small></span>
                              <span className="bi-file-name"><strong>{file.name}</strong><small>{formatFileSize(file.size)} · {file.kind === "pdf" ? "pages remain in PDF order" : `image ${fileIndex + 1} of ${group.files.length}`}</small></span>
                              {file.duplicate && <span className="bi-duplicate-tag">Seen before</span>}
                              {group.files.length > 1 && <div className="bi-reorder-actions"><button aria-label={`Move ${file.name} earlier`} className="bi-icon-button" disabled={inputLocked || fileIndex === 0} onClick={() => moveFile(group.id, fileIndex, -1)} type="button"><ArrowUp size={14} /></button><button aria-label={`Move ${file.name} later`} className="bi-icon-button" disabled={inputLocked || fileIndex === group.files.length - 1} onClick={() => moveFile(group.id, fileIndex, 1)} type="button"><ArrowDown size={14} /></button></div>}
                              <button aria-label={`Remove ${file.name}`} className="bi-icon-button is-danger" disabled={inputLocked} onClick={() => removeFile(group.id, file.id)} type="button"><Trash2 size={15} /></button>
                            </div>
                          ))}
                        </div>
                        {group.files.length > 1 && group.files.every((file) => file.kind === "image") && <button className="bi-text-button bi-ungroup" disabled={inputLocked} onClick={() => ungroupImages(group.id)} type="button">Ungroup into separate documents</button>}
                      </article>
                    ))}
                  </div>
                </div>
              ) : (
                <div className="bi-empty-inline"><FileImage size={22} /><div><strong>No documents yet</strong><p>Choose PDF or image files above to prepare this import.</p></div></div>
              )}
            </article>

            {currentBatch && <BatchProgress batch={currentBatch} cancel={() => void cancelBatch()} debug={(group) => void openDebug(group)} openBill={openCreditCardBill} openEvidence={(group) => void openEvidence(group)} openReview={(group) => void openReview(group, currentBatch.id)} remove={["draft", "cancelled"].includes(currentBatch.status) ? () => void removeBatch(currentBatch.id) : undefined} retry={(groupId) => void retryGroup(currentBatch.id, groupId)} />}
          </section>

          <aside className="bi-summary-column">
            <div className="bi-summary-card">
              <div className="bi-summary-title"><span><ShieldCheck size={19} /></span><div><p>Ready to process</p><h2>Batch summary</h2></div></div>
              <dl className="bi-summary-list">
                <div><dt>Template</dt><dd>{selectedTemplate?.title ?? "Not selected"}</dd></div>
                <div><dt>Document type</dt><dd>{selectedTemplate ? documentTypeLabel(selectedTemplate.documentType) : "—"}</dd></div>
                <div><dt>Accounts</dt><dd>{batchAccountIds.length ? `${batchAccountIds.length} selected` : "None selected"}</dd></div>
                <div><dt>Documents</dt><dd>{groups.length ? `${groups.length} groups · ${totalFiles} files` : "No files"}</dd></div>
              </dl>
              <div className="bi-summary-assurance"><Check size={15} /><p>The template and Account choices become an immutable snapshot when processing starts.</p></div>
              <button className="button button-primary bi-submit" disabled={Boolean(isBatchActive) || busy || (duplicateRequired && !duplicateConfirmed)} onClick={() => void startBatch()} type="button">{isBatchActive || busy ? <><LoaderCircle className="bi-spin" size={17} /> {busy ? "Uploading…" : "Processing batch"}</> : uploadRecovery ? <>Resume upload <RotateCcw size={17} /></> : <>Start processing <ArrowRight size={17} /></>}</button>
              <p className="bi-submit-note">Files upload privately and processing continues in the background.</p>
            </div>
            <div className="bi-side-note"><Sparkles size={18} /><div><strong>Evidence first</strong><p>Uploaded files remain the source of truth. Your template guides reading but cannot supply missing transaction facts.</p></div></div>
          </aside>
        </div>
      ) : (
        <section className="bi-history-layout">
          <div className="bi-history-toolbar">
            <div><h2>Batch history</h2><p>Return to processing, inspect evidence, and retry only failed document groups.</p></div>
            <div className="bi-history-controls">
              <label className="bi-search"><span className="sr-only">Search history</span><Search size={17} /><input onChange={(event) => setHistorySearch(event.target.value)} placeholder="Search batches" value={historySearch} /></label>
              <label className="bi-filter"><span className="sr-only">Filter batch status</span><select onChange={(event) => setHistoryFilter(event.target.value as HistoryFilter)} value={historyFilter}><option value="all">All statuses</option><option value="completed">Completed</option><option value="completed_with_errors">With errors</option><option value="processing">Processing</option><option value="cancelled">Cancelled</option><option value="failed">Failed</option></select><ChevronDown size={15} /></label>
            </div>
          </div>
          <div className="bi-history-grid">
            <div className="bi-batch-list">
              {filteredHistory.length ? filteredHistory.map((batch) => (
                <button aria-pressed={selectedBatch?.id === batch.id} className={`bi-batch-list-item ${selectedBatch?.id === batch.id ? "is-selected" : ""}`} key={batch.id} onClick={() => void selectHistoryBatch(batch.id)} type="button">
                  <span className="bi-batch-topline"><span className={`bi-status is-${batch.status}`}>{statusIcon(batch.status)}{statusLabel(batch.status)}</span><small>{batch.createdAt}</small></span>
                  <strong>{batch.name}</strong>
                  <span>{batch.templateTitle} · {batch.totalGroups} document{batch.totalGroups === 1 ? "" : "s"}</span>
                  <span className="bi-batch-counts"><b>{batch.candidates.created} new</b><b>{batch.candidates.matched} matched</b><b>{batch.candidates.review} review</b></span>
                </button>
              )) : <div className="bi-history-empty"><Search size={24} /><strong>No matching batches</strong><p>Try another search or clear the status filter.</p><button className="bi-text-button" onClick={() => { setHistorySearch(""); setHistoryFilter("all"); }} type="button">Clear filters</button></div>}
            </div>
            <div className="bi-history-detail">
              {selectedBatch ? <BatchProgress batch={selectedBatch} cancel={selectedBatch.id === currentBatch?.id ? () => void cancelBatch() : undefined} debug={(group) => void openDebug(group)} openBill={openCreditCardBill} openEvidence={(group) => void openEvidence(group)} openReview={(group) => void openReview(group, selectedBatch.id)} remove={["draft", "cancelled"].includes(selectedBatch.status) ? () => void removeBatch(selectedBatch.id) : undefined} retry={(groupId) => void retryGroup(selectedBatch.id, groupId)} standalone /> : <div className="bi-history-empty"><History size={26} /><strong>Select a batch</strong><p>Its document groups and candidate outcomes will appear here.</p></div>}
            </div>
          </div>
        </section>
      )}

      {templateDialog && <TemplateDialog accounts={accounts} close={() => setTemplateDialog(null)} save={saveTemplate} template={templateDialog.template} templates={templates} />}
      {archiveTarget && <Dialog close={() => setArchiveTarget(null)} descriptionId="bi-archive-description" titleId="bi-archive-title"><DialogHeader close={() => setArchiveTarget(null)} eyebrow="ARCHIVE TEMPLATE" title="Archive this template?" titleId="bi-archive-title" /><p className="bi-dialog-copy" id="bi-archive-description"><strong>{archiveTarget.title}</strong> will disappear from new imports. Existing batches keep their saved template snapshot, and you can restore it later.</p><footer className="bi-dialog-actions"><button className="button button-secondary" data-initial-focus onClick={() => setArchiveTarget(null)} type="button">Keep active</button><button className="button button-danger" disabled={busy} onClick={() => void toggleArchived(archiveTarget)} type="button"><Archive size={16} /> Archive template</button></footer></Dialog>}
      {evidenceGroup && <Dialog className="bi-evidence-dialog" close={() => setEvidenceGroup(null)} descriptionId="bi-evidence-description" titleId="bi-evidence-title"><DialogHeader close={() => setEvidenceGroup(null)} eyebrow="ORIGINAL EVIDENCE" title={evidenceGroup.name} titleId="bi-evidence-title" /><p className="bi-dialog-copy" id="bi-evidence-description">{evidenceGroup.fileSummary}</p><div className="bi-evidence-preview"><FileSearch size={34} /><strong>Private evidence</strong>{evidenceLoading ? <span>Creating short-lived links…</span> : evidenceError ? <><span role="alert">{evidenceError}</span><button className="button button-secondary bi-compact" onClick={() => void openEvidence(evidenceGroup)} type="button"><RefreshCw size={15} /> Retry</button></> : evidenceItems.length ? evidenceItems.map((item) => <a href={item.signed_url} key={item.id} rel="noreferrer" target="_blank">{item.filename} · {formatFileSize(item.byte_size)}</a>) : <span>No evidence files are available for this document.</span>}<p>Links are owner-checked, expire after five minutes, and are never cached by this page.</p></div><div className="bi-evidence-meta"><div><span>Parse attempt</span><strong>#{evidenceGroup.attempt}</strong></div><div><span>Result</span><strong>{evidenceGroup.status}</strong></div><div><span>Retention</span><strong>{evidenceGroup.bill ? "Bill protected" : "Eligible"}</strong></div></div>{evidenceGroup.bill && <p className="bi-retention-note"><ShieldCheck size={15} /> This evidence cannot be deleted while {evidenceGroup.bill.id} references it.</p>}<footer className="bi-dialog-actions">{!evidenceGroup.bill && ["completed", "failed", "cancelled"].includes(evidenceGroup.status) && <button className="button button-danger" onClick={() => { setDeleteTarget(evidenceGroup); setEvidenceGroup(null); }} type="button"><Trash2 size={16} /> Delete evidence</button>}<button className="button button-secondary" onClick={() => setEvidenceGroup(null)} type="button">Close</button></footer></Dialog>}
      {reviewGroup && <Dialog close={() => setReviewGroup(null)} descriptionId="bi-review-description" titleId="bi-review-title"><DialogHeader close={() => setReviewGroup(null)} eyebrow="CANDIDATE REVIEW" title={`${reviewCandidates.length} candidates need attention`} titleId="bi-review-title" /><p className="bi-dialog-copy" id="bi-review-description">Resolve only uncertain candidates from {reviewGroup.name}; safe transactions are already preserved.</p>{reviewCandidates.some((candidate) => !candidate.account_id) && <label className="bi-review-account">Account for unassigned candidates<select onChange={(event) => setReviewAccountId(event.target.value)} value={reviewAccountId}><option value="">Choose an Account</option>{accounts.map((account) => <option key={account.id} value={account.id}>{account.institution_name} · {account.name}</option>)}</select></label>}<div className="bi-review-list">{reviewCandidates.length ? reviewCandidates.map((candidate) => <article key={candidate.id}><div><strong>Candidate {candidate.ordinal}</strong><span>{JSON.stringify(candidate.parsed_candidate)}</span></div><span className="bi-review-reason">{candidate.reconciliation_reason ?? "Review required"}</span><button className="button button-secondary bi-compact" disabled={!candidate.account_id && !reviewAccountId} onClick={() => void resolveReviewCandidate(candidate)} type="button">{candidate.account_id ? "Create" : "Set Account"}</button></article>) : <p>No unresolved candidates remain for this document.</p>}</div><footer className="bi-dialog-actions"><button className="button button-secondary" onClick={() => setReviewGroup(null)} type="button">Close</button></footer></Dialog>}
      {debugGroup && <Dialog className="bi-evidence-dialog" close={() => setDebugGroup(null)} descriptionId="bi-debug-description" titleId="bi-debug-title"><DialogHeader close={() => setDebugGroup(null)} eyebrow="PARSE DEBUG" title={`${debugGroup.name} · attempt ${debugGroup.attempt}`} titleId="bi-debug-title" /><p className="bi-dialog-copy" id="bi-debug-description">Stored request metadata for each immutable parse attempt.</p><pre className="bi-debug-code">{JSON.stringify(debugPayload ?? { status: "Loading…" }, null, 2)}</pre><footer className="bi-dialog-actions"><button className="button button-secondary" onClick={() => setDebugGroup(null)} type="button">Close</button></footer></Dialog>}
      {promptPreviewPayload !== null && <Dialog className="bi-evidence-dialog" close={() => setPromptPreviewPayload(null)} descriptionId="bi-prompt-preview-description" titleId="bi-prompt-preview-title"><DialogHeader close={() => setPromptPreviewPayload(null)} eyebrow="LLM PROMPT PREVIEW" title={selectedTemplate?.title ?? "Prompt preview"} titleId="bi-prompt-preview-title" /><p className="bi-dialog-copy" id="bi-prompt-preview-description">This is the exact prompt envelope before dynamic document content is attached.</p><pre className="bi-debug-code">{JSON.stringify(promptPreviewPayload, null, 2)}</pre><footer className="bi-dialog-actions"><button className="button button-secondary" onClick={() => setPromptPreviewPayload(null)} type="button">Close</button></footer></Dialog>}
      {deleteTarget && <Dialog close={() => setDeleteTarget(null)} descriptionId="bi-delete-description" titleId="bi-delete-title"><DialogHeader close={() => setDeleteTarget(null)} eyebrow="DELETE EVIDENCE" title="Delete these uploaded files?" titleId="bi-delete-title" /><p className="bi-dialog-copy" id="bi-delete-description"><strong>{deleteTarget.fileSummary}</strong> and its parse history will be removed. Reconciled transactions remain protected.</p><footer className="bi-dialog-actions"><button className="button button-secondary" data-initial-focus onClick={() => setDeleteTarget(null)} type="button">Keep evidence</button><button className="button button-danger" onClick={() => void deleteEvidence(deleteTarget.id)} type="button"><Trash2 size={16} /> Delete permanently</button></footer></Dialog>}
      <p aria-live="polite" className="sr-only">{reorderAnnouncement}</p>
      {toast && <div aria-live="polite" className="bi-toast" role="status"><CheckCircle2 size={18} />{toast}</div>}
    </section>
  );
}

function BatchProgress({
  batch,
  cancel,
  retry,
  openEvidence,
  openBill,
  openReview,
  debug,
  remove,
  standalone = false,
}: {
  batch: ImportBatch;
  cancel?: () => void;
  retry: (groupId: string) => void;
  openEvidence: (group: BatchGroupResult) => void;
  openBill: (bill: NonNullable<BatchGroupResult["bill"]>) => void;
  openReview: (group: BatchGroupResult) => void;
  debug: (group: BatchGroupResult) => void;
  remove?: () => void;
  standalone?: boolean;
}) {
  const active = ["queued", "processing", "cancel_requested"].includes(batch.status);
  return (
    <article aria-live="polite" className={`bi-progress-panel ${standalone ? "is-standalone" : ""}`}>
      <header className="bi-progress-header">
        <div><p className="bi-kicker">{batch.id}</p><h2>{batch.name}</h2><span>{batch.templateTitle} · {batch.accountNames.join(", ")}</span></div>
        <span className={`bi-status is-${batch.status}`}>{statusIcon(batch.status)}{statusLabel(batch.status)}</span>
      </header>
      <div className="bi-progress-overview">
        <div className="bi-progress-copy"><strong>{active ? `${batch.processedGroups} of ${batch.totalGroups} document groups finished` : `${batch.totalGroups} document groups · ${totalCandidates(batch.candidates)} candidates`}</strong><span>{batch.status === "cancel_requested" ? "Active work will stop at the next safe boundary." : batch.status === "processing" ? "You can leave this page while processing continues." : batch.status === "cancelled" ? "Completed results remain intact." : "Candidate reconciliation summary"}</span></div>
        <div aria-label={`${batch.progress}% complete`} aria-valuemax={100} aria-valuemin={0} aria-valuenow={batch.progress} className="bi-progress-track" role="progressbar"><span style={{ width: `${batch.progress}%` }} /></div>
        {active && cancel && <button className="button button-secondary bi-cancel" disabled={batch.status === "cancel_requested"} onClick={cancel} type="button">{batch.status === "cancel_requested" ? "Cancellation requested" : "Cancel queued work"}</button>}
        {!active && remove && <button className="button button-secondary bi-cancel" onClick={remove} type="button"><Trash2 size={15} /> Delete batch</button>}
      </div>
      <div className="bi-result-list">
        {batch.groups.map((group, index) => (
          <article className={`bi-result-card is-${group.status}`} key={group.id}>
            <div className="bi-result-marker">{group.status === "processing" ? <LoaderCircle className="bi-spin" size={18} /> : group.status === "completed" ? <Check size={18} /> : group.status === "failed" ? <CircleAlert size={18} /> : group.status === "cancelled" ? <X size={18} /> : <Clock3 size={18} />}</div>
            <div className="bi-result-main">
              <div className="bi-result-heading"><div><small>Document {index + 1} · attempt {group.attempt}</small><h3>{group.name}</h3><p>{group.fileSummary}</p></div><span className={`bi-group-status is-${group.status}`}>{group.status}</span></div>
              <p className="bi-result-message">{group.message}</p>
              <CandidateSummary group={group} />
              {group.bill && <BillResultCard bill={group.bill} openBill={() => openBill(group.bill!)} />}
            </div>
            <div className="bi-result-actions">
              <button className="button button-secondary bi-compact" disabled={group.evidenceDeleted} onClick={() => openEvidence(group)} type="button"><FileSearch size={15} /> {group.evidenceDeleted ? "Evidence deleted" : "Evidence"}</button>
              <button className="button button-secondary bi-compact" disabled={group.evidenceDeleted} onClick={() => debug(group)} type="button">Debug</button>
              {group.candidates.review > 0 && <button className="button button-secondary bi-compact" onClick={() => openReview(group)} type="button"><CircleAlert size={15} /> Review {group.candidates.review}</button>}
              {group.status === "failed" && <button className="button button-primary bi-compact" onClick={() => retry(group.id)} type="button"><RefreshCw size={15} /> Retry group</button>}
            </div>
          </article>
        ))}
      </div>
    </article>
  );
}

export default BulkImportPage;
