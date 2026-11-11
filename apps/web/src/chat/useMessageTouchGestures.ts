/**
 * The two touch gestures a message answers to (issue #1152): swipe right to
 * reply, press and hold to open its actions.
 *
 * Only recognition lives here. What a long press opens, and whether a reply is
 * still allowed when the finger lifts, belong to the caller — this decides that
 * a gesture happened, for which message id, and nothing else.
 *
 * Precedence, in the order a touch can resolve:
 * - a press that starts on a control (link, button, media, editor, the menu
 *   itself) is never a gesture: the control owns it;
 * - moving past the slop ends any chance of a long press — a scroll that has
 *   started never opens a menu late;
 * - past the slop, a mostly-horizontal move to the right is a swipe and
 *   anything else (vertical, leftward, diagonal) is the browser's scroll;
 * - a swipe that turns back, or turns more vertical than horizontal, is over
 *   for good: bringing the finger right again does not revive it, and the
 *   release is measured again where the finger actually lifts;
 * - a press held still for the long-press time opens the menu; lifting before
 *   it is a tap, which does nothing here;
 * - a second finger anywhere, the page scrolling, the browser taking the touch
 *   over (pointercancel) or the finger leaving the row cancels whatever was in
 *   progress, and a long press is asked again, when its time comes, whether
 *   the message it started on is still there to act on;
 * - a modal dialog (showModal) opened over a press ends it when it takes the
 *   focus, and nothing fires late under one — the browser itself is asked
 *   whether the row is still reachable, so every modal is covered without
 *   each telling the timeline.
 *
 * Mouse and pen never reach any of this: hover and focus already serve them.
 * The handlers are React props on the row, so a timeline of thousands of rows
 * holds no listener and no timer beyond the one press under a finger.
 */

import { useCallback, useEffect, useLayoutEffect, useRef } from "react";
import type { MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent } from "react";

/** Held this long without moving, a press is a long press. */
export const LONG_PRESS_MS = 500;
/** Movement under this is a finger settling, not a gesture. */
export const TOUCH_SLOP_PX = 10;
/** A swipe this far right, released, replies. */
export const SWIPE_REPLY_PX = 72;
/** How far the row follows the finger; past it the row stays put. */
const SWIPE_MAX_TRAVEL_PX = 96;
/** Touches starting this close to the left edge belong to the OS/browser back gesture. */
export const EDGE_GUARD_PX = 24;

/**
 * Where a press is not a message gesture. `[role=toolbar]` and
 * `[role=dialog]` are the menu and picker this gesture opens: pressing inside
 * them is using them.
 */
const CONTROL_SELECTOR =
  'a, button, input, textarea, select, label, audio, video, img, pre, [contenteditable="true"], [role="toolbar"], [role="dialog"], .chat-msg-area__attachments';

type Phase = "pending" | "swiping" | "longPressed" | "cancelled";

interface Gesture {
  pointerId: number;
  messageId: string;
  startX: number;
  startY: number;
  phase: Phase;
  armed: boolean;
  /** The furthest right the swipe has reached: retreating from it ends it. */
  maxDx: number;
  /** Where the finger is now, in client coordinates. */
  x: number;
  y: number;
  row: HTMLElement;
}

export interface MessageTouchGestureOptions {
  messageId: string;
  /** A message nothing may be done to (removed): no gesture starts. */
  disabled: boolean;
  /** A menu, picker or dialog is open: a swipe must not reply under it. */
  swipeBlocked: boolean;
  onLongPress: (messageId: string) => void;
  onSwipeReply: (messageId: string) => void;
}

export interface MessageTouchGestureHandlers {
  onPointerDown: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerMove: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerUp: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerCancel: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerLeave: (event: ReactPointerEvent<HTMLElement>) => void;
  onContextMenu: (event: ReactMouseEvent<HTMLElement>) => void;
}

/** Whether a press on this target is the message's, not one of its controls'. */
function startsOnMessage(event: ReactPointerEvent<HTMLElement>): boolean {
  const target = event.target;
  if (!(target instanceof Element) || !event.currentTarget.contains(target)) return false;
  const control = target.closest(CONTROL_SELECTOR);
  return !control || !event.currentTarget.contains(control);
}

