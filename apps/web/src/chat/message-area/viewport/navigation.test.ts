/**
 * The #880 navigation decisions, exercised where they are decided.
 *
 * Every one of these is a rule the timeline used to arrive at by way of three
 * effects agreeing with each other — which is exactly why the viewport could
 * end up in a state none of them intended. Here each is a plain function over
 * a plain object, so the priority, the arrival conditions and the control's
 * two meanings can be read (and broken) one at a time.
 */

import { describe, expect, it } from "vitest";

import {
  MAX_NAVIGATION_PASSES,
  NAVIGATION_PRIORITY,
  UNREAD_ANCHOR_TOLERANCE_PX,
  UNREAD_CONTEXT_MAX_PX,
  UNREAD_CONTEXT_MIN_PX,
  isScrollbarPointer,
  movedByReader,
  navigationStep,
  scrollButtonState,
  tailConfirmed,
  unreadConfirmed,
  unreadContextOffsetPx,
  type NavigationState,
} from "./navigation";

describe("the navigation priority", () => {
  it("is deep link, then restored anchor, then unread boundary, then the tail", () => {
    expect([...NAVIGATION_PRIORITY]).toEqual([
      "MESSAGE_TARGET",
      "RESTORED_ANCHOR",
      "FIRST_UNREAD",
      "TAIL",
    ]);
  });
});

describe("the unread boundary's contextual offset", () => {
  it("leaves a proportional strip of already-read context above the separator", () => {
    expect(unreadContextOffsetPx(800)).toBe(176);
  });

  it("never collapses onto the top edge on a short viewport", () => {
    // block: "start" is what this exists to avoid: the boundary flush against
    // the top reads as the beginning of the conversation.
    expect(unreadContextOffsetPx(120)).toBe(UNREAD_CONTEXT_MIN_PX);
    expect(unreadContextOffsetPx(0)).toBe(UNREAD_CONTEXT_MIN_PX);
  });

  it("never spends half a tall window on messages already read", () => {
    expect(unreadContextOffsetPx(2400)).toBe(UNREAD_CONTEXT_MAX_PX);
  });
});

describe("confirming the tail", () => {
  it("needs the sentinel and the geometry to agree", () => {
    expect(tailConfirmed(0, true)).toBe(true);
    expect(tailConfirmed(2, true)).toBe(true);
  });

  it("refuses a sentinel that reports arrival with content still below the fold", () => {
    // The #880 capture exactly: intersecting, 214px short, operation over.
    expect(tailConfirmed(214, true)).toBe(false);
  });

  it("refuses geometry alone, which a mid-animation frame satisfies", () => {
    expect(tailConfirmed(0, false)).toBe(false);
  });
});

describe("confirming the unread boundary", () => {
  it("is not confirmed while the separator is not even mounted", () => {
    expect(unreadConfirmed(null, 176, false, true)).toBe(false);
  });

  it("accepts the separator within tolerance of its contextual offset", () => {
    expect(unreadConfirmed(176, 176, false, true)).toBe(true);
    expect(unreadConfirmed(176 + UNREAD_ANCHOR_TOLERANCE_PX, 176, false, true)).toBe(true);
    expect(unreadConfirmed(176 + UNREAD_ANCHOR_TOLERANCE_PX + 1, 176, false, true)).toBe(false);
  });

  it("accepts less context than asked for when there is no more history above", () => {
    // Nothing left to scroll: the boundary sits as low as the conversation
    // allows, and correcting further would only be a pass that cannot move.
    expect(unreadConfirmed(20, 176, true, true)).toBe(true);
    expect(unreadConfirmed(400, 176, true, true)).toBe(false);
  });

  it("refuses a reading taken while rows are still being measured", () => {
    // The separator's own box is its only authority, and a box measured in the
    // middle of estimates turning into real heights is one the next
    // measurement moves — 180px, in the case this rule comes from.
    expect(unreadConfirmed(176, 176, false, false)).toBe(false);
  });
});

function state(overrides: Partial<NavigationState> = {}): NavigationState {
  return {
    confirmed: false,
    desiredScrollTopPx: 9_000,
    currentScrollTopPx: 1_000,
    lastScrollTopPx: null,
    writtenScrollTopPx: null,
    animatingTowardPx: null,
    successorExpected: false,
    passes: 1,
    behavior: "auto",
    ...overrides,
  };
}

