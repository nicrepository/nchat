/**
 * Persisting the read cursor without a request per message (issue #1082).
 *
 * Reading produces a stream of ever-later positions — one per row that comes
 * into view — and the server only needs the latest. So, per conversation:
 *
 *  - a short debounce gathers a burst into one write; it is armed by the first
 *    position after a quiet spell and never pushed back by later ones, so a
 *    reader scrolling without pause still gets a write every window;
 *  - at most one write is in flight; whatever arrives meanwhile is held as the
 *    single greatest pending position and sent one window after the write
 *    settles — so however fast the network answers, a reader never causes more
 *    than one write per window plus a round trip, never one per message;
 *  - a position that is not ahead of what is pending, in flight or already
 *    known to the server is dropped, so a duplicate or an older one never
 *    writes.
 *
 * "Mark the whole conversation read" is not a position and is not ordered
 * against one. It means "through whatever the server holds when it resolves
 * the request", which says nothing about a message that arrives while it is in
 * flight — so it is its own flag beside the explicit cursor, and a cursor
 * observed meanwhile is kept and sent after it, measured against the point the
 * server answered with.
 *
 * A failed write is not retried: nothing is acknowledged, so the next advance
 * goes out, and the sidebar's next refetch reconciles with the server either
 * way. The server is the last line of monotonicity — this only saves it work.
 *
 * One writer serves one session — one user in one workspace. Its owner
 * disposes it when that identity ends; nothing pending survives into the next
 * session, and a write still in flight finishes without effect.
 *
 * Whether the server takes explicit positions at all can change while a write
 * waits — a rollback reaches an open tab as a refetch. So it is asked when the
 * write is sent, not only when it is queued: a position the server no longer
 * understands is dropped there and never leaves.
 */

import type { ConversationReadState } from "./chatApi";
import { compareTimelinePositions, type TimelinePosition } from "./messages/messageOrder";
import { isBehind, type ReadPosition } from "./readCursor";

export interface ReadTarget {
  kind: "channel" | "dm";
  targetId: string;
}

/** Short enough to feel immediate in the sidebar, long enough to absorb a scroll. */
export const READ_CURSOR_DEBOUNCE_MS = 400;

export type ReadWriteSender<S extends ConversationReadState = ConversationReadState> = (
  target: ReadTarget,
  lastReadMessageId: string | undefined,
  options: { keepalive: boolean },
) => Promise<S | undefined>;

/**
 * How one write ended, as the owner needs to know it. `state` is the server's
 * read state after the write — absent when the write failed, when the server
 * predates that answer, or when the answer is older than one already
 * acknowledged and so carries nothing new.
 */
export interface ReadWriteOutcome<S extends ConversationReadState = ConversationReadState> {
  request: ReadPosition | "all";
  ok: boolean;
  state?: S;
}

export interface ReadCursorWriterOptions {
  debounceMs?: number;
  /** Whether the server takes explicit positions right now; asked at send time. */
  acceptsPositions?: () => boolean;
}

interface Entry {
  target: ReadTarget;
  /** The greatest explicit position waiting to be sent. */
  pending: ReadPosition | null;
  /** A mark-all waiting to be sent. */
  markAllPending: boolean;
  /** The request on the wire: a position, "all", or nothing. */
  inFlight: ReadPosition | "all" | null;
  /**
   * The furthest point the server is known to hold. It only moves forward,
   * whichever request's answer brought it — a normal write's or one sent as
   * the page went away — so a late answer from an older request is ignored.
   */
  acknowledged: TimelinePosition | null;
  timer: ReturnType<typeof setTimeout> | null;
}

export interface ReadCursorWriter {
  /** A position the reader was observed to read through. */
  advance: (target: ReadTarget, position: ReadPosition) => void;
  /** Everything the server holds in this conversation, as of the request. */
  markAll: (target: ReadTarget) => void;
  /**
   * Sends every debounced write now, as keepalive, still one in flight per
   * conversation — for a tab going to the background, which may never return.
   */
  flush: () => void;
  /**
   * The page is being hidden away (pagehide): send every pending write now, as
   * keepalive, even beside a write already in flight — the one exception to
   * "one in flight", safe because the server never moves a cursor backwards
   * whatever order the two arrive in. Their answers are acknowledged like any
   * other, so if the page lives on (back/forward cache) nothing older than what
   * they brought is ever taken. The writer stays usable.
   */
  flushOnUnload: () => void;
  /** Ends the session: timers cancelled, pending dropped, late answers ignored. */
  dispose: () => void;
}

export function readTargetKey(target: ReadTarget): string {
  return `${target.kind}:${target.targetId}`;
}

/** The inverse of readTargetKey; undefined for anything it did not produce. */
export function readTargetFromKey(key: string): ReadTarget | undefined {
  const separator = key.indexOf(":");
  const kind = key.slice(0, separator);
  const targetId = key.slice(separator + 1);
  if ((kind !== "channel" && kind !== "dm") || !targetId) return undefined;
  return { kind, targetId };
}

