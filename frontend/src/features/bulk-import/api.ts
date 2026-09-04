import type { Session } from "@supabase/supabase-js";

const apiBaseUrl = (import.meta.env.VITE_API_BASE_URL ?? "/api").replace(/\/$/, "");

export type BulkDocumentType = "physical_receipt" | "invoice" | "e_wallet_history" | "bank_statement" | "credit_card_bill" | "transaction_confirmation" | "other";
export type BulkBatchStatus = "draft" | "queued" | "running" | "cancelling" | "completed" | "completed_with_errors" | "failed" | "cancelled";
export type BulkDocumentStatus = "draft" | "queued" | "preparing" | "parsing" | "aggregating" | "reconciling" | "completed" | "completed_with_errors" | "failed" | "cancelled";

export interface BulkAccountDto { account_id: string; account_ref?: string; name?: string; institution_name?: string; account_type?: string; sort_order: number }
export interface BulkTemplateDto { id: string; title: string; document_type: BulkDocumentType; parsing_prompt: string; version: number; archived_at: string | null; accounts: BulkAccountDto[]; created_at: string; updated_at: string }
export interface BulkFileDto { id: string; document_id: string; sort_order: number; display_filename: string; declared_mime_type: string; declared_byte_size: number; declared_sha256: string; status: "reserved" | "uploaded" | "verified" | "failed" | "cleanup_pending"; reservation_expires_at: string; finalized_at: string | null }
export interface BulkSpecializedResultDto { kind: string; resource_id: string; path: string }
export interface BulkDocumentDto { id: string; batch_id: string; data_source_id: string | null; sort_order: number; display_label: string; status: BulkDocumentStatus; attempt_generation: number; page_count: number; candidate_count: number; created_count: number; attached_count: number; review_count: number; failed_count: number; duplicate_count: number; files: BulkFileDto[]; document_summary?: unknown; specialized_result?: BulkSpecializedResultDto | null }
export interface BulkCountersDto { files: number; documents: number; pages: number; parsed_candidates: number; created: number; attached: number; review: number; failed: number; duplicates: number }
export interface BulkBatchDto { id: string; template_id: string | null; template_version: number; title_snapshot: string; document_type_snapshot: BulkDocumentType; parsing_prompt_snapshot: string; status: BulkBatchStatus; accounts: BulkAccountDto[]; documents?: BulkDocumentDto[]; counters: BulkCountersDto; cancel_requested_at: string | null; error_summary: string | null; started_at: string | null; completed_at: string | null; created_at: string; updated_at: string }
export interface BulkCandidateDto { id: string; batch_id: string; document_id: string; attempt_generation: number; ordinal: number; fingerprint: string; parsed_candidate: unknown; account_id: string | null; status: "pending_reconciliation" | "created" | "attached" | "review_required" | "duplicate" | "failed" | "cancelled" | "superseded"; transaction_id: string | null; duplicate_of_candidate_id: string | null; reconciliation_reason: string | null }
export interface BulkReservationDto { file: BulkFileDto; upload_url: string; method: string; headers: Record<string, string> }
export interface BulkPromptPreviewDto { system_prompt: string; request: unknown }
export interface BulkDebugAttemptDto { id: string; chunk_id: string; chunk_index: number; attempt_generation: number; model_name: string | null; status: string; request_metadata: unknown; parsed_candidate: unknown; assembled_system_prompt: string | null; normalized_input: string | null; provider_request: string | null; provider_response: string | null; model_output: string | null; prompt_components: unknown; error_summary: string | null; started_at: string | null; completed_at: string | null; created_at: string; truncated_fields: string[] }
export interface BulkDebugFieldDto { source_id: string; attempt_id: string; field: string; value: string | null; max_bytes: number }
export interface BulkEvidenceItemDto { id: string; filename: string; mime_type: string; byte_size: number; sha256: string; signed_url: string }

export class BulkApiError extends Error {
  readonly status: number;
  readonly code: string | null;
  constructor(message: string, status: number, code: string | null = null) { super(message); this.name = "BulkApiError"; this.status = status; this.code = code; }
}

