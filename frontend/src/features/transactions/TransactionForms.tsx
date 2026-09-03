import { Plus, Trash2 } from "lucide-react";
import type {
  OwnedAccountOption,
  TransactionCategory,
} from "./model";
import {
  lineItemKey,
  MAX_LINE_ITEMS,
  updateLineItemDraft,
  type LineItemDraft,
} from "./transactionFormModel";

export function LineItemsEditor({
  drafts,
  onChange,
  defaultCurrency,
  disabled = false,
}: {
  drafts: LineItemDraft[];
  onChange: (drafts: LineItemDraft[]) => void;
  defaultCurrency: string;
  disabled?: boolean;
}) {
  function update(key: string, field: keyof LineItemDraft, value: string) {
    onChange(
      drafts.map((draft) =>
        draft.key === key ? updateLineItemDraft(draft, field, value) : draft,
      ),
    );
  }

  return (
    <fieldset className="line-items-editor" disabled={disabled}>
      <legend>Line items <span className="optional">(optional)</span></legend>
      <p>Enter normal currency amounts, such as 12.50. Additional details must be a JSON object.</p>
      {drafts.length === 0 ? (
        <p className="line-items-empty">No line items recorded.</p>
      ) : (
        <div className="line-item-stack">
          {drafts.map((draft, index) => (
            <fieldset className="line-item-card" key={draft.key}>
              <legend>Item {index + 1}</legend>
              <div className="line-item-heading">
                <label>
                  Description
                  <input
                    maxLength={250}
                    onChange={(event) => update(draft.key, "description", event.target.value)}
                    required
                    value={draft.description}
                  />
                </label>
                <button
                  aria-label={`Remove line item ${index + 1}`}
                  className="icon-button danger-button"
                  onClick={() => onChange(drafts.filter((item) => item.key !== draft.key))}
                  type="button"
                >
                  <Trash2 aria-hidden="true" size={17} />
                </button>
              </div>
              <div className="line-item-grid">
                <label>
                  Quantity
                  <input
                    inputMode="numeric"
                    min="1"
                    onChange={(event) => update(draft.key, "quantity", event.target.value)}
                    required
                    type="number"
                    value={draft.quantity}
                  />
                </label>
                <label>
                  Currency
                  <input
                    autoCapitalize="characters"
                    maxLength={3}
                    onChange={(event) => update(draft.key, "currency", event.target.value.toUpperCase())}
                    pattern="[A-Z]{3}"
                    required
                    value={draft.currency}
                  />
                </label>
                {([
                  ["Unit price", "unitPrice"],
                  ["Line total", "lineTotal"],
                  ["Tax", "tax"],
                  ["Discount", "discount"],
                ] as const).map(([label, field]) => (
                  <label key={field}>
                    {label} <span className="optional">(optional)</span>
                    <input
                      inputMode="decimal"
                      onChange={(event) => update(draft.key, field, event.target.value)}
                      placeholder="0.00"
                      type="text"
                      value={draft[field]}
                    />
                  </label>
                ))}
              </div>
              <label>
                Additional details <span className="optional">(JSON object)</span>
                <textarea
                  className="json-textarea"
                  onChange={(event) => update(draft.key, "details", event.target.value)}
                  rows={3}
                  spellCheck={false}
                  value={draft.details}
                />
              </label>
            </fieldset>
          ))}
        </div>
      )}
      <button
        className="button button-secondary add-line-item"
        disabled={disabled || drafts.length >= MAX_LINE_ITEMS}
        onClick={() =>
          onChange([
            ...drafts,
            {
              key: lineItemKey(),
              description: "",
              quantity: "1",
              unitPrice: "",
              lineTotal: "",
              tax: "",
              discount: "",
              currency: /^[A-Z]{3}$/.test(defaultCurrency) ? defaultCurrency : "SGD",
              details: "{}",
            },
          ])
        }
        type="button"
      >
        <Plus aria-hidden="true" size={16} />
        {drafts.length >= MAX_LINE_ITEMS
          ? `${MAX_LINE_ITEMS} line item limit reached`
          : "Add line item"}
      </button>
    </fieldset>
  );
}

export function CategorySelect({
  categories,
  value,
  onChange,
  disabled = false,
}: {
  categories: TransactionCategory[];
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
}) {
  const groups = new Map<string, TransactionCategory[]>();
  for (const category of categories) {
    const group = groups.get(category.parent_name) ?? [];
    group.push(category);
    groups.set(category.parent_name, group);
  }
  return (
    <select
      disabled={disabled}
      onChange={(event) => onChange(event.target.value)}
      value={value}
    >
      <option value="">Uncategorized</option>
      {[...groups.entries()].map(([group, items]) => (
        <optgroup key={group} label={group}>
          {items.map((category) => (
            <option key={category.id} value={category.id}>
              {category.emoji ? `${category.emoji} ` : ""}{category.name}
            </option>
          ))}
        </optgroup>
      ))}
    </select>
  );
}

export function AccountSelect({
  accounts,
  value,
  onChange,
  disabled = false,
  placeholder = "Choose an account",
  excludedId,
}: {
  accounts: OwnedAccountOption[];
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  placeholder?: string;
  excludedId?: string;
}) {
  return (
    <select
      disabled={disabled}
      onChange={(event) => onChange(event.target.value)}
      required
      value={value}
    >
      <option value="">{placeholder}</option>
      {accounts.map((account) => (
        <option disabled={account.id === excludedId} key={account.id} value={account.id}>
          {account.name}{account.institution_name ? ` · ${account.institution_name}` : ""}
        </option>
      ))}
    </select>
  );
}
