import assert from "node:assert/strict";
import test from "node:test";
import {
  emptyGlobalRuleDraft,
  isCatchAllGlobalRuleDraft,
} from "../src/features/transactions/globalRuleModel.ts";

test("new global rules are safely disabled with neutral priority", () => {
  const draft = emptyGlobalRuleDraft();
  assert.equal(draft.active, false);
  assert.equal(draft.priority, "0");
  assert.equal(isCatchAllGlobalRuleDraft(draft), true);
});

test("catch-all detection ignores whitespace and clears when either matcher is set", () => {
  const draft = { ...emptyGlobalRuleDraft(), senderMatcher: "  ", contentMatcher: "\n" };
  assert.equal(isCatchAllGlobalRuleDraft(draft), true);
  assert.equal(isCatchAllGlobalRuleDraft({ ...draft, senderMatcher: "@ocbc\\.com$" }), false);
  assert.equal(isCatchAllGlobalRuleDraft({ ...draft, contentMatcher: "receipt" }), false);
});
