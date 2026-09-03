const dismissalKeyPrefix = "wealth-builder:transactions:gmail-sync-dismissed:v1";

interface SyncBannerStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

export function syncBannerDismissalKey(userID: string, syncRunID: string): string {
  return `${dismissalKeyPrefix}:${encodeURIComponent(userID)}:${encodeURIComponent(syncRunID)}`;
}

function getBrowserStorage(): SyncBannerStorage | null {
  try {
    return typeof window === "undefined" ? null : window.localStorage;
  } catch {
    return null;
  }
}

export function isSyncBannerDismissed(
  userID: string,
  syncRunID: string,
  storage: SyncBannerStorage | null = getBrowserStorage(),
): boolean {
  try {
    return storage?.getItem(syncBannerDismissalKey(userID, syncRunID)) === "dismissed";
  } catch {
    return false;
  }
}

export function persistSyncBannerDismissal(
  userID: string,
  syncRunID: string,
  storage: SyncBannerStorage | null = getBrowserStorage(),
): void {
  try {
    storage?.setItem(syncBannerDismissalKey(userID, syncRunID), "dismissed");
  } catch {
    // Storage may be disabled or full. The page still dismisses the banner in memory.
  }
}
