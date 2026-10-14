import type { ConversationActivity } from "./chatTypes";
import { compareInstants, parseInstant } from "./instant";

/**
 * The minimum a sidebar row must expose to be ordered (issue #414).
 *
 * Channels, groups and 1:1 conversations are three different aggregates, but
 * the ordering rule is one rule, so it is stated once against the fields all
 * three share. Nothing here is specific to a section — which is what makes it
 * impossible for a channel to influence where a group or a DM lands.
 */
export interface ActivityOrdered extends ConversationActivity {
  id: string;
  name: string;
}

/**
 * The total order the sidebar renders every section in.
 *
 * Four keys, applied in order:
 *
 *  1. **Activity before silence.** A conversation with a persisted message
 *     always precedes one without, whatever the timestamps say. This is the
 *     first comparison and not a consequence of a merged sort key, because a
 *     conversation created a second ago must still sit behind one that was
 *     last written in months — `lastMessageAt ?? createdAt` gets that backwards.
 *  2. **Newest activity first**, among rows that have activity.
 *  3. **Newest creation first**, among rows that have none. Rows whose
 *     `createdAt` is missing or unparseable are treated as the oldest, so they
 *     land at the end of their group deterministically instead of at a position
 *     that depends on the input order.
 *  4. **Name, then id.** The name is compared normalised (trimmed, lowercased)
 *     with plain string ordering rather than `localeCompare`: collation is an
 *     ICU-dependent detail, and this comparator must return the same answer in
 *     every environment for the sidebar to look identical after a reload. The
 *     id is the final key and is unique, which is what makes the order *total* —
 *     no pair is ever "equal", so nothing is left to the (unspecified in older
 *     runtimes) stability of `Array.prototype.sort`.
 */
export function compareByActivity(a: ActivityOrdered, b: ActivityOrdered): number {
  const aPinned = parseInstant(a.pinnedAt);
  const bPinned = parseInstant(b.pinnedAt);
  if ((aPinned === undefined) !== (bPinned === undefined)) {
    return aPinned === undefined ? 1 : -1;
  }
  if (aPinned !== undefined && bPinned !== undefined) {
    const byPinnedAt = compareInstants(aPinned, bPinned);
    if (byPinnedAt !== 0) return byPinnedAt;
  }

  const aLast = parseInstant(a.lastMessageAt);
  const bLast = parseInstant(b.lastMessageAt);

  if ((aLast === undefined) !== (bLast === undefined)) {
    return aLast === undefined ? 1 : -1;
  }
  if (aLast !== undefined && bLast !== undefined) {
    const byActivity = compareInstants(bLast, aLast);
    if (byActivity !== 0) return byActivity;
  } else {
    const aCreated = parseInstant(a.createdAt);
    const bCreated = parseInstant(b.createdAt);
    // A row with no usable creation instant sorts last within its group, the
    // same place the previous -Infinity sentinel put it.
    if ((aCreated === undefined) !== (bCreated === undefined)) {
      return aCreated === undefined ? 1 : -1;
    }
    if (aCreated !== undefined && bCreated !== undefined) {
      const byCreation = compareInstants(bCreated, aCreated);
      if (byCreation !== 0) return byCreation;
    }
  }

  const aName = a.name.trim().toLowerCase();
  const bName = b.name.trim().toLowerCase();
  if (aName !== bName) return aName < bName ? -1 : 1;
  if (a.id !== b.id) return a.id < b.id ? -1 : 1;
  return 0;
}

/**
 * One section, ordered.
 *
 * Returns a new array: the input may be React state or a cached response, and
 * `Array.prototype.sort` mutates in place. Apply it per section — never to a
 * list that mixes categories — so the sections stay independent by construction.
 */
export function sortByActivity<T extends ActivityOrdered>(items: readonly T[]): T[] {
  return [...items].sort(compareByActivity);
}

/**
 * The later of two activity instants, as a monotonic merge.
 *
 * Activity only ever moves forward: a message that was persisted stays
 * persisted, and deletion is soft — the row and its `created_at` survive. So
 * when two sources disagree about a conversation's last activity, the newer one
 * is the one that has seen more, regardless of which arrived last. That is what
 * makes a stale refetch harmless: a response computed before an event cannot
 * roll the sidebar back to a moment that has already passed, and re-applying
 * the same event any number of times changes nothing.
 *
 * It compares with the same sub-millisecond precision the ordering uses, so a
 * message 78µs newer than the one already recorded still counts as newer. Two
 * values that denote the same instant in different notations compare equal, and
 * equal keeps `current` — an equivalent rewrite is not a change and must not
 * look like one to the caller deciding whether to allocate new state.
 */
export function laterActivity(
  current: string | null | undefined,
  incoming: string | null | undefined,
): string | null {
  const currentInstant = parseInstant(current);
  const incomingInstant = parseInstant(incoming);
  if (incomingInstant === undefined) return current ?? null;
  if (currentInstant === undefined || compareInstants(incomingInstant, currentInstant) > 0) {
    return incoming ?? null;
  }
  return current ?? null;
}
