/**
 * The read cursor (issue #1082), as pure functions over messages.
 *
 * Read state is not viewport state. The viewport answers "where is the
 * timeline"; the read cursor answers "how far has this reader actually read",
 * and it only ever moves forward. Its unit is a position in the timeline's
 * canonical (created_at, id) order — messageOrder's, the same one the server
 * resolves POST …/read in — so the client and the server never disagree about
 * which of two messages comes later.
 *
 * Unread is derived from the cursor, never counted on the side: the messages
 * after it that isEligibleUnreadMessage accepts, which is the server's own
 * definition (active, from someone else).
 */

import { findFirstUnreadBoundary, isEligibleUnreadMessage } from "./chatViewportState";
import { compareTimelinePositions, type TimelinePosition } from "./messages/messageOrder";

/** A message's own position: what a reader can be observed to have read. */
export interface ReadPosition {
  id: string;
  createdAt: string;
}

/**
 * What an open timeline reports as the reader reads: a cursor this session
 * was observed to reach past everything already known read. The sidebar
 * persists it; the server's answer is what moves the count.
 */
export interface ReadProgress {
  readThrough: ReadPosition;
}

interface PositionedMessage extends ReadPosition {
  status: string;
  senderId: string;
}

/**
 * The monotonic merge: the later of the two, and `current` itself when the
 * incoming position is not ahead of it — so a caller can tell "nothing
 * changed" by identity.
 */
export function laterReadPosition<T extends TimelinePosition>(
  current: T | null | undefined,
  incoming: T,
): T {
  if (current && compareTimelinePositions(incoming, current) <= 0) return current;
  return incoming;
}

/** Whether `position` has been read by a reader whose cursor is `cursor`. */
export function isReadThrough(
  cursor: TimelinePosition | null | undefined,
  position: TimelinePosition,
): boolean {
  return cursor != null && compareTimelinePositions(position, cursor) <= 0;
}

/**
 * Whether a server read point is behind a confirmed one — the one comparison
 * every confirmed read frontier is held to (#1082). The server's read point
 * never moves back, so an answer whose point is behind one already confirmed
 * was computed before that point was written: it is stale, whatever order the
 * requests started or the answers arrived in.
 */
export function isBehind(
  point: TimelinePosition | null,
  frontier: TimelinePosition | null | undefined,
): boolean {
  if (!frontier) return false;
  return point === null || compareTimelinePositions(point, frontier) < 0;
}

/**
 * How many eligible messages sit after `cursor`.
 *
 * Walks back from the newest message and stops at the first eligible message
 * the cursor covers, so the cost is the unread tail, not the loaded history.
 * The array is in canonical order, so nothing older can follow that stop. The
 * reader's own messages are skipped without stopping the walk: they never
 * count, and an optimistic send stamped by the browser's clock is not a
 * position to measure against.
 */
export function unreadAfter(
  messages: readonly PositionedMessage[],
  cursor: TimelinePosition,
  currentUserId: string,
): number {
  let count = 0;
  for (let i = messages.length - 1; i >= 0; i--) {
    const message = messages[i];
    if (!isEligibleUnreadMessage(message, currentUserId)) continue;
    if (isReadThrough(cursor, message)) break;
    count++;
  }
  return count;
}

/**
 * Where the cursor stood when the conversation was opened, as far as the
 * loaded window and the opening count can tell.
 *
 * Nothing unread: everything loaded was read, so the newest message. Unread
 * whose first message is in the window: the message just before it. Anything
 * else — the boundary is older than the window — is null, "somewhere before
 * everything loaded", which is exactly what makes every eligible message on
 * screen an unread one.
 */
export function openingReadCursor(
  messages: readonly PositionedMessage[],
  currentUserId: string,
  unreadCountAtOpen: number,
): ReadPosition | null {
  if (unreadCountAtOpen <= 0) return messages.at(-1) ?? null;
  const boundary = findFirstUnreadBoundary(messages, currentUserId, unreadCountAtOpen);
  return boundary && boundary.index > 0 ? messages[boundary.index - 1] : null;
}

/**
 * Whether a server read point says anything about the loaded window.
 *
 * A point older than the first loaded message cannot be counted from: the
 * unread messages between it and the window are not loaded. Such a point is
 * left to the server's own count instead.
 */
export function pointInWindow(
  point: TimelinePosition | null | undefined,
  messages: readonly ReadPosition[],
): point is TimelinePosition {
  const first = messages[0];
  return point != null && first !== undefined && compareTimelinePositions(point, first) >= 0;
}

export interface UnreadProjectionInput {
  messages: readonly PositionedMessage[];
  currentUserId: string;
  /** The cursor, or null while it is still before everything loaded. */
  cursor: TimelinePosition | null;
  /** The server's count when the conversation was opened. */
  unreadCountAtOpen: number;
  /** The newest message when the conversation was opened. */
  openTail: ReadPosition | null;
}

/**
 * The timeline's loaded unread count after the cursor.
 *
 * A deep link may leave gaps, so this is not the conversation's total unread
 * count. Before a cursor is known, the opening count stands, plus loaded
 * messages newer than the opening tail. The sidebar owns the server total.
 */
export function projectUnread(input: UnreadProjectionInput): number {
  const { messages, currentUserId, cursor, openTail } = input;
  if (cursor) return unreadAfter(messages, cursor, currentUserId);
  return input.unreadCountAtOpen + (openTail ? unreadAfter(messages, openTail, currentUserId) : 0);
}
