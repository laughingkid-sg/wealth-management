export type WorkspacePage =
  | "accounts"
  | "transactions"
  | "bulk-import"
  | "credit-card"
  | "transaction-settings"
  | "transaction-global-settings"
  | "transaction-prompt-preview";

export function workspacePageFromLocation(): WorkspacePage {
  const parameters = new URL(window.location.href).searchParams;
  if (parameters.get("gmail")) return "transactions";
  const page = parameters.get("page");
  if (
    page === "transactions" ||
    page === "bulk-import" ||
    page === "credit-card" ||
    page === "transaction-settings" ||
    page === "transaction-global-settings" ||
    page === "transaction-prompt-preview"
  )
    return page;
  return "accounts";
}
