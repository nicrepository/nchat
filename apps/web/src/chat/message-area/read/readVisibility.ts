/**
 * What "this message was actually seen" means, as geometry (issue #1082).
 *
 * A row counts as read when enough of it has been exposed inside the scroll
 * viewport: half of the row, or — for a row taller than the viewport, which can
 * never show half of itself — half of the viewport. One inequality covers both:
 *
 *   visibleHeight >= READ_EXPOSURE_FRACTION * min(rowHeight, viewportHeight)
 *
 * Half, and not "any pixel": a row peeking in at the edge of the screen has not
 * been read. Not "all of it" either: a row the reader is looking at while the
 * composer or a rounding pixel clips it has been. The rule depends only on the
 * row's box against the viewport's, so it gives the same answer however the
 * row got there — wheel, keyboard, touch, scrollbar, a jump — and a row that
 * grows later (an image finishing its layout) can only be shown more, never
 * un-read: the cursor that already passed it does not move back.
 *
 * Only mounted rows are ever asked. The virtualizer already limits those to the
 * visible window plus overscan, so this never touches the rest of the history.
 */

import { compareTimelinePositions } from "../../messages/messageOrder";
import type { ReadPosition } from "../../readCursor";

export const READ_EXPOSURE_FRACTION = 0.5;

export interface VerticalSpan {
  top: number;
  bottom: number;
}

export function isExposedEnough(row: VerticalSpan, viewport: VerticalSpan): boolean {
  const rowHeight = row.bottom - row.top;
  const viewportHeight = viewport.bottom - viewport.top;
  if (rowHeight <= 0 || viewportHeight <= 0) return false;
  const visible = Math.min(row.bottom, viewport.bottom) - Math.max(row.top, viewport.top);
  return visible >= READ_EXPOSURE_FRACTION * Math.min(rowHeight, viewportHeight);
}

/**
 * The latest read position the mounted rows that pass the rule account for, or
 * null when none does.
 *
 * `readFor` resolves a row's id to the position reading it accounts for, or
 * nothing — the reader's own messages account for nothing of their own, so an
 * own message on screen never carries the cursor past somebody else's that was
 * not seen. Only the rows passed in are measured, and those are the mounted
 * ones: the visible window plus the virtualizer's overscan.
 */
export function latestExposedMessage(
  rows: Iterable<[string, Element]>,
  viewport: VerticalSpan,
  readFor: (messageId: string) => ReadPosition | undefined,
): ReadPosition | null {
  let latest: ReadPosition | null = null;
  for (const [id, element] of rows) {
    const position = readFor(id);
    if (!position || (latest && compareTimelinePositions(position, latest) <= 0)) continue;
    if (isExposedEnough(element.getBoundingClientRect(), viewport)) latest = position;
  }
  return latest;
}