/** Past the slop: is this a swipe to the right, or somebody else's gesture? */
function resolveDirection(gesture: Gesture, dx: number, dy: number, swipeBlocked: boolean): Phase {
  const horizontal = dx > 0 && dx > Math.abs(dy) * 2;
  if (!horizontal || swipeBlocked || gesture.startX < EDGE_GUARD_PX) return "cancelled";
  return "swiping";
}

/**
 * Whether a recognised swipe is still one: it has not turned back past the
 * slop from the furthest it reached, nor become more vertical than horizontal.
 */
function swipeHolds(gesture: Gesture, dx: number, dy: number): boolean {
  return gesture.maxDx - dx <= TOUCH_SLOP_PX && Math.abs(dy) <= Math.max(dx, TOUCH_SLOP_PX);
}

/** A swipe replies only far enough right and still mostly horizontal. */
function isArmed(dx: number, dy: number): boolean {
  return dx >= SWIPE_REPLY_PX && Math.abs(dy) * 2 < dx;
}

function paintSwipe(gesture: Gesture, dx: number) {
  const travel = Math.min(Math.max(dx, 0), SWIPE_MAX_TRAVEL_PX);
  gesture.row.style.transform = travel > 0 ? `translateX(${travel}px)` : "";
  gesture.row.toggleAttribute("data-swipe-armed", gesture.armed);
}

function unpaint(row: HTMLElement) {
  row.style.transform = "";
  row.removeAttribute("data-swiping");
  row.removeAttribute("data-swipe-armed");
}

/**
 * Whether a modal surface now stands between the finger and its row. Several
 * elements can match :modal at once (a fullscreen element, nested or sibling
 * dialogs) and the top layer orders them by opening, not by the DOM, so which
 * one is active is the browser's to say: hit-testing stops at the topmost
 * backdrop and skips inert nodes. Without any modal nothing is asked, so a
 * dialog opened with show(), or a popover, never blocks a gesture.
 */
function blockedByModal(gesture: Gesture): boolean {
  const doc = gesture.row.ownerDocument;
  if (!doc.querySelector(":modal")) return false;
  const hit = doc.elementFromPoint(gesture.x, gesture.y);
  return !hit || !gesture.row.contains(hit);
}

/** Where the finger is now — what a revalidation hit-tests — and how far it has come. */
function track(gesture: Gesture, event: ReactPointerEvent<HTMLElement>) {
  gesture.x = event.clientX;
  gesture.y = event.clientY;
  return { dx: event.clientX - gesture.startX, dy: event.clientY - gesture.startY };
}

