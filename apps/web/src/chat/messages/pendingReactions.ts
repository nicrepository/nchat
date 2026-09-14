/**
 * The algebra of a reader's outstanding reaction toggles (issue #496).
 *
 * What is drawn for a message is always the last list the server stated, with
 * this reader's still-unconfirmed intents applied on top. Keeping the two apart
 * is what lets a confirmed list from the server replace the baseline without
 * swallowing a toggle the reader is still waiting on, and what makes replaying
 * an intent harmless.
 *
 * Nothing here reads the whole conversation state: every function takes the one
 * message's reactions or the pending map, so a reaction rule can be exercised
 * without building a timeline around it.
 */

import type { Message } from "../chatTypes";
import type { WSReactionUpdatedEvent } from "../useChatWebSocket";
import type { PendingReactions, ReactionIntent } from "./types";

/** Applies a local toggle to a message's reaction list, mirroring server semantics for count/reactedByMe. */
export function toggleOptimisticReaction(
  reactions: Message["reactions"],
  emoji: string,
): Message["reactions"] {
  const index = reactions.findIndex((item) => item.emoji === emoji);
  if (index < 0) {
    // No named author yet: the tooltip says "Você" from reactedByMe, and the
    // server's own list replaces this the moment the toggle is confirmed.
    return [...reactions, { emoji, count: 1, reactedByMe: true, users: [] }];
  }
  const current = reactions[index];
  if (!current.reactedByMe) {
    return reactions.map((item, i) =>
      i === index ? { ...item, count: item.count + 1, reactedByMe: true } : item,
    );
  }
  if (current.count <= 1) {
    return reactions.filter((_, i) => i !== index);
  }
  return reactions.map((item, i) =>
    i === index ? { ...item, count: item.count - 1, reactedByMe: false } : item,
  );
}

/**
 * Forces one emoji to the state an intent asks for.
 *
 * Idempotent on purpose: replaying an intent over a list that already agrees
 * with it changes nothing, which is what lets the same intents be applied again
 * over every new confirmed list without the count drifting.
 */
function applyReactionIntent(
  reactions: Message["reactions"],
  emoji: string,
  desired: ReactionIntent,
): Message["reactions"] {
  const reacted = reactions.find((item) => item.emoji === emoji)?.reactedByMe ?? false;
  if (reacted === (desired === "added")) return reactions;
  return toggleOptimisticReaction(reactions, emoji);
}

/** Confirmed server state + this reader's outstanding intents = what is drawn. */
export function applyPendingReactions(pending: PendingReactions): Message["reactions"] {
  let reactions = pending.confirmed;
  for (const [emoji, desired] of pending.intents) {
    reactions = applyReactionIntent(reactions, emoji, desired);
  }
  return reactions;
}

/** The pending entry for a message, or an empty one anchored on what is drawn. */
export function pendingFor(
  pendingReactions: Map<string, PendingReactions>,
  messageId: string,
  rendered: Message["reactions"],
): PendingReactions {
  return pendingReactions.get(messageId) ?? { confirmed: rendered, intents: new Map() };
}

/**
 * Writes a message's pending entry back, dropping it once nothing is in flight
 * so the map holds only messages that actually have something outstanding.
 */
export function withPending(
  pendingReactions: Map<string, PendingReactions>,
  messageId: string,
  pending: PendingReactions | null,
): Map<string, PendingReactions> {
  const next = new Map(pendingReactions);
  if (pending === null || pending.intents.size === 0) next.delete(messageId);
  else next.set(messageId, pending);
  return next;
}

/**
 * What an incoming event settled, if anything.
 *
 * Carries the intent it settled so the caller does not have to work out again
 * whether it was an addition — that question decides the emoji history, and it
 * must have exactly one answer.
 */
export interface ReactionConfirmation {
  confirmed: boolean;
  intent?: ReactionIntent;
}

const unconfirmed: ReactionConfirmation = { confirmed: false };

/**
 * Whether an event is the confirmation of what *this reader* asked for.
 *
 * Observing an event is not the same as having your own toggle confirmed. The
 * server fans one event out to every subscriber, so a reaction the reader is
 * still waiting on and an identical reaction from somebody else look alike on
 * the wire; and the reader's own events can arrive after they have already
 * changed their mind. Only an event that says back the state the outstanding
 * intent asked for settles it — which is what makes it safe to stop that
 * intent's rollback timer and to count the emoji as used.
 *
 * The three cases this exists to refuse:
 *
 *  - another reader toggling the same emoji. It moves the confirmed count, and
 *    this reader's own toggle stays pending on top of it;
 *  - a stale `added` reaching a reader who has since asked to remove the same
 *    emoji, and the mirror case. The newer intent survives and is re-applied,
 *    so the event cannot resurrect what the reader has already taken back;
 *  - a redelivery. The first copy settled the intent, so by the second there is
 *    nothing outstanding and nothing to settle.
 *
 * Two tabs of the same reader are indistinguishable here: the protocol carries
 * an actor, not an origin. When the other tab's action happens to be the state
 * this tab is waiting for, the server already satisfies the intent and treating
 * that as convergence is correct.
 */
export function confirmReactionIntent(
  pending: Map<string, PendingReactions>,
  reaction: NonNullable<WSReactionUpdatedEvent["reaction"]>,
  actorIsMe: boolean,
): ReactionConfirmation {
  if (!actorIsMe) return unconfirmed;
  const intent = pending.get(reaction.message_id)?.intents.get(reaction.emoji);
  if (intent === undefined || intent !== (reaction.added ? "added" : "removed")) {
    return unconfirmed;
  }
  return { confirmed: true, intent };
}

/** The intents left once an event has settled the one it confirms, if any. */
export function remainingIntents(
  pending: PendingReactions,
  emoji: string,
  confirmation: ReactionConfirmation,
): Map<string, ReactionIntent> {
  if (!confirmation.confirmed) return pending.intents;
  const next = new Map(pending.intents);
  next.delete(emoji);
  return next;
}
