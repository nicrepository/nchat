/**
 * The one ordering rule the timeline has: messages are kept in the server's
 * canonical (created_at, id) order — the order every listing returns, and the
 * order POST …/read resolves a read cursor in (issue #1082).
 *
 * Delivery is not ordered — a realtime event, a fallback GET and a focused-page
 * read can all produce a message that belongs before the last one drawn — so
 * every path that adds a message goes through here rather than appending.
 *
 * created_at is compared as an instant, never as text: RFC3339Nano drops
 * trailing zeros, so "10:00:00Z" and "10:00:00.1Z" sort the wrong way as
 * strings, and two notations of one instant must compare equal. Ties on the
 * instant fall to the id, as the database's uuid ordering does — and for the
 * lowercase canonical text the API sends, that is plain string order.
 */

import type { Message } from "../chatTypes";
import { compareInstants, parseInstant } from "../instant";

/**
 * A point in the timeline: a message's position, or — with `id` null — the end
 * of an instant, after every message created at it. The second kind is what a
 * read state written before the cursor existed means (see readCursor.ts).
 */
export interface TimelinePosition {
  createdAt: string;
  id: string | null;
}

/**
 * Ascending by instant; an unparseable timestamp sorts before every parseable
 * one, and among themselves by text, so the order stays total.
 */
function compareCreatedAt(a: string, b: string): number {
  const aInstant = parseInstant(a);
  const bInstant = parseInstant(b);
  if (aInstant && bInstant) return compareInstants(aInstant, bInstant);
  if (aInstant || bInstant) return aInstant ? 1 : -1;
  if (a === b) return 0;
  return a < b ? -1 : 1;
}

/** Ascending: negative when `a` comes first in the timeline. */
export function compareTimelinePositions(a: TimelinePosition, b: TimelinePosition): number {
  const byInstant = compareCreatedAt(a.createdAt, b.createdAt);
  if (byInstant !== 0) return byInstant;
  if (a.id === b.id) return 0;
  if (a.id === null) return 1;
  if (b.id === null) return -1;
  return a.id < b.id ? -1 : 1;
}

export function insertMessageChronologically(
  messages: Message[],
  message: Message,
): { messages: Message[]; isNewer: boolean } {
  // Most messages are newer than everything drawn, so a tail check avoids a
  // full sort in the common case.
  const last = messages.at(-1);
  const isNewer = last === undefined || compareTimelinePositions(message, last) > 0;
  return {
    messages: isNewer
      ? [...messages, message]
      : [...messages, message].sort(compareTimelinePositions),
    isNewer,
  };
}
