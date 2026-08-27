/**
 * The geometry behind "is there anything to point at?" (issue #496 overflow fix).
 *
 * A floating surface must not be drawn against an anchor that has scrolled out
 * of sight — it would hang over the page attached to nothing. This is the whole
 * decision, and it is pure arithmetic, which is why it is tested here: jsdom
 * gives every element a zero-sized box, so the browser specs can drive the
 * behaviour but never the edges.
 */
import { describe, expect, it } from "vitest";

import { anchorIsVisible, type VisibleBounds } from "./useAnchoredPicker";

/** A 900×1280 band, as a list filling a desktop viewport would give. */
const bounds: VisibleBounds = { top: 0, bottom: 900, left: 0, right: 1280 };

function rect(over: Partial<DOMRect>): DOMRect {
  return { top: 100, bottom: 130, left: 100, right: 160, ...over } as DOMRect;
}

describe("anchorIsVisible", () => {
  it("accepts an anchor sitting inside the band", () => {
    expect(anchorIsVisible(rect({ top: 100, bottom: 130 }), bounds)).toBe(true);
  });

  // Half past an edge is still an anchor the reader is looking at.
  it("accepts an anchor crossing the top edge", () => {
    expect(anchorIsVisible(rect({ top: -10, bottom: 20 }), bounds)).toBe(true);
  });

  it("accepts an anchor crossing the bottom edge", () => {
    expect(anchorIsVisible(rect({ top: 880, bottom: 910 }), bounds)).toBe(true);
  });

  it("rejects an anchor entirely above the band", () => {
    expect(anchorIsVisible(rect({ top: -40, bottom: -1 }), bounds)).toBe(false);
  });

  it("rejects an anchor entirely below the band", () => {
    expect(anchorIsVisible(rect({ top: 901, bottom: 940 }), bounds)).toBe(false);
  });

  it("rejects an anchor entirely to the left of the band", () => {
    expect(anchorIsVisible(rect({ left: -80, right: 0 }), bounds)).toBe(false);
  });

  it("rejects an anchor entirely to the right of the band", () => {
    expect(anchorIsVisible(rect({ left: 1280, right: 1340 }), bounds)).toBe(false);
  });

  // An edge exactly on the boundary shares no area with it, so it is outside.
  it("treats an anchor resting exactly on an edge as outside", () => {
    expect(anchorIsVisible(rect({ top: -30, bottom: 0 }), bounds)).toBe(false);
    expect(anchorIsVisible(rect({ top: 900, bottom: 930 }), bounds)).toBe(false);
  });

  // The band is the list's box, not the window's: a badge below a list that
  // ends halfway down the page is hidden even with viewport left over.
  it("uses the band it is given, not the window", () => {
    const clipped: VisibleBounds = { top: 60, bottom: 400, left: 0, right: 1280 };
    expect(anchorIsVisible(rect({ top: 500, bottom: 530 }), clipped)).toBe(false);
    expect(anchorIsVisible(rect({ top: 380, bottom: 410 }), clipped)).toBe(true);
  });
});
