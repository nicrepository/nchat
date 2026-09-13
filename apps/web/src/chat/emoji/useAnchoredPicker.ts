/**
 * Where a floating emoji picker sits, and when it closes (issue #496).
 *
 * Two surfaces open the same picker — the reaction toolbar over a message, and
 * the composer's toolbar — and both need the identical set of rules: place it
 * against its button, prefer above, flip below only when it does not fit, never
 * leave the viewport, follow a scroll, re-place when the lazily-loaded catalog
 * changes its size, and dismiss on Escape or on a click outside. Writing that
 * twice would be two chances to get the viewport arithmetic wrong.
 */

import { useCallback, useEffect, useLayoutEffect, useRef, type RefObject } from "react";

/** Distance kept from every edge of the band a floating element is placed in. */
const viewportPadding = 8;

/** The band of screen an anchor is actually painted in. */
export interface VisibleBounds {
  top: number;
  bottom: number;
  left: number;
  right: number;
}

/** The window itself: the band a floating element is confined to by default. */
function viewportBounds(): VisibleBounds {
  return { top: 0, bottom: window.innerHeight, left: 0, right: window.innerWidth };
}

/**
 * Places a floating element against an anchor, whole inside a band of screen.
 *
 * Only against an anchor the reader can see: one that has left the band — a
 * row the virtualizer keeps mounted well past the list's edge, say — gets
 * nothing, however well a box would fit beside where it is. Then above the
 * anchor when it fits there, below when it fits there instead, and
 * horizontally clamped either way. When it fits on neither side — an anchor as
 * tall as the band — nothing is placed either: a box nudged into place would
 * cross the anchor it belongs to, and a toolbar drawn over its own message
 * misattributes every action on it. Not placed means left hidden, with no
 * coordinate written; the caller decides what closing means for it, which is
 * why this returns whether it placed.
 *
 * The band is the viewport unless the caller knows better: the reaction
 * toolbar hangs off a bubble inside a scroll container that the header and the
 * composer bound, so it passes that container's visible band (issue #839) and
 * the toolbar is never drawn over either. Exported because the toolbar places
 * itself with the same rules as a picker, against the message bubble rather
 * than against a button.
 */
export function placeAgainstAnchor(
  element: HTMLElement,
  anchor: DOMRect,
  box: DOMRect,
  left: number,
  gapBelow: number,
  gapAbove: number,
  bounds: VisibleBounds = viewportBounds(),
): boolean {
  const above = anchor.top - box.height - gapAbove;
  const below = anchor.bottom + gapBelow;
  const fitsAbove = above >= bounds.top + viewportPadding;
  const fitsBelow = below + box.height <= bounds.bottom - viewportPadding;
  if (!anchorIsVisible(anchor, bounds) || (!fitsAbove && !fitsBelow)) {
    element.style.visibility = "hidden";
    return false;
  }
  const leftLimit = bounds.right - box.width - viewportPadding;
  element.style.left = `${Math.min(Math.max(bounds.left + viewportPadding, left), leftLimit)}px`;
  element.style.top = `${fitsAbove ? above : below}px`;
  element.style.visibility = "visible";
  return true;
}

/**
 * Whether any part of an anchor is inside the band it can be painted in.
 *
 * A floating surface may only be drawn against an anchor the reader can see. The
 * band is not always the viewport: an anchor inside a scroll container is
 * clipped by it too, so the caller passes the intersection it knows about.
 *
 * Partly visible counts as visible — an anchor half past an edge is still one
 * the reader is looking at.
 */
export function anchorIsVisible(anchor: DOMRect, bounds: VisibleBounds): boolean {
  return (
    anchor.bottom > bounds.top &&
    anchor.top < bounds.bottom &&
    anchor.right > bounds.left &&
    anchor.left < bounds.right
  );
}

/**
 * Where an anchor inside the message list can actually be seen.
 *
 * The list clips vertically, so the window alone is the wrong answer: an anchor
 * scrolled past the list's edge is invisible even while the window still has
 * room for it. The list is the one clipping ancestor a message overlay ever
 * has — a reaction badge, a message bubble — so this asks for it by name rather
 * than walking the tree looking for scroll parents. Without it (an anchor
 * somewhere else one day) the window is the boundary, which is still an
 * improvement on none.
 */
