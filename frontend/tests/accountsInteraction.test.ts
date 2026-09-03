import assert from "node:assert/strict";
import test from "node:test";
import { shouldDismissAccountForm } from "../src/features/accounts/interactions.ts";

test("account form Escape dismissal is blocked only while saving", () => {
  assert.equal(shouldDismissAccountForm("Escape", false), true);
  assert.equal(shouldDismissAccountForm("Escape", true), false);
  assert.equal(shouldDismissAccountForm("Enter", false), false);
});
