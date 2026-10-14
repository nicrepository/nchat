import { describe, expect, it } from "vitest";

import { isExposedEnough, latestExposedMessage, READ_EXPOSURE_FRACTION } from "./readVisibility";

const viewport = { top: 100, bottom: 500 }; // 400px tall

function element(top: number, bottom: number): Element {
  return { getBoundingClientRect: () => ({ top, bottom }) } as unknown as Element;
}

const position = (id: string, minute: number) => ({
  id,
  createdAt: `2026-07-15T10:${String(minute).padStart(2, "0")}:00Z`,
});

describe("isExposedEnough", () => {
  it("reads a row fully inside the viewport", () => {
    expect(isExposedEnough({ top: 200, bottom: 240 }, viewport)).toBe(true);
  });

  it(`reads a row at exactly ${READ_EXPOSURE_FRACTION * 100}% exposure, not below`, () => {
    expect(isExposedEnough({ top: 480, bottom: 520 }, viewport)).toBe(true);
    expect(isExposedEnough({ top: 481, bottom: 521 }, viewport)).toBe(false);
  });

  it("does not read a row mounted outside the viewport (overscan)", () => {
    expect(isExposedEnough({ top: 600, bottom: 640 }, viewport)).toBe(false);
    expect(isExposedEnough({ top: 20, bottom: 60 }, viewport)).toBe(false);
  });

  it("does not read a row with no box", () => {
    expect(isExposedEnough({ top: 300, bottom: 300 }, viewport)).toBe(false);
    expect(isExposedEnough({ top: 200, bottom: 240 }, { top: 0, bottom: 0 })).toBe(false);
  });

  it("reads a row taller than the viewport once it covers half of the viewport", () => {
    // 2000px row: half of it can never be on screen at once.
    expect(isExposedEnough({ top: 300, bottom: 2300 }, viewport)).toBe(true); // 200px visible
    expect(isExposedEnough({ top: 301, bottom: 2301 }, viewport)).toBe(false); // 199px
    expect(isExposedEnough({ top: -1000, bottom: 1000 }, viewport)).toBe(true); // fills it
  });

  it("only gets more read as a seen row grows", () => {
    const seen = { top: 200, bottom: 240 };
    const grown = { top: 200, bottom: 640 };
    expect(isExposedEnough(seen, viewport)).toBe(true);
    expect(isExposedEnough(grown, viewport)).toBe(true);
  });
});

describe("latestExposedMessage", () => {
  const positions = new Map([
    ["a", position("a", 1)],
    ["b", position("b", 2)],
    ["c", position("c", 3)],
  ]);

  it("picks the latest exposed eligible row, in timeline order not DOM order", () => {
    const rows: [string, Element][] = [
      ["c", element(300, 340)],
      ["a", element(150, 190)],
      ["b", element(200, 240)],
    ];
    expect(latestExposedMessage(rows, viewport, (id) => positions.get(id))?.id).toBe("c");
  });

  it("ignores rows that are not eligible, such as the reader's own", () => {
    const rows: [string, Element][] = [
      ["a", element(150, 190)],
      ["own", element(300, 340)],
    ];
    expect(latestExposedMessage(rows, viewport, (id) => positions.get(id))?.id).toBe("a");
  });

  it("ignores eligible rows that are mounted but off screen", () => {
    const rows: [string, Element][] = [
      ["a", element(150, 190)],
      ["c", element(700, 740)],
    ];
    expect(latestExposedMessage(rows, viewport, (id) => positions.get(id))?.id).toBe("a");
  });

  it("is null when nothing qualifies", () => {
    expect(latestExposedMessage([], viewport, (id) => positions.get(id))).toBeNull();
  });
});
