/**
 * presence — this tab's single authority on who is online (RF-58).
 *
 * Why a module store and not props: presence is asked about in four places at
 * once (the sidebar's DM rows, the details panel's member list, the DM header,
 * the message list's avatars) and answered for the same person in all of them.
 * Threading it through those trees would mean four copies of one fact, four
 * chances for them to disagree, and a re-render of the whole chat when any
 * single user changes.
 *
 * Why it listens to the socket itself: `acquireChatSocket` is refcounted and
 * shared, so joining it here adds a listener, never a connection. The store
 * therefore never needs a subscription of its own, never polls, and cannot
 * become a second realtime path — it reads the frames the one connection is
 * already delivering.
 *
 * Authority: every value here came from the server. Nothing in this module can
 * assert a presence — there is no local "I am away" and no way to state
 * anything about another user. Reconciliation is the one decision it makes, and
 * it is made against server timestamps only (see `applyPresenceUpdate`).
 */

import { useSyncExternalStore } from "react";

import { getSessionGeneration, onAuthChange } from "../lib/authSession";
import { acquireChatSocket, type ChatSocketHandle } from "./chatSocket";

/**
 * What the UI may show for one person.
 *
 * `unknown` is not a fourth server state: it is "this tab has not been told
 * yet". It exists so a still-loading avatar can stay silent instead of claiming
 * the person is offline, which is a statement, not an absence of one.
 */
export type PresenceState = "online" | "away" | "offline" | "unknown";

/** The three states the server actually asserts. */
export type ServerPresenceState = Exclude<PresenceState, "unknown">;

export const presenceLabels: Record<PresenceState, string> = {
  online: "Online",
  away: "Ausente",
  offline: "Offline",
  // Deliberately not "Offline": not knowing where someone is and knowing they
  // are gone are different facts, and only one of them has been established.
  unknown: "Status indisponível",
};

export function presenceLabel(state: PresenceState): string {
  return presenceLabels[state];
}

/**
 * A server instant, split so that no precision is lost on the way in.
 *
 * The server stamps presence with RFC 3339 *nano*, and two transitions of the
 * same user can land in the same millisecond — the away timer and a disconnect
 * arriving together is enough. `Date.parse` truncates to milliseconds, which
 * turned those two distinct instants into a tie, and a tie is read as a
 * duplicate: the newer state was silently dropped and the avatar kept the older
 * one until something else moved.
 *
 * So the fraction is kept whole, beside the second it belongs to. Two numbers
 * rather than one because a nanosecond epoch does not fit in a JS number, and
 * `BigInt` would buy nothing here: a comparison never needs the sum.
 */
export interface PresenceInstant {
  /** Epoch milliseconds of the whole-second part, always a multiple of 1000. */
  secondMs: number;
  /** The fraction of that second, 0 – 999_999_999. */
  nanosecond: number;
}

/**
 * "The server stated no instant." Ordered before every real one, so an entry
 * carrying it can never displace an entry that does.
 */
const unstamped: PresenceInstant = { secondMs: 0, nanosecond: 0 };

function isUnstamped(instant: PresenceInstant): boolean {
  return instant.secondMs === 0 && instant.nanosecond === 0;
}

/**
 * RFC 3339 with an optional fraction of one to nine digits — the exact shape Go's
 * `time.RFC3339Nano` produces, plus the numeric offsets the format allows.
 *
 * Anchored, so a string that is nearly a timestamp is not read as one. Anything
 * this rejects is treated as "no instant stated" by the callers below, which is
 * the behaviour the store already had for an unparseable value.
 */
