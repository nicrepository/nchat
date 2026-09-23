/**
 * The one thing allowed to drive a programmatic navigation of the timeline
 * (issue #880).
 *
 * Before this, "take me to the end" was a single scrollIntoView call and a
 * hope: the tail-lock corrected for content that grew afterwards, the prepend
 * restoration could still be armed, and the virtualizer compensated for rows it
 * measured on the way — three writers on one scrollTop, none of them
 * responsible for the trip finishing. A DEV capture of the defect (see
 * docs/reviews or the PR body) has all three in eleven frames, ending 238px
 * short of the tail with the button hidden, because the sentinel confirmed
 * arrival against a canvas whose height had not caught up and a restoration
 * then put a stale anchor back.
 *
 * So the navigation became an object with an owner. While core.navigationRef
 * holds one, nothing else writes scrollTop; each pass re-derives where the
 * destination *now* is and moves there; and the operation ends only when the
 * destination is confirmed, when it cannot progress at all, or when something
 * stronger cancels it — never because a scroll command returned.
 *
 * Passes are driven by real layout events and nothing else: the commits the
 * virtualizer produces when it measures, the content ResizeObserver, scroll
 * events, and the bottom sentinel. No timers, no polling, no frame budget.
 */

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import { UNREAD_DIVIDER_KEY } from "../../timelineVirtualization";
import {
  isScrollbarPointer,
  navigationStep,
  tailConfirmed,
  unreadConfirmed,
  unreadContextOffsetPx,
  type DrivenTarget,
  type NavigationReason,
} from "./navigation";
import { explicitScrollBehavior, prependScrollTopFor, readingPositionOf } from "./scrollCommands";
import type { Navigation, ViewportCore } from "./useViewportCore";

export interface NavigatorParams {
  core: ViewportCore;
  /** `${kind}:${targetId}` — a navigation never survives a change of it. */
  conversationKey: string;
  /** Run when the tail is confirmed: the read-state side of arriving. */
  onTailArrived: () => void;
}

export interface NavigatorState {
  /** Takes the reader to the end, and stays responsible until it is reached. */
  navigateToTail: (reason: NavigationReason) => void;
  /** Takes the reader to the unread boundary, with context above it. */
  navigateToFirstUnread: (reason: NavigationReason) => void;
  /** One more pass, for an observer that saw the layout move. */
  requestPass: () => void;
  /** The reader took over: end the trip and adopt where they are. */
  yieldToReader: () => void;
}

/** Where the scrollport has to be, and whether it already is. */
interface Destination {
  desiredScrollTopPx: number | null;
  confirmed: boolean;
}

/** The distance still to travel, in the only terms that are not a guess. */
function remainingPx(el: HTMLDivElement): number {
  return el.scrollHeight - el.scrollTop - el.clientHeight;
}

/**
 * The end of the conversation, as the current layout has it.
 *
 * Deliberately the scrollport's own geometry rather than the virtualizer's
 * total size: the scrollport is what the reader sees, and it already includes
 * everything the canvas does not (the pagination sentinel, the loading row, the
 * bottom sentinel). Under virtualization this answer is as good as the
 * estimates it still contains — which is exactly why it is re-derived on every
 * pass instead of being computed once when the reader asked.
 */
function tailDestination(core: ViewportCore, el: HTMLDivElement): Destination {
  return {
    desiredScrollTopPx: Math.max(0, el.scrollHeight - el.clientHeight),
    // Without an IntersectionObserver there is no sentinel to be authoritative
    // — the same capability check the rest of the viewport degrades through.
    // The geometry then answers alone, which is what it did before the
    // sentinel existed at all.
    confirmed: tailConfirmed(
      remainingPx(el),
      core.tailSentinelVisibleRef.current || typeof IntersectionObserver === "undefined",
    ),
  };
}

/**
 * The unread boundary, with a little already-read context above it (#880 8).
 *
 * Two ways to find it, in this order: the separator's own box while it is
 * mounted, and the row model while it is not. The second is how a boundary
 * hundreds of rows above the mounted window is reached without loading the
 * whole history — and the first is what corrects the estimates it was placed
 * from, once the row exists and can be measured.
 */
