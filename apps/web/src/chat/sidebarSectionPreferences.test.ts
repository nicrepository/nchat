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

  it("returns safe defaults (all expanded) when nothing is persisted", () => {
    expect(DEFAULT_SECTION_PREFS).toEqual({
      channels: { collapsed: false },
      directs: { collapsed: false },
      groups: { collapsed: false },
    });
    expect(loadSectionPrefs(userId, workspaceId)).toEqual(DEFAULT_SECTION_PREFS);
  });

  it("round-trips a save/load", () => {
    const prefs: SidebarSectionPrefs = {
      channels: { collapsed: true },
      directs: { collapsed: false },
      groups: { collapsed: true },
    };
    saveSectionPrefs(userId, workspaceId, prefs);

    expect(loadSectionPrefs(userId, workspaceId)).toEqual(prefs);
  });

  it("isolates storage by (userId, workspaceId)", () => {
    saveSectionPrefs("user-a", "workspace-1", {
      channels: { collapsed: true },
      directs: { collapsed: false },
      groups: { collapsed: false },
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

  it.each([JSON.stringify(["not", "an", "object"]), "null", "42", '"text"'])(
    "returns defaults when the stored value is not an object (%s)",
    (stored) => {
      localStorage.setItem(storageKey, stored);
      expect(loadSectionPrefs(userId, workspaceId)).toEqual(DEFAULT_SECTION_PREFS);
    },
  );

  it("falls back to defaults per-section when one section's shape is invalid", () => {
    localStorage.setItem(
      storageKey,
      JSON.stringify({
        channels: { collapsed: true },
        directs: { collapsed: "yes" },
        groups: null,
        unknownSection: { collapsed: true },
      }),
    );

    expect(loadSectionPrefs(userId, workspaceId)).toEqual({
      channels: { collapsed: true },
      directs: DEFAULT_SECTION_PREFS.directs,
      groups: DEFAULT_SECTION_PREFS.groups,
    });
  });

  it("ignores unknown extra fields on a section without throwing", () => {
    localStorage.setItem(
      storageKey,
      JSON.stringify({
        channels: { collapsed: true, extra: "ignored" },
        directs: DEFAULT_SECTION_PREFS.directs,
        groups: DEFAULT_SECTION_PREFS.groups,
      }),
    );

    expect(loadSectionPrefs(userId, workspaceId).channels).toEqual({ collapsed: true });
  });

  // Issue #779 stored `{ collapsed, showUnreadOnly }` under the same key. Since
  // issue #1005 collapsing always shows unread, so only `collapsed` survives.
  it.each([
    { legacy: { collapsed: true, showUnreadOnly: true }, collapsed: true },
    { legacy: { collapsed: true, showUnreadOnly: false }, collapsed: true },
    { legacy: { collapsed: false, showUnreadOnly: true }, collapsed: false },
  ])("keeps only `collapsed` from the legacy #779 shape $legacy", ({ legacy, collapsed }) => {
    localStorage.setItem(
      storageKey,
      JSON.stringify({ channels: legacy, directs: legacy, groups: legacy }),
    );

    expect(loadSectionPrefs(userId, workspaceId)).toEqual({
      channels: { collapsed },
      directs: { collapsed },
      groups: { collapsed },
    });
  });

  it("falls back to defaults for a legacy entry whose `collapsed` is invalid", () => {
    localStorage.setItem(
      storageKey,
      JSON.stringify({ channels: { collapsed: null, showUnreadOnly: true } }),
    );

    expect(loadSectionPrefs(userId, workspaceId)).toEqual(DEFAULT_SECTION_PREFS);
  });

  it("drops the legacy field on the next save", () => {
    localStorage.setItem(
      storageKey,
      JSON.stringify({ channels: { collapsed: true, showUnreadOnly: true } }),
    );

    saveSectionPrefs(userId, workspaceId, loadSectionPrefs(userId, workspaceId));

    expect(localStorage.getItem(storageKey)).not.toContain("showUnreadOnly");
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
