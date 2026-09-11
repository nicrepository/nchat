/**
 * Characterization tests for where a conversation opens (#492 A/B/C).
 *
 * These freeze the behaviour the viewport had before issue #834 moved the
 * decision out of ChatMessageArea — they are not a new specification. Every
 * ending the resolution can reach is exercised from a plain object here, which
 * is the whole point of the decision being pure: the rendered-timeline tests in
 * ChatMessageArea.test.tsx still cover the scrolling that follows from it.
 */

import { describe, expect, it } from "vitest";

import { MAX_BOUNDARY_SEARCH_PAGES } from "../../chatViewportState";
import type { Message } from "../../chatTypes";
import {
  decideOpenPositionResolution,
  resolveOpenPosition,
  type OpenPositionInput,
  type ResolutionInput,
} from "./openPosition";

const ME = "u-me";
const THEM = "u-them";

function message(id: string, senderId = THEM, status = "active"): Message {
  return { id, senderId, status } as unknown as Message;
}

function input(overrides: Partial<OpenPositionInput> = {}): OpenPositionInput {
  return {
    messages: [message("m1"), message("m2"), message("m3")],
    currentUserId: ME,
    hasMore: false,
    unreadCountAtOpen: 0,
    initialAnchor: null,
    searchAttempts: 0,
    ...overrides,
  };
}

function anchorAt(anchorMessageId: string | null, atBottom = false) {
  return { atBottom, anchorMessageId, anchorOffsetPx: 0, savedAt: 0 };
}

describe("resolveOpenPosition", () => {
  it("opens at the bottom when there is no anchor and nothing unread", () => {
    expect(resolveOpenPosition(input())).toEqual({ kind: "bottom" });
  });

  it("lets a deep link outrank a saved anchor and an unread boundary", () => {
    const position = resolveOpenPosition(
      input({
        focusMessageId: "m2",
        initialAnchor: anchorAt("m1"),
        unreadCountAtOpen: 2,
      }),
    );
    expect(position).toEqual({ kind: "deep-link" });
  });

  it("restores a saved anchor that is in the loaded window", () => {
    const position = resolveOpenPosition(input({ initialAnchor: anchorAt("m2") }));
    expect(position).toEqual({ kind: "anchor", messageId: "m2" });
  });

  it("ignores an anchor that was saved at the bottom", () => {
    const position = resolveOpenPosition(input({ initialAnchor: anchorAt(null, true) }));
    expect(position).toEqual({ kind: "bottom" });
  });

  it("asks for more history when the saved anchor is not loaded yet", () => {
    const position = resolveOpenPosition(
      input({ initialAnchor: anchorAt("m-old"), hasMore: true }),
    );
    expect(position).toEqual({ kind: "need-more-history" });
  });

  it("falls back rather than guessing when the anchored message is gone", () => {
    // Whole history loaded, and the message it named is not in it.
    const position = resolveOpenPosition(
      input({ initialAnchor: anchorAt("m-deleted"), hasMore: false }),
    );
    expect(position).toEqual({ kind: "bottom" });
  });

  it("stops searching for the anchor at the page cap", () => {
    const position = resolveOpenPosition(
      input({
        initialAnchor: anchorAt("m-old"),
        hasMore: true,
        searchAttempts: MAX_BOUNDARY_SEARCH_PAGES,
      }),
    );
    expect(position).toEqual({ kind: "bottom" });
  });

  it("opens at the first unread message", () => {
    // Two unread means the boundary is the second-newest eligible message.
    const position = resolveOpenPosition(input({ unreadCountAtOpen: 2 }));
    expect(position).toEqual({ kind: "first-unread", messageId: "m2" });
  });

  it("counts only active messages from other people as unread", () => {
    const position = resolveOpenPosition(
      input({
        messages: [message("m1"), message("m2", ME), message("m3", THEM, "removed"), message("m4")],
        unreadCountAtOpen: 2,
      }),
    );
    expect(position).toEqual({ kind: "first-unread", messageId: "m1" });
  });

  it("asks for more history when the unread boundary is not loaded yet", () => {
    const position = resolveOpenPosition(input({ unreadCountAtOpen: 99, hasMore: true }));
    expect(position).toEqual({ kind: "need-more-history" });
  });

  it("anchors at the oldest loaded message when the unread count outruns history", () => {
    // A stale count or a race: the whole history is loaded and still shorter
    // than unreadCountAtOpen, so the oldest message is the safe boundary.
    const position = resolveOpenPosition(input({ unreadCountAtOpen: 99, hasMore: false }));
    expect(position).toEqual({ kind: "first-unread", messageId: "m1" });
  });

  it("gives up on the unread boundary at the page cap rather than guessing one", () => {
    const position = resolveOpenPosition(
      input({
        unreadCountAtOpen: 99,
        hasMore: true,
        searchAttempts: MAX_BOUNDARY_SEARCH_PAGES,
      }),
    );
    expect(position).toEqual({ kind: "bottom" });
  });

  it("prefers a loaded saved anchor over an unread boundary", () => {
    const position = resolveOpenPosition(
      input({ initialAnchor: anchorAt("m3"), unreadCountAtOpen: 2 }),
    );
    expect(position).toEqual({ kind: "anchor", messageId: "m3" });
  });
});

