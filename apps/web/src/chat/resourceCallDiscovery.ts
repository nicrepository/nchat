import type { ResourceCallKind } from "./callApi";
import type { Call } from "./callState";

/**
 * Discovery — separated from participation (issue #622 round 2).
 *
 * This module tracks, for every channel/group-DM target this tab has ever
 * observed (via a broadcast or an explicit call.resource.sync), the
 * authoritative answer to "is there an active call here right now" — nothing
 * about whether *this* user is in it. Participation stays exclusively
 * useResourceCallSession's job. Observing X is never participating in X:
 * this store is populated by every subscribed channel/group-DM's lifecycle
 * broadcasts (CallSessionProvider's global subscription) regardless of
 * whether the current user is busy with a different call entirely — #609
 * only ever blocks that user's own join/start of X, never their ability to
 * see that X exists.
 *
 * Every ordering decision here is authoritative-server-derived: call.version
 * for two events about the same call_id, and a server timestamp
 * (occurred_at, or observed_at for a call:null sync answer) for anything
 * else. Nothing here ever reads Date.now(), a browser clock, or trusts
 * WebSocket delivery order as evidence of anything.
 */

export type ResourceKey = string;

export function resourceKey(kind: ResourceCallKind, id: string): ResourceKey {
  return `${kind}:${id}`;
}

/**
 * One target's current, authoritative discovery observation.
 *
 * `call` is the active call payload, or null — meaning "no active call as of
 * `cursor`", which covers both a genuinely idle target and one that just
 * went terminal. `cursor` is always a server timestamp: the observation's
 * own call.occurred_at when `call` is non-null, or the sync's observed_at
 * when it is null. It is retained even once `call` is null specifically so a
 * stale, older event can never resurrect what a newer null (or a newer,
 * different call_id) already superseded — never deleted wholesale on a
 * terminal transition (see convergeResourceCallEvent).
 */
export interface ResourceCallObservation {
  kind: ResourceCallKind;
  id: string;
  call: Call | null;
  cursor: string;
}

export type ResourceCallDiscoveryState = ReadonlyMap<ResourceKey, ResourceCallObservation>;

export const emptyResourceCallDiscovery: ResourceCallDiscoveryState = new Map();

function parseTimestamp(value: string): number {
  return Date.parse(value);
}

/**
 * Whether `candidate` (a server timestamp) is strictly after `current` (also
 * a server timestamp). A malformed candidate never wins — the existing
 * observation is kept exactly as it was, so one corrupt/unparseable
 * timestamp can never overwrite otherwise-good state. A malformed `current`
 * (which should never occur — every stored cursor was itself validated on
 * the way in) does not block a well-formed candidate from advancing.
 */
function isStrictlyNewer(candidate: string, current: string): boolean {
  const candidateMs = parseTimestamp(candidate);
  if (Number.isNaN(candidateMs)) return false;
  const currentMs = parseTimestamp(current);
  if (Number.isNaN(currentMs)) return true;
  return candidateMs > currentMs;
}

function converge(
  current: ResourceCallObservation | undefined,
  kind: ResourceCallKind,
  id: string,
  candidateCall: Call | null,
  candidateCursor: string,
): ResourceCallObservation {
  if (!current) return { kind, id, call: candidateCall, cursor: candidateCursor };

  // Rule A: same call_id on both sides — version is the sole authority, never
  // a timestamp. A terminal status at a higher version still wins (hiding the
  // indicator); an equal-or-lower version never retreats state, so a stale
  // "accepted" delivered after a higher-version "ended" can never resurrect it.
  if (current.call && candidateCall && current.call.call_id === candidateCall.call_id) {
    if (candidateCall.version > current.call.version) {
      return { kind, id, call: candidateCall, cursor: candidateCursor };
    }
    return current;
  }

  // Rule B/C/D: a different call_id, or either side is a null tombstone —
  // the server timestamp cursor is the sole authority. A tie never advances
  // (treated as "not newer"): with millisecond-resolution parsing of a
  // nanosecond-precision server timestamp, an exact tie means "no evidence
  // this is newer", not "this is the same event".
  if (isStrictlyNewer(candidateCursor, current.cursor)) {
    return { kind, id, call: candidateCall, cursor: candidateCursor };
  }
  return current;
}

function withObservation(
  state: ResourceCallDiscoveryState,
  key: ResourceKey,
  next: ResourceCallObservation,
): ResourceCallDiscoveryState {
  const existing = state.get(key);
  if (existing === next) return state;
  const copy = new Map(state);
  copy.set(key, next);
  return copy;
}

/**
 * Converges a lifecycle broadcast (call.ringing is never relevant here —
 * resource calls have no ringing state — but call.accepted/declined/
 * cancelled/timed_out/ended all arrive as a Call payload the same way) or a
 * non-null call.resource.sync answer (issue #622 round 2, rule E: a sync
 * carrying a Call converges by the exact same version rule as a broadcast,
 * never trusting response order).
 */
export function convergeResourceCallEvent(
  state: ResourceCallDiscoveryState,
  kind: ResourceCallKind,
  id: string,
  call: Call,
): ResourceCallDiscoveryState {
  const key = resourceKey(kind, id);
  const next = converge(state.get(key), kind, id, call, call.occurred_at);
  return withObservation(state, key, next);
}

/**
 * Converges a call.resource.sync answer of call: null — a tombstone at
 * observedAt. Never deletes a target's entry; a null answer only ever wins
 * against a strictly OLDER observation (rule C), so a null that was already
 * "in flight" when a newer call started can never erase that newer call once
 * both have been observed in either order.
 */
export function convergeResourceSyncNull(
  state: ResourceCallDiscoveryState,
  kind: ResourceCallKind,
  id: string,
  observedAt: string,
): ResourceCallDiscoveryState {
  const key = resourceKey(kind, id);
  const next = converge(state.get(key), kind, id, null, observedAt);
  return withObservation(state, key, next);
}

/**
 * Removes every observation whose target is not in `knownKeys` — issue #622
 * round 2 section 6: a resource that stopped existing or became
 * inaccessible in the directory must not keep a stale "call ativa" indicator
 * forever, since it will never again receive a subscription/broadcast to
 * correct it. Purely a cache-eviction, never an authorization decision on
 * its own — the server remains the sole authority for whether a join is
 * actually allowed.
 */
export function pruneResourceCallDiscovery(
  state: ResourceCallDiscoveryState,
  knownKeys: ReadonlySet<ResourceKey>,
): ResourceCallDiscoveryState {
  let next: Map<ResourceKey, ResourceCallObservation> | null = null;
  for (const key of state.keys()) {
    if (!knownKeys.has(key)) {
      next ??= new Map(state);
      next.delete(key);
    }
  }
  return next ?? state;
}
