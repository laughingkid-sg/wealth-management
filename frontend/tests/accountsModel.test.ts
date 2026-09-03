import assert from "node:assert/strict";
import test from "node:test";
import {
  accountTypeLabel,
  accountTypesForSide,
} from "../src/features/accounts/model.ts";

test("new account types are offered on their supported sides with product labels", () => {
  const assets = accountTypesForSide("asset");
  const liabilities = accountTypesForSide("liability");

  assert.equal(assets.find(({ value }) => value === "robo_advisor")?.label, "Robo Advisors");
  assert.equal(
    assets.find(({ value }) => value === "retirement_account")?.label,
    "Retirement Account",
  );
  assert.equal(assets.find(({ value }) => value === "other")?.label, "Others");
  assert.equal(liabilities.find(({ value }) => value === "other")?.label, "Others");
  assert.equal(liabilities.some(({ value }) => value === "robo_advisor"), false);
  assert.equal(liabilities.some(({ value }) => value === "retirement_account"), false);
});

test("all-side filters contain shared types once and row labels resolve", () => {
  const all = accountTypesForSide("all");
  assert.equal(all.filter(({ value }) => value === "other").length, 1);
  assert.equal(new Set(all.map(({ value }) => value)).size, all.length);
  assert.equal(accountTypeLabel("robo_advisor"), "Robo Advisors");
  assert.equal(accountTypeLabel("retirement_account"), "Retirement Account");
  assert.equal(accountTypeLabel("other"), "Others");
});
