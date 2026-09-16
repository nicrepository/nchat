/**
 * Burst control for notification presentation (issue #750).
 *
 * This module answers one question, locally, and it can only ever *remove* a
 * surface the policy already permitted: has this client announced this event
 * already, and has this conversation chimed a moment ago?
 *
 * It decides no policy. The authority for *whether an event may alert at all*
 * remains `libs/go/platform/notificationpolicy`, resolved per recipient by
 * chat-service (#744) — including the historical and imported origins, which
 * that engine already denies on every channel (`denyHistorical`). Nothing here
 * reconstructs that decision, and nothing here can turn a `deny` into a
 * surface.
 *
 * ## Temporality is a boundary, not a flag
 *
 * There is deliberately no client-side "origin" on an event. Whether something
 * is news is settled by *which code path is holding it*, and that boundary is
 * structural: only the live WebSocket fan-out reaches
 * `presentLiveMessageNotification`, and the rule is enforced by ESLint rather
 * than by a value a caller could pass wrongly. See notificationPresentation
 * for the boundary and for what each recovered path does instead.
 *
 * ## Identity
 *
 * Dedupe identity is the server's `message_id`, alone. Not the arrival time,
 * not the envelope's `event_id` (the hub mints a fresh one per publish), not a
 * client-generated id — a fresh id per delivery is a dedupe that never fires —
 * and nothing derived from the body: two deliveries of one id are the same
 * event whatever they carry.
 *
 * ## What is retained
 *
 * Opaque ids and cooldown keys mapped to an expiry instant, in the bounded
 * structure from expiringKeySet: no body, no preview, no sender, no
 * conversation name. Both stores are bounded by TTL *and* by capacity, neither
 * sweeps, every hot-path operation is O(1) amortised, and no timer is ever
 * created for an event.
 */

import { createExpiringKeySet } from "./expiringKeySet";

/**
 * How long this client remembers having announced an event.
 *
 * It is a presentation memory and nothing else — long enough to cover a
 * reconnect and a relay echo, short enough that it is not a record of what the
 * reader has seen. Unread is not decided here and does not consult it.
 */
export const PRESENTATION_MEMORY_TTL_MS = 5 * 60_000;

/** Most events remembered at once; the oldest insertion is evicted beyond it. */
export const PRESENTATION_MEMORY_CAPACITY = 500;

/**
 * The sound burst window.
 *
 * Short on purpose: it exists to collapse a rajada into one chime, not to mask
 * activity. A conversation that keeps producing messages keeps chiming, once
 * per window.
 */
export const SOUND_COOLDOWN_MS = 3_000;

/** Most cooldown keys held at once — one per conversation and class in play. */
export const SOUND_COOLDOWN_CAPACITY = 64;

export interface BurstGateOptions {
  /** Injectable so a test drives the window instead of waiting for it. */
  now?: () => number;
  memoryTtlMs?: number;
  memoryCapacity?: number;
  soundCooldownMs?: number;
  soundCooldownCapacity?: number;
}

export interface BurstGate {
  /** Whether this event has already been announced by this client. */
  hasPresented(eventId: string): boolean;
  /** Records an announcement. Call it only when a surface actually ran. */
  markPresented(eventId: string): void;
  /**
   * Consumes this key's sound budget: true the first time in a window, false
   * for the rest of it. Consumes nothing when it returns false.
   */
  allowSound(cooldownKey: string): boolean;
  /** How many keys are retained, across both stores. Bounded by construction. */
  retainedKeyCount(): number;
}

/**
 * The local memory of a presentation surface: what has been announced, and
 * which conversations have chimed recently.
 *
 * One instance per client session. It is deliberately not persisted: it exists
 * to stop a burst and a redelivery within one session, and a store that
 * survived a reload would be a record of the reader's activity, which is a
 * different thing with different obligations.
 */
export function createBurstGate(options: BurstGateOptions = {}): BurstGate {
  // Called through rather than captured: a test that installs a clock after the
  // gate was built must still be the clock this gate reads.
  const now = options.now ?? (() => Date.now());
  const memoryTtlMs = options.memoryTtlMs ?? PRESENTATION_MEMORY_TTL_MS;
  const soundCooldownMs = options.soundCooldownMs ?? SOUND_COOLDOWN_MS;
  const presented = createExpiringKeySet(options.memoryCapacity ?? PRESENTATION_MEMORY_CAPACITY);
  const chimed = createExpiringKeySet(options.soundCooldownCapacity ?? SOUND_COOLDOWN_CAPACITY);

  return {
    hasPresented: (eventId) => presented.has(eventId, now()),
    markPresented: (eventId) => presented.add(eventId, now(), memoryTtlMs),
    allowSound(cooldownKey) {
      const instant = now();
      if (chimed.has(cooldownKey, instant)) return false;
      chimed.add(cooldownKey, instant, soundCooldownMs);
      return true;
    },
    retainedKeyCount: () => presented.size() + chimed.size(),
  };
}
