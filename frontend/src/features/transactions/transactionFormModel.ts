import {
  isISO4217Currency,
  majorAmountToMinor,
  minorAmountToMajor,
  type JsonValue,
  type TransactionLineItem,
} from "./model";

export interface LineItemDraft {
  key: string;
  description: string;
  quantity: string;
  unitPrice: string;
  lineTotal: string;
  tax: string;
  discount: string;
  currency: string;
  details: string;
}

export const MAX_LINE_ITEMS = 100;
export const MAX_LINE_ITEMS_BYTES = 262_144;

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
    unitPrice: item.unit_price_minor
      ? minorAmountToMajor(item.unit_price_minor, item.currency)
      : "",
    lineTotal: item.line_total_minor
      ? minorAmountToMajor(item.line_total_minor, item.currency)
      : "",
    tax: item.tax_minor ? minorAmountToMajor(item.tax_minor, item.currency) : "",
    discount: item.discount_minor
      ? minorAmountToMajor(item.discount_minor, item.currency)
      : "",
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
  if (drafts.length > MAX_LINE_ITEMS) {
    return {
      items: [],
      error: `A transaction can contain at most ${MAX_LINE_ITEMS} line items.`,
    };
  }
  const items: TransactionLineItem[] = [];
  for (const [index, draft] of drafts.entries()) {
    const label = `Line item ${index + 1}`;
    const description = draft.description.trim();
    if (!description) return { items: [], error: `${label} needs a description.` };
    if ([...description].length > 250) {
      return { items: [], error: `${label} description must contain at most 250 characters.` };
    }
    if (!/^[1-9]\d*$/.test(draft.quantity) || !Number.isSafeInteger(Number(draft.quantity))) {
      return { items: [], error: `${label} quantity must be a positive whole number.` };
    }
    const currency = draft.currency.trim().toUpperCase();
    if (!isISO4217Currency(currency)) {
      return { items: [], error: `${label} currency must be an ISO 4217 code.` };
    }
    const amounts = [
      ["unit price", "unitPrice", "unit_price_minor"],
      ["line total", "lineTotal", "line_total_minor"],
      ["tax", "tax", "tax_minor"],
      ["discount", "discount", "discount_minor"],
    ] as const;
    const parsedAmounts = new Map<string, string>();
    for (const [name, draftField, outputField] of amounts) {
      if (!draft[draftField]) continue;
      try {
        parsedAmounts.set(
          outputField,
          majorAmountToMinor(draft[draftField], currency, true),
        );
      } catch (error) {
        const reason = error instanceof Error ? error.message : "Enter a valid amount.";
        return { items: [], error: `${label} ${name}: ${reason}` };
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
    for (const [, , outputField] of amounts) {
      const amount = parsedAmounts.get(outputField);
      if (amount !== undefined) item[outputField] = amount;
    }
    items.push(item);
  }
  if (new TextEncoder().encode(JSON.stringify(items)).byteLength > MAX_LINE_ITEMS_BYTES) {
    return {
      items: [],
      error: "Line items are too large. Shorten their additional details and try again.",
    };
  }
  return { items, error: null };
}

export function calculateLineTotal(
  quantity: string,
  unitPrice: string,
  currency: string,
): string | null {
  if (!/^[1-9]\d*$/.test(quantity) || !Number.isSafeInteger(Number(quantity))) {
    return null;
  }
  try {
    const unitPriceMinor = majorAmountToMinor(unitPrice, currency, true);
    const totalMinor = BigInt(unitPriceMinor) * BigInt(quantity);
    if (totalMinor > BigInt(Number.MAX_SAFE_INTEGER)) return null;
    return minorAmountToMajor(totalMinor.toString(), currency);
  } catch {
    return null;
  }
}

export function updateLineItemDraft(
  draft: LineItemDraft,
  field: keyof LineItemDraft,
  value: string,
): LineItemDraft {
  const updated = { ...draft, [field]: value };
  if (field === "quantity" || field === "unitPrice" || field === "currency") {
    const calculated = calculateLineTotal(
      updated.quantity,
      updated.unitPrice,
      updated.currency,
    );
    if (calculated !== null) updated.lineTotal = calculated;
  }
  return updated;
}