describe("one pass of a navigation", () => {
  it("confirms when the destination is confirmed", () => {
    expect(navigationStep(state({ confirmed: true }))).toEqual({ kind: "confirm" });
  });

  it("seeks the destination as it is now, not as it was when asked for", () => {
    expect(navigationStep(state({ desiredScrollTopPx: 12_345 }))).toEqual({
      kind: "seek",
      scrollTopPx: 12_345,
      behavior: "auto",
    });
  });

  it("carries the behavior the caller chose, so reduced motion stays honoured", () => {
    expect(navigationStep(state({ behavior: "smooth" }))).toMatchObject({ behavior: "smooth" });
  });

  it("waits while the scrollport is still moving on its own", () => {
    // An animated scroll in flight: a write here would cancel it and leave
    // nobody to finish the trip, which is the #675 rule this preserves.
    expect(navigationStep(state({ lastScrollTopPx: 900, currentScrollTopPx: 1_000 }))).toEqual({
      kind: "wait",
    });
  });

  it("does not mistake its own write landing for something else moving", () => {
    // Otherwise every instant seek would cost a wasted pass waiting for an
    // animation that is not running — and, with nothing else to produce a
    // commit, the trip would stall a step short of its destination.
    expect(
      navigationStep(
        state({
          lastScrollTopPx: 600,
          currentScrollTopPx: 1_000,
          writtenScrollTopPx: 1_000,
          desiredScrollTopPx: 1_400,
        }),
      ),
    ).toEqual({ kind: "seek", scrollTopPx: 1_400, behavior: "auto" });
  });

  it("gives up when it is already where the destination says and still is not there", () => {
    // A pass that cannot move the scrollport has no successor, so staying
    // armed would mean waiting for a commit that is never coming.
    expect(
      navigationStep(
        state({ desiredScrollTopPx: 1_000, currentScrollTopPx: 1_000, lastScrollTopPx: 1_000 }),
      ),
    ).toEqual({ kind: "abandon" });
  });

  it("waits, rather than gives up, while another pass is already on its way", () => {
    // Nothing to move this frame — but a measurement still in flight, or an
    // arrival an observer has yet to report, means the destination is not
    // finished with. Giving up here is what ends a trip in the middle.
    expect(
      navigationStep(
        state({
          desiredScrollTopPx: 1_000,
          currentScrollTopPx: 1_000,
          lastScrollTopPx: 1_000,
          successorExpected: true,
        }),
      ),
    ).toEqual({ kind: "wait" });
  });

  it("lets an animated scroll finish rather than cancelling it with a write", () => {
    // The commit that follows asking for the trip arrives before the browser
    // has moved a pixel; writing there would leave the reader wherever the
    // cancelled animation had got to, which is the defect in miniature.
    expect(
      navigationStep(
        state({ desiredScrollTopPx: 9_000, currentScrollTopPx: 1_000, animatingTowardPx: 9_000 }),
      ),
    ).toEqual({ kind: "wait" });
  });

  it("retargets an animation whose destination has moved, instantly", () => {
    expect(
      navigationStep(
        state({ desiredScrollTopPx: 9_400, currentScrollTopPx: 1_000, animatingTowardPx: 9_000 }),
      ),
    ).toEqual({ kind: "seek", scrollTopPx: 9_400, behavior: "auto" });
  });

  it("gives up when no layout can say where the destination is", () => {
    expect(navigationStep(state({ desiredScrollTopPx: null }))).toEqual({ kind: "abandon" });
  });

  it("gives up rather than hold the scrollport forever", () => {
    expect(navigationStep(state({ passes: MAX_NAVIGATION_PASSES + 1 }))).toEqual({
      kind: "abandon",
    });
  });
});

describe("a scroll the navigation did not produce", () => {
  const own = { lastScrollTopPx: 4_000, writtenScrollTopPx: 9_400, animating: false };

  it("is the reader's when the scrollport is at neither position the trip knows", () => {
    // An overlay scrollbar's drag reaches the page as this and nothing else.
    expect(movedByReader(own, 2_000)).toBe(true);
  });

  it("is the navigation's own write coming back", () => {
    expect(movedByReader(own, 9_400)).toBe(false);
    expect(movedByReader(own, 9_401)).toBe(false);
  });

  it("is the position the pass looked at, which it did not move", () => {
    expect(movedByReader(own, 4_000)).toBe(false);
  });

  it("says nothing while an animation is travelling between the two", () => {
    expect(movedByReader({ ...own, animating: true }, 2_000)).toBe(false);
  });

  it("says nothing about a trip that has neither looked nor written yet", () => {
    expect(
      movedByReader({ lastScrollTopPx: null, writtenScrollTopPx: null, animating: false }, 0),
    ).toBe(true);
  });
});

describe("a pointer on the scrollport", () => {
  it("is on the scrollbar once it is past the padding box", () => {
    // clientWidth excludes the scrollbar, so anything at or beyond it is on
    // the scrollbar and cannot be anything else.
    expect(isScrollbarPointer(720, 720)).toBe(true);
    expect(isScrollbarPointer(733, 720)).toBe(true);
  });

  it("is on the content while it is inside that box", () => {
    expect(isScrollbarPointer(0, 720)).toBe(false);
    expect(isScrollbarPointer(719, 720)).toBe(false);
  });

  it("is on the scrollbar when a right-to-left timeline puts it on the left", () => {
    expect(isScrollbarPointer(-6, 720)).toBe(true);
  });
});

describe("what the floating control offers", () => {
  const away = { awayFromTail: true, hasBoundary: true, boundaryAhead: true, unreadCount: 3 };

  it("is hidden at the end of the conversation", () => {
    expect(scrollButtonState({ ...away, awayFromTail: false }).visible).toBe(false);
  });

  it("offers the unread boundary while it is still below the reader", () => {
    expect(scrollButtonState(away)).toEqual({ visible: true, mode: "first-unread", count: 3 });
  });

  it("offers the end once the boundary has been reached or passed", () => {
    expect(scrollButtonState({ ...away, boundaryAhead: false })).toEqual({
      visible: true,
      mode: "tail",
      count: 3,
    });
  });

  it("offers the end when there is nothing unread, however much is below", () => {
    // #880 item 11: the count is unread, never "messages below the fold" —
    // a reader who scrolled up three hundred read messages has unread 0.
    expect(scrollButtonState({ ...away, unreadCount: 0 })).toEqual({
      visible: true,
      mode: "tail",
      count: 0,
    });
  });

  it("offers the end when this conversation has no boundary row at all", () => {
    expect(scrollButtonState({ ...away, hasBoundary: false }).mode).toBe("tail");
  });
});