/** Whether `candidate` is beyond every known point ("all" is not one). */
function isAhead(candidate: ReadPosition, ...known: (TimelinePosition | "all" | null)[]) {
  return known.every(
    (point) => point === null || point === "all" || compareTimelinePositions(candidate, point) > 0,
  );
}

function newEntry(target: ReadTarget): Entry {
  return {
    target,
    pending: null,
    markAllPending: false,
    inFlight: null,
    acknowledged: null,
    timer: null,
  };
}

/**
 * The next request this entry owes the server, taken off its queue. A position
 * the server no longer takes is dropped rather than sent.
 */
function takeNext(entry: Entry, acceptsPositions: () => boolean): ReadPosition | "all" | null {
  if (entry.markAllPending) {
    entry.markAllPending = false;
    return "all";
  }
  const next = entry.pending;
  entry.pending = null;
  return next && acceptsPositions() ? next : null;
}

export function createReadCursorWriter<S extends ConversationReadState = ConversationReadState>(
  send: ReadWriteSender<S>,
  /** Every write's ending, failures included, while the session lasts. */
  onSettled: (target: ReadTarget, outcome: ReadWriteOutcome<S>) => void,
  {
    debounceMs = READ_CURSOR_DEBOUNCE_MS,
    acceptsPositions = () => true,
  }: ReadCursorWriterOptions = {},
): ReadCursorWriter {
  const entries = new Map<string, Entry>();
  let disposed = false;

  const request = (entry: Entry, through: ReadPosition | "all", keepalive: boolean) =>
    // Started from a resolved promise so that a `send` throwing synchronously
    // becomes a rejection like any other: a write always settles exactly once.
    Promise.resolve()
      .then(() => {
        if (disposed) return undefined;
        return send(entry.target, through === "all" ? undefined : through.id, { keepalive });
      })
      .then(
        (state) => acknowledge(entry, through, state),
        () => {
          // Not acknowledged: a later advance is free to try again.
          if (!disposed) onSettled(entry.target, { request: through, ok: false });
        },
      );

  const acknowledge = (entry: Entry, through: ReadPosition | "all", state: S | undefined) => {
    if (disposed) return;
    const fresh = state && !isBehind(state.readThrough, entry.acknowledged) ? state : undefined;
    if (fresh?.readThrough) entry.acknowledged = fresh.readThrough;
    // A pending position the server already holds is not worth a request.
    if (entry.pending && !isAhead(entry.pending, entry.acknowledged)) entry.pending = null;
    onSettled(entry.target, { request: through, ok: true, state: fresh });
  };

  const write = (entry: Entry, keepalive = false) => {
    entry.timer = null;
    if (disposed || entry.inFlight !== null) return;
    const through = takeNext(entry, acceptsPositions);
    if (through === null) return;
    entry.inFlight = through;
    void request(entry, through, keepalive).finally(() => {
      entry.inFlight = null;
      if (entry.pending !== null || entry.markAllPending) schedule(entry);
    });
  };

  const schedule = (entry: Entry) => {
    if (!disposed && entry.inFlight === null && entry.timer === null) {
      entry.timer = setTimeout(() => write(entry), debounceMs);
    }
  };

  const entryFor = (target: ReadTarget): Entry => {
    const key = readTargetKey(target);
    const existing = entries.get(key);
    if (existing) return existing;
    const entry = newEntry(target);
    entries.set(key, entry);
    return entry;
  };

  return {
    advance: (target, position) => {
      if (disposed) return;
      const entry = entryFor(target);
      if (!isAhead(position, entry.pending, entry.inFlight, entry.acknowledged)) return;
      entry.pending = position;
      schedule(entry);
    },
    markAll: (target) => {
      if (disposed) return;
      const entry = entryFor(target);
      entry.markAllPending = true;
      schedule(entry);
    },
    flush: () => {
      for (const entry of entries.values()) {
        if (entry.timer === null) continue;
        clearTimeout(entry.timer);
        write(entry, true);
      }
    },
    flushOnUnload: () => {
      if (disposed) return;
      for (const entry of entries.values()) {
        if (entry.timer !== null) clearTimeout(entry.timer);
        entry.timer = null;
        // Mark-all first, then the explicit cursor: the server keeps the
        // greater of the two whichever lands last, and so does `acknowledged`.
        for (
          let through = takeNext(entry, acceptsPositions);
          through !== null;
          through = takeNext(entry, acceptsPositions)
        ) {
          void request(entry, through, true);
        }
      }
    },
    dispose: () => {
      disposed = true;
      for (const entry of entries.values()) {
        if (entry.timer !== null) clearTimeout(entry.timer);
      }
      entries.clear();
    },
  };
}
