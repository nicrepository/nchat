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

import { anchorIsVisible, placeAgainstAnchor, type VisibleBounds } from "./useAnchoredPicker";

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

/**
 * Where a floating box lands against its anchor (issue #839 follow-up).
 *
 * The property under test is that a placed box never crosses the anchor it
 * belongs to: above it with the gap, below it with the gap, or not placed at
 * all — a box "nudged" into the band by clamping would land over its own
 * anchor, and a toolbar drawn across its message misattributes every action.
 */
describe("placeAgainstAnchor", () => {
  /** The list's band: a 640px-tall list below a header, across the window. */
  const band: VisibleBounds = { top: 60, bottom: 700, left: 0, right: 1280 };
  const gap = 6;
  const box = { width: 246, height: 36 } as DOMRect;

  function place(anchor: DOMRect) {
    const element = document.createElement("div");
    const placed = placeAgainstAnchor(element, anchor, box, 300, gap, gap, band);
    const top = Number.parseFloat(element.style.top);
    return { placed, top, bottom: top + box.height, visibility: element.style.visibility };
  }

  /** Placed boxes sit wholly above or wholly below the anchor, gap included. */
  function expectClearOf(anchor: DOMRect, result: ReturnType<typeof place>) {
    const above = result.bottom <= anchor.top - gap;
    const below = result.top >= anchor.bottom + gap;
    expect(above || below).toBe(true);
  }

  it("goes above when there is room above", () => {
    const anchor = rect({ top: 300, bottom: 340 });
    const result = place(anchor);
    expect(result).toMatchObject({ placed: true, top: 300 - 36 - gap, visibility: "visible" });
    expect(anchor.top - result.bottom).toBe(gap);
    expectClearOf(anchor, result);
  });

  it("goes below when there is room only below", () => {
    const anchor = rect({ top: 70, bottom: 110 });
    const result = place(anchor);
    expect(result).toMatchObject({ placed: true, top: 110 + gap, visibility: "visible" });
    expect(result.top).toBeGreaterThanOrEqual(anchor.bottom + gap);
    expectClearOf(anchor, result);
  });

  // An anchor spanning the band: neither side has 36 + 6 + 8 pixels to spare.
  it("places nothing when the box fits on neither side", () => {
    const anchor = rect({ top: 70, bottom: 690 });
    const element = document.createElement("div");
    expect(placeAgainstAnchor(element, anchor, box, 300, gap, gap, band)).toBe(false);
    expect(element.style.visibility).toBe("hidden");
    expect(element.style.top).toBe("");
  });

  // An anchor outside the band is not one the reader is looking at, however
  // well a box would fit beside where it is: nothing is written, nothing shown.
  it.each([
    ["below the band", rect({ top: 710, bottom: 750 })],
    ["above the band", rect({ top: 10, bottom: 50 })],
  ])("places nothing against an anchor %s", (_where, anchor) => {
    const element = document.createElement("div");
    element.style.top = "100px";
    element.style.left = "100px";
    expect(placeAgainstAnchor(element, anchor, box, 300, gap, gap, band)).toBe(false);
    expect(element.style.visibility).toBe("hidden");
    expect(element.style.top).toBe("100px");
    expect(element.style.left).toBe("100px");
  });

  // Half past an edge is still an anchor the reader is looking at.
  it("still places against an anchor crossing an edge of the band", () => {
    const anchor = rect({ top: 40, bottom: 80 });
    const result = place(anchor);
    expect(result).toMatchObject({ placed: true, top: 80 + gap, visibility: "visible" });
    expectClearOf(anchor, result);
  });

  it("stays clear of the anchor wherever the anchor sits in the band", () => {
    for (let top = 60; top <= 660; top += 20) {
      const anchor = rect({ top, bottom: top + 40 });
      const result = place(anchor);
      expect(result.placed).toBe(true);
      expectClearOf(anchor, result);
    }
  });
});