export function visibleBounds(anchor: Element): VisibleBounds {
  const viewport = viewportBounds();
  const clip = anchor.closest(".chat-msg-area__list")?.getBoundingClientRect();
  if (!clip) return viewport;
  return {
    top: Math.max(viewport.top, clip.top),
    bottom: Math.min(viewport.bottom, clip.bottom),
    left: Math.max(viewport.left, clip.left),
    right: Math.min(viewport.right, clip.right),
  };
}

export interface AnchoredPickerOptions {
  open: boolean;
  /** The control the picker belongs to; also where focus returns on Escape. */
  anchorRef: RefObject<HTMLElement | null>;
  /**
   * Closes the picker. `restoreFocus` is true when the reader closed it
   * deliberately — Escape — and false for a click elsewhere, where focus
   * belongs wherever the click put it.
   */
  onDismiss: (restoreFocus: boolean) => void;
  /**
   * An element that counts as inside for the outside-click test, beyond the
   * picker itself: the toolbar the anchor lives in, so pressing the button
   * again toggles rather than closing and reopening.
   */
  containerRef?: RefObject<HTMLElement | null>;
  /** How the picker lines up with the anchor horizontally. */
  align?: "start" | "end";
  gap?: number;
}

/**
 * Returns the ref to put on the picker's own element. Everything else — the
 * placement, the listeners and their cleanup — is owned here.
 */
export function useAnchoredPicker({
  open,
  anchorRef,
  onDismiss,
  containerRef,
  align = "end",
  gap = 7,
}: AnchoredPickerOptions): RefObject<HTMLDivElement | null> {
  const pickerRef = useRef<HTMLDivElement>(null);

  const position = useCallback(() => {
    if (!open || !anchorRef.current || !pickerRef.current) return;
    const anchor = anchorRef.current.getBoundingClientRect();
    // An anchor scrolled out of sight has nothing to hang off any more.
    if (anchor.bottom < 0 || anchor.top > window.innerHeight) {
      onDismiss(false);
      return;
    }
    const picker = pickerRef.current.getBoundingClientRect();
    const left = align === "end" ? anchor.right - picker.width : anchor.left;
    // No room on either side of the button is as good as no button.
    if (!placeAgainstAnchor(pickerRef.current, anchor, picker, left, gap, gap)) onDismiss(false);
  }, [align, anchorRef, gap, onDismiss, open]);

  useLayoutEffect(position, [position]);

  useEffect(() => {
    if (!open) return;
    const closeOnOutsideClick = (event: MouseEvent) => {
      const target = event.target as Node;
      if (pickerRef.current?.contains(target) || containerRef?.current?.contains(target)) return;
      // The skin-tone palette is portalled to the body so it cannot be clipped
      // by the picker's own scroll container — which means it is not a DOM
      // descendant of the picker, and a click on it must not read as a click
      // outside. Without this the picker closes on mousedown and the tone the
      // reader was choosing never arrives.
      if (target instanceof Element && target.closest(".chat-emoji-tone")) return;
      onDismiss(false);
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onDismiss(true);
    };
    // The picker opens at the size of its Suspense fallback and grows when the
    // lazily-imported catalog arrives, so the first placement is computed
    // against a box that is about to change. Watching the picker itself is what
    // makes the second placement happen at the moment the size actually
    // changes — no timer, no guessed delay, and one observer for the whole
    // picker rather than one per emoji.
    const resize = new ResizeObserver(() => position());
    if (pickerRef.current) resize.observe(pickerRef.current);
    document.addEventListener("mousedown", closeOnOutsideClick);
    document.addEventListener("keydown", closeOnEscape);
    document.addEventListener("scroll", position, true);
    return () => {
      resize.disconnect();
      document.removeEventListener("mousedown", closeOnOutsideClick);
      document.removeEventListener("keydown", closeOnEscape);
      document.removeEventListener("scroll", position, true);
    };
  }, [containerRef, onDismiss, open, position]);

  return pickerRef;
}
