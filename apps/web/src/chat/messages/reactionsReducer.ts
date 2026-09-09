/**
 * Reactions: this reader's optimistic toggles, their confirmations, and the
 * refusals that undo them.
 *
 * Every transition here keeps the confirmed server list and the reader's
 * outstanding intents separate — see pendingReactions.ts — so a snapshot, a
 * redelivered event and a rollback all converge on the same result whatever
 * order they arrive in.
 */

import { parseReactionUsers } from "../chatTypes";
import type { WSReactionUpdatedEvent } from "../useChatWebSocket";
import {
  applyPendingReactions,
  confirmReactionIntent,
  pendingFor,
  remainingIntents,
  withPending,
} from "./pendingReactions";
import type { Action, ActionOf, MessagesState } from "./types";

/**
 * Applies a reaction.updated event to the message it names (issue #496).
 *
 * The event carries absolute values — a count and a bounded list of names, never
 * an increment — so replaying one is a no-op: nothing accumulates, and no author
 * is listed twice. Each event's list replaces the previous one wholesale, and
 * events for a target arrive in publication order over a single connection, so
 * the last one delivered is the current state; a reconnect is reconciled by
 * refetching the message, not by ordering rules here.
 *
 * What the event does *not* carry is `reacted_by_me`: one event is fanned out to
 * every subscriber, so the reader's own state is derived here instead.
 *
 * That derivation reconciles against the pre-optimistic baseline, not the
 * current — possibly still unconfirmed — optimistic guess, so an update from
 * another actor cannot inherit this reader's own pending toggle as ground truth.
 */
function applyReactionEvent(
  state: MessagesState,
  event: WSReactionUpdatedEvent,
  actorIsMe: boolean,
): MessagesState {
  const { reaction } = event;
  if (!reaction) return state;
  const index = state.messages.findIndex((message) => message.id === reaction.message_id);
  if (index < 0) return state;
  const message = state.messages[index];
  const previous = pendingFor(state.pendingReactions, reaction.message_id, message.reactions);
  const wasReacted = new Map(previous.confirmed.map((item) => [item.emoji, item.reactedByMe]));
  const confirmed = reaction.reactions.map((item) => ({
    emoji: item.emoji,
    count: item.count,
    // The names travel with the event, so a tooltip is correct the instant the
    // count changes — no refetch, and no request when it is hovered.
    users: parseReactionUsers(item.users),
    reactedByMe:
      actorIsMe && item.emoji === reaction.emoji
        ? reaction.added
        : (wasReacted.get(item.emoji) ?? false),
  }));
  const confirmation = confirmReactionIntent(state.pendingReactions, reaction, actorIsMe);
  const pending = {
    confirmed,
    intents: remainingIntents(previous, reaction.emoji, confirmation),
  };
  const messages = [...state.messages];
  messages[index] = { ...message, reactions: applyPendingReactions(pending) };
  return {
    ...state,
    messages,
    pendingReactions: withPending(state.pendingReactions, reaction.message_id, pending),
    lastMutation: "none",
    realtimeError: null,
    actionError: null,
  };
}

function applyReactionError(
  state: MessagesState,
  action: ActionOf<"reaction_error">,
): MessagesState {
  // Server-level errors (rate limit, feature unavailable) aren't scoped to a
  // single message, so every optimistic toggle still in flight is reverted.
  if (state.pendingReactions.size === 0) {
    return { ...state, actionError: action.error };
  }
  const messages = state.messages.map((message) => {
    const pending = state.pendingReactions.get(message.id);
    return pending ? { ...message, reactions: pending.confirmed } : message;
  });
  return { ...state, messages, actionError: action.error, pendingReactions: new Map() };
}

function applyReactionOptimistic(
  state: MessagesState,
  action: ActionOf<"reaction_optimistic">,
): MessagesState {
  const index = state.messages.findIndex((message) => message.id === action.messageId);
  if (index < 0) return state;
  const message = state.messages[index];
  const previous = pendingFor(state.pendingReactions, action.messageId, message.reactions);
  // The toggle is read off what the reader is looking at, so a second toggle of
  // the same emoji supersedes the first rather than stacking with it.
  const reacted =
    message.reactions.find((item) => item.emoji === action.emoji)?.reactedByMe ?? false;
  const intents = new Map(previous.intents).set(action.emoji, reacted ? "removed" : "added");
  const pending = { confirmed: previous.confirmed, intents };
  const messages = [...state.messages];
  messages[index] = { ...message, reactions: applyPendingReactions(pending) };
  return {
    ...state,
    messages,
    pendingReactions: withPending(state.pendingReactions, action.messageId, pending),
    actionError: null,
  };
}

/** Undoes one intent — a refused send, or a confirmation that never came. */
function applyReactionRevert(
  state: MessagesState,
  action: ActionOf<"reaction_revert">,
): MessagesState {
  const previous = state.pendingReactions.get(action.messageId);
  if (!previous?.intents.has(action.emoji)) return { ...state, actionError: action.error };
  const intents = new Map(previous.intents);
  intents.delete(action.emoji);
  const pending = { confirmed: previous.confirmed, intents };
  const index = state.messages.findIndex((message) => message.id === action.messageId);
  const reactions = applyPendingReactions(pending);
  const messages =
    index < 0
      ? state.messages
      : state.messages.map((message, i) => (i === index ? { ...message, reactions } : message));
  return {
    ...state,
    messages,
    pendingReactions: withPending(state.pendingReactions, action.messageId, pending),
    actionError: action.error,
  };
}

/**
 * A refetched message replaces what the server had said, and the intents still
 * in flight are re-applied on top of it — a resync must not swallow a toggle the
 * reader is still waiting on.
 */
function applyReactionSnapshot(
  state: MessagesState,
  action: ActionOf<"reaction_snapshot">,
): MessagesState {
  const index = state.messages.findIndex((message) => message.id === action.messageId);
  if (index < 0) return state;
  const previous = state.pendingReactions.get(action.messageId);
  const pending = { confirmed: action.reactions, intents: previous?.intents ?? new Map() };
  const messages = [...state.messages];
  messages[index] = { ...messages[index], reactions: applyPendingReactions(pending) };
  return {
    ...state,
    messages,
    pendingReactions: withPending(state.pendingReactions, action.messageId, pending),
    lastMutation: "none",
    realtimeError: null,
    actionError: null,
  };
}

/** Reactions: optimistic toggles, their confirmations and their refusals. */
export function reduceReactions(state: MessagesState, action: Action): MessagesState | undefined {
  switch (action.type) {
    case "reaction_optimistic":
      return applyReactionOptimistic(state, action);
    case "reaction_revert":
      return applyReactionRevert(state, action);
    case "reaction_updated":
      return applyReactionEvent(state, action.event, action.actorIsMe);
    case "reaction_snapshot":
      return applyReactionSnapshot(state, action);
    case "reaction_error":
      return applyReactionError(state, action);
    case "reaction_error_clear":
      return { ...state, actionError: null };
    default:
      return undefined;
  }
}
