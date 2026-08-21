import { describe, expect, it } from "vitest";

import { clampPosition, positionForCorner, snapPosition } from "./floatingGeometry";

const size = { width: 320, height: 180 };
const viewport = { width: 1000, height: 700 };

describe("floating call geometry", () => {
  it("clamps the window inside the viewport", () => {
    expect(clampPosition({ x: 900, y: -5 }, size, viewport, 16)).toEqual({ x: 664, y: 16 });
  });

  it("handles viewports smaller than the floating window", () => {
    expect(
      clampPosition(
        { x: 100, y: 100 },
        { width: 500, height: 500 },
        { width: 320, height: 240 },
        16,
      ),
    ).toEqual({ x: 16, y: 16 });
  });

  it("snaps a position near a viewport corner", () => {
    expect(snapPosition({ x: 660, y: 510 }, size, viewport, 24, 16)).toEqual({
      position: { x: 664, y: 504 },
      corner: "bottom-right",
    });
  });

  it("keeps a clamped free position outside the snap threshold", () => {
    expect(snapPosition({ x: 300, y: 250 }, size, viewport, 24, 16)).toEqual({
      position: { x: 300, y: 250 },
      corner: null,
    });
  });

  it("provides keyboard-selectable corner positions", () => {
    expect(positionForCorner("top-left", size, viewport, 16)).toEqual({ x: 16, y: 16 });
    expect(positionForCorner("bottom-right", size, viewport, 16)).toEqual({ x: 664, y: 504 });
  });
});
