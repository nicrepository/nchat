/**
 * Following the tail, and knowing when the reader stopped (#492/#788 — moved
 * out of ChatMessageArea, issue #834).
 *
 * Three DOM facts, one intent. The scroll handler reads where the reader is,
 * the bottom sentinel confirms when the *real* tail is on screen, and a
 * ResizeObserver keeps the viewport pinned there through an async reflow. What
 * they agree on is followTailRef: the intent to stay at the end, which is not
 * the same thing as the geometry of currently being at it.
 *
 * None of the three drives a *trip* to the end — that is useNavigator's, and
 * every one of them stands down while it owns the scrollport (#880). What is
 * left here is the steady state: staying at the end once it has been reached,
 * and noticing when the reader leaves.
 */

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import type { ViewportPhase } from "../../chatViewportState";
import type { LastMutation } from "../../useMessages";
import type { Message } from "../../chatTypes";
import { movedByReader, tailConfirmed } from "./navigation";
import { computeTopmostVisible, readingPositionOf } from "./scrollCommands";
import { decideTailMutation } from "./tailMutation";
import type { NavigatorState } from "./useNavigator";
import type { ViewportCore } from "./useViewportCore";

/**
 * Arriving at the end, as one routine with one owner.
 *
 * Two things can observe the arrival — the sentinel, and the navigation's own
 * confirmation pass — and both mean exactly the same thing to the rest of the
 * app: the tail is on screen, the pending badge is spent, and #492 item G's
 * single mark-read trigger has fired. Writing it once is what keeps the two
 * from disagreeing, and the guard is what keeps a sentinel that reports every
 * frame from sending a receipt per frame.
 */
export interface TailArrival {
  /** The tail is confirmed on screen. Idempotent until the reader leaves it. */
  arrive: () => void;
  /** The tail is no longer on screen: the next arrival is a new one. */
  leave: () => void;
}

export function useTailArrival(
  core: ViewportCore,
  onReachedBottom: () => void,
  clearUnread: () => void,
): TailArrival {
  const firedRef = useRef(false);
  const onReachedBottomRef = useRef(onReachedBottom);
  const clearUnreadRef = useRef(clearUnread);
  useLayoutEffect(() => {
    onReachedBottomRef.current = onReachedBottom;
    clearUnreadRef.current = clearUnread;
  });

  const arrive = useCallback(() => {
    // #788: the tail being confirmed is also what re-arms the follow-the-tail
    // intent after any reading position — the recovery path the first version
    // never had.
    core.confirmAtTail();
    core.setPhase("AT_BOTTOM");
    clearUnreadRef.current();
    if (firedRef.current) return;
    firedRef.current = true;
    onReachedBottomRef.current();
  }, [core]);

  const leave = useCallback(() => {
    firedRef.current = false;
  }, []);

  // One identity for as long as the two callbacks are the same ones: the
  // sentinel's observer takes this as a dependency, and a fresh object every
  // render would tear that observer down and build it again on each one.
  return useMemo(() => ({ arrive, leave }), [arrive, leave]);
}

interface Params {
  core: ViewportCore;
  phase: ViewportPhase;
  messages: Message[];
  currentUserId: string;
  lastMutation: LastMutation;
  /** Whether the opening position has been decided; mutations wait for it. */
  resolved: boolean;
  arrival: TailArrival;
  navigator: NavigatorState;
  /** A message arrived behind the reader: the unread count is the caller's. */
  onUnreadArrival: () => void;
}

export function useTailFollow({
  core,
  phase,
  messages,
  currentUserId,
  lastMutation,
  resolved,
  arrival,
  navigator,
  onUnreadArrival,
}: Params): void {
  const [countedMessages, setCountedMessages] = useState(messages);
  const [ownSendRequest, setOwnSendRequest] = useState(0);

  useScrollTracking(core, navigator.yieldToReader);
  useBottomConfirmation(core, arrival, navigator.requestPass);
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
    if (response.kind === "count-unread") onUnreadArrival();
    else if (response.kind === "return-to-bottom") setOwnSendRequest((n) => n + 1);
  }

  // Consumes an own send's request (set during render above). #880 item 13:
  // sending is an explicit intent to come back to the present, and it uses the
  // very same navigator a button press does — never a second scroll path of
  // its own, and never by way of the unread boundary.
  const { navigateToTail } = navigator;
  useEffect(() => {
    if (ownSendRequest > 0) navigateToTail("own-send");
  }, [navigateToTail, ownSendRequest]);
}

