/**
 * How a sidebar row's unread state moves (issue #1082), as pure functions.
 *
 * The count is the server's. The browser can prove what the server answered
 * and in which causal order, but not which messages a count included: a
 * message it holds may have been deleted without an event reaching it, a late
 * commit or a deep link across a gap may hold one the count never saw. So
 * reading here never takes anything off a count — it moves the reader's
 * cursor, which the writer persists, and the answer to that write is the next
 * count. The badge follows the server's acknowledgement of what was read.
 *
 *  - The row's base is one server answer; its read point is the confirmed
 *    read frontier, which never moves back. An answer whose point is behind it
 *    was computed before that point was written, whatever order the requests
 *    started in: its count is refused. An answer from further ahead becomes
 *    the base. At the base's own point, an answer to a request started after
 *    the base arrived is newer, and taken whatever it says; two concurrent
 *    answers cannot be ordered, so the higher count stands.
 *  - A realtime message adds one, until the reader's cursor passes it or a
 *    base whose request started after it arrived accounts for it.
 *  - "Marcar como lida" is the reader's explicit word that everything is read:
 *    the count shows only what arrives afterwards, until the server answers.
 *
 * Whenever the browser cannot tell, it shows more unread, never fewer — and
 * every such upper bound marks the row for reconciliation: one refetch, sent
 * after it, settles it.
 */

import type { ConversationReadState, RealtimeArrival, ServerReadSnapshot } from "./chatTypes";
import type { TimelinePosition } from "./messages/messageOrder";
import {
  isBehind,
  isReadThrough,
  laterReadPosition,
  type ReadPosition,
  type ReadProgress,
} from "./readCursor";

/** The fields of a sidebar row this module reads and writes; see Channel's. */
export interface ReadRow {
  id: string;
  unreadCount?: number;
  hasMentionUnread?: boolean;
  readState?: ConversationReadState;
  serverRead?: ServerReadSnapshot;
  confirmedAt?: number;
  arrivals?: RealtimeArrival[];
  reconcileAfter?: number;
  markAllSince?: number;
  readThrough?: TimelinePosition;
  unreadMention?: ReadPosition;
  unknownMention?: boolean;
}

/** When, on the session's request clock, an answer's request started and its answer landed. */
export interface AnswerProvenance {
  startedAt: number;
  receivedAt: number;
}

/** The unread count a row with a base shows; see the module comment. */
export function projectedUnread(row: ReadRow): number | undefined {
  const base = row.serverRead;
  if (!base) return row.unreadCount;
  const since = row.markAllSince;
  const unreadArrivals = (row.arrivals ?? []).filter(
    (arrival) =>
      !isReadThrough(row.readThrough, arrival) && (since === undefined || arrival.at > since),
  ).length;
  return since === undefined ? base.unreadCount + unreadArrivals : unreadArrivals;
}

function laterPoint(
  a: TimelinePosition | null | undefined,
  b: TimelinePosition | null | undefined,
): TimelinePosition | undefined {
  if (!a) return b ?? undefined;
  return b ? laterReadPosition(a, b) : a;
}

/** Whether an answer becomes the base, and whether the browser could tell. */
function judge(
  row: ReadRow,
  state: ConversationReadState,
  provenance: AnswerProvenance,
): { taken: boolean; ambiguous: boolean } {
  const base = row.serverRead;
  if (!base) return { taken: true, ambiguous: false };
  if (isBehind(state.readThrough, base.readThrough)) {
    // Asked before that point was confirmed: a refetch asked now will see it.
    // Asked after, and still behind: no refetch would do better.
    const askedBefore = provenance.startedAt < (row.confirmedAt ?? Number.POSITIVE_INFINITY);
    return { taken: false, ambiguous: askedBefore };
  }
  if (isBehind(base.readThrough, state.readThrough) || provenance.startedAt > base.receivedAt) {
    return { taken: true, ambiguous: false };
  }
  // Concurrent with the base, at its point: the higher count is the safe one.
  return { taken: state.unreadCount >= base.unreadCount, ambiguous: true };
}

/** A row's reconciliation mark once an answer is taken. */
function reconcileAfter(
  current: number | undefined,
  provenance: AnswerProvenance,
  ambiguous: boolean,
): number | undefined {
  if (ambiguous) return provenance.receivedAt;
  return current !== undefined && provenance.startedAt > current ? undefined : current;
}

