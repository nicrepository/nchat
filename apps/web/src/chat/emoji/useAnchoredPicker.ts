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

/** Distance kept from every edge of the viewport. */
const viewportPadding = 8;

/**
 * Places a floating element against an anchor, whole inside the viewport.
 *
 * Above the anchor when it fits there, below when it does not, and horizontally
 * clamped either way. Exported because the reaction toolbar places itself with
 * the same rules against the message bubble rather than against a button.
 */
export function placeAgainstAnchor(
  element: HTMLElement,
  anchor: DOMRect,
  box: DOMRect,
  left: number,
  gapBelow: number,
  gapAbove: number,
): void {
  const above = anchor.top - box.height - gapAbove;
  const top =
    above >= viewportPadding
      ? above
      : Math.min(anchor.bottom + gapBelow, window.innerHeight - box.height - viewportPadding);
  element.style.left = `${Math.min(Math.max(viewportPadding, left), window.innerWidth - box.width - viewportPadding)}px`;
  element.style.top = `${top}px`;
  element.style.visibility = "visible";
}

/** The band of screen an anchor is actually painted in. */
export interface VisibleBounds {
  top: number;
  bottom: number;
  left: number;
  right: number;
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
    placeAgainstAnchor(pickerRef.current, anchor, picker, left, gap, gap);
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
