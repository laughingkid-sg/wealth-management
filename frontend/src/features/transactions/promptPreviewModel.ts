import type {
  AutomaticPromptPreviewInput,
  ManualPromptPreviewInput,
  PromptPreviewMode,
  PromptPreviewSource,
  QwenPromptPreviewImagePart,
  QwenPromptPreviewRequest,
  QwenPromptPreviewTextPart,
} from "./model";

const emailContentPlaceholder = "<EMAIL CONTENT OMITTED FROM PREVIEW>" as const;
const attachmentPlaceholder =
  "<ELIGIBLE RECEIPT OR INVOICE IMAGE OMITTED FROM PREVIEW>" as const;

type UnknownRecord = Record<string, unknown>;

function record(value: unknown, field: string): UnknownRecord {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error(`${field} must be an object`);
  }
  return value as UnknownRecord;
}

export function manualPromptPreviewInput(
  globalRuleID: string,
  includeUserDefault: boolean,
  userRuleID: string,
): ManualPromptPreviewInput {
  return {
    mode: "manual",
    include_user_default: includeUserDefault,
    ...(globalRuleID ? { global_rule_id: globalRuleID } : {}),
    ...(userRuleID ? { user_rule_id: userRuleID } : {}),
  };
}

export function automaticPromptPreviewInput(
  sourceID: string,
): AutomaticPromptPreviewInput {
  if (!sourceID) throw new Error("Choose a past email to preview.");
  return { mode: "automatic", data_source_id: sourceID };
}

export function assertPromptPreviewSourceRelationship(
  mode: PromptPreviewMode,
  selectedSource: PromptPreviewSource | null,
): void {
  if (mode === "automatic" && selectedSource === null) {
    throw new Error("selected_source is required in automatic mode");
  }
  if (mode === "manual" && selectedSource !== null) {
    throw new Error("selected_source must be null in manual mode");
  }
}

export function parseQwenPromptPreviewRequest(
  value: unknown,
  assembledSystemPrompt: string,
): QwenPromptPreviewRequest {
  const request = record(value, "provider_request");
  if (request.model !== "qwen3.8-flash") {
    throw new Error("provider_request.model must be qwen3.8-flash");
  }
  if (request.enable_thinking !== false) {
    throw new Error("provider_request.enable_thinking must be false");
  }
  const responseFormat = record(request.response_format, "provider_request.response_format");
  if (responseFormat.type !== "json_object") {
    throw new Error("provider_request.response_format.type must be json_object");
  }
  if (!Array.isArray(request.messages) || request.messages.length !== 2) {
    throw new Error("provider_request.messages must contain exactly two messages");
  }
  const systemMessage = record(request.messages[0], "provider_request.messages[0]");
  if (systemMessage.role !== "system" || typeof systemMessage.content !== "string") {
    throw new Error("provider_request.messages[0] must be a system text message");
  }
  if (systemMessage.content !== assembledSystemPrompt) {
    throw new Error("provider_request system content must equal assembled_system_prompt");
  }
  const userMessage = record(request.messages[1], "provider_request.messages[1]");
  if (userMessage.role !== "user" || !Array.isArray(userMessage.content)) {
    throw new Error("provider_request.messages[1] must be a user content-parts message");
  }
  if (userMessage.content.length < 1 || userMessage.content.length > 2) {
    throw new Error("provider_request user content must contain one or two placeholder parts");
  }
  const textValue = record(userMessage.content[0], "provider_request.messages[1].content[0]");
  if (textValue.type !== "text" || textValue.text !== emailContentPlaceholder) {
    throw new Error("provider_request user text must contain the email-content placeholder");
  }
  const textPart: QwenPromptPreviewTextPart = {
    type: "text",
    text: emailContentPlaceholder,
  };
  let content: QwenPromptPreviewRequest["messages"][1]["content"] = [textPart];
  if (userMessage.content.length === 2) {
    const imageValue = record(userMessage.content[1], "provider_request.messages[1].content[1]");
    const imageURL = record(
      imageValue.image_url,
      "provider_request.messages[1].content[1].image_url",
    );
    if (imageValue.type !== "image_url" || imageURL.url !== attachmentPlaceholder) {
      throw new Error("provider_request image must contain the attachment placeholder");
    }
    const imagePart: QwenPromptPreviewImagePart = {
      type: "image_url",
      image_url: { url: attachmentPlaceholder },
    };
    content = [textPart, imagePart];
  }
  return {
    model: "qwen3.8-flash",
    messages: [
      { role: "system", content: systemMessage.content },
      { role: "user", content },
    ],
    response_format: { type: "json_object" },
    enable_thinking: false,
  };
}

export function formatPromptPreviewRequest(value: QwenPromptPreviewRequest): string {
  return JSON.stringify(value, null, 2);
}