function unreadDestination(
  core: ViewportCore,
  el: HTMLDivElement,
  layoutSettled: boolean,
): Destination {
  const contextOffsetPx = unreadContextOffsetPx(el.clientHeight);
  const node = core.unreadDividerRef.current;
  const measuredOffsetPx = node
    ? node.getBoundingClientRect().top - el.getBoundingClientRect().top
    : null;
  const index = core.rowIndexRef.current.get(UNREAD_DIVIDER_KEY);
  const virtualizer = core.virtualizerRef.current;
  let desiredScrollTopPx: number | null = null;
  if (measuredOffsetPx !== null) {
    desiredScrollTopPx = Math.max(0, el.scrollTop + measuredOffsetPx - contextOffsetPx);
  } else if (virtualizer && index !== undefined) {
    desiredScrollTopPx = prependScrollTopFor(virtualizer, index, contextOffsetPx);
  }
  return {
    desiredScrollTopPx,
    confirmed: unreadConfirmed(measuredOffsetPx, contextOffsetPx, el.scrollTop <= 0, layoutSettled),
  };
}

/** Whether a trip to the end moved the scrollport and has yet to hear it arrived. */
function awaitingSentinel(navigation: Navigation, confirmed: boolean): boolean {
  return navigation.target === "TAIL" && !confirmed && navigation.writtenScrollTopPx !== null;
}

/** Whether the content stopped changing size since the previous pass. */
function layoutSettled(navigation: Navigation, el: HTMLDivElement): boolean {
  return (
    navigation.lastScrollHeightPx === null || navigation.lastScrollHeightPx === el.scrollHeight
  );
}

function destinationOf(
  core: ViewportCore,
  el: HTMLDivElement,
  navigation: Navigation,
  settled: boolean,
): Destination {
  if (navigation.target === "TAIL") return tailDestination(core, el);
  return unreadDestination(core, el, settled);
}

/**
 * Moves the scrollport, animated only when the caller asked for it, and says
 * whether it actually moved.
 *
 * An instant write that changes nothing is the end of the road — it produced
 * no scroll event, so it has no successor, and staying armed would mean
 * waiting for a commit that is never coming. (An animated one has not moved
 * *yet*, which is a different thing, so it always counts as movement.) This is
 * the same termination rule PREPEND_RESTORE's moveOrFinish carries.
 */
function seek(el: HTMLDivElement, scrollTopPx: number, behavior: ScrollBehavior): boolean {
  if (behavior === "smooth" && typeof el.scrollTo === "function") {
    el.scrollTo({ top: scrollTopPx, behavior });
    return true;
  }
  const before = el.scrollTop;
  el.scrollTop = scrollTopPx;
  return el.scrollTop !== before;
}

/**
 * The viewport's semantic state, re-read from the scrollport after a
 * navigation ends without arriving: the phase and the two refs the tail-follow
 * steady state runs on, all from the same reading. One transition for every
 * way a trip can end short — given up, or taken over by the reader.
 */
function adoptReadingPosition(core: ViewportCore, el: HTMLDivElement): void {
  const reading = readingPositionOf(el);
  core.recordScrollEvent(el.scrollHeight, reading);
  core.setPhase(reading.nearBottom ? "AT_BOTTOM" : "READING_HISTORY");
}

