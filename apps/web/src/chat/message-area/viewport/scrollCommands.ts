/**
 * The scrollport commands and readings the conversation viewport is built from
 * (moved out of ChatMessageArea, issue #834).
 *
 * Every function here talks to the DOM and nothing else: no React, no state,
 * no lifecycle. That is what lets the hooks above them be about *when* the
 * viewport moves without also being about *how*.
 */

import type { RefObject } from "react";
import type { Virtualizer } from "@tanstack/react-virtual";

import { scrollToEndBehavior } from "../../timelineVirtualization";

/**
 * Scrolls the timeline to its newest message with an explicit behavior —
 * never a default, so every call site states whether this is an instant
 * positioning (open, restore, first-unread) or an animated one (explicit user
 * action / own-send). #492: smooth scrolling must never happen just because a
 * conversation opened.
 *
 * The capability check is for jsdom, which implements no layout and therefore no
 * scrollIntoView; a test asserting on message order must not fail on it.
 */
export function scrollToBottom(
  bottomRef: RefObject<HTMLDivElement | null>,
  behavior: ScrollBehavior,
): void {
  if (typeof bottomRef.current?.scrollIntoView === "function") {
    bottomRef.current.scrollIntoView({ behavior });
  }
}

/** #492: reduced motion always wins over an explicit animated scroll. */
export function prefersReducedMotion(): boolean {
  return (
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

/**
 * The behavior an explicit user-triggered scroll (button, own-send) should use.
 *
 * Animated when the trip is short enough for the animation to survive it — see
 * MAX_SMOOTH_SCROLL_DISTANCE_PX for why distance is the deciding factor and not
 * taste.
 */
export function explicitScrollBehavior(container: HTMLDivElement | null): ScrollBehavior {
  const remaining = container
    ? container.scrollHeight - container.scrollTop - container.clientHeight
    : 0;
  return scrollToEndBehavior(remaining, prefersReducedMotion());
}

/**
 * Where the scrollport has to sit for row `index` to show `offsetPx` below its
 * top edge, according to the row model (issue #675).
 *
 * Only ever used to *reach* a row that is not mounted — while it is unmounted
 * there is no box to measure, and the model is the only answer available. It is
 * as good as the estimates the model still holds, which is why the restoration
 * measures again as soon as the row exists.
 */
export function prependScrollTopFor(
  virtualizer: Virtualizer<HTMLDivElement, Element>,
  index: number,
  offsetPx: number,
): number {
  const rowStart = virtualizer.getOffsetForIndex(index, "start")?.[0] ?? 0;
  return Math.max(0, rowStart - offsetPx);
}

/** The anchor #492 restores a history reading position from. */
export interface ViewportAnchorPoint {
  messageId: string;
  offsetPx: number;
}

/**
 * The topmost visible message and its offset from the container's top edge
 * — the anchor #492 restores a history position from. jsdom reports every
 * box as zero-sized, so this deterministically resolves to the first loaded
 * message there; a real browser resolves it to whatever message is actually
 * scrolled to the top of the viewport.
 *
 * #675: null when no *mounted* message is in the viewport at all. Under
 * virtualization the mounted window is recomputed a commit after the scroll
 * that moved it, so between the two there is a moment when every mounted row
 * is far below the viewport — and without this bound the nearest of them,
 * hundreds of pixels down, would be recorded as "the message being read".
 * Restoring a prepend to that is a reading position nobody ever had.
 */
export function computeTopmostVisible(
  container: HTMLDivElement,
  refs: Map<string, HTMLDivElement>,
): ViewportAnchorPoint | null {
  const containerTop = container.getBoundingClientRect().top;
  const viewportPx = container.clientHeight;
  let best: ViewportAnchorPoint | null = null;
  for (const [id, el] of refs) {
    const offsetPx = el.getBoundingClientRect().top - containerTop;
    if (offsetPx >= -4 && offsetPx < viewportPx && (!best || offsetPx < best.offsetPx)) {
      best = { messageId: id, offsetPx };
    }
  }
  return best;
}
