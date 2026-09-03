import assert from "node:assert/strict";
import test from "node:test";
import {
  isSyncBannerDismissed,
  persistSyncBannerDismissal,
  syncBannerDismissalKey,
} from "../src/features/transactions/syncBannerDismissal";

class MemoryStorage {
  readonly values = new Map<string, string>();

  getItem(key: string): string | null {
    return this.values.get(key) ?? null;
  }

  setItem(key: string, value: string): void {
    this.values.set(key, value);
  }
}

test("sync dismissal keys are scoped to both user and run", () => {
  const first = syncBannerDismissalKey("user/a", "run?1");
  assert.notEqual(first, syncBannerDismissalKey("user/b", "run?1"));
  assert.notEqual(first, syncBannerDismissalKey("user/a", "run?2"));
  assert.match(first, /user%2Fa:run%3F1$/);
});

test("a persisted dismissal survives subsequent reads without affecting other runs", () => {
  const storage = new MemoryStorage();
  persistSyncBannerDismissal("user-1", "run-1", storage);

  assert.equal(isSyncBannerDismissed("user-1", "run-1", storage), true);
  assert.equal(isSyncBannerDismissed("user-1", "run-2", storage), false);
  assert.equal(isSyncBannerDismissed("user-2", "run-1", storage), false);
  assert.equal(
    storage.values.get(syncBannerDismissalKey("user-1", "run-1")),
    "dismissed",
  );
});

test("unavailable or failing storage falls back without throwing", () => {
  const failingStorage = {
    getItem(): string | null {
      throw new Error("blocked");
    },
    setItem(): void {
      throw new Error("full");
    },
  };

  assert.equal(isSyncBannerDismissed("user-1", "run-1", null), false);
  assert.equal(isSyncBannerDismissed("user-1", "run-1", failingStorage), false);
  assert.doesNotThrow(() => persistSyncBannerDismissal("user-1", "run-1", null));
  assert.doesNotThrow(() =>
    persistSyncBannerDismissal("user-1", "run-1", failingStorage),
  );
});