export function useNavigator({
  core,
  conversationKey,
  onTailArrived,
}: NavigatorParams): NavigatorState {
  const onTailArrivedRef = useRef(onTailArrived);
  useLayoutEffect(() => {
    onTailArrivedRef.current = onTailArrived;
  });

  /**
   * Ends the operation and says where that leaves the reader.
   *
   * Confirmed or given up, the phase stops describing a trip in progress: it
   * describes the position the scrollport is actually in, read from the
   * geometry rather than from what the navigation had hoped for. A navigation
   * that cannot progress is therefore visible — the control stays on screen
   * when the reader is still up in the history — instead of leaving the
   * viewport in a state nothing owns.
   */
  const settle = useCallback(
    (navigation: Navigation, el: HTMLDivElement, confirmed: boolean) => {
      if (!core.endNavigation(navigation)) return;
      if (confirmed) {
        if (navigation.target === "TAIL") onTailArrivedRef.current();
        else core.setPhase("AT_FIRST_UNREAD");
        return;
      }
      // Given up. Only a trip to the end leaves behind a phase that describes
      // a trip in progress, so only that one has to be replaced — with what
      // the geometry says, never with what the navigation had hoped for. A
      // boundary trip that could not finish leaves the reader exactly where
      // they were, which their phase already describes.
      //
      // Decided from the navigation's own target rather than from the phase:
      // a trip that fails on its first pass does so in the same tick that
      // asked for it, before React has committed SCROLLING_TO_BOTTOM at all,
      // and reading the phase there would leave the viewport describing a trip
      // that is already over.
      if (navigation.target === "TAIL") adoptReadingPosition(core, el);
    },
    [core],
  );

  /**
   * The reader took over (#880): a wheel, a touch or a key is a stronger intent
   * than any trip in flight. A complete transition, not just a release — the
   * generation ends, ownership goes back, and the phase and the follow-the-tail
   * refs are derived from where the scrollport really is right now, so nothing
   * waits on a scroll event that may never come.
   */
  const yieldToReader = useCallback(() => {
    const navigation = core.navigationRef.current;
    const el = core.listRef.current;
    if (!navigation || !el || !core.endNavigation(navigation)) return;
    adoptReadingPosition(core, el);
  }, [core]);

  /**
   * One pass. Every caller is a layout event that may have moved the
   * destination — never a timer, and never a retry of its own.
   */
  const runPass = useCallback(() => {
    const navigation = core.navigationRef.current;
    if (!navigation) return;
    const el = core.listRef.current;
    // A navigation whose conversation has been left is over: a late observer
    // from the previous conversation must never move the new one.
    if (!el || navigation.conversationKey !== conversationKey) {
      core.endNavigation(navigation);
      return;
    }
    const passes = core.countNavigationPass();
    const settled = layoutSettled(navigation, el);
    const destination = destinationOf(core, el, navigation, settled);
    const step = navigationStep({
      confirmed: destination.confirmed,
      // The sentinel reports arrival on its own schedule, a frame or more
      // after the scrollport lands: a pass that finds nothing left to move
      // while that report is outstanding is early, not finished. A report is
      // only outstanding if this navigation moved the scrollport, though — one
      // that never had anything to move has no report coming, and waiting for
      // it would hold the viewport for nothing.
      successorExpected: !settled || awaitingSentinel(navigation, destination.confirmed),
      desiredScrollTopPx: destination.desiredScrollTopPx,
      currentScrollTopPx: el.scrollTop,
      lastScrollTopPx: navigation.lastScrollTopPx,
      writtenScrollTopPx: navigation.writtenScrollTopPx,
      animatingTowardPx: navigation.animating ? navigation.writtenScrollTopPx : null,
      passes,
      behavior: passes === 1 && navigation.reason !== "open" ? explicitScrollBehavior(el) : "auto",
    });
    core.noteNavigationPosition(el.scrollTop, el.scrollHeight);
    if (step.kind === "seek") {
      core.noteNavigationWrite(step.scrollTopPx, step.behavior === "smooth");
      if (!seek(el, step.scrollTopPx, step.behavior)) settle(navigation, el, false);
    } else if (step.kind !== "wait") {
      settle(navigation, el, step.kind === "confirm");
    }
  }, [core, conversationKey, settle]);

  const navigate = useCallback(
    (target: DrivenTarget, reason: NavigationReason) => {
      const el = core.listRef.current;
      if (!el) return;
      core.beginNavigation(target, reason, conversationKey);
      // #880 item 1: the phase says who owns the scrollport, so a trip to the
      // end says so before the first pixel moves. A trip to the boundary keeps
      // the reader's current phase — it is not a return to the present, and
      // the control must keep offering it until it is reached.
      //
      // Except when opening: the conversation's first position is established
      // under RESTORING_POSITION, which keeps the control hidden (#492: no
      // flash of "Ir para o final" on a conversation that opens at the end)
      // while still not claiming AT_BOTTOM before the arrival is confirmed.
      if (target === "TAIL" && reason !== "open") core.setPhase("SCROLLING_TO_BOTTOM");
      runPass();
    },
    [core, conversationKey, runPass],
  );

  const navigateToTail = useCallback(
    (reason: NavigationReason) => navigate("TAIL", reason),
    [navigate],
  );
  const navigateToFirstUnread = useCallback(
    (reason: NavigationReason) => navigate("FIRST_UNREAD", reason),
    [navigate],
  );

  useNavigationPasses(core, runPass);
  useNavigationCancellation(core, conversationKey, yieldToReader);

  // Stable while its callbacks are: consumers take these as effect
  // dependencies, and a new object each render would re-run them all.
  return useMemo(
    () => ({ navigateToTail, navigateToFirstUnread, requestPass: runPass, yieldToReader }),
    [navigateToTail, navigateToFirstUnread, runPass, yieldToReader],
  );
}

/**
 * Every event that can have moved the destination, and nothing else.
 *
 * - every commit, which is how a row being measured is noticed (the
 *   virtualizer re-renders on measurement, exactly as PREPEND_RESTORE relies
 *   on);
 * - the content's own resize, which is how late media and attachments arrive;
 * - the bottom sentinel, which is what confirms a trip to the end.
 *
 * Deliberately NOT the scroll event. Every one of the three above is a report
 * that the *destination* may have moved, which is the only thing a pass has an
 * answer for; a scroll event says the scrollport moved, which during a trip is
 * usually this navigation's own write coming back. Writing from inside one
 * would also mean writing scrollTop from within a scroll dispatch, which is
 * how the two-writer cascades this issue is about get started.
 */
