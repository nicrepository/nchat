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
import { NAVIGATION_PRIORITY } from "./navigation";
import {
  decideOpenPositionResolution,
  resolveOpenPosition,
  targetFor,
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
    searchPending: false,
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
/**
 * #880 item 14: the priority is a list, and this is the only place that
 * applies it. Rather than restate the order, each case removes the strongest
 * intent available and asserts the next one down wins — so the day the list
 * changes, this is what says so.
 */
describe("the navigation priority, end to end", () => {
  const everything = input({
    focusMessageId: "m2",
    initialAnchor: anchorAt("m1"),
    unreadCountAtOpen: 2,
  });

  /** The winning destination, for an input that always settles on one. */
  function targetOf(candidate: OpenPositionInput) {
    const position = resolveOpenPosition(candidate);
    if (position.kind === "need-more-history") throw new Error("expected a settled position");
    return targetFor(position);
  }

  it("walks down the list as each stronger intent is taken away", () => {
    const withoutDeepLink = { ...everything, focusMessageId: undefined };
    const withoutAnchor = { ...withoutDeepLink, initialAnchor: null };
    const withoutUnread = { ...withoutAnchor, unreadCountAtOpen: 0 };

    expect([
      targetOf(everything),
      targetOf(withoutDeepLink),
      targetOf(withoutAnchor),
      targetOf(withoutUnread),
    ]).toEqual([...NAVIGATION_PRIORITY]);
  });

  it("names the winning destination on the settled resolution", () => {
    const decision = decideOpenPositionResolution({
      ...everything,
      resolved: false,
      olderPagesSettled: 0,
      searchedAt: -1,
    });
    expect(decision).toMatchObject({ kind: "settle", target: "MESSAGE_TARGET" });
  });
});

describe("decideOpenPositionResolution", () => {
  function resolutionInput(overrides: Partial<ResolutionInput> = {}): ResolutionInput {
    return { ...input(), resolved: false, olderPagesSettled: 0, searchedAt: -1, ...overrides };
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

  it("settles on the tail with no unread, leaving AT_BOTTOM to the confirmed arrival", () => {
    expect(decideOpenPositionResolution(resolutionInput())).toEqual({
      kind: "settle",
      firstUnreadMessageId: null,
      target: "TAIL",
      scrollTarget: { messageId: null },
      phase: "RESTORING_POSITION",
    });
  });

  it("settles on the unread boundary in AT_FIRST_UNREAD", () => {
    expect(decideOpenPositionResolution(resolutionInput({ unreadCountAtOpen: 2 }))).toEqual({
      kind: "settle",
      firstUnreadMessageId: "m2",
      target: "FIRST_UNREAD",
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
      target: "RESTORED_ANCHOR",
      scrollTarget: { messageId: "m2" },
      phase: "READING_HISTORY",
    });
  });

  it("leaves a deep link to position itself, with no scroll target of its own", () => {
    expect(decideOpenPositionResolution(resolutionInput({ focusMessageId: "m2" }))).toEqual({
      kind: "settle",
      firstUnreadMessageId: null,
      target: "MESSAGE_TARGET",
      scrollTarget: undefined,
      phase: "READING_HISTORY",
    });
  });

  it("moves from need-more-history to settled once the page arrives", () => {
    // First render: the anchored message is older than the loaded window.
    const before = resolutionInput({ initialAnchor: anchorAt("m0"), hasMore: true });
    const search = decideOpenPositionResolution(before);
    expect(search).toEqual({ kind: "search", searchedAt: 0 });

    // The page arrives: the same anchor is now loaded, and the decision
    // settles on it rather than asking for more.
    const after = decideOpenPositionResolution({
      ...before,
      messages: [message("m0"), ...before.messages],
      searchAttempts: 1,
      // The hook records how many pages had come back when it asked; this one
      // has come back since, so it must not stand in the way of settling.
      searchedAt: 0,
      olderPagesSettled: 1,
    });
    expect(after).toEqual({
      kind: "settle",
      firstUnreadMessageId: null,
      target: "RESTORED_ANCHOR",
      scrollTarget: { messageId: "m0" },
      phase: "READING_HISTORY",
    });
  });

  it("does not ask for the same page twice across repeated renders", () => {
    const first = resolutionInput({ initialAnchor: anchorAt("m0"), hasMore: true });
    expect(decideOpenPositionResolution(first)).toEqual({ kind: "search", searchedAt: 0 });

    // Re-rendered with the request already in flight: no page has come back
    // since it was asked for, so nothing may be requested again.
    const again = decideOpenPositionResolution({
      ...first,
      searchAttempts: 1,
      searchedAt: 0,
    });
    expect(again).toEqual({ kind: "wait" });
  });

  it("settles rather than searching once the page cap is spent", () => {
    const decision = decideOpenPositionResolution(
      resolutionInput({
        initialAnchor: anchorAt("m0"),
        hasMore: true,
        searchAttempts: MAX_BOUNDARY_SEARCH_PAGES,
      }),
    );
    expect(decision).toEqual({
      kind: "settle",
      firstUnreadMessageId: null,
      target: "TAIL",
      scrollTarget: { messageId: null },
      phase: "RESTORING_POSITION",
    });
  });
});

/**
 * #1088: a deep link to a message older than the loaded window used to settle
 * at once, and the jump then had nothing to travel to. It now takes part in the
 * same bounded backward search as an anchor or an unread boundary.
 *
 * The search's clock is olderPagesSettled: a page counts once it has come
 * back, whatever it held — so the last page the cap allows is still awaited,
 * and a page of duplicates or a cursor that did not move cannot stall it.
 */
describe("MESSAGE_TARGET outside the loaded window (#1088)", () => {
  function resolutionInput(overrides: Partial<ResolutionInput> = {}): ResolutionInput {
    return {
      ...input({ focusMessageId: "m-old", hasMore: true }),
      resolved: false,
      olderPagesSettled: 0,
      searchedAt: -1,
      ...overrides,
    };
  }

  /** The render after the page asked for as attempt `attempt` has come back. */
  function afterPage(attempt: number, overrides: Partial<ResolutionInput> = {}) {
    return resolutionInput({
      searchAttempts: attempt,
      searchedAt: attempt - 1,
      olderPagesSettled: attempt,
      ...overrides,
    });
  }

  const withTarget = [message("m-old"), ...input().messages];

  it("settles on a loaded target without asking for another page", () => {
    expect(decideOpenPositionResolution(resolutionInput({ focusMessageId: "m1" }))).toMatchObject({
      kind: "settle",
      target: "MESSAGE_TARGET",
    });
  });

  it("keeps searching while the target is missing, ahead of a loaded anchor and unread", () => {
    const position = resolveOpenPosition(
      input({
        focusMessageId: "m-old",
        hasMore: true,
        initialAnchor: anchorAt("m2"),
        unreadCountAtOpen: 1,
      }),
    );
    expect(position).toEqual({ kind: "need-more-history" });
  });

  it("settles on the target found on an intermediate page", () => {
    const decision = decideOpenPositionResolution(afterPage(3, { messages: withTarget }));
    expect(decision).toMatchObject({ kind: "settle", target: "MESSAGE_TARGET" });
  });

  it("waits for the last page the cap allows instead of falling back while it is on its way", () => {
    const pending = resolutionInput({
      searchAttempts: MAX_BOUNDARY_SEARCH_PAGES,
      searchedAt: MAX_BOUNDARY_SEARCH_PAGES - 1,
      olderPagesSettled: MAX_BOUNDARY_SEARCH_PAGES - 1,
    });
    expect(decideOpenPositionResolution(pending)).toEqual({ kind: "wait" });
  });

  it("settles on the target found exactly on the last page the cap allows", () => {
    const decision = decideOpenPositionResolution(
      afterPage(MAX_BOUNDARY_SEARCH_PAGES, { messages: withTarget }),
    );
    expect(decision).toMatchObject({ kind: "settle", target: "MESSAGE_TARGET" });
  });

  it("falls back only once that last page has come back without the target", () => {
    const decision = decideOpenPositionResolution(afterPage(MAX_BOUNDARY_SEARCH_PAGES));
    expect(decision).toMatchObject({ kind: "settle", target: "TAIL" });
  });

  it("asks for the next page after one that only repeated loaded messages", () => {
    // Same messages, same length — but a page came back and the cursor moved.
    expect(decideOpenPositionResolution(afterPage(2))).toEqual({ kind: "search", searchedAt: 2 });
  });

  it("asks for the next page after an empty one too, within the same cap", () => {
    expect(decideOpenPositionResolution(afterPage(4))).toEqual({ kind: "search", searchedAt: 4 });
  });

  it("ends at the cap when the cursor never moves, rather than asking forever", () => {
    // A page that comes back without moving the cursor still spends one of the
    // cap's pages, so a stuck cursor is asked for at most that many times.
    const asked = Array.from({ length: MAX_BOUNDARY_SEARCH_PAGES }, (_, attempt) =>
      decideOpenPositionResolution(afterPage(attempt)),
    );
    expect(asked.map((decision) => decision.kind)).toEqual(
      Array(MAX_BOUNDARY_SEARCH_PAGES).fill("search"),
    );
    expect(decideOpenPositionResolution(afterPage(MAX_BOUNDARY_SEARCH_PAGES))).toMatchObject({
      kind: "settle",
      target: "TAIL",
    });
  });

  it("never asks twice while a page is still on its way", () => {
    const inFlight = resolutionInput({ searchAttempts: 1, searchedAt: 0 });
    expect(decideOpenPositionResolution(inFlight)).toEqual({ kind: "wait" });
  });

  it("falls back down the priority when the whole history lacks the target", () => {
    // Removed, inaccessible or never existed — deliberately the same answer.
    expect(
      resolveOpenPosition(input({ focusMessageId: "m-gone", initialAnchor: anchorAt("m2") })),
    ).toEqual({ kind: "anchor", messageId: "m2" });
    expect(resolveOpenPosition(input({ focusMessageId: "m-gone", unreadCountAtOpen: 1 }))).toEqual({
      kind: "first-unread",
      messageId: "m3",
    });
    expect(resolveOpenPosition(input({ focusMessageId: "m-gone" }))).toEqual({ kind: "bottom" });
  });

  it("does not wait on a page once the history has ended", () => {
    const ended = resolutionInput({ hasMore: false, searchAttempts: 1, searchedAt: 0 });
    expect(decideOpenPositionResolution(ended)).toMatchObject({ kind: "settle", target: "TAIL" });
  });
});
