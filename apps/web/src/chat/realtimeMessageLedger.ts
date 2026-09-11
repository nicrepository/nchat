/**
 * The state layer's answer to "have I already taken this message in?" (#750).
 *
 * ## Why it exists, and why it is not the presentation layer's memory
 *
 * A realtime `message.created` frame does two separate things: it moves
 * conversation state (unread, mention flag, activity order) and it may produce
 * a presentation candidate. Both must happen at most once per message, and the
 * two records are deliberately *not* the same set:
 *
 *   - this ledger records every live message this client took in, whether or
 *     not anything was announced for it;
 *   - notificationBurst's memory records only what this tab actually
 *     announced, because a tab that lost the cross-tab claim, or had the
 *     conversation open, announced nothing and must not remember doing so.
 *
 * Merging them would make unread depend on who won a Web Lock, which is the
 * one coupling issue #749 exists to avoid.
 *
 * ## Why a bounded ledger is correct here, rather than a cache that may drop a
 * live id and double-count it
 *
 * The server states its realtime contract as best-effort in-process delivery
 * with **no durability and no replay** (ws/doc.go, ws/bus.go): a client that
 * was disconnected never receives the events it missed, and a reconnect
 * replays nothing. A bus event that originated on this instance is discarded
 * rather than echoed back (Hub, SourceInstanceID), and the client drops frames
 * from a superseded socket generation (useChatWebSocket). So a second delivery
 * of one `message_id` is not something the contract can produce minutes later
 * — when it happens at all it is a near-simultaneous duplicate, which is
 * exactly what a window of minutes covers.
 *
 * There is a second line of defence that costs nothing: `unreadCount` is the
 * server's own number on every sidebar response, and mergeUnread prefers it
 * over anything held locally. Local drift is therefore corrected by the next
 * refetch rather than accumulating.
 *
 * Identity is `message_id` — server-assigned, stable across deliveries, the
 * same value in every tab. Never the envelope's `event_id`, which the hub
 * mints fresh per publish, and never an arrival time.
 *
 * Nothing is persisted: this is about redelivery inside one session, not a
 * record of what the reader has seen.
 */

import { sessionScoped } from "../lib/sessionScoped";
import { createExpiringKeySet } from "./expiringKeySet";

/** How long one live message id is remembered. See the module comment. */
export const REALTIME_LEDGER_TTL_MS = 5 * 60_000;

/** Most ids held at once; beyond it the oldest insertion is evicted. */
export const REALTIME_LEDGER_CAPACITY = 1_000;

const ledger = sessionScoped(() => createExpiringKeySet(REALTIME_LEDGER_CAPACITY));

/**
 * Takes one live message id into this session's state, and reports whether it
 * was the first time.
 *
 * `true` means this client had not taken this message in: the caller may count
 * it towards unread and offer it for presentation. `false` means a redelivery,
 * and the caller must do neither — the message itself is already in state.
 *
 * Check-and-record in one call on purpose: two steps are two chances to record
 * without checking, which is a dedupe that quietly stops working.
 */
export function admitRealtimeMessage(messageId: string): boolean {
  const seen = ledger();
  const now = Date.now();
  if (seen.has(messageId, now)) return false;
  seen.add(messageId, now, REALTIME_LEDGER_TTL_MS);
  return true;
}

/** How many ids are retained. Bounded by construction; read by tests. */
export function retainedRealtimeIdCount(): number {
  return ledger().size();
}
