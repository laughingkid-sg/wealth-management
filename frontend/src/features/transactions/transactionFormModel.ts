import {
  isISO4217Currency,
  type JsonValue,
  type TransactionLineItem,
} from "./model";

export interface LineItemDraft {
  key: string;
  description: string;
  quantity: string;
  unit_price_minor: string;
  line_total_minor: string;
  tax_minor: string;
  discount_minor: string;
  currency: string;
  details: string;
}

let nextLineItemKey = 0;

export function lineItemKey(): string {
  nextLineItemKey += 1;
  return `line-item-${nextLineItemKey}`;
}

export function lineItemsToDrafts(items: TransactionLineItem[]): LineItemDraft[] {
  return items.map((item) => ({
    key: lineItemKey(),
    description: item.description,
    quantity: String(item.quantity),
    unit_price_minor: item.unit_price_minor ?? "",
    line_total_minor: item.line_total_minor ?? "",
    tax_minor: item.tax_minor ?? "",
    discount_minor: item.discount_minor ?? "",
    currency: item.currency,
    details: JSON.stringify(item.details, null, 2),
  }));
}

function isFiniteJsonValueInternal(value: unknown, ancestors: Set<object>): value is JsonValue {
  if (
    value === null ||
    typeof value === "string" ||
    typeof value === "boolean"
  ) {
    return true;
  }
  if (typeof value === "number") return Number.isFinite(value);
  if (typeof value !== "object") return false;
  if (ancestors.has(value)) return false;
  ancestors.add(value);
  let valid = false;
  if (Array.isArray(value)) {
    valid = true;
    for (let index = 0; index < value.length; index += 1) {
      if (
        !Object.prototype.hasOwnProperty.call(value, index) ||
        !isFiniteJsonValueInternal(value[index], ancestors)
      ) {
        valid = false;
        break;
      }
    }
  } else if (
    Object.getPrototypeOf(value) === Object.prototype ||
    Object.getPrototypeOf(value) === null
  ) {
    const keys = Reflect.ownKeys(value);
    valid = keys.every(
      (key) => typeof key === "string" &&
        Object.prototype.propertyIsEnumerable.call(value, key) &&
        isFiniteJsonValueInternal((value as Record<string, unknown>)[key], ancestors),
    );
  }
  ancestors.delete(value);
  return valid;
}

export function isFiniteJsonValue(value: unknown): value is JsonValue {
  return isFiniteJsonValueInternal(value, new Set<object>());
}

export function parseLineItemDrafts(
  drafts: LineItemDraft[],
): { items: TransactionLineItem[]; error: string | null } {
  const items: TransactionLineItem[] = [];
  for (const [index, draft] of drafts.entries()) {
    const label = `Line item ${index + 1}`;
    const description = draft.description.trim();
    if (!description) return { items: [], error: `${label} needs a description.` };
    if (!/^[1-9]\d*$/.test(draft.quantity) || !Number.isSafeInteger(Number(draft.quantity))) {
      return { items: [], error: `${label} quantity must be a positive whole number.` };
    }
    const currency = draft.currency.trim().toUpperCase();
    if (!isISO4217Currency(currency)) {
      return { items: [], error: `${label} currency must be an ISO 4217 code.` };
    }
    const amounts = [
      ["unit price", "unit_price_minor"],
      ["line total", "line_total_minor"],
      ["tax", "tax_minor"],
      ["discount", "discount_minor"],
    ] as const;
    for (const [name, field] of amounts) {
      if (draft[field] && !/^\d+$/.test(draft[field])) {
        return { items: [], error: `${label} ${name} must be a non-negative minor-unit integer.` };
      }
    }
    let details: unknown;
    try {
      details = JSON.parse(draft.details || "{}");
    } catch {
      return { items: [], error: `${label} details must be valid JSON.` };
    }
    if (typeof details !== "object" || details === null || Array.isArray(details)) {
      return { items: [], error: `${label} details must be a JSON object.` };
    }
    if (!isFiniteJsonValue(details)) {
      return { items: [], error: `${label} details must contain only finite JSON values.` };
    }
    const item: TransactionLineItem = {
      schema_version: 1,
      description,
      quantity: Number(draft.quantity),
      currency,
      details: details as { [key: string]: JsonValue },
    };
    for (const [, field] of amounts) {
      if (draft[field]) item[field] = draft[field];
    }
    items.push(item);
  }
  return { items, error: null };
}
