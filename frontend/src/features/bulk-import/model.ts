export type DocumentType =
  | "physical_receipt"
  | "invoice"
  | "e_wallet_history"
  | "bank_statement"
  | "credit_card_bill"
  | "transaction_confirmation"
  | "other";

export interface ImportTemplate {
  id: string;
  title: string;
  documentType: DocumentType;
  prompt: string;
  accountIds: string[];
  archived: boolean;
  lastUsed: string;
}

export interface DraftFile {
  id: string;
  name: string;
  mimeType: string;
  size: number;
  kind: "pdf" | "image";
  duplicate: boolean;
  previewTone: "orange" | "blue" | "green" | "violet";
  raw?: File;
}

export interface DocumentGroup { id: string; label: string; files: DraftFile[] }
export type BatchStatus = "draft" | "queued" | "processing" | "cancel_requested" | "completed" | "completed_with_errors" | "failed" | "cancelled";
export type GroupStatus = "queued" | "processing" | "completed" | "failed" | "cancelled";
export interface CandidateCounts { created: number; matched: number; review: number; failed: number }
export interface BillResult { id: string; accountId?: string; cardName: string; statementPeriod: string; dueDate: string; amountDue: string; status: "Paid" | "Unpaid" | "Review" }
export interface BatchGroupResult { id: string; name: string; fileSummary: string; status: GroupStatus; candidates: CandidateCounts; message: string; attempt: number; evidenceDeleted?: boolean; dataSourceId?: string; bill?: BillResult }
export interface ImportBatch { id: string; name: string; createdAt: string; templateTitle: string; documentType: DocumentType; accountIds: string[]; accountNames: string[]; status: BatchStatus; progress: number; processedGroups: number; totalGroups: number; candidates: CandidateCounts; groups: BatchGroupResult[] }

export const documentTypeOptions: Array<{ value: DocumentType; label: string }> = [
  { value: "physical_receipt", label: "Physical receipt" },
  { value: "invoice", label: "Invoice" },
  { value: "e_wallet_history", label: "E-wallet history" },
  { value: "bank_statement", label: "Bank statement" },
  { value: "credit_card_bill", label: "Credit Card bill" },
  { value: "transaction_confirmation", label: "Transaction confirmation" },
  { value: "other", label: "Other" },
];

export function documentTypeLabel(value: DocumentType): string { return documentTypeOptions.find((option) => option.value === value)?.label ?? value; }
export function formatFileSize(bytes: number): string { return bytes < 1_000_000 ? `${Math.max(1, Math.round(bytes / 1_000))} KB` : `${(bytes / 1_000_000).toFixed(1)} MB`; }
export function totalCandidates(counts: CandidateCounts): number { return counts.created + counts.matched + counts.review + counts.failed; }
