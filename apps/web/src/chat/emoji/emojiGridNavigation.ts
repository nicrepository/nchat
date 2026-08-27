/**
 * Arrow-key movement inside the emoji grid (issue #496).
 *
 * A grid of several hundred buttons is unusable with Tab alone, so the grid is
 * one tab stop and the arrows move within it — the roving-tabindex pattern.
 * Keeping the arithmetic here, away from the DOM, is what makes "down from the
 * last row stays put" and "right from the last cell stops" testable without
 * rendering anything.
 *
 * Returns the index to move to, or null when the key is not a movement key or
 * the movement would leave the grid.
 */

const rowSteps: Record<string, number> = { ArrowRight: 1, ArrowLeft: -1 };

export function nextEmojiIndex(
  key: string,
  index: number,
  total: number,
  columns: number,
): number | null {
  if (total <= 0) return null;
  const target = movementTarget(key, index, total, columns);
  if (target === null || target === index) return null;
  return target >= 0 && target < total ? target : null;
}

function movementTarget(key: string, index: number, total: number, columns: number): number | null {
  if (key === "Home") return 0;
  if (key === "End") return total - 1;
  if (key === "ArrowDown") return index + columns;
  if (key === "ArrowUp") return index - columns;
  return key in rowSteps ? index + rowSteps[key] : null;
}