async function api<T>(session: Session, path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`${apiBaseUrl}${path}`, { ...init, headers: { Accept: "application/json", Authorization: `Bearer ${session.access_token}`, ...init.headers } });
  const body: unknown = response.status === 204 ? null : await response.json().catch(() => null);
  if (!response.ok) {
    const value = body && typeof body === "object" ? body as { error?: string | { message?: string; code?: string }; message?: string; code?: string } : null;
    const message = typeof value?.error === "string" ? value.error : typeof value?.error === "object" && typeof value.error.message === "string" ? value.error.message : value?.message ?? "The request could not be completed.";
    const code = typeof value?.code === "string" ? value.code : typeof value?.error === "object" && typeof value.error.code === "string" ? value.error.code : null;
    throw new BulkApiError(message, response.status, code);
  }
  return body as T;
}

const json = (method: string, body: object): RequestInit => ({ method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
const root = "/v1/transactions/bulk-import";

export async function listTemplates(session: Session, includeArchived: boolean, signal?: AbortSignal): Promise<BulkTemplateDto[]> { return (await api<{ items: BulkTemplateDto[] }>(session, `${root}/templates?include_archived=${includeArchived}`, { signal })).items; }
export async function createTemplate(session: Session, input: { title: string; document_type: BulkDocumentType; parsing_prompt: string; account_ids: string[] }): Promise<BulkTemplateDto> { return api(session, `${root}/templates`, json("POST", input)); }
export async function updateTemplate(session: Session, template: BulkTemplateDto, input: { title: string; document_type: BulkDocumentType; parsing_prompt: string; account_ids: string[] }): Promise<BulkTemplateDto> { return api(session, `${root}/templates/${encodeURIComponent(template.id)}`, json("PATCH", { ...input, expected_version: template.version })); }
export async function setTemplateArchived(session: Session, id: string, archived: boolean): Promise<BulkTemplateDto> { return api(session, `${root}/templates/${encodeURIComponent(id)}/${archived ? "archive" : "restore"}`, { method: "POST" }); }
export async function createBatch(session: Session, templateId: string, accountIds: string[]): Promise<BulkBatchDto> { return api(session, `${root}/batches`, json("POST", { template_id: templateId, account_ids: accountIds })); }
export async function listBatches(session: Session, signal?: AbortSignal): Promise<BulkBatchDto[]> {
  const items: BulkBatchDto[] = [];
  const seenCursors = new Set<string>();
  let cursor: string | null = null;
  for (let pageNumber = 0; pageNumber < 20; pageNumber += 1) {
    const suffix: string = cursor ? `&cursor=${encodeURIComponent(cursor)}` : "";
    const page: { items: BulkBatchDto[]; next_cursor: string | null } = await api(session, `${root}/batches?limit=100${suffix}`, { signal });
    items.push(...page.items);
    if (!page.next_cursor) return items;
    if (seenCursors.has(page.next_cursor)) throw new BulkApiError("Bulk Import history returned a repeated cursor.", 502);
    seenCursors.add(page.next_cursor);
    cursor = page.next_cursor;
  }
  throw new BulkApiError("Bulk Import history is larger than the supported 2,000-batch view.", 422);
}
export async function getBatch(session: Session, id: string, signal?: AbortSignal): Promise<BulkBatchDto> { return api(session, `${root}/batches/${encodeURIComponent(id)}`, { signal }); }
export async function deleteBatch(session: Session, id: string): Promise<void> { await api(session, `${root}/batches/${encodeURIComponent(id)}`, { method: "DELETE" }); }
export async function reserveFile(session: Session, batchId: string, input: { filename: string; mime_type: string; byte_size: number; sha256: string; intentional_duplicate: boolean }): Promise<BulkReservationDto> { return api(session, `${root}/batches/${encodeURIComponent(batchId)}/files/reservations`, json("POST", input)); }
export async function uploadReservedFile(reservation: BulkReservationDto, file: File): Promise<void> {
  const target = new URL(reservation.upload_url);
  const localDevelopment = import.meta.env.DEV && target.protocol === "http:" && ["localhost", "127.0.0.1", "::1"].includes(target.hostname);
  if (target.protocol !== "https:" && !localDevelopment) throw new BulkApiError("The upload service returned an unsafe URL.", 502);
  if ((reservation.method || "PUT").toUpperCase() !== "PUT") throw new BulkApiError("The upload service returned an unsupported method.", 502);
  const response = await fetch(target, { method: "PUT", headers: reservation.headers, body: file });
  if (!response.ok) throw new BulkApiError("The file upload could not be completed.", response.status);
}
export async function finalizeFile(session: Session, batchId: string, fileId: string): Promise<BulkFileDto> { return api(session, `${root}/batches/${encodeURIComponent(batchId)}/files/${encodeURIComponent(fileId)}/finalize`, { method: "POST" }); }
export async function replaceDocumentLayout(session: Session, batchId: string, documents: { id: string; label: string; file_ids: string[] }[]): Promise<BulkBatchDto> { return api(session, `${root}/batches/${encodeURIComponent(batchId)}/documents`, json("PUT", { documents })); }
export async function submitBatch(session: Session, id: string): Promise<BulkBatchDto> { return api(session, `${root}/batches/${encodeURIComponent(id)}/submit`, { method: "POST" }); }
export async function cancelBatch(session: Session, id: string): Promise<BulkBatchDto> { return api(session, `${root}/batches/${encodeURIComponent(id)}/cancel`, { method: "POST" }); }
export async function retryDocument(session: Session, id: string): Promise<BulkDocumentDto> { return api(session, `${root}/documents/${encodeURIComponent(id)}/retry`, { method: "POST" }); }
export async function deleteDocument(session: Session, id: string): Promise<void> { await api(session, `${root}/documents/${encodeURIComponent(id)}`, { method: "DELETE" }); }
export async function listCandidates(session: Session, batchId: string, signal?: AbortSignal): Promise<BulkCandidateDto[]> { return (await api<{ items: BulkCandidateDto[] }>(session, `${root}/batches/${encodeURIComponent(batchId)}/candidates`, { signal })).items; }
export async function resolveCandidate(session: Session, candidateId: string, input: { action: "set_account" | "attach" | "create" | "internal_transfer"; account_id?: string; transaction_id?: string; debit_account_id?: string; credit_account_id?: string; category_id?: string; expected_generation: number }): Promise<BulkCandidateDto> { return api(session, `${root}/candidates/${encodeURIComponent(candidateId)}/resolve`, json("POST", input)); }
export async function previewPrompt(session: Session, templateId: string, accountIds: string[]): Promise<BulkPromptPreviewDto> { return api(session, `${root}/prompt-preview`, json("POST", { template_id: templateId, account_ids: accountIds })); }
export async function listDebugAttempts(session: Session, sourceId: string, signal?: AbortSignal): Promise<BulkDebugAttemptDto[]> { return (await api<{ items: BulkDebugAttemptDto[] }>(session, `/v1/transactions/sources/${encodeURIComponent(sourceId)}/debug/bulk-attempts`, { signal })).items; }
export async function getDebugAttemptField(session: Session, sourceId: string, attemptId: string, field: string, signal?: AbortSignal): Promise<BulkDebugFieldDto> { return api(session, `/v1/transactions/sources/${encodeURIComponent(sourceId)}/debug/bulk-attempts/${encodeURIComponent(attemptId)}/fields/${encodeURIComponent(field)}`, { signal }); }
export async function getDocumentEvidence(session: Session, documentId: string, signal?: AbortSignal): Promise<BulkEvidenceItemDto[]> {
  const response = await api<{ document_id: string; items: BulkEvidenceItemDto[] }>(session, `/v1/bulk-import/documents/${encodeURIComponent(documentId)}`, { signal });
  return response.items.map((item) => {
    const target = new URL(item.signed_url);
    const localDevelopment = import.meta.env.DEV && target.protocol === "http:" && ["localhost", "127.0.0.1", "::1"].includes(target.hostname);
    if (target.protocol !== "https:" && !localDevelopment) throw new BulkApiError("The evidence service returned an unsafe URL.", 502);
    return item;
  });
}

export async function sha256Hex(file: File): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", await file.arrayBuffer());
  return [...new Uint8Array(digest)].map((value) => value.toString(16).padStart(2, "0")).join("");
}