export function useMessageTouchGestures(
  options: MessageTouchGestureOptions,
): MessageTouchGestureHandlers {
  const gestureRef = useRef<Gesture | null>(null);
  const timerRef = useRef<number | undefined>(undefined);
  const unwatchRef = useRef<(() => void) | null>(null);
  // The timer fires after the render that started it: it reads the options of
  // whatever render is current then, not the ones it was created with. A layout
  // effect, so no timer runs between a commit and the options it committed.
  const latestRef = useRef(options);
  useLayoutEffect(() => {
    latestRef.current = options;
  });

  const reset = useCallback(() => {
    window.clearTimeout(timerRef.current);
    timerRef.current = undefined;
    unwatchRef.current?.();
    unwatchRef.current = null;
    const gesture = gestureRef.current;
    gestureRef.current = null;
    if (gesture) unpaint(gesture.row);
  }, []);

  // A recycled row now showing another message, or an unmounted one, must not
  // carry a press — or its timer — over from the message it used to show.
  const { messageId, disabled } = options;
  useEffect(() => reset, [messageId, reset]);
  // A message removed under the finger ends the press there and then.
  useEffect(() => {
    if (disabled) reset();
  }, [disabled, reset]);

  /** The message the gesture started on is still this row's, and still open to it. */
  const stillValid = useCallback((gesture: Gesture) => {
    const latest = latestRef.current;
    return (
      !latest.disabled &&
      latest.messageId === gesture.messageId &&
      gesture.row.isConnected &&
      !blockedByModal(gesture)
    );
  }, []);

  const fireLongPress = useCallback(() => {
    window.clearTimeout(timerRef.current);
    timerRef.current = undefined;
    const gesture = gestureRef.current;
    if (!gesture || gesture.phase !== "pending") return;
    if (!stillValid(gesture)) {
      reset();
      return;
    }
    gesture.phase = "longPressed";
    latestRef.current.onLongPress(gesture.messageId);
  }, [reset, stillValid]);

  // While a finger is down: another pointer anywhere, or anything scrolling
  // the row away from under it, ends the press; so does the focus moving into
  // a modal opened over it (by keyboard, say). Listened to for the one press
  // only, so an idle timeline holds no listener at all.
  const watchPress = useCallback(
    (pointerId: number) => {
      const cancel = (event: Event) => {
        if ((event as PointerEvent).pointerId !== pointerId) reset();
      };
      const revalidate = () => {
        const gesture = gestureRef.current;
        if (gesture && !stillValid(gesture)) reset();
      };
      document.addEventListener("pointerdown", cancel, true);
      document.addEventListener("scroll", cancel, true);
      document.addEventListener("focusin", revalidate, true);
      unwatchRef.current = () => {
        document.removeEventListener("pointerdown", cancel, true);
        document.removeEventListener("scroll", cancel, true);
        document.removeEventListener("focusin", revalidate, true);
      };
    },
    [reset, stillValid],
  );

  const onPointerDown = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      if (event.pointerType !== "touch") return;
      // A second finger: neither a swipe nor a hold, whatever the first was.
      if (gestureRef.current) {
        reset();
        return;
      }
      if (!event.isPrimary || latestRef.current.disabled || !startsOnMessage(event)) return;
      gestureRef.current = {
        pointerId: event.pointerId,
        messageId: latestRef.current.messageId,
        startX: event.clientX,
        startY: event.clientY,
        phase: "pending",
        armed: false,
        maxDx: 0,
        x: event.clientX,
        y: event.clientY,
        row: event.currentTarget,
      };
      watchPress(event.pointerId);
      timerRef.current = window.setTimeout(fireLongPress, LONG_PRESS_MS);
    },
    [fireLongPress, reset, watchPress],
  );

  const onPointerMove = useCallback((event: ReactPointerEvent<HTMLElement>) => {
    const gesture = gestureRef.current;
    if (!gesture || event.pointerId !== gesture.pointerId) return;
    const { dx, dy } = track(gesture, event);
    if (gesture.phase === "pending") {
      if (Math.max(Math.abs(dx), Math.abs(dy)) < TOUCH_SLOP_PX) return;
      window.clearTimeout(timerRef.current);
      timerRef.current = undefined;
      gesture.phase = resolveDirection(gesture, dx, dy, latestRef.current.swipeBlocked);
      if (gesture.phase === "swiping") gesture.row.setAttribute("data-swiping", "");
    }
    if (gesture.phase !== "swiping") return;
    if (!swipeHolds(gesture, dx, dy)) {
      // Over for good: the rest of this touch is the browser's.
      gesture.phase = "cancelled";
      unpaint(gesture.row);
      return;
    }
    gesture.maxDx = Math.max(gesture.maxDx, dx);
    gesture.armed = isArmed(dx, dy);
    paintSwipe(gesture, dx);
  }, []);

  const onPointerUp = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      const gesture = gestureRef.current;
      if (!gesture || event.pointerId !== gesture.pointerId) return;
      // Measured again where the finger lifts, not where it last moved.
      const { dx, dy } = track(gesture, event);
      const replies = gesture.phase === "swiping" && swipeHolds(gesture, dx, dy) && isArmed(dx, dy);
      const valid = stillValid(gesture);
      reset();
      // Asked again at release: a menu may have opened during the drag.
      if (replies && valid && !latestRef.current.swipeBlocked) {
        latestRef.current.onSwipeReply(gesture.messageId);
      }
    },
    [reset, stillValid],
  );

  const onPointerCancel = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      if (gestureRef.current?.pointerId === event.pointerId) reset();
    },
    [reset],
  );

  // Android raises contextmenu for the same hold, possibly before our timer:
  // whichever comes first opens the menu once, and the browser's own menu (or
  // text-selection callout) is suppressed for a press we own.
  const onContextMenu = useCallback(
    (event: ReactMouseEvent<HTMLElement>) => {
      const phase = gestureRef.current?.phase;
      if (phase !== "pending" && phase !== "longPressed") return;
      event.preventDefault();
      fireLongPress();
    },
    [fireLongPress],
  );

  return {
    onPointerDown,
    onPointerMove,
    onPointerUp,
    onPointerCancel,
    // A touch is captured by the row it started on, so the finger leaving it
    // means the browser released it: the same as a cancel.
    onPointerLeave: onPointerCancel,
    onContextMenu,
  };
}
