export function shouldDismissAccountForm(key: string, saveInProgress: boolean): boolean {
  return key === "Escape" && !saveInProgress;
}
