import { describe, it, expect, beforeEach } from "vitest";
import { loadViewportAnchor, saveViewportAnchor } from "./chatViewportPersistence";

describe("chatViewportPersistence", () => {
  beforeEach(() => sessionStorage.clear());

  it("round-trips a saved anchor", () => {
    saveViewportAnchor("u1", "channel", "c1", {
      atBottom: false,
      anchorMessageId: "m42",
      anchorOffsetPx: 84,
      savedAt: 1000,
    });
    expect(loadViewportAnchor("u1", "channel", "c1")).toEqual({
      atBottom: false,
      anchorMessageId: "m42",
      anchorOffsetPx: 84,
      savedAt: 1000,
    });
  });

  it("returns null when nothing was saved", () => {
    expect(loadViewportAnchor("u1", "channel", "missing")).toBeNull();
  });

  it("scopes by user — a different user never sees another's anchor", () => {
    saveViewportAnchor("u1", "channel", "c1", {
      atBottom: true,
      anchorMessageId: null,
      anchorOffsetPx: 0,
      savedAt: 1,
    });
    expect(loadViewportAnchor("u2", "channel", "c1")).toBeNull();
  });

  it("scopes by kind — a channel and a dm with the same id never collide", () => {
    saveViewportAnchor("u1", "channel", "x", {
      atBottom: false,
      anchorMessageId: "a",
      anchorOffsetPx: 1,
      savedAt: 1,
    });
    expect(loadViewportAnchor("u1", "dm", "x")).toBeNull();
  });

  it("never throws and returns null on a corrupted payload", () => {
    sessionStorage.setItem("nchat.chat.viewport.v1:u1:channel:c1", "{not json");
    expect(loadViewportAnchor("u1", "channel", "c1")).toBeNull();
  });

  it("never throws and returns null on a payload with an invalid shape", () => {
    sessionStorage.setItem(
      "nchat.chat.viewport.v1:u1:channel:c1",
      JSON.stringify({ atBottom: "not-a-boolean" }),
    );
    expect(loadViewportAnchor("u1", "channel", "c1")).toBeNull();
  });

  it("never stores message content — only the fields of ViewportAnchor", () => {
    saveViewportAnchor("u1", "channel", "c1", {
      atBottom: false,
      anchorMessageId: "m1",
      anchorOffsetPx: 10,
      savedAt: 1,
    });
    const raw = sessionStorage.getItem("nchat.chat.viewport.v1:u1:channel:c1")!;
    expect(Object.keys(JSON.parse(raw)).sort()).toEqual(
      ["anchorMessageId", "anchorOffsetPx", "atBottom", "savedAt"].sort(),
    );
  });
});
