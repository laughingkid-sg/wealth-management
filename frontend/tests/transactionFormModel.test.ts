import assert from "node:assert/strict";
import test from "node:test";
import {
  buildManualDuplicatePreflightParams,
  buildManualTransactionInsertPayload,
  emptyManualTransactionDraft,
  validateManualTransactionDraft,
} from "../src/features/transactions/manualTransactionModel";
import {
  majorAmountToMinor,
  minorAmountToMajor,
  type ManualTransactionInput,
  type OwnedAccountOption,
  type TransactionCategory,
} from "../src/features/transactions/model";
import {
  MAX_LINE_ITEMS,
  parseLineItemDrafts,
  updateLineItemDraft,
  type LineItemDraft,
} from "../src/features/transactions/transactionFormModel";

test("major and minor conversions are exact for ISO currency precision", () => {
  assert.equal(majorAmountToMinor("12.5", "SGD"), "1250");
  assert.equal(minorAmountToMajor("1250", "SGD"), "12.50");
  assert.equal(majorAmountToMinor("12", "JPY"), "12");
  assert.equal(minorAmountToMajor("12", "JPY"), "12");
  assert.equal(majorAmountToMinor("1.234", "KWD"), "1234");
  assert.equal(majorAmountToMinor("0", "SGD", true), "0");
});

test("major conversion rejects ambiguous and unsafe numbers", () => {
  for (const amount of ["-1", "1,234.56", "1e2", "1.001", "90071992547409.92"]) {
    assert.throws(() => majorAmountToMinor(amount, "SGD"), Error, amount);
  }
  assert.throws(() => majorAmountToMinor("0", "SGD"));
  assert.throws(() => majorAmountToMinor("12.0", "JPY"));
});

function lineItemDraft(): LineItemDraft {
  return {
    key: "line-1",
    description: "Apples",
    quantity: "3",
    unitPrice: "",
    lineTotal: "",
    tax: "0.10",
    discount: "",
    currency: "SGD",
    details: "{}",
  };
}

test("line total auto-calculates in major units but remains editable", () => {
  const calculated = updateLineItemDraft(lineItemDraft(), "unitPrice", "1.25");
  assert.equal(calculated.lineTotal, "3.75");
  const overridden = updateLineItemDraft(calculated, "lineTotal", "3.70");
  assert.equal(overridden.lineTotal, "3.70");
  const recalculated = updateLineItemDraft(overridden, "quantity", "4");
  assert.equal(recalculated.lineTotal, "5.00");

  const parsed = parseLineItemDrafts([overridden]);
  assert.equal(parsed.error, null);
  assert.deepEqual(parsed.items[0], {
    schema_version: 1,
    description: "Apples",
    quantity: 3,
    unit_price_minor: "125",
    line_total_minor: "370",
    tax_minor: "10",
    currency: "SGD",
    details: {},
  });
});

test("line item validation enforces character, count, and payload limits", () => {
  const tooLongDescription = parseLineItemDrafts([
    { ...lineItemDraft(), description: "🙂".repeat(251) },
  ]);
  assert.match(tooLongDescription.error ?? "", /at most 250 characters/);

  const tooMany = parseLineItemDrafts(
    Array.from({ length: MAX_LINE_ITEMS + 1 }, (_, index) => ({
      ...lineItemDraft(),
      key: `line-${index}`,
    })),
  );
  assert.match(tooMany.error ?? "", /at most 100 line items/);

  const tooLarge = parseLineItemDrafts([
    {
      ...lineItemDraft(),
      details: JSON.stringify({ note: "x".repeat(262_144) }),
    },
  ]);
  assert.match(tooLarge.error ?? "", /Line items are too large/);
});

const account: OwnedAccountOption = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Everyday card",
  institution_name: "Bank",
};
const category: TransactionCategory = {
  id: "22222222-2222-4222-8222-222222222222",
  parent_name: "Food & Dining",
  name: "Groceries",
  emoji: "",
  sort_order: 1,
};

test("manual form validation creates minor-unit input and mirrors SGD", () => {
  const draft = {
    ...emptyManualTransactionDraft(new Date("2026-09-03T02:00:00.000Z")),
    accountId: account.id,
    title: "Groceries",
    merchantName: "FairPrice",
    originalAmount: "12.5",
    categoryId: category.id,
    userNotes: "  weekly shop  ",
  };
  const result = validateManualTransactionDraft(draft, [account], [category]);
  assert.equal(result.error, null);
  assert.equal(result.input?.original_amount_minor, "1250");
  assert.equal(result.input?.sgd_amount_minor, "1250");
  assert.equal(result.input?.user_notes, "weekly shop");

  const invalid = validateManualTransactionDraft(
    { ...draft, originalAmount: "0" },
    [account],
    [category],
  );
  assert.match(invalid.error ?? "", /greater than zero/);
});

function manualInput(): ManualTransactionInput {
  return {
    account_id: account.id,
    transaction_kind: "debit",
    title: "Groceries",
    merchant_name: "FairPrice",
    original_amount_minor: "1250",
    original_currency: "SGD",
    sgd_amount_minor: "1250",
    occurred_at: "2026-09-03T02:00:00.000Z",
    category_id: category.id,
    line_items: [],
    user_notes: "weekly shop",
  };
}

test("manual insert payload is owner-bound and excludes immutable fields", () => {
  const payload = buildManualTransactionInsertPayload("user-1", manualInput());
  assert.equal(payload.user_id, "user-1");
  assert.equal(payload.review_status, "confirmed");
  assert.deepEqual(payload.details, { user_notes: "weekly shop" });
  assert.equal("creation_method" in payload, false);
  assert.equal("id" in payload, false);
  assert.equal("created_at" in payload, false);

  const withoutNotes = buildManualTransactionInsertPayload("user-1", {
    ...manualInput(),
    user_notes: null,
  });
  assert.equal("details" in withoutNotes, false);
});

test("duplicate preflight uses exact identity fields and a ten-minute window", () => {
  const params = buildManualDuplicatePreflightParams(manualInput());
  assert.equal(params.get("account_id"), `eq.${account.id}`);
  assert.equal(params.get("transaction_kind"), "eq.debit");
  assert.equal(params.get("original_amount_minor"), "eq.1250");
  assert.equal(params.get("original_currency"), "eq.SGD");
  assert.deepEqual(params.getAll("occurred_at"), [
    "gte.2026-09-03T01:50:00.000Z",
    "lte.2026-09-03T02:10:00.000Z",
  ]);
  assert.equal(params.get("limit"), "10");
});
