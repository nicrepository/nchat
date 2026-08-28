import { afterEach, describe, expect, it, vi } from "vitest";

import { loadPersistedUnread, savePersistedUnread } from "./sidebarUnreadPersistence";

const userId = "user-a";
const workspaceId = "workspace-1";
const storageKey = `nchat.sidebar.unread.v1:${workspaceId}:${userId}`;

describe("sidebarUnreadPersistence", () => {
  afterEach(() => {
    localStorage.clear();
    vi.restoreAllMocks();
  });

  it("round-trips a save/load for entries that have unread", () => {
    savePersistedUnread(userId, workspaceId, [
      { id: "channel-1", type: "channel", unreadCount: 2, hasMentionUnread: false },
      { id: "dm-1", type: "dm", unreadCount: 1, hasMentionUnread: true },
    ]);

    expect(loadPersistedUnread(userId, workspaceId)).toEqual([
      { id: "channel-1", type: "channel", unreadCount: 2, hasMentionUnread: false },
      { id: "dm-1", type: "dm", unreadCount: 1, hasMentionUnread: true },
    ]);
  });

  it("omits zero-unread, non-mentioned entries when saving", () => {
    savePersistedUnread(userId, workspaceId, [
      { id: "channel-1", type: "channel", unreadCount: 0, hasMentionUnread: false },
      { id: "channel-2", type: "channel", unreadCount: 3, hasMentionUnread: false },
    ]);

    expect(loadPersistedUnread(userId, workspaceId)).toEqual([
      { id: "channel-2", type: "channel", unreadCount: 3, hasMentionUnread: false },
    ]);
  });

  it("overwrites down to empty once every conversation is read", () => {
    savePersistedUnread(userId, workspaceId, [
      { id: "channel-1", type: "channel", unreadCount: 2, hasMentionUnread: false },
    ]);
    expect(loadPersistedUnread(userId, workspaceId)).toHaveLength(1);

    savePersistedUnread(userId, workspaceId, [
      { id: "channel-1", type: "channel", unreadCount: 0, hasMentionUnread: false },
    ]);

    expect(loadPersistedUnread(userId, workspaceId)).toEqual([]);
  });

  it("returns [] when nothing is persisted yet", () => {
    expect(loadPersistedUnread(userId, workspaceId)).toEqual([]);
  });

  it("returns [] and does not throw for corrupted/non-JSON stored data", () => {
    localStorage.setItem(storageKey, "{not json");
    expect(() => loadPersistedUnread(userId, workspaceId)).not.toThrow();
    expect(loadPersistedUnread(userId, workspaceId)).toEqual([]);
  });

  it("drops individually invalid entries but keeps the valid ones", () => {
    localStorage.setItem(
      storageKey,
      JSON.stringify([
        { id: "channel-1", type: "channel", unreadCount: 2, hasMentionUnread: false },
        { id: "channel-2", type: "not-a-real-type", unreadCount: 1, hasMentionUnread: false },
        { id: "", type: "channel", unreadCount: 1, hasMentionUnread: false },
        { id: "channel-3", type: "channel", unreadCount: -1, hasMentionUnread: false },
        { id: "channel-4", type: "channel", unreadCount: 1, hasMentionUnread: "yes" },
        "not-an-object",
      ]),
    );

    expect(loadPersistedUnread(userId, workspaceId)).toEqual([
      { id: "channel-1", type: "channel", unreadCount: 2, hasMentionUnread: false },
    ]);
  });

  it("isolates storage by (userId, workspaceId)", () => {
    savePersistedUnread("user-a", "workspace-1", [
      { id: "channel-1", type: "channel", unreadCount: 1, hasMentionUnread: false },
    ]);

    expect(loadPersistedUnread("user-b", "workspace-1")).toEqual([]);
    expect(loadPersistedUnread("user-a", "workspace-2")).toEqual([]);
    expect(loadPersistedUnread("user-a", "workspace-1")).toHaveLength(1);
  });

  it("does not throw and returns [] when localStorage.getItem throws", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("storage unavailable");
    });
    expect(() => loadPersistedUnread(userId, workspaceId)).not.toThrow();
    expect(loadPersistedUnread(userId, workspaceId)).toEqual([]);
  });

  it("does not throw when localStorage.setItem throws", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("storage unavailable");
    });
    expect(() =>
      savePersistedUnread(userId, workspaceId, [
        { id: "channel-1", type: "channel", unreadCount: 1, hasMentionUnread: false },
      ]),
    ).not.toThrow();
  });

  it("never persists any field beyond the 4 allowlisted ones", () => {
    savePersistedUnread(userId, workspaceId, [
      { id: "channel-1", type: "channel", unreadCount: 1, hasMentionUnread: false },
    ]);

    const raw: unknown = JSON.parse(localStorage.getItem(storageKey) ?? "[]");
    expect(Array.isArray(raw)).toBe(true);
    const entry = (raw as unknown[])[0] as Record<string, unknown>;
    expect(Object.keys(entry).sort()).toEqual(
      ["hasMentionUnread", "id", "type", "unreadCount"].sort(),
    );
  });
});
