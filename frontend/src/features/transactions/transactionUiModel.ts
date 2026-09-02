import type {
  InternalTransferSourceSeed,
  TransactionListItem,
} from "./model";

export function mergeCandidateOptions(
  items: TransactionListItem[],
  recommended: TransactionListItem | null,
): TransactionListItem[] {
  if (!recommended) return items;
  return [recommended, ...items.filter(({ id }) => id !== recommended.id)];
}

export function sourceIDsForTransferLeg(
  source: InternalTransferSourceSeed | undefined,
  leg: "debit" | "credit",
): string[] {
  return source && (source.role === leg || source.role === "both") ? [source.id] : [];
}