function useNavigationPasses(core: ViewportCore, runPass: () => void) {
  const runPassRef = useRef(runPass);
  useLayoutEffect(() => {
    runPassRef.current = runPass;
  });

  // No dependency list: while a navigation is armed, every commit is a pass.
  useLayoutEffect(() => {
    if (core.navigationRef.current) runPassRef.current();
  });

  useEffect(() => {
    const content = core.contentRef.current;
    if (!content || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => {
      if (core.navigationRef.current) runPassRef.current();
    });
    observer.observe(content);
    return () => observer.disconnect();
  }, [core]);
}

/**
 * What cancels a navigation: the reader taking over, and leaving.
 *
 * A wheel, a touch, a key or a hand on the scrollbar is an intent of the
 * reader's own, and it outranks a trip they asked for a moment ago —
 * continuing to correct against it would be the viewport fighting the person
 * using it. Leaving the conversation ends it outright, before any pass can
 * write into a timeline that is now showing somebody else's messages.
 *
 * The scrollbar is the one of those that announces itself only as a pointer:
 * dragging it produces scroll events indistinguishable from the navigation's
 * own writes, so waiting for one would mean guessing. `pointerdown` is the
 * moment the reader takes hold, before a pixel has moved — and it is theirs
 * only when the scrollport itself is the target (a message, a button or a
 * selection inside the timeline targets that element instead) and the pointer
 * is past the padding box, where nothing but the scrollbar exists.
 */
function useNavigationCancellation(
  core: ViewportCore,
  conversationKey: string,
  yieldToReader: () => void,
) {
  useEffect(() => {
    const el = core.listRef.current;
    if (!el) return;
    const onPointerDown = (event: PointerEvent) => {
      if (event.target !== el) return;
      if (isScrollbarPointer(event.offsetX, el.clientWidth)) yieldToReader();
    };
    el.addEventListener("wheel", yieldToReader, { passive: true });
    el.addEventListener("touchstart", yieldToReader, { passive: true });
    el.addEventListener("keydown", yieldToReader);
    el.addEventListener("pointerdown", onPointerDown, { passive: true });
    return () => {
      el.removeEventListener("wheel", yieldToReader);
      el.removeEventListener("touchstart", yieldToReader);
      el.removeEventListener("keydown", yieldToReader);
      el.removeEventListener("pointerdown", onPointerDown);
    };
  }, [core, yieldToReader]);

  useEffect(() => {
    return () => {
      const navigation = core.navigationRef.current;
      if (navigation) core.endNavigation(navigation);
    };
  }, [core, conversationKey]);
}

/**
 * Whether the unread boundary is still below the reader (#880 item 10).
 *
 * What decides which of its two meanings the single floating control carries,
 * and it is a question about geometry rather than about the phase: a reader
 * who opened on the boundary and then scrolled up is back above it, and the
 * control has to offer it again.
 *
 * Measured from the separator's own box while it is mounted, and from the row
 * model while it is not — one rect per scroll event, never a scan.
 */
export function useUnreadBoundary(
  core: ViewportCore,
  firstUnreadMessageId: string | null,
): boolean {
  const [ahead, setAhead] = useState(false);

  useEffect(() => {
    const el = core.listRef.current;
    if (!el || !firstUnreadMessageId) return;
    // Scroll events alone, and they are enough: every way the viewport can end
    // up somewhere — the reader, a navigation, a restoration, the opening
    // positioning — moves the scrollport, and moving it is what a scroll event
    // is. Nothing here has to run on a commit that changed no position.
    const read = () => {
      const node = core.unreadDividerRef.current;
      if (node) {
        const offsetPx = node.getBoundingClientRect().top - el.getBoundingClientRect().top;
        setAhead(offsetPx > el.clientHeight);
        return;
      }
      const dividerIndex = core.rowIndexRef.current.get(UNREAD_DIVIDER_KEY);
      const anchorId = core.currentAnchorRef.current?.messageId;
      const anchorIndex = anchorId ? core.rowIndexRef.current.get(anchorId) : undefined;
      if (dividerIndex !== undefined && anchorIndex !== undefined)
        setAhead(dividerIndex > anchorIndex);
    };
    el.addEventListener("scroll", read, { passive: true });
    return () => el.removeEventListener("scroll", read);
  }, [core, firstUnreadMessageId]);

  return ahead;
}
