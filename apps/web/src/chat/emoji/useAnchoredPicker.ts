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
export const viewportPadding = 8;

/**
 * Dispatched by a picker, bubbling, each time it has been placed (CQR R9 of
 * issue #1238): whatever hangs off a cell inside it — the tone palette, which
 * is portalled out of it — follows the cell there. The picker is placed on
 * every resize, viewport change, scroll (its own results included) and change
 * of its size, so one event covers every way a cell moves on the screen.
 */
export const pickerPlacedEvent = "chat-emoji-picker-placed";

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
 * The part of the page the reader actually sees (issue #1152), in the same
 * coordinates a fixed element is placed in: pinch zoom, the on-screen keyboard
 * and a browser's own bars shrink and shift it inside the layout viewport,
 * which `window.innerWidth/innerHeight` alone do not report. A browser without
 * the Visual Viewport API gets the window.
 */
export function visualViewportBounds(): VisibleBounds {
  const visual = window.visualViewport;
  if (!visual) return viewportBounds();
  return {
    top: visual.offsetTop,
    left: visual.offsetLeft,
    bottom: visual.offsetTop + visual.height,
    right: visual.offsetLeft + visual.width,
  };
}

/** Where two bands overlap. */
export function intersectBounds(a: VisibleBounds, b: VisibleBounds): VisibleBounds {
  return {
    top: Math.max(a.top, b.top),
    bottom: Math.min(a.bottom, b.bottom),
    left: Math.max(a.left, b.left),
    right: Math.min(a.right, b.right),
  };
}

/**
 * The part of a band on one side of a box: the side another element is on
 * (issue #1152). Without both boxes, the whole band.
 */
export function bandBeside(band: VisibleBounds, box?: DOMRect, side?: DOMRect): VisibleBounds {
  if (!box || !side) return band;
  return side.top < box.top
    ? { ...band, bottom: Math.min(band.bottom, box.top) }
    : { ...band, top: Math.max(band.top, box.bottom) };
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

/**
 * Caps a picker to a band before it is measured: no wider than the band, no
 * taller than the larger side of its anchor. The picker's own stylesheet reads
 * these as limits on top of its usual size, and its list scrolls inside.
 */
function capToBand(element: HTMLElement, anchor: DOMRect, band: VisibleBounds, gap: number) {
  const room = Math.max(anchor.top - band.top, band.bottom - anchor.bottom) - gap;
  const width = band.right - band.left - 2 * viewportPadding;
  element.style.setProperty("--chat-emoji-picker-max-width", `${Math.max(0, width)}px`);
  element.style.setProperty(
    "--chat-emoji-picker-max-height",
    `${Math.max(0, room - viewportPadding)}px`,
  );
}

/** For a caller with nothing to do before or after its picker is placed. */
function ignorePlacement(): void {}

/** A band as a value, to tell whether it moved. */
function bandKey(band: VisibleBounds): string {
  return `${band.top} ${band.right} ${band.bottom} ${band.left}`;
}

/** The box a picker is placed against: its button's, or the one it must clear. */
function placementBox(button: DOMRect, clearRef?: RefObject<HTMLElement | null>): DOMRect {
  return clearRef?.current?.getBoundingClientRect() ?? button;
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
   * The picker's anchor read as gone — out of its band, or no room on either
   * side — for a caller whose verdict on that is its own (CQR R10 F-1 of issue
   * #1238): a touch menu still confirming that its message left the reader's
   * view. Absent, the picker closes as for a click elsewhere.
   */
  onLost?: () => void;
  /**
   * Given the picker's placement, for a caller that must place it again on its
   * own terms — a touch menu whose message came back into view (CQR R10 F-1).
   */
  placeRef?: RefObject<() => void>;
  /**
   * An element that counts as inside for the outside-click test, beyond the
   * picker itself: the toolbar the anchor lives in, so pressing the button
   * again toggles rather than closing and reopening.
   */
  containerRef?: RefObject<HTMLElement | null>;
  /** How the picker lines up with the anchor horizontally. */
  align?: "start" | "end";
  /**
   * The band the picker must stay inside, when the window is not it (issue
   * #1152: the visual viewport, for a menu opened by touch). The picker is
   * then also capped to that band — width, and the taller of the two sides of
   * its button — and scrolls inside instead of being cut off or closed.
   */
  bounds?: () => VisibleBounds;
  /**
   * What the picker must keep clear of, when that is more than its button
   * (issue #1152): a touch menu, whose button sits in a second row. The menu is
   * a popover in the top layer, so whatever overlaps it is painted under it and
   * cannot be touched there, whatever its z-index. The picker is placed above
   * or below this box instead of the button; it still lines up with the button
   * horizontally. The box is watched too: a menu that grows or shrinks under
   * an open picker — its quick reactions arriving late — moves its edge, and
   * the picker follows it there.
   */
  clearRef?: RefObject<HTMLElement | null>;
  /**
   * Places the box in clearRef, for a caller that owns where it is drawn and
   * draws it in the same band (CQR R7 of issue #1238). Run before every
   * placement: when the band moves — a search's results changing what a touch
   * sheet needs — that box is still where the old band put it, and judged
   * there an anchor in plain sight reads as scrolled away.
   */
  placeClear?: () => void;
  /**
   * Called with the picker after every placement, for a caller whose layout
   * depends on what the placed picker has room to show. True when that moved
   * the box in clearRef: the picker is placed once more, against it.
   */
  onPlaced?: (picker: HTMLElement) => boolean | void;
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
  onLost,
  placeRef,
  containerRef,
  align = "end",
  gap = 7,
  bounds,
  clearRef,
  placeClear = ignorePlacement,
  onPlaced = ignorePlacement,
}: AnchoredPickerOptions): RefObject<HTMLDivElement | null> {
  const pickerRef = useRef<HTMLDivElement>(null);
  // The caller's band the picker was last placed in.
  const placedBandRef = useRef("");

  const lost = useCallback(() => (onLost ? onLost() : onDismiss(false)), [onDismiss, onLost]);

  const position = useCallback(() => {
    const place = (): boolean => {
      if (!open || !anchorRef.current || !pickerRef.current) return false;
      placeClear();
      const button = anchorRef.current.getBoundingClientRect();
      const anchor = placementBox(button, clearRef);
      const band = (bounds ?? viewportBounds)();
      // An anchor scrolled out of sight has nothing to hang off any more.
      if (anchor.bottom < band.top || anchor.top > band.bottom) {
        lost();
        return false;
      }
      // Only a band the picker was capped to counts as placed in (CQR R10 F-1
      // of issue #1238): a rotation's first reading, judged lost while its touch
      // menu confirms it, capped nothing — and taken for placed, the next
      // observer callback capped the picker inside itself, a loop in WebKit.
      placedBandRef.current = bandKey(band);
      if (bounds) capToBand(pickerRef.current, anchor, band, gap);
      const picker = pickerRef.current.getBoundingClientRect();
      const left = align === "end" ? button.right - picker.width : button.left;
      // No room on either side of the button is as good as no button.
      if (!placeAgainstAnchor(pickerRef.current, anchor, picker, left, gap, gap, band)) {
        lost();
        return false;
      }
      return onPlaced(pickerRef.current) === true;
    };
    // A caller that moved what the picker clears, without resizing it (CQR R6
    // of issue #1238), gets one more placement against where that is now; once,
    // so the two cannot chase each other.
    if (place()) place();
    pickerRef.current?.dispatchEvent(new Event(pickerPlacedEvent, { bubbles: true }));
  }, [align, anchorRef, bounds, clearRef, gap, lost, onPlaced, open, placeClear]);

  useLayoutEffect(position, [position]);

  useLayoutEffect(() => {
    if (placeRef) placeRef.current = position;
  }, [placeRef, position]);

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
    // picker rather than one per emoji. The box it keeps clear of is watched
    // for the same reason. A placement writes only the picker's own limits and
    // position, never that box, and a box whose size did not change is not
    // reported again, so this settles rather than loops.
    //
    // Unless the caller's band moved with that size (CQR R6 of issue #1238: a
    // touch sheet's band is chosen from what the arrived catalog shows): the
    // new band's cap would resize the picker inside its own observer's
    // callback, which the browser reports as a loop. That placement waits for
    // the next frame, outside the callback.
    let frame = 0;
    const resize = new ResizeObserver(() => {
      if (bounds && bandKey(bounds()) !== placedBandRef.current) {
        cancelAnimationFrame(frame);
        frame = requestAnimationFrame(position);
        return;
      }
      position();
    });
    if (pickerRef.current) resize.observe(pickerRef.current);
    if (clearRef?.current) resize.observe(clearRef.current);
    document.addEventListener("mousedown", closeOnOutsideClick);
    document.addEventListener("keydown", closeOnEscape);
    document.addEventListener("scroll", position, true);
    // Zoom, the keyboard and rotation move the visual viewport only.
    window.visualViewport?.addEventListener("resize", position);
    window.visualViewport?.addEventListener("scroll", position);
    return () => {
      cancelAnimationFrame(frame);
      resize.disconnect();
      document.removeEventListener("mousedown", closeOnOutsideClick);
      document.removeEventListener("keydown", closeOnEscape);
      document.removeEventListener("scroll", position, true);
      window.visualViewport?.removeEventListener("resize", position);
      window.visualViewport?.removeEventListener("scroll", position);
    };
  }, [bounds, clearRef, containerRef, onDismiss, open, position]);

  return pickerRef;
}
