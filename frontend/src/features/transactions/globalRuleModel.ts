export interface GlobalRuleDraft {
  name: string;
  senderMatcher: string;
  contentMatcher: string;
  promptFragment: string;
  priority: string;
  active: boolean;
}

export function emptyGlobalRuleDraft(): GlobalRuleDraft {
  return {
    name: "",
    senderMatcher: "",
    contentMatcher: "",
    promptFragment: "",
    priority: "0",
    active: false,
  };
}

export function isCatchAllGlobalRuleDraft(draft: GlobalRuleDraft): boolean {
  return draft.senderMatcher.trim() === "" && draft.contentMatcher.trim() === "";
}
