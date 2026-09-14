import { describe, it, expect, beforeEach } from "vitest";
import {
  clearDraftPersistence,
  clearUserDraftPersistence,
  loadAllDraftPersistence,
  loadDraftPersistence,
  saveDraftPersistence,
} from "./chatDraftPersistence";

describe("chatDraftPersistence", () => {
  beforeEach(() => sessionStorage.clear());

  it("round-trips a saved draft", () => {
    const text = {
      type: "doc",
      content: [{ type: "paragraph", content: [{ type: "text", text: "oi" }] }],
    };
    saveDraftPersistence("u1", "channel:c1", { text, replyToMessageId: "m1", updatedAt: 1000 });
    expect(loadDraftPersistence("u1", "channel:c1")).toEqual({
      text,
      replyToMessageId: "m1",
      updatedAt: 1000,
    });
  });

  it("returns null when nothing was saved", () => {
    expect(loadDraftPersistence("u1", "channel:missing")).toBeNull();
  });

  it("scopes by user — a different user never sees another's draft", () => {
    saveDraftPersistence("u1", "channel:c1", { text: null, replyToMessageId: null, updatedAt: 1 });
    expect(loadDraftPersistence("u2", "channel:c1")).toBeNull();
  });

  it("scopes by draft key — a channel and a dm with the same id never collide", () => {
    saveDraftPersistence("u1", "channel:x", { text: null, replyToMessageId: "m1", updatedAt: 1 });
    expect(loadDraftPersistence("u1", "dm:x")).toBeNull();
  });

  it("never throws and returns null on a corrupted payload", () => {
    sessionStorage.setItem("nchat.chat.draft.v1:u1:channel:c1", "{not json");
    expect(loadDraftPersistence("u1", "channel:c1")).toBeNull();
  });

  it("never throws and returns null on a payload with an invalid shape", () => {
    sessionStorage.setItem(
      "nchat.chat.draft.v1:u1:channel:c1",
      JSON.stringify({ text: 42, replyToMessageId: null, updatedAt: 1 }),
    );
    expect(loadDraftPersistence("u1", "channel:c1")).toBeNull();
  });

  it("clearDraftPersistence removes only that draft's key", () => {
    saveDraftPersistence("u1", "channel:c1", { text: null, replyToMessageId: "m1", updatedAt: 1 });
    saveDraftPersistence("u1", "channel:c2", { text: null, replyToMessageId: "m2", updatedAt: 1 });
    clearDraftPersistence("u1", "channel:c1");
    expect(loadDraftPersistence("u1", "channel:c1")).toBeNull();
    expect(loadDraftPersistence("u1", "channel:c2")).not.toBeNull();
  });

  it("clearUserDraftPersistence removes every draft for that user, and none of another user's", () => {
    saveDraftPersistence("u1", "channel:c1", { text: null, replyToMessageId: null, updatedAt: 1 });
    saveDraftPersistence("u1", "dm:d1", { text: null, replyToMessageId: null, updatedAt: 1 });
    saveDraftPersistence("u2", "channel:c1", { text: null, replyToMessageId: null, updatedAt: 1 });
    clearUserDraftPersistence("u1");
    expect(loadDraftPersistence("u1", "channel:c1")).toBeNull();
    expect(loadDraftPersistence("u1", "dm:d1")).toBeNull();
    expect(loadDraftPersistence("u2", "channel:c1")).not.toBeNull();
  });

  it("never stores anything beyond text, replyToMessageId and updatedAt", () => {
    saveDraftPersistence("u1", "channel:c1", { text: null, replyToMessageId: "m1", updatedAt: 1 });
    const raw = sessionStorage.getItem("nchat.chat.draft.v1:u1:channel:c1")!;
    expect(Object.keys(JSON.parse(raw)).sort()).toEqual(
      ["replyToMessageId", "text", "updatedAt"].sort(),
    );
  });

  it("rejects a node with a non-string type, a non-string text, or non-array content", () => {
    const key = "nchat.chat.draft.v1:u1:channel:c1";
    sessionStorage.setItem(
      key,
      JSON.stringify({ text: { type: 42 }, replyToMessageId: null, updatedAt: 1 }),
    );
    expect(loadDraftPersistence("u1", "channel:c1")).toBeNull();

    sessionStorage.setItem(
      key,
      JSON.stringify({ text: { type: "doc", text: 42 }, replyToMessageId: null, updatedAt: 1 }),
    );
    expect(loadDraftPersistence("u1", "channel:c1")).toBeNull();

    sessionStorage.setItem(
      key,
      JSON.stringify({
        text: { type: "doc", content: "not-an-array" },
        replyToMessageId: null,
        updatedAt: 1,
      }),
    );
    expect(loadDraftPersistence("u1", "channel:c1")).toBeNull();

    sessionStorage.setItem(
      key,
      JSON.stringify({
        text: { type: "doc", content: [{ type: 42 }] },
        replyToMessageId: null,
        updatedAt: 1,
      }),
    );
    expect(loadDraftPersistence("u1", "channel:c1")).toBeNull();
  });

  describe("loadAllDraftPersistence", () => {
    it("returns nothing for an empty userId", () => {
      expect(loadAllDraftPersistence("")).toEqual([]);
    });

    it("returns every valid entry for the user, skipping corrupt or invalid ones", () => {
      saveDraftPersistence("u1", "channel:c1", {
        text: null,
        replyToMessageId: "m1",
        updatedAt: 1,
      });
      saveDraftPersistence("u1", "dm:d1", { text: null, replyToMessageId: "m2", updatedAt: 2 });
      sessionStorage.setItem("nchat.chat.draft.v1:u1:channel:broken", "{not json");
      sessionStorage.setItem(
        "nchat.chat.draft.v1:u1:channel:invalid-shape",
        JSON.stringify({ text: 42, replyToMessageId: null, updatedAt: 1 }),
      );
      saveDraftPersistence("u2", "channel:c1", {
        text: null,
        replyToMessageId: "other-user",
        updatedAt: 1,
      });

      const entries = loadAllDraftPersistence("u1");
      expect(new Map(entries)).toEqual(
        new Map([
          ["channel:c1", { text: null, replyToMessageId: "m1", updatedAt: 1 }],
          ["dm:d1", { text: null, replyToMessageId: "m2", updatedAt: 2 }],
        ]),
      );
    });
  });
});
