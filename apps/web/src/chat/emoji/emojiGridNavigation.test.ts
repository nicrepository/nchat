import { describe, expect, it } from "vitest";

import { nextEmojiIndex } from "./emojiGridNavigation";

describe("nextEmojiIndex", () => {
  const total = 20;
  const columns = 8;

  it("moves along the row and down a row", () => {
    expect(nextEmojiIndex("ArrowRight", 0, total, columns)).toBe(1);
    expect(nextEmojiIndex("ArrowLeft", 5, total, columns)).toBe(4);
    expect(nextEmojiIndex("ArrowDown", 2, total, columns)).toBe(10);
    expect(nextEmojiIndex("ArrowUp", 10, total, columns)).toBe(2);
  });

  it("jumps to the ends of the grid", () => {
    expect(nextEmojiIndex("Home", 7, total, columns)).toBe(0);
    expect(nextEmojiIndex("End", 7, total, columns)).toBe(total - 1);
  });

  // Movement that would leave the grid is refused rather than clamped: the key
  // then keeps its default meaning and focus stays where the user put it.
  it("refuses to move outside the grid", () => {
    expect(nextEmojiIndex("ArrowLeft", 0, total, columns)).toBeNull();
    expect(nextEmojiIndex("ArrowRight", total - 1, total, columns)).toBeNull();
    expect(nextEmojiIndex("ArrowUp", 3, total, columns)).toBeNull();
    expect(nextEmojiIndex("ArrowDown", 18, total, columns)).toBeNull();
  });

  it("ignores keys that are not movement and grids with nothing in them", () => {
    expect(nextEmojiIndex("Enter", 0, total, columns)).toBeNull();
    expect(nextEmojiIndex("a", 0, total, columns)).toBeNull();
    expect(nextEmojiIndex("ArrowRight", 0, 0, columns)).toBeNull();
    expect(nextEmojiIndex("Home", 0, total, columns)).toBeNull();
  });
});