/** The row on a newly accepted base. */
function rebase<T extends ReadRow>(
  row: T,
  state: ConversationReadState,
  provenance: AnswerProvenance,
  ambiguousAnswer: boolean,
): T {
  const point = state.readThrough;
  const arrivals = (row.arrivals ?? []).filter(
    (arrival) => arrival.at > provenance.startedAt && !isReadThrough(point, arrival),
  );
  // An arrival seen while the request was out may already be in its count.
  const ambiguous =
    ambiguousAnswer || arrivals.some((arrival) => arrival.at < provenance.receivedAt);
  const advanced = !row.serverRead || isBehind(row.serverRead.readThrough, point);
  return {
    ...row,
    serverRead: { ...state, ...provenance },
    confirmedAt: advanced ? provenance.receivedAt : row.confirmedAt,
    arrivals,
    reconcileAfter: reconcileAfter(row.reconcileAfter, provenance, ambiguous),
  };
}

/** An answer offered to a row; see judge. */
function offer<T extends ReadRow>(
  row: T,
  state: ConversationReadState,
  provenance: AnswerProvenance,
): T {
  const { taken, ambiguous } = judge(row, state, provenance);
  if (taken) return rebase(row, state, provenance, ambiguous);
  return ambiguous ? { ...row, reconcileAfter: provenance.receivedAt } : row;
}

/**
 * Zero as the server stated it — its own count, or the reader's explicit "all
 * read" — rather than a projection reaching it. Only this clears a mention the
 * client cannot place.
 */
function isSettledZero(row: ReadRow): boolean {
  if (row.unreadCount !== 0) return false;
  const base = row.serverRead;
  return !base || row.markAllSince !== undefined || base.unreadCount === 0;
}

/**
 * The mention fields once the read state is known: a known mention goes when
 * a read point passes it, an unknown one only on a settled zero.
 */
function settleMentions<T extends ReadRow>(row: T): T {
  const readPoint = laterPoint(row.readThrough, row.serverRead?.readThrough);
  const settledZero = isSettledZero(row);
  const unknownMention = !settledZero && Boolean(row.unknownMention);
  const known = row.unreadMention;
  const unreadMention =
    settledZero || (known && isReadThrough(readPoint, known)) ? undefined : known;
  return {
    ...row,
    unknownMention,
    unreadMention,
    hasMentionUnread: unknownMention || !!unreadMention,
  };
}

const sameValues = [
  "unreadCount",
  "serverRead",
  "confirmedAt",
  "reconcileAfter",
  "markAllSince",
  "readThrough",
  "unreadMention",
] as const;
const sameFlags = ["unknownMention", "hasMentionUnread"] as const;

function sameArrivals(a: RealtimeArrival[] | undefined, b: RealtimeArrival[] | undefined) {
  const x = a ?? [];
  const y = b ?? [];
  return x.length === y.length && x.every((arrival, i) => arrival === y[i]);
}

/** Whether two rows show and hold the same read state. */
function sameReadState(a: ReadRow, b: ReadRow): boolean {
  return (
    sameValues.every((key) => a[key] === b[key]) &&
    sameFlags.every((key) => Boolean(a[key]) === Boolean(b[key])) &&
    sameArrivals(a.arrivals, b.arrivals)
  );
}

/**
 * The count and mentions recomputed from the row's facts. Returns `before`
 * itself when nothing changed, so a no-op costs no render.
 */
function settle<T extends ReadRow>(before: T, next: T): T {
  const settled = settleMentions({ ...next, unreadCount: projectedUnread(next) });
  return sameReadState(settled, before) ? before : settled;
}

/**
 * A precise server answer offered to a row — a refetch's, or the answer to
 * one of this session's writes. The answer to "Marcar como lida" — even a
 * failed or refused one — ends the optimistic zero.
 */
export function acceptServerRead<T extends ReadRow>(
  row: T,
  state: ConversationReadState | undefined,
  provenance: AnswerProvenance | undefined,
  { endsMarkAll = false }: { endsMarkAll?: boolean } = {},
): T {
  let next: T = endsMarkAll ? { ...row, markAllSince: undefined } : row;
  if (state && provenance) next = offer(next, state, provenance);
  return settle(row, next);
}

/**
 * A sidebar fetch's answer for one row. Without the precise cursor (#1082
 * rollback) its count is the new base and nothing precise survives it,
 * because that server knows none of it.
 */
