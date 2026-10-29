/**
 * What this document knows of the browser history behind the entry it is on
 * (issue #1124): for every entry the app has been on, its path and the entry
 * right before it.
 *
 * Recorded where documentEntry records leaving the reloaded entry, and for the
 * same reason: the router hears every move — PUSH, REPLACE, POP — through the
 * one listener it subscribes to the history, synchronously, before React renders
 * anything. A render can be suspended or thrown away, a transition superseded by
 * the next one; the history entry was still created, so nothing rendered can
 * stand in for this record.
 *
 * The browser keeps the order, so this only has to keep the links:
 *  - PUSH creates an entry right after the current one, and the browser drops
 *    every entry that was after it — reachable by Forward until then — so they
 *    are dropped here too (CQ-06). Which ones they are is known by position:
 *    the router keeps each entry's index in the browser's own history state
 *    (`idx`, kept across reloads), so an entry from before a reload is placed
 *    as surely as one recorded here (CQ-08);
 *  - REPLACE swaps the current entry for a new one in the same place, so the new
 *    one inherits the old one's predecessor and the entry after it (reachable by
 *    Forward) now points back at the new one;
 *  - POP moves between existing entries. An entry this document never recorded —
 *    one from before a reload — is known by its path only, with nothing behind
 *    it, because nothing here can tell what is.
 *
 * Only paths are kept: no query, no state, no title. A path is what the address
 * bar already shows; the query is where a search or a callback's code would be.
 * Module memory, like documentEntry's: nothing is persisted, a reload starts
 * empty, and a session change (sign-in, sign-out, another user) starts over from
 * the entry on screen, so one session's trail is never another's.
 *
 * A token rotation is not proof of the same user — the refresh cookie is shared
 * by every tab, so a rotation can install another account. The trail is
 * therefore owned by the identity the server confirmed (the sidebar's user and
 * workspace, claimHistoryTrail): a claim by anyone else starts it over too.
 * A rotation before anyone confirmed the trail has no owner to compare a later
 * claim with, so it starts the trail over at once.
 */

import type { Location, NavigationType } from "react-router";

import { onAuthChange } from "./authSession";

export interface TrailEntry {
  key: string;
  pathname: string;
}

interface RecordedEntry {
  pathname: string;
  previous: string | null;
  /** Its position in the browser's list, as the router recorded it; null when unknown. */
  index: number | null;
}

const entries = new Map<string, RecordedEntry>();
let current: TrailEntry = { key: "", pathname: "" };
let owner: string | null = null;

/**
 * A position as the router records it: a whole, non-negative number. Anything
 * else — NaN, Infinity, a fraction, a negative — places nothing, so it is
 * unknown (CQ-12): never pruned by, never pruning anything.
 */
function position(index: number | null): number | null {
  return index !== null && Number.isSafeInteger(index) && index >= 0 ? index : null;
}

/** Starts the record at `location`, at `index`, with nothing known behind it. */
export function startHistoryTrail(
  location: Pick<Location, "key" | "pathname">,
  index: number | null = null,
): void {
  entries.clear();
  owner = null;
  current = { key: location.key, pathname: location.pathname };
  entries.set(current.key, { pathname: current.pathname, previous: null, index: position(index) });
}

/** Starts over from the entry on screen, where it is. */
function restartHistoryTrail(): void {
  startHistoryTrail(current, entries.get(current.key)?.index ?? null);
}

/** The identity the trail was claimed for, null before any claim. */
export function historyTrailOwner(): string | null {
  return owner;
}

/**
 * Binds the trail to the identity the server confirmed for this session. The
 * first claim adopts what was recorded since the session started; a claim by
 * another identity — a rotation that turned out to be another user, another
 * workspace — starts over from the entry on screen.
 */
export function claimHistoryTrail(identity: string): void {
  if (owner !== null && owner !== identity) restartHistoryTrail();
  owner = identity;
}

/**
 * Called by documentHistory's listener for every move, before the router hears
 * it, with the position the move landed on (null when unknown).
 */
export function recordHistoryMove(
  action: NavigationType,
  location: Pick<Location, "key" | "pathname">,
  index: number | null = null,
): void {
  const from = current.key;
  const at = position(index);
  current = { key: location.key, pathname: location.pathname };
  if (action === "POP") {
    if (!entries.has(location.key))
      entries.set(location.key, { pathname: location.pathname, previous: null, index: at });
    return;
  }
  if (action === "PUSH") {
    if (at !== null) forgetEntriesFrom(at);
    entries.set(location.key, { pathname: location.pathname, previous: from, index: at });
    return;
  }
  recordReplace(from, location, at);
}

function recordReplace(
  from: string,
  location: Pick<Location, "key" | "pathname">,
  index: number | null,
): void {
  // REPLACE: the new entry is the old one's slot in the browser's list, so it
  // keeps what was behind the old one, and whatever was recorded after the old
  // one — an entry still reachable by Forward — now has the new one behind it.
  entries.set(location.key, {
    pathname: location.pathname,
    previous: entries.get(from)?.previous ?? null,
    index,
  });
  if (from === location.key) return;
  entries.delete(from);
  for (const entry of entries.values()) if (entry.previous === from) entry.previous = location.key;
}

/**
 * Drops every entry at `index` or later: the ones a PUSH to `index` cut off. An
 * entry whose position is unknown is kept — kept too long at worst, never lost.
 */
function forgetEntriesFrom(index: number): void {
  for (const [key, entry] of entries)
    if (entry.index !== null && entry.index >= index) entries.delete(key);
}

/** How many entries are on record. For tests of what is kept only. */
export function _recordedEntryCount(): number {
  return entries.size;
}

/**
 * The entries from the current one backwards, nearest first, for as far as this
 * document knows them. Index `n` is `n` steps back (`history.go(-n)`).
 */
export function historyTrail(): TrailEntry[] {
  const trail: TrailEntry[] = [];
  for (let key: string | null = current.key; key !== null; ) {
    const entry = entries.get(key);
    if (!entry) break;
    trail.push({ key, pathname: entry.pathname });
    key = entry.previous;
  }
  return trail;
}

// ponytail: document-lifetime subscription, the same one chatApi keeps for its
// session cache — this module lives exactly as long as the document does.
onAuthChange((change) => {
  if (change === "session" || owner === null) restartHistoryTrail();
});
