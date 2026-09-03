import {
  isISO4217Currency,
  majorAmountToMinor,
  toDateTimeLocal,
  toRFC3339,
  type ManualTransactionInput,
  type OwnedAccountOption,
  type TransactionCategory,
  type TransactionKind,
  type TransactionLineItem,
} from "./model";
import { parseLineItemDrafts, type LineItemDraft } from "./transactionFormModel";

export interface ManualTransactionDraft {
  accountId: string;
  kind: TransactionKind;
  title: string;
  merchantName: string;
  occurredAt: string;
  originalAmount: string;
  originalCurrency: string;
  sgdAmount: string;
  categoryId: string;
  lineItems: LineItemDraft[];
  userNotes: string;
}

export interface ManualTransactionInsertPayload {
  user_id: string;
  account_id: string;
  transaction_kind: TransactionKind;
  title: string;
  merchant_name: string | null;
  original_amount_minor: string;
  original_currency: string;
  sgd_amount_minor: string | null;
  occurred_at: string;
  category_id: string | null;
  line_items: TransactionLineItem[];
  review_status: "confirmed";
  details?: { user_notes: string };
}

export function emptyManualTransactionDraft(now = new Date()): ManualTransactionDraft {
  return {
    accountId: "",
    kind: "debit",
    title: "",
    merchantName: "",
    occurredAt: toDateTimeLocal(now.toISOString()),
    originalAmount: "",
    originalCurrency: "SGD",
    sgdAmount: "",
    categoryId: "",
    lineItems: [],
    userNotes: "",
  };
}

export function validateManualTransactionDraft(
  draft: ManualTransactionDraft,
  accounts: OwnedAccountOption[],
  categories: TransactionCategory[],
): { input: ManualTransactionInput | null; error: string | null } {
  const title = draft.title.trim();
  const merchantName = draft.merchantName.trim();
  const currency = draft.originalCurrency.trim().toUpperCase();
  const occurredAt = toRFC3339(draft.occurredAt);
  if (!accounts.some(({ id }) => id === draft.accountId)) {
    return { input: null, error: "Choose an active account." };
  }
  if (draft.kind !== "debit" && draft.kind !== "credit") {
    return { input: null, error: "Choose debit or credit." };
  }
  if (!title || title.length > 250) {
    return { input: null, error: "Title must contain 1 to 250 characters." };
  }
  if (merchantName.length > 250) {
    return { input: null, error: "Merchant or payee must contain at most 250 characters." };
  }
  if (!occurredAt) {
    return { input: null, error: "Enter a valid transaction date and time." };
  }
  if (!isISO4217Currency(currency)) {
    return { input: null, error: "Original currency must be an ISO 4217 code." };
  }
  let originalAmountMinor: string;
  try {
    originalAmountMinor = majorAmountToMinor(draft.originalAmount, currency);
  } catch (error) {
    const reason = error instanceof Error ? error.message : "Enter a valid amount.";
    return { input: null, error: `Original amount: ${reason}` };
  }
  let sgdAmountMinor: string | null = null;
  if (currency === "SGD") {
    sgdAmountMinor = originalAmountMinor;
  } else if (draft.sgdAmount.trim()) {
    try {
      sgdAmountMinor = majorAmountToMinor(draft.sgdAmount, "SGD");
    } catch (error) {
      const reason = error instanceof Error ? error.message : "Enter a valid amount.";
      return { input: null, error: `SGD amount: ${reason}` };
    }
  }
  if (
    draft.categoryId &&
    !categories.some(({ id }) => id === draft.categoryId)
  ) {
    return {
      input: null,
      error: "Choose an active category or leave the transaction uncategorized.",
    };
  }
  const parsedLines = parseLineItemDrafts(draft.lineItems);
  if (parsedLines.error) return { input: null, error: parsedLines.error };
  const userNotes = draft.userNotes.trim();
  if ([...userNotes].length > 4000) {
    return { input: null, error: "User notes must contain at most 4,000 characters." };
  }
  return {
    input: {
      account_id: draft.accountId,
      transaction_kind: draft.kind,
      title,
      merchant_name: merchantName || null,
      original_amount_minor: originalAmountMinor,
      original_currency: currency,
      sgd_amount_minor: sgdAmountMinor,
      occurred_at: occurredAt,
      category_id: draft.categoryId || null,
      line_items: parsedLines.items,
      user_notes: userNotes || null,
    },
    error: null,
  };
}

export function buildManualTransactionInsertPayload(
  userId: string,
  input: ManualTransactionInput,
): ManualTransactionInsertPayload {
  const payload: ManualTransactionInsertPayload = {
    user_id: userId,
    account_id: input.account_id,
    transaction_kind: input.transaction_kind,
    title: input.title,
    merchant_name: input.merchant_name,
    original_amount_minor: input.original_amount_minor,
    original_currency: input.original_currency,
    sgd_amount_minor: input.sgd_amount_minor,
    occurred_at: input.occurred_at,
    category_id: input.category_id,
    line_items: input.line_items,
    review_status: "confirmed",
  };
  if (input.user_notes) payload.details = { user_notes: input.user_notes };
  return payload;
}

export function buildManualDuplicatePreflightParams(
  input: ManualTransactionInput,
): URLSearchParams {
  const occurredAt = new Date(input.occurred_at);
  if (Number.isNaN(occurredAt.getTime())) {
    throw new Error("The transaction time is invalid.");
  }
  const params = new URLSearchParams({
    account_id: `eq.${input.account_id}`,
    transaction_kind: `eq.${input.transaction_kind}`,
    original_amount_minor: `eq.${input.original_amount_minor}`,
    original_currency: `eq.${input.original_currency}`,
    occurred_at: `gte.${new Date(occurredAt.getTime() - 10 * 60 * 1000).toISOString()}`,
    order: "occurred_at.desc",
    limit: "10",
  });
  params.append(
    "occurred_at",
    `lte.${new Date(occurredAt.getTime() + 10 * 60 * 1000).toISOString()}`,
  );
  return params;
}
