import { afterEach, describe, expect, it, vi } from "vitest";

import {
  DEFAULT_SECTION_PREFS,
  loadSectionPrefs,
  saveSectionPrefs,
  type SidebarSectionPrefs,
} from "./sidebarSectionPreferences";

const userId = "user-a";
const workspaceId = "workspace-1";
const storageKey = `nchat.sidebar.sections.v1:${workspaceId}:${userId}`;

describe("sidebarSectionPreferences", () => {
  afterEach(() => {
    localStorage.clear();
    vi.restoreAllMocks();
  });

  it("returns safe defaults (all expanded, unread-only off) when nothing is persisted", () => {
    expect(loadSectionPrefs(userId, workspaceId)).toEqual(DEFAULT_SECTION_PREFS);
  });

  it("round-trips a save/load", () => {
    const prefs: SidebarSectionPrefs = {
      channels: { collapsed: true, showUnreadOnly: true },
      directs: { collapsed: true, showUnreadOnly: false },
      groups: { collapsed: false, showUnreadOnly: false },
    };
    saveSectionPrefs(userId, workspaceId, prefs);

    expect(loadSectionPrefs(userId, workspaceId)).toEqual(prefs);
  });

  it("isolates storage by (userId, workspaceId)", () => {
    saveSectionPrefs("user-a", "workspace-1", {
      channels: { collapsed: true, showUnreadOnly: true },
      directs: { collapsed: false, showUnreadOnly: false },
      groups: { collapsed: false, showUnreadOnly: false },
    });

    expect(loadSectionPrefs("user-b", "workspace-1")).toEqual(DEFAULT_SECTION_PREFS);
    expect(loadSectionPrefs("user-a", "workspace-2")).toEqual(DEFAULT_SECTION_PREFS);
    expect(loadSectionPrefs("user-a", "workspace-1").channels.collapsed).toBe(true);
  });

  it("returns defaults and does not throw for corrupted/non-JSON stored data", () => {
    localStorage.setItem(storageKey, "{not json");
    expect(() => loadSectionPrefs(userId, workspaceId)).not.toThrow();
    expect(loadSectionPrefs(userId, workspaceId)).toEqual(DEFAULT_SECTION_PREFS);
  });

  it("returns defaults when the stored value is not an object", () => {
    localStorage.setItem(storageKey, JSON.stringify(["not", "an", "object"]));
    expect(loadSectionPrefs(userId, workspaceId)).toEqual(DEFAULT_SECTION_PREFS);
  });

  it("falls back to defaults per-section when one section's shape is invalid", () => {
    localStorage.setItem(
      storageKey,
      JSON.stringify({
        channels: { collapsed: true, showUnreadOnly: true },
        directs: { collapsed: "yes", showUnreadOnly: false },
        groups: null,
      }),
    );

    expect(loadSectionPrefs(userId, workspaceId)).toEqual({
      channels: { collapsed: true, showUnreadOnly: true },
      directs: DEFAULT_SECTION_PREFS.directs,
      groups: DEFAULT_SECTION_PREFS.groups,
    });
  });

  it("ignores unknown extra fields on a section without throwing", () => {
    localStorage.setItem(
      storageKey,
      JSON.stringify({
        channels: { collapsed: true, showUnreadOnly: false, extra: "ignored" },
        directs: DEFAULT_SECTION_PREFS.directs,
        groups: DEFAULT_SECTION_PREFS.groups,
      }),
    );

    expect(loadSectionPrefs(userId, workspaceId).channels).toEqual({
      collapsed: true,
      showUnreadOnly: false,
    });
  });

  it("does not throw and returns defaults when localStorage.getItem throws", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("storage unavailable");
    });
    expect(() => loadSectionPrefs(userId, workspaceId)).not.toThrow();
    expect(loadSectionPrefs(userId, workspaceId)).toEqual(DEFAULT_SECTION_PREFS);
  });

  it("does not throw when localStorage.setItem throws", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("storage unavailable");
    });
    expect(() => saveSectionPrefs(userId, workspaceId, DEFAULT_SECTION_PREFS)).not.toThrow();
  });
});