const rfc3339NanoPattern =
  /^(\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?([Zz]|[+-]\d{2}:\d{2})$/;

/**
 * Reads one RFC 3339 timestamp, keeping its fraction to the nanosecond.
 *
 * The whole-second part is handed to `Date.parse` — with the fraction removed,
 * so nothing is truncated and the offset is still applied by the platform rather
 * than by arithmetic here — and the fraction is padded to nine digits, because
 * `.1` and `.100000000` are the same instant while `"1" < "100000000"`
 * lexicographically. Returns null for anything that is not a timestamp.
 */
export function parsePresenceInstant(value: unknown): PresenceInstant | null {
  if (typeof value !== "string") return null;
  const match = rfc3339NanoPattern.exec(value);
  if (!match) return null;
  const secondMs = Date.parse(`${match[1]}${match[3]}`.toUpperCase());
  if (!Number.isFinite(secondMs)) return null;
  const fraction = match[2];
  return {
    secondMs,
    nanosecond: fraction ? Number.parseInt(fraction.padEnd(9, "0"), 10) : 0,
  };
}

/**
 * Orders two server instants: -1, 0 or 1, second first and fraction second.
 *
 * 0 means the same instant, which the callers read as "this is a repeat" — the
 * duplicate rule presence depends on. It is never a licence to break the tie on
 * something else: a state cannot order itself.
 */
export function comparePresenceInstant(a: PresenceInstant, b: PresenceInstant): -1 | 0 | 1 {
  if (a.secondMs !== b.secondMs) return a.secondMs < b.secondMs ? -1 : 1;
  if (a.nanosecond !== b.nanosecond) return a.nanosecond < b.nanosecond ? -1 : 1;
  return 0;
}

interface PresenceEntry {
  state: ServerPresenceState;
  /**
   * The server instant this state was decided, or `unstamped` when the server
   * sent none. Never a browser clock: it is only ever compared with another
   * value from the same source, so it decides ordering without this tab having
   * any opinion about what time it is.
   */
  updatedAt: PresenceInstant;
}

/** One conversation's view: who it says is present, and what it said about them. */
export type TargetEntries = ReadonlyMap<string, PresenceEntry>;

/** Every conversation this session has been told about, keyed by target. */
export type PresenceEntries = ReadonlyMap<string, TargetEntries>;

/**
 * A conversation, in the form presence is scoped by: `"channel:<id>"` or
 * `"dm:<id>"`. Built here so no caller has to remember the separator.
 */
export function presenceTargetKey(kind: "channel" | "dm", targetId: string): string {
  return `${kind}:${targetId}`;
}

/**
 * What the store holds. Exported so the reducer can be tested as a function.
 *
 * Entries are kept *per conversation* rather than in one map keyed by person,
 * and the reason is that the evidence itself is conversation-scoped: every
 * snapshot and every event says "here is what is true in this target". Folding
 * that into a single global answer threw away the provenance, and once it was
 * gone a later complete snapshot could no longer correct anything — it could add
 * people, never remove them, because the stale global entry always won. The
 * server's reconciliation was therefore unable to reach the screen.
 *
 * The person's presence is still one thing conceptually. What is target-scoped
 * is the *evidence*, and the selector is what turns evidence back into an
 * answer.
 */
export interface PresenceSnapshotState {
  entries: PresenceEntries;
  /**
   * The conversations the server has given a *complete* answer for.
   *
   * This is what makes absence readable, and why it is a set of targets rather
   * than one flag. A snapshot covers exactly one conversation: it lists the
   * people present in it, and says nothing whatsoever about anyone else. Reading
   * one as a global answer is how a user nobody had mentioned yet ended up shown
   * as offline on the strength of an empty snapshot for an unrelated channel.
   *
   * So a person is offline only when they are missing from a conversation the
   * server has fully described *and* the caller asked about them in that
   * conversation. Everywhere else they stay `unknown`, and no dot is drawn.
   *
   * An incomplete snapshot (see the server's `complete` flag) never lands here:
   * its entries are still applied, because each names one person and is true on
   * its own, but nothing may be concluded from who is missing from a list that
   * was cut short.
   *
   * It also *takes a conversation out* again. Coverage is a claim about what the
   * server can currently see, not a badge earned once: when the directory
   * degrades, the same target that answered completely a minute ago starts
   * answering `complete: false`, and continuing to read absence as offline would
   * turn "I no longer know" into "they are gone" — for everyone the shortened
   * list dropped, indefinitely, since no later frame contradicts an omission.
   */
  covered: ReadonlySet<string>;
}

export const emptyPresenceState: PresenceSnapshotState = {
  entries: new Map(),
  covered: new Set(),
};

/** One user's presence as it arrives on the wire. */
export interface PresenceWireEntry {
  user_id: string;
  state: string;
  updated_at?: string;
}

function parseWireEntry(value: unknown): { userId: string; entry: PresenceEntry } | null {
  if (!value || typeof value !== "object") return null;
  const raw = value as Record<string, unknown>;
  const userId = raw["user_id"];
  const state = raw["state"];
  if (typeof userId !== "string" || userId === "") return null;
  if (state !== "online" && state !== "away" && state !== "offline") return null;
  return {
    userId,
    // An unparseable or absent instant becomes the oldest possible value, so it
    // can never displace an update that does carry one.
    entry: { state, updatedAt: parsePresenceInstant(raw["updated_at"]) ?? unstamped },
  };
}

/**
 * Applies one user's presence inside one conversation, keeping the newer of the
 * two.
 *
 * Three properties, all from the same comparison:
 *  - a newer update replaces an older one;
 *  - an older update is discarded — presence arrives on several targets at once
 *    and over a bus, so out-of-order delivery is normal, not exceptional;
 *  - the same update applied twice is indistinguishable from applying it once.
 *
 * An update carrying the *same* instant as the stored one is a repeat and is
 * ignored, which is what makes the duplicate copies a user in several shared
 * conversations receives free to absorb. "Same instant" is to the nanosecond:
 * two transitions inside one millisecond are two facts, and the later one must
 * win. The one exception is an unstamped entry, meaning the server stated no
 * instant: there is no ordering to respect, so the latest arrival wins rather
 * than the first one sticking forever.
 *
 * Returns the same map when nothing changed, so React can skip the render.
 */
export function applyPresenceUpdate(
  entries: TargetEntries,
  userId: string,
  entry: PresenceEntry,
): TargetEntries {
  const current = entries.get(userId);
  if (current) {
    const order = comparePresenceInstant(entry.updatedAt, current.updatedAt);
    if (order < 0) return entries;
    if (order === 0) {
      if (entry.state === current.state) return entries;
      if (!isUnstamped(current.updatedAt)) return entries;
    }
  }
  const next = new Map(entries);
  next.set(userId, entry);
  return next;
}

/** Replaces one conversation's view, leaving every other conversation alone. */
function withTarget(
  entries: PresenceEntries,
  targetKey: string,
  target: TargetEntries,
): PresenceEntries {
  if (entries.get(targetKey) === target) return entries;
  const next = new Map(entries);
  next.set(targetKey, target);
  return next;
}

/**
 * Applies a snapshot to the conversation it describes.
 *
 * A complete snapshot *replaces* that conversation's view: everyone it lists is
 * applied through the ordering rule above, and everyone it does not list is
 * removed — that removal is the whole point, and it is what lets the server's
 * reconciliation correct a client that has been told something no longer true.
 *
 * Removal still respects ordering. An entry the client holds that is newer than
 * the snapshot survives it: the snapshot was read before that transition
 * happened, so it is not evidence about it. `takenAt` is the server's own
 * instant for the read, from the same clock as every `updated_at`.
 *
 * An incomplete snapshot only adds. Its entries are each true on their own, but
 * absence from a list that was cut short means nothing.
 */
export function applyPresenceSnapshot(
  entries: TargetEntries,
  users: readonly unknown[],
  options: { complete: boolean; takenAt: PresenceInstant },
): TargetEntries {
  let next = entries;
  const named = new Set<string>();
  for (const user of users) {
    const parsed = parseWireEntry(user);
    if (!parsed) continue;
    named.add(parsed.userId);
    next = applyPresenceUpdate(next, parsed.userId, parsed.entry);
  }
  if (!options.complete) return next;

  const survivors = new Map(next);
  let removed = false;
  for (const [userId, entry] of next) {
    if (named.has(userId)) continue;
    // Newer than this read — to the nanosecond, so a snapshot taken earlier in
    // the same millisecond cannot undo the transition that followed it.
    if (comparePresenceInstant(entry.updatedAt, options.takenAt) > 0) continue;
    survivors.delete(userId);
    removed = true;
  }
  return removed ? survivors : next;
}

/**
 * Folds one inbound socket frame into the state.
 *
 * A pure function of (state, frame), so every ordering, duplication and
 * malformed-payload case is testable without a socket, a component or a timer.
 * Frames that are not presence leave the state untouched and identical.
 */
export function reducePresence(
  state: PresenceSnapshotState,
  frame: Record<string, unknown>,
): PresenceSnapshotState {
  const target = frameTarget(frame);
  if (target === "") return state;

  if (frame["type"] === "presence.snapshot") {
    const users = Array.isArray(frame["users"]) ? frame["users"] : [];
    // A missing completeness flag is read as incomplete: a server that did not
    // say does not authorise an inference on its behalf.
    const complete = frame["complete"] === true;
    const takenAt = parseInstant(frame["taken_at"]);
    const current = state.entries.get(target) ?? emptyTarget;
    const updated = applyPresenceSnapshot(current, users, { complete, takenAt });
    const wasCovered = state.covered.has(target);
    // Coverage follows the *latest* answer in both directions: a complete one
    // grants it, an incomplete one withdraws it. A server that has stopped being
    // able to answer fully does not leave its previous answer standing in.
    const coverageMoved = complete !== wasCovered;
    if (updated === current && !coverageMoved) return state;

    const entries = withTarget(state.entries, target, updated);
    if (!coverageMoved) return { entries, covered: state.covered };
    const covered = new Set(state.covered);
    // Covered even when the list is empty: "nobody is here" is a complete answer
    // about this conversation.
    if (complete) covered.add(target);
    else covered.delete(target);
    return { entries, covered };
  }

  if (frame["type"] === "presence.updated") {
    const parsed = parseWireEntry(frame["presence"]);
    if (!parsed) return state;
    const current = state.entries.get(target) ?? emptyTarget;
    const updated = applyPresenceUpdate(current, parsed.userId, parsed.entry);
    if (updated === current) return state;
    return { entries: withTarget(state.entries, target, updated), covered: state.covered };
  }
  return state;
}

const emptyTarget: TargetEntries = new Map();

/** The conversation a presence frame is about. Both kinds carry it. */
function frameTarget(frame: Record<string, unknown>): string {
  const kind = frame["target_type"];
  const targetId = frame["target_id"];
  if ((kind !== "channel" && kind !== "dm") || typeof targetId !== "string" || targetId === "") {
    return "";
  }
  return presenceTargetKey(kind, targetId);
}

function parseInstant(value: unknown): PresenceInstant {
  return parsePresenceInstant(value) ?? unstamped;
}

/**
 * Resolves what to show for one user.
 *
 * `targetKey` is the conversation the caller is rendering this person in, and it
 * is answered from that conversation's evidence alone. That scoping is what
 * makes a complete snapshot able to correct a stale value: the answer comes from
 * the view the snapshot just replaced, not from a global memory of everything
 * ever heard.
 *
 * Without a conversation — the viewer's own avatar in the sidebar footer — the
 * answer is whatever any conversation currently says, and never `offline`:
 * nothing establishes absence for a caller that named no place to be absent
 * from. The scan is over conversations this session is subscribed to, which is
 * the user's own list, not the workspace.
 */
export function selectPresence(
  state: PresenceSnapshotState,
  userId: string,
  targetKey?: string,
): PresenceState {
  if (!userId) return "unknown";
  if (targetKey) {
    const entry = state.entries.get(targetKey)?.get(userId);
    if (entry) return entry.state;
    return state.covered.has(targetKey) ? "offline" : "unknown";
  }
  let best: PresenceEntry | undefined;
  for (const target of state.entries.values()) {
    const entry = target.get(userId);
    if (entry && (!best || comparePresenceInstant(entry.updatedAt, best.updatedAt) > 0)) {
      best = entry;
    }
  }
  return best ? best.state : "unknown";
}

// ── store ────────────────────────────────────────────────────────────────────

let state: PresenceSnapshotState = emptyPresenceState;
const listeners = new Set<() => void>();
let handle: ChatSocketHandle | null = null;
/**
 * The socket generation the current entries were learned on.
 *
 * The reset has to key on this rather than on attaching, because attaching
 * happens twice for one connection: the shared socket is refcounted, so a route
 * change that unmounts every avatar and mounts a new set releases and re-joins
 * a connection that never went anywhere. Resetting on join would drop the map
 * with nothing able to refill it — snapshots only arrive with a subscribe, and
 * no resubscribe happens for a socket that stayed open.
 */
let lastGeneration = -1;
/**
 * The session these entries belong to.
 *
 * Presence is scoped to an identity: it is a set of statements about the people
 * *this* user can see, in the workspace their session resolves to. When the
 * session changes — a logout, a different account, a token replaced — those
 * statements stop being about anything the new session is entitled to, and they
 * have to go at once rather than when some later socket happens to open. A
 * failed sign-in that never gets a socket would otherwise leave the previous
 * user's contacts on screen.
 *
 * The workspace needs no separate treatment: it is resolved server-side from
 * the session, so a client cannot change it without changing this.
 */
let sessionScope = getSessionGeneration();
let unsubscribeAuth: (() => void) | null = null;

function emit(): void {
  for (const listener of [...listeners]) listener();
}

function setState(next: PresenceSnapshotState): void {
  if (next === state) return;
  state = next;
  emit();
}

/**
 * Drops everything learned under a session that is no longer the current one.
 *
 * Synchronous and unconditional: it does not wait for a socket, because the
 * case it exists for is the one where no socket ever comes. It also moves the
 * socket generation forward, so a frame still in flight from the previous
 * connection cannot land in the new scope's state.
 */
function handleSessionChange(): void {
  const generation = getSessionGeneration();
  if (generation === sessionScope) return;
  sessionScope = generation;
  lastGeneration = -1;
  setState(emptyPresenceState);
}

function attach(): void {
  if (handle) return;
  sessionScope = getSessionGeneration();
  unsubscribeAuth = onAuthChange(handleSessionChange);
  handle = acquireChatSocket({
    // A new socket generation means everything known was learned over a
    // connection that no longer exists. Someone may have gone offline while
    // this tab was away, and no event for that is coming — the server does not
    // replay. So the map is dropped and rebuilt from the snapshots the
    // resubscribes produce, and until the first one lands every avatar reads
    // `unknown` rather than a state that was true a minute ago.
    onOpen: (generation) => {
      if (generation === lastGeneration) return;
      lastGeneration = generation;
      setState(emptyPresenceState);
    },
    onMessage: (data) => setState(reducePresence(state, data)),
    // A closed socket is not evidence about anybody else. The entries are kept
    // so a brief drop does not blank every avatar, but the coverage is dropped:
    // whatever the server had fully described is no longer something this tab
    // can reason about absence with until it has answered again.
    onClose: () => setState({ entries: state.entries, covered: new Set() }),
    // Keeping the entries across a drop is only defensible while a reconnect is
    // still coming to correct them. `failed` is the connection saying it has
    // stopped trying — a permanent rejection, or the bounded give-up after
    // repeated failures — and from there no reconnect, no snapshot and no event
    // will ever arrive. Whatever is on screen would stay on screen for as long as
    // the tab is open: an avatar asserting somebody is online on the strength of
    // a connection that died, with nothing left that could ever contradict it.
    //
    // So the answer becomes "this tab does not know", which is the truth. If the
    // connection recovers — a new session, a fresh acquire — the open callback
    // rebuilds from the snapshots the resubscribes produce.
    onStatus: (status) => {
      if (status !== "failed") return;
      lastGeneration = -1;
      setState(emptyPresenceState);
    },
  });
}

// Releases the shared connection without discarding what was learned on it: the
// generation check in onOpen decides whether the entries are still valid, and a
// socket that outlived this release is still the one they came from.
function detach(): void {
  handle?.release();
  handle = null;
  unsubscribeAuth?.();
  unsubscribeAuth = null;
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  if (listeners.size === 1) attach();
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0) detach();
  };
}

/**
 * The presence of one user, as this session currently understands it.
 *
 * Every caller subscribes to the same store and each re-renders only when *its*
 * user's value changes: the snapshot returned is the state string itself, so
 * React bails out of a render for the other ninety-nine avatars when one person
 * goes away.
 *
 * `targetKey` names the conversation the caller is rendering this person in —
 * the DM row's conversation, the panel's channel, the message list's target.
 * It is what lets "not in that conversation's roster" mean offline. A caller
 * with no conversation to point at (the viewer's own avatar in the sidebar
 * footer, a face in a search result) gets `online`/`away` when the server has
 * said so and `unknown` otherwise, which is the honest answer rather than a
 * grey dot standing in for a fact nobody established.
 */
export function usePresence(userId: string | undefined, targetKey?: string): PresenceState {
  return useSyncExternalStore(
    subscribe,
    () => selectPresence(state, userId ?? "", targetKey),
    () => "unknown" as PresenceState,
  );
}

/** Test-only: drop every listener and reset the state. */
export function _resetPresenceStore(): void {
  listeners.clear();
  detach();
  lastGeneration = -1;
  sessionScope = getSessionGeneration();
  state = emptyPresenceState;
}
