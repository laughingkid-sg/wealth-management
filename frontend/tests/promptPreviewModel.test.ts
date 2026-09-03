import assert from "node:assert/strict";
import test from "node:test";
import {
  assertPromptPreviewSourceRelationship,
  automaticPromptPreviewInput,
  formatPromptPreviewRequest,
  manualPromptPreviewInput,
  parseQwenPromptPreviewRequest,
} from "../src/features/transactions/promptPreviewModel.ts";

test("manual preview sends only explicitly selected configurable parts", () => {
  assert.deepEqual(manualPromptPreviewInput("", true, ""), {
    mode: "manual",
    include_user_default: true,
  });
  assert.deepEqual(manualPromptPreviewInput("global-1", false, "personal-1"), {
    mode: "manual",
    include_user_default: false,
    global_rule_id: "global-1",
    user_rule_id: "personal-1",
  });
});

test("automatic preview sends only the selected stored source identifier", () => {
  assert.deepEqual(automaticPromptPreviewInput("source-1"), {
    mode: "automatic",
    data_source_id: "source-1",
  });
  assert.throws(() => automaticPromptPreviewInput(""), /Choose a past email/);
});

test("selected source presence follows the preview mode", () => {
  const source = {
    id: "source-1",
    subject: "Receipt",
    sender: "merchant@example.com",
    received_at: "2026-09-03T01:00:00Z",
    parse_status: "parsed" as const,
  };
  assert.doesNotThrow(() => assertPromptPreviewSourceRelationship("manual", null));
  assert.doesNotThrow(() => assertPromptPreviewSourceRelationship("automatic", source));
  assert.throws(
    () => assertPromptPreviewSourceRelationship("automatic", null),
    /required in automatic mode/,
  );
  assert.throws(
    () => assertPromptPreviewSourceRelationship("manual", source),
    /must be null in manual mode/,
  );
});

function validProviderRequest(
  systemContent = "assembled prompt",
  attachmentURL?: string,
) {
  const content: unknown[] = [
    { type: "text", text: "<EMAIL CONTENT OMITTED FROM PREVIEW>" },
  ];
  if (attachmentURL) {
    content.push({ type: "image_url", image_url: { url: attachmentURL } });
  }
  return {
    model: "qwen3.8-flash",
    messages: [
      { role: "system", content: systemContent },
      {
        role: "user",
        content,
      },
    ],
    response_format: { type: "json_object" },
    enable_thinking: false,
  };
}

test("provider request parser enforces the production Qwen envelope", () => {
  const parsed = parseQwenPromptPreviewRequest(validProviderRequest(), "assembled prompt");
  assert.deepEqual(parsed, validProviderRequest());
  assert.equal(
    formatPromptPreviewRequest(parsed),
    JSON.stringify(validProviderRequest(), null, 2),
  );
});

test("provider request parser accepts only the documented visual placeholder", () => {
  const request = validProviderRequest(
    "assembled prompt",
    "<ELIGIBLE RECEIPT OR INVOICE IMAGE OMITTED FROM PREVIEW>",
  );
  const parsed = parseQwenPromptPreviewRequest(request, "assembled prompt");
  assert.equal(parsed.messages[1].content.length, 2);

  assert.throws(
    () => parseQwenPromptPreviewRequest(
      validProviderRequest("assembled prompt", "data:image/png;base64,private-source-data"),
      "assembled prompt",
    ),
    /attachment placeholder/,
  );
});

test("provider request parser rejects drift in model, thinking, schema, or prompt", () => {
  assert.throws(
    () => parseQwenPromptPreviewRequest(
      { ...validProviderRequest(), model: "another-model" },
      "assembled prompt",
    ),
    /qwen3\.8-flash/,
  );
  assert.throws(
    () => parseQwenPromptPreviewRequest(
      { ...validProviderRequest(), enable_thinking: true },
      "assembled prompt",
    ),
    /enable_thinking must be false/,
  );
  assert.throws(
    () => parseQwenPromptPreviewRequest(
      { ...validProviderRequest(), response_format: { type: "text" } },
      "assembled prompt",
    ),
    /json_object/,
  );
  assert.throws(
    () => parseQwenPromptPreviewRequest(validProviderRequest("stale prompt"), "assembled prompt"),
    /must equal assembled_system_prompt/,
  );
});
