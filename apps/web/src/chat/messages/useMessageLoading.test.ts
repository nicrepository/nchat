/**
 * nextFocusLoad — which message-target changes owe the conversation a fresh
 * load (#1088, issue #1099).
 */

import { describe, expect, it } from "vitest";

import { nextFocusLoad, type FocusLoad } from "./useMessageLoading";

const start: FocusLoad = { messageId: "", request: "", generation: 0 };

describe("nextFocusLoad", () => {
  it("keeps the same object while nothing changed, so render settles", () => {
    expect(nextFocusLoad(start, "", "", false)).toBe(start);
  });

  it("reloads for a new target", () => {
    expect(nextFocusLoad(start, "m-old", "k1", false)).toEqual({
      messageId: "m-old",
      request: "k1",
      generation: 1,
    });
  });

  it("reloads when the target goes away, as it always has", () => {
    const focused = { messageId: "m-old", request: "k1", generation: 1 };
    expect(nextFocusLoad(focused, "", "", false).generation).toBe(2);
  });

  it("reloads when the same unreached target is asked for again", () => {
    // The earlier search ended short of it — the cap, or a page that failed —
    // and the reader asked again: that is a retry, so it searches again.
    const missed = { messageId: "m-old", request: "k1", generation: 1 };
    expect(nextFocusLoad(missed, "m-old", "k2", false).generation).toBe(2);
  });

  it("does not reload for a request whose target is already loaded", () => {
    // The timeline jumps to it on its own; reloading would throw away the pages.
    const reached = { messageId: "m-old", request: "k1", generation: 1 };
    expect(nextFocusLoad(reached, "m-old", "k2", true)).toEqual({
      messageId: "m-old",
      request: "k2",
      generation: 1,
    });
  });

  it("does not reload for a new target that is already loaded", () => {
    // A still being searched for, then B — already on screen — asked for: B
    // replaces A as the target, and the pages already loaded are kept.
    const searching = { messageId: "m-old", request: "k1", generation: 1 };
    expect(nextFocusLoad(searching, "m-loaded", "k2", true)).toEqual({
      messageId: "m-loaded",
      request: "k2",
      generation: 1,
    });
  });

  it("does not reload for an unmarked navigation that keeps the query", () => {
    // Sending a message replaces the entry and drops the mark.
    const missed = { messageId: "m-old", request: "k1", generation: 1 };
    expect(nextFocusLoad(missed, "m-old", "", false).generation).toBe(1);
  });
});