/**
 * What a scroll event says about where the reader is — and, first, whether it
 * says anything at all.
 *
 * #788: a scroll event is not, by itself, evidence of anything the reader did.
 * Every correction this component makes — the tail-lock's pin, prepend
 * compensation, a navigation's seek — is reported back as one, and so is a
 * scroll the browser performs on its own; none of them carry their origin.
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
function useScrollTracking(core: ViewportCore, yieldToReader: () => void) {
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
      // #880: a settled scroll that left the scrollport somewhere the
      // navigation did not put it is the reader's — dragging an overlay
      // scrollbar announces itself no other way — and it ends the trip through
      // the same transition a wheel does.
      const navigation = core.navigationRef.current;
      if (reading && navigation && movedByReader(navigation, el.scrollTop)) yieldToReader();
      else if (reading) applyReadingPhase(core, reading.nearBottom);
      // #675/#880: whoever owns the scrollport is writing it itself, and the
      // scroll events that produces describe intermediate layouts, not a
      // reading position. Re-deriving the anchor from one of them would replace
      // the very target being travelled to, so while something owns it this
      // records nothing. Ownership lasts a handful of commits at most.
      if (core.prependRestoreRef.current || core.navigationRef.current) return;
      // A null reading means the mounted window has not caught up with this
      // scroll yet: rather than record a message nobody is looking at, ask the
      // next commit — the one that mounts the right rows — to answer instead.
      core.recordAnchor(computeTopmostVisible(el, core.messageRefs.current));
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, [core, yieldToReader]);
}

/**
 * The phase that reading implies.
 *
 * Only a confirmed arrival ends a navigation — a near-bottom scroll position
 * reached mid-trip is not an arrival (a media resize could still be in
 * flight), and a phase written here would be describing a viewport that
 * somebody else is still moving.
 */
function applyReadingPhase(core: ViewportCore, nearBottom: boolean) {
  if (core.navigationRef.current) return;
  if (nearBottom) {
    if (core.phaseRef.current !== "AT_BOTTOM") core.setPhase("AT_BOTTOM");
  } else if (core.phaseRef.current === "AT_BOTTOM") {
    core.setPhase("READING_HISTORY");
  }
}

/**
 * Bottom sentinel: the same node a tail navigation aims at also tells us,
 * authoritatively, when the real tail is on screen — surviving remeasure from
 * late-loading media instead of trusting a scroll command's mere return. This
 * is also the single place mark-read is triggered from (#492 G): never from
 * opening the route, only from confirmed arrival.
 *
 * #880: "intersecting" alone is not that confirmation under virtualization.
 * The sentinel is positioned by the canvas, whose height is a commit behind a
 * row that has just been measured, so it can be fully on screen with hundreds
 * of pixels of real content still below the fold — which is how the operation
 * used to end in the middle of the conversation. The geometry has to agree,
 * and while a navigation owns the scrollport it is that navigation, not this
 * observer, that decides it has arrived.
 */
function useBottomConfirmation(core: ViewportCore, arrival: TailArrival, requestPass: () => void) {
  useEffect(() => {
    const sentinel = core.bottomRef.current;
    const el = core.listRef.current;
    // Capability check: some test environments (and, historically, older
    // browsers) have no IntersectionObserver — degrade to "never confirmed",
    // matching this file's existing capability checks.
    if (!sentinel || !el || typeof IntersectionObserver === "undefined") return;
    const observer = new IntersectionObserver(
      (entries) => {
        const visible = Boolean(entries[0]?.isIntersecting);
        core.noteTailSentinel(visible);
        if (!visible) {
          arrival.leave();
          return;
        }
        if (core.navigationRef.current) {
          requestPass();
          return;
        }
        const remaining = el.scrollHeight - el.scrollTop - el.clientHeight;
        if (tailConfirmed(remaining, true)) arrival.arrive();
      },
      { threshold: 1 },
    );
    observer.observe(sentinel);
    return () => observer.disconnect();
  }, [core, arrival, requestPass]);
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
 * It re-pins while AT_BOTTOM, and ONLY while the follow-the-tail intent still
 * holds: #492's scroll handler assigns AT_BOTTOM anywhere within 150px of the
 * end, so without followTailRef a reader who deliberately scrolled up a little
 * — and stayed inside that threshold — would be yanked back down by an
 * unrelated image finishing its layout.
 *
 * #880: it no longer pins during a trip to the end. It used to, and that is
 * one of the two writers that made the trip end in the middle — its correction
 * cancelled the animation it was trying to help, and it had no opinion about
 * where the tail would be one measurement later. The navigation owns that
 * state now; this one owns the steady state after it.
 *
 * The correction is a direct scrollTop assignment, never a scroll command with
 * a behavior: it has to win outright, and it is only ever a few pixels.
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
      // Single scroll authority (#675/#880): a prepend restoration or a
      // navigation owns scrollTop until it finishes, and neither wants help.
      if (core.prependRestoreRef.current || core.navigationRef.current) return;
      const holdsTail = core.phaseRef.current === "AT_BOTTOM" && core.followTailRef.current;
      if (holdsTail) el.scrollTop = el.scrollHeight - el.clientHeight;
    });
    observer.observe(content);
    return () => observer.disconnect();
  }, [core]);
}
