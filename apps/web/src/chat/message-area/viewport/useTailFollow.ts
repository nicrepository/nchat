/**
 * Following the tail, and knowing when the reader stopped (#492/#788 — moved
 * out of ChatMessageArea, issue #834).
 *
 * Three DOM facts, one intent. The scroll handler reads where the reader is,
 * the bottom sentinel confirms when the *real* tail is on screen, and a
 * ResizeObserver keeps the viewport pinned there through an async reflow. What
 * they agree on is followTailRef: the intent to stay at the end, which is not
 * the same thing as the geometry of currently being at it.
 */

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

import { isNearBottom, TAIL_EPSILON_PX, type ViewportPhase } from "../../chatViewportState";
import type { LastMutation } from "../../useMessages";
import type { Message } from "../../chatTypes";
import { computeTopmostVisible, explicitScrollBehavior, scrollToBottom } from "./scrollCommands";
import { decideTailMutation } from "./tailMutation";
import type { ReadingPosition, ViewportCore } from "./useViewportCore";

export interface TailFollowState {
  /** #492: unread messages that arrived while the reader was not at the tail. */
  pendingCount: number;
  /** The floating "Ir para o final" action's handler. */
  scrollToBottomNow: () => void;
}

interface Params {
  core: ViewportCore;
  phase: ViewportPhase;
  messages: Message[];
  currentUserId: string;
  lastMutation: LastMutation;
  /** Whether the opening position has been decided; mutations wait for it. */
  resolved: boolean;
  /** Called once the bottom sentinel confirms the real tail was reached. */
  onReachedBottom: () => void;
}

export function useTailFollow({
  core,
  phase,
  messages,
  currentUserId,
  lastMutation,
  resolved,
  onReachedBottom,
}: Params): TailFollowState {
  const [pendingCount, setPendingCount] = useState(0);
  const [countedMessages, setCountedMessages] = useState(messages);
  const [scrollAnimationRequest, setScrollAnimationRequest] = useState(0);

  useScrollTracking(core);
  useBottomConfirmation(core, setPendingCount, onReachedBottom);
  useTailLock(core);

  // #492: reacts to a new message array. The identity comparison is what makes
  // it fire exactly once per message even across repeated "ws_append"/"append"
  // values — see decideTailMutation, which answers what the arrival means.
  // Kept out of an effect for the same set-state-in-effect reason as the
  // open-position resolution; setCountedMessages is what closes the window, so
  // a re-render with no new array does nothing at all.
  if (countedMessages !== messages) {
    setCountedMessages(messages);
    const response = decideTailMutation({
      messages,
      currentUserId,
      lastMutation,
      phase,
      resolved,
    });
    if (response.kind === "count-unread") {
      setPendingCount((count) => count + 1);
    } else if (response.kind === "return-to-bottom") {
      // setPhase is idempotent (useState's setter bails out on an equal value),
      // so it is applied unconditionally rather than re-checking the phase the
      // decision already took into account.
      core.setPhase("SCROLLING_TO_BOTTOM");
      setScrollAnimationRequest((n) => n + 1);
    }
  }

  // Consumes an own-send's animated-scroll request (set during render above)
  // — a plain DOM operation, no setState of its own.
  useEffect(() => {
    if (scrollAnimationRequest > 0) {
      scrollToBottom(core.bottomRef, explicitScrollBehavior(core.listRef.current));
    }
  }, [core, scrollAnimationRequest]);

  // Real event handler (button onClick) — calling setState here is completely
  // ordinary, not an effect.
  const scrollToBottomNow = useCallback(() => {
    core.setPhase("SCROLLING_TO_BOTTOM");
    scrollToBottom(core.bottomRef, explicitScrollBehavior(core.listRef.current));
  }, [core]);

  return { pendingCount, scrollToBottomNow };
}

/**
 * What a scroll event says about where the reader is — and, first, whether it
 * says anything at all.
 *
 * #788: a scroll event is not, by itself, evidence of anything the reader did.
 * Every correction this component makes — the tail-lock's pin, prepend
 * compensation, scrollIntoView — is reported back as one, and so is a scroll
 * the browser performs on its own; none of them carry their origin.
 *
 * What separates them is the timeline's size. If scrollHeight moved since the
 * previous scroll event, this event's distance from the tail describes a layout
 * that shifted underneath a stationary viewport, not a position anyone chose.
 * Trusting it is precisely what broke #788: a reflow landing between the
 * tail-lock's pin and that pin's own scroll event made the handler record "not
 * at the tail", which disarmed the tail-lock for good and left every later
 * reflow uncorrected.
 *
 * Device-agnostic on purpose: wheel, trackpad, keyboard, touch and a scrollbar
 * drag all produce scroll events with a stable scrollHeight as soon as no
 * reflow is in flight, so none of them needs its own case.
 *
 * ponytail: a continuous burst of reflows defers the reader's own scrolls until
 * one event lands with a stable scrollHeight. Bounded by the burst, and it
 * never pulls them anywhere — followTailRef simply keeps whatever it already
 * held. Upgrade path if that ever bites: correlate against this component's own
 * last programmatic write.
 */