export function acceptSidebarAnswer<T extends ReadRow>(
  row: T,
  answer: { readState?: ConversationReadState; unreadCount?: number; precise: boolean },
  provenance: AnswerProvenance,
): T {
  if (answer.precise && answer.readState) {
    return acceptServerRead(row, answer.readState, provenance);
  }
  return settleMentions({
    ...row,
    // An older server may omit the count; the live one stands then.
    unreadCount: answer.unreadCount ?? row.unreadCount,
    serverRead: undefined,
    confirmedAt: undefined,
    arrivals: undefined,
    reconcileAfter: undefined,
    markAllSince: undefined,
  });
}

/**
 * What a row carries across a refetch, before the refetch's own answer is
 * offered to it. In-memory state wins; the persisted cache only bridges the
 * first load, and a mention restored from it carries no position, so it is an
 * unknown one — never the absence of one.
 */
export function rememberRead<T extends ReadRow>(
  incoming: T,
  previous: ReadRow | undefined,
  persisted: { unreadCount: number; hasMentionUnread: boolean } | undefined,
): T {
  if (previous) {
    return {
      ...incoming,
      unreadCount: previous.unreadCount,
      serverRead: previous.serverRead,
      confirmedAt: previous.confirmedAt,
      arrivals: previous.arrivals,
      reconcileAfter: previous.reconcileAfter,
      markAllSince: previous.markAllSince,
      readThrough: previous.readThrough,
      unreadMention: previous.unreadMention,
      unknownMention: previous.unknownMention,
      hasMentionUnread: previous.hasMentionUnread,
    };
  }
  const unknownMention = Boolean(persisted?.hasMentionUnread ?? incoming.hasMentionUnread);
  return {
    ...incoming,
    serverRead: undefined,
    unreadCount: incoming.unreadCount ?? persisted?.unreadCount,
    unknownMention,
    hasMentionUnread: unknownMention,
  };
}

/** A mention remembered with its position, unless a read point passed it. */
function rememberMention<T extends ReadRow>(row: T, message: ReadPosition): T {
  const read =
    isReadThrough(row.readThrough, message) || isReadThrough(row.serverRead?.readThrough, message);
  return read ? row : { ...row, unreadMention: laterReadPosition(row.unreadMention, message) };
}

/** How one realtime message bears on a row's count. */
export interface ArrivalKind {
  /** From someone else: an unread message, wherever it lands. */
  eligible: boolean;
  /** Counted without a precise base, until a server answer reconciles it. */
  counts: boolean;
  isMention: boolean;
}

/** A counting arrival, by the precise rule when the row has a base. */
function countArrival<T extends ReadRow>(row: T, kind: ArrivalKind, arrival: RealtimeArrival): T {
  const base = row.serverRead;
  if (!base) return kind.counts ? { ...row, unreadCount: (row.unreadCount ?? 0) + 1 } : row;
  const known = (row.arrivals ?? []).some(({ id }) => id === arrival.id);
  if (!kind.eligible || known || isReadThrough(base.readThrough, arrival)) return row;
  return { ...row, arrivals: [...(row.arrivals ?? []), arrival] };
}

/**
 * One realtime message on its row, observed at tick `at`. With a base, an
 * eligible message adds one until the reader's cursor passes it or a base
 * taken after it arrived accounts for it. Without one, the old rule adds to
 * the count.
 */
export function applyArrival<T extends ReadRow>(
  row: T,
  kind: ArrivalKind,
  message: ReadPosition,
  at: number,
): T {
  const counted = countArrival(row, kind, { ...message, at });
  const next = kind.isMention ? rememberMention(counted, message) : counted;
  return row.serverRead ? settle(row, next) : settleMentions(next);
}

/**
 * The open timeline's progress: the reader's cursor, which only moves
 * forward. It takes nothing off the server's count — the write it feeds does,
 * when the server answers. Against a server without the read cursor there is
 * no safe partial projection: the loaded timeline may contain gaps, so its
 * server count stands.
 */
export function applyReadProgress<T extends ReadRow>(row: T, progress: ReadProgress): T {
  if (!row.serverRead) return row;
  const readThrough = laterReadPosition(row.readThrough, progress.readThrough);
  return settle(row, { ...row, readThrough });
}

/**
 * The explicit "everything here is read", asked for at tick `at`, before the
 * server has answered. The count drops to what arrives from here on; no read
 * point is invented for it — the server's answer brings the real one.
 */
export function applyMarkAllRequested<T extends ReadRow>(row: T, at: number): T {
  if (!row.serverRead) return settleMentions({ ...row, unreadCount: 0 });
  return settle(row, { ...row, markAllSince: at });
}
