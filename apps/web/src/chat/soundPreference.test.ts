import { afterEach, describe, expect, it, vi } from "vitest";

import { getSoundNotificationMode, setSoundNotificationMode } from "./soundPreference";

const MODE_KEY = "nchat.notifications.sound.mode";
const LEGACY_ENABLED_KEY = "nchat.notifications.sound.enabled";
const LEGACY_DM_WITHOUT_MENTION_KEY = "nchat.notifications.sound.dmWithoutMention";

describe("soundPreference", () => {
  afterEach(() => {
    localStorage.clear();
    vi.restoreAllMocks();
  });

  it("defaults to 'all' when nothing is persisted", () => {
    expect(getSoundNotificationMode()).toBe("all");
  });

  it("persists and reads back 'off'", () => {
    setSoundNotificationMode("off");
    expect(getSoundNotificationMode()).toBe("off");
  });

  it("persists and reads back 'all'", () => {
    setSoundNotificationMode("mentions");
    setSoundNotificationMode("all");
    expect(getSoundNotificationMode()).toBe("all");
  });

  it("persists and reads back 'mentions'", () => {
    setSoundNotificationMode("mentions");
    expect(getSoundNotificationMode()).toBe("mentions");
  });

  it("persists and reads back 'mentions_and_dms'", () => {
    setSoundNotificationMode("mentions_and_dms");
    expect(getSoundNotificationMode()).toBe("mentions_and_dms");
  });

  it("survives a fresh read (simulated reload)", () => {
    setSoundNotificationMode("off");
    expect(getSoundNotificationMode()).toBe("off");
    expect(getSoundNotificationMode()).toBe("off");
  });

  it("falls back to the default for a corrupted/unexpected stored value", () => {
    localStorage.setItem(MODE_KEY, "loud");
    expect(getSoundNotificationMode()).toBe("all");
  });

  it("uses the documented storage key", () => {
    setSoundNotificationMode("mentions");
    expect(localStorage.getItem(MODE_KEY)).toBe("mentions");
  });

  it("does not throw and keeps the default when localStorage.getItem throws", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("storage unavailable");
    });
    expect(() => getSoundNotificationMode()).not.toThrow();
    expect(getSoundNotificationMode()).toBe("all");
  });

  it("does not throw when localStorage.setItem throws", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("storage unavailable");
    });
    expect(() => setSoundNotificationMode("off")).not.toThrow();
  });

  describe("migration from the legacy boolean preference", () => {
    it("migrates a legacy 'false' to 'off' when the new key is absent", () => {
      localStorage.setItem(LEGACY_ENABLED_KEY, "false");
      expect(getSoundNotificationMode()).toBe("off");
    });

    it("migrates a legacy 'true' to 'all' when the new key is absent", () => {
      localStorage.setItem(LEGACY_ENABLED_KEY, "true");
      expect(getSoundNotificationMode()).toBe("all");
    });

    it("prefers the new key over the legacy one once the user has chosen a mode", () => {
      localStorage.setItem(LEGACY_ENABLED_KEY, "false");
      setSoundNotificationMode("mentions");
      expect(getSoundNotificationMode()).toBe("mentions");
    });

    it("does not delete the legacy key when migrating", () => {
      localStorage.setItem(LEGACY_ENABLED_KEY, "false");
      getSoundNotificationMode();
      expect(localStorage.getItem(LEGACY_ENABLED_KEY)).toBe("false");
    });
  });

  describe("migration from the legacy 'mentions' + separate DM flag combination", () => {
    it("upgrades 'mentions' with the legacy DM flag on to 'mentions_and_dms'", () => {
      localStorage.setItem(MODE_KEY, "mentions");
      localStorage.setItem(LEGACY_DM_WITHOUT_MENTION_KEY, "true");
      expect(getSoundNotificationMode()).toBe("mentions_and_dms");
    });

    it("keeps plain 'mentions' when the legacy DM flag is off", () => {
      localStorage.setItem(MODE_KEY, "mentions");
      localStorage.setItem(LEGACY_DM_WITHOUT_MENTION_KEY, "false");
      expect(getSoundNotificationMode()).toBe("mentions");
    });

    it("keeps plain 'mentions' when the legacy DM flag was never set", () => {
      localStorage.setItem(MODE_KEY, "mentions");
      expect(getSoundNotificationMode()).toBe("mentions");
    });

    it("does not affect 'all' or 'off' even if the legacy DM flag is on", () => {
      localStorage.setItem(MODE_KEY, "all");
      localStorage.setItem(LEGACY_DM_WITHOUT_MENTION_KEY, "true");
      expect(getSoundNotificationMode()).toBe("all");

      localStorage.setItem(MODE_KEY, "off");
      expect(getSoundNotificationMode()).toBe("off");
    });

    it("clears the legacy flag once the user explicitly picks plain 'mentions' again", () => {
      // Without clearing the flag, this would silently re-upgrade to
      // "mentions_and_dms" on every future read — a deliberate later choice
      // must win, not the stale legacy flag.
      localStorage.setItem(LEGACY_DM_WITHOUT_MENTION_KEY, "true");
      setSoundNotificationMode("mentions");

      expect(getSoundNotificationMode()).toBe("mentions");
      expect(localStorage.getItem(LEGACY_DM_WITHOUT_MENTION_KEY)).toBeNull();
    });

    it("clears the legacy flag on any explicit choice, not only 'mentions'", () => {
      localStorage.setItem(LEGACY_DM_WITHOUT_MENTION_KEY, "true");
      setSoundNotificationMode("off");
      expect(localStorage.getItem(LEGACY_DM_WITHOUT_MENTION_KEY)).toBeNull();
    });

    it("does not delete the legacy DM flag key on a mere read (no explicit choice yet)", () => {
      localStorage.setItem(MODE_KEY, "mentions");
      localStorage.setItem(LEGACY_DM_WITHOUT_MENTION_KEY, "true");
      getSoundNotificationMode();
      expect(localStorage.getItem(LEGACY_DM_WITHOUT_MENTION_KEY)).toBe("true");
    });
  });
});