function useScrollTracking(core: ViewportCore) {
  useEffect(() => {
    const el = core.listRef.current;
    if (!el) return;
    // ponytail: recomputes the anchor on every scroll event rather than
    // throttling via rAF — fine at MVP message-list sizes; add throttling if
    // profiling ever shows this loop hot.
    core.recordScrollEvent(el.scrollHeight, null);
    const onScroll = () => {
      const layoutChanged = el.scrollHeight !== core.lastScrollHeightRef.current;
      const reading = layoutChanged ? null : readingPositionOf(el);
      core.recordScrollEvent(el.scrollHeight, reading);
      if (reading) applyReadingPhase(core, reading.nearBottom);
      // #675: a restoration in flight is writing scrollTop itself, and the
      // scroll events it produces describe intermediate layouts, not a reading
      // position. Re-deriving the anchor from one of them would replace the
      // very target being restored to, so while it owns the scrollport this
      // records nothing. It owns it for a handful of commits at most.
      if (core.prependRestoreRef.current) return;
      // A null reading means the mounted window has not caught up with this
      // scroll yet: rather than record a message nobody is looking at, ask the
      // next commit — the one that mounts the right rows — to answer instead.
      core.recordAnchor(computeTopmostVisible(el, core.messageRefs.current));
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, [core]);
}

/** The reader's position, read off a scrollport whose geometry is trustworthy. */
function readingPositionOf(el: HTMLDivElement): ReadingPosition {
  return {
    nearBottom: isNearBottom(el.scrollHeight, el.scrollTop, el.clientHeight),
    followTail: isNearBottom(el.scrollHeight, el.scrollTop, el.clientHeight, TAIL_EPSILON_PX),
  };
}

/**
 * The phase that reading implies.
 *
 * Only the bottom sentinel ends SCROLLING_TO_BOTTOM — a near-bottom scroll
 * position reached mid-animation is not yet a confirmed arrival (a media resize
 * could still be in flight).
 */
function applyReadingPhase(core: ViewportCore, nearBottom: boolean) {
  if (core.phaseRef.current === "SCROLLING_TO_BOTTOM") return;
  if (nearBottom) {
    if (core.phaseRef.current !== "AT_BOTTOM") core.setPhase("AT_BOTTOM");
  } else if (core.phaseRef.current === "AT_BOTTOM") {
    core.setPhase("READING_HISTORY");
  }
}

/**
 * Bottom sentinel: the same node scrollToBottom() targets also tells us,
 * authoritatively, when the real tail is on screen — surviving remeasure from
 * late-loading media instead of trusting a scrollIntoView call's mere return.
 * This is also the single place mark-read is triggered from (#492 G): never
 * from opening the route, only from confirmed arrival.
 */
function useBottomConfirmation(
  core: ViewportCore,
  setPendingCount: (count: number) => void,
  onReachedBottom: () => void,
) {
  const firedRef = useRef(false);
  const onReachedBottomRef = useRef(onReachedBottom);
  useLayoutEffect(() => {
    onReachedBottomRef.current = onReachedBottom;
  });
  useEffect(() => {
    const sentinel = core.bottomRef.current;
    // Capability check: some test environments (and, historically, older
    // browsers) have no IntersectionObserver — degrade to "never confirmed",
    // matching this file's existing scrollIntoView capability check.
    if (!sentinel || typeof IntersectionObserver === "undefined") return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (!entries[0]?.isIntersecting) {
          firedRef.current = false;
          return;
        }
        // #788: the sentinel being fully on screen is the one unambiguous
        // confirmation that the viewport really is at the tail, so it is also
        // what re-arms the follow-the-tail intent after any reading position —
        // the recovery path the previous version never had.
        core.confirmAtTail();
        core.setPhase("AT_BOTTOM");
        setPendingCount(0);
        if (firedRef.current) return;
        firedRef.current = true;
        onReachedBottomRef.current();
      },
      { threshold: 1 },
    );
    observer.observe(sentinel);
    return () => observer.disconnect();
  }, [core, setPendingCount]);
}

/**
 * #788 tail-lock: keeps the viewport pinned to the real bottom through an async
 * reflow (an attachment/media preview finishing its layout well after the
 * initial positioning) — for ANY variable-height content, never an
 * attachment-specific special case.
 *
 * Observes contentRef, not listRef: listRef's clientHeight is fixed by CSS and
 * never changes when a message grows, so only the content wrapper's own box
 * height tracks what would otherwise show up as listRef.scrollHeight.
 *
 * Two ways in, and the phase alone is not enough for either:
 *
 * - SCROLLING_TO_BOTTOM: the reader explicitly asked for the end (button or
 *   own-send), so a resize re-pins unconditionally — mid-flight is exactly when
 *   a late-loading preview would otherwise strand the animation short.
 * - AT_BOTTOM: re-pins ONLY while the follow-the-tail intent still holds.
 *   #492's scroll handler assigns AT_BOTTOM anywhere within 150px of the end,
 *   so without followTailRef a reader who deliberately scrolled up a little —
 *   and stayed inside that threshold — would be yanked back down by an
 *   unrelated image finishing its layout.
 *
 * The correction is a direct scrollTop assignment, never scrollIntoView: during
 * SCROLLING_TO_BOTTOM there is already an in-flight scrollIntoView animation,
 * and a second scrollIntoView call races that animation instead of cleanly
 * overriding it — a plain scrollTop write always wins.
 *
 * While READING_HISTORY/AT_FIRST_UNREAD/RESTORING_POSITION this is a no-op: a
 * reading position is never overridden by a layout shift alone.
 */
function useTailLock(core: ViewportCore) {
  useEffect(() => {
    const content = core.contentRef.current;
    if (!content || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => {
      const el = core.listRef.current;
      if (!el) return;
      // #675 single scroll authority: a prepend restoration owns scrollTop
      // until it finishes. The phase alone already makes this a no-op during
      // one (a prepend only happens up in the history), but saying it here is
      // what keeps the two from ever being two writers.
      if (core.prependRestoreRef.current) return;
      const holdsTail =
        core.phaseRef.current === "SCROLLING_TO_BOTTOM" ||
        (core.phaseRef.current === "AT_BOTTOM" && core.followTailRef.current);
      if (holdsTail) el.scrollTop = el.scrollHeight - el.clientHeight;
    });
    observer.observe(content);
    return () => observer.disconnect();
  }, [core]);
}