/**
 * Characterization for what a given render has to do about the opening
 * position — the guard that stops a bounded search from asking twice for the
 * same page included, which is what issue #834's review asked to see proved.
 */
describe("decideOpenPositionResolution", () => {
  function resolutionInput(overrides: Partial<ResolutionInput> = {}): ResolutionInput {
    return { ...input(), resolved: false, searchedForLength: -1, ...overrides };
  }

  it("does nothing once the position has already been settled", () => {
    expect(decideOpenPositionResolution(resolutionInput({ resolved: true }))).toEqual({
      kind: "wait",
    });
  });

  it("does nothing while no messages have loaded", () => {
    expect(decideOpenPositionResolution(resolutionInput({ messages: [] }))).toEqual({
      kind: "wait",
    });
  });

  it("settles at the bottom, in AT_BOTTOM, with no unread", () => {
    expect(decideOpenPositionResolution(resolutionInput())).toEqual({
      kind: "settle",
      firstUnreadMessageId: null,
      scrollTarget: { messageId: null },
      phase: "AT_BOTTOM",
    });
  });

  it("settles on the unread boundary in AT_FIRST_UNREAD", () => {
    expect(decideOpenPositionResolution(resolutionInput({ unreadCountAtOpen: 2 }))).toEqual({
      kind: "settle",
      firstUnreadMessageId: "m2",
      scrollTarget: { messageId: "m2" },
      phase: "AT_FIRST_UNREAD",
    });
  });

  it("settles a saved anchor in READING_HISTORY, and names no unread", () => {
    expect(
      decideOpenPositionResolution(resolutionInput({ initialAnchor: anchorAt("m2") })),
    ).toEqual({
      kind: "settle",
      firstUnreadMessageId: null,
      scrollTarget: { messageId: "m2" },
      phase: "READING_HISTORY",
    });
  });

  it("leaves a deep link to position itself, with no scroll target of its own", () => {
    expect(decideOpenPositionResolution(resolutionInput({ focusMessageId: "m2" }))).toEqual({
      kind: "settle",
      firstUnreadMessageId: null,
      scrollTarget: undefined,
      phase: "READING_HISTORY",
    });
  });

  it("moves from need-more-history to settled once the page arrives", () => {
    // First render: the anchored message is older than the loaded window.
    const before = resolutionInput({ initialAnchor: anchorAt("m0"), hasMore: true });
    const search = decideOpenPositionResolution(before);
    expect(search).toEqual({ kind: "search", searchedLength: 3 });

    // The page arrives: the same anchor is now loaded, and the decision
    // settles on it rather than asking for more.
    const after = decideOpenPositionResolution({
      ...before,
      messages: [message("m0"), ...before.messages],
      searchAttempts: 1,
      // The hook records the length that asked for the page; the new page made
      // the window longer, so this must not stand in the way of settling.
      searchedForLength: (search as { searchedLength: number }).searchedLength,
    });
    expect(after).toEqual({
      kind: "settle",
      firstUnreadMessageId: null,
      scrollTarget: { messageId: "m0" },
      phase: "READING_HISTORY",
    });
  });

  it("does not ask for the same page twice across repeated renders", () => {
    const first = resolutionInput({ initialAnchor: anchorAt("m0"), hasMore: true });
    expect(decideOpenPositionResolution(first)).toEqual({ kind: "search", searchedLength: 3 });

    // Re-rendered with the request already in flight: same messages, so the
    // window has not advanced and nothing may be requested again.
    const again = decideOpenPositionResolution({
      ...first,
      searchAttempts: 1,
      searchedForLength: 3,
    });
    expect(again).toEqual({ kind: "wait" });
  });

  it("settles rather than searching once the page cap is spent", () => {
    const decision = decideOpenPositionResolution(
      resolutionInput({
        initialAnchor: anchorAt("m0"),
        hasMore: true,
        searchAttempts: MAX_BOUNDARY_SEARCH_PAGES,
        searchedForLength: -1,
      }),
    );
    expect(decision).toEqual({
      kind: "settle",
      firstUnreadMessageId: null,
      scrollTarget: { messageId: null },
      phase: "AT_BOTTOM",
    });
  });
});
