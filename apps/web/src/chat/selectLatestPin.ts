/**
 * selectLatestPin — the single rule for "which pinned message is the current
 * one" (issue #435).
 *
 * The pinned bar above the conversation and the details panel both call this,
 * with the same PinnedItem[] from the same usePins instance, so the two
 * surfaces cannot drift: there is one query, one list and one selector.
 */

import type { PinnedItem } from "./chatTypes";

/**
 * Ranking key for one pin.
 *
 * pinnedAt is the criterion, because "most recently pinned" is what the panel
 * claims to show — a message written last year and pinned today is the current
 * pin. The fallback matters: pinnedAt is optional in older payloads and may be
 * an unparseable string, and Date.parse of garbage yields NaN, which loses every
 * comparison silently. So an unusable pinnedAt falls back to the message's
 * createdAt (the only other timestamp the domain guarantees), and an unusable
 * createdAt falls back to -Infinity, which keeps such a pin selectable only when
 * nothing better exists rather than letting NaN decide the winner.
 */
function pinRank(pin: PinnedItem): number {
  const pinnedAt = Date.parse(pin.pinnedAt ?? "");
  if (!Number.isNaN(pinnedAt)) return pinnedAt;
  const createdAt = Date.parse(pin.message.createdAt ?? "");
  return Number.isNaN(createdAt) ? Number.NEGATIVE_INFINITY : createdAt;
}

/**
 * Returns the most recently pinned message, or null when there is none.
 *
 * Ties are broken by the message ID (the greater one wins), so two pins written
 * in the same transaction always resolve to the same message — the order the
 * HTTP response happened to arrive in is never the deciding factor.
 *
 * The input array is never mutated: this is a reduce, not a sort.
 */
export function selectLatestPin(pins: readonly PinnedItem[]): PinnedItem | null {
  let best: PinnedItem | null = null;
  let bestRank = Number.NEGATIVE_INFINITY;
  for (const pin of pins) {
    const rank = pinRank(pin);
    if (
      best === null ||
      rank > bestRank ||
      (rank === bestRank && pin.message.id > best.message.id)
    ) {
      best = pin;
      bestRank = rank;
    }
  }
  return best;
}
