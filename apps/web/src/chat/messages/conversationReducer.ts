/**
 * The reader's own selection beside the list: the message being replied to, and
 * the favourite flag.
 *
 * Both are per-reader state that the timeline carries but the conversation does
 * not, which is why neither is reconciled from realtime.
 */

import type { Action, ActionOf, MessagesState } from "./types";

function applyFavoriteSet(state: MessagesState, action: ActionOf<"favorite_set">): MessagesState {
  const index = state.messages.findIndex((message) => message.id === action.messageId);
  if (index < 0) return state;
  const messages = [...state.messages];
  messages[index] = { ...messages[index], isFavorited: action.isFavorited };
  return { ...state, messages };
}

/** Reply target and favourites — per-reader state beside the list. */
export function reduceConversation(
  state: MessagesState,
  action: Action,
): MessagesState | undefined {
  switch (action.type) {
    case "reply_set":
      return { ...state, replyTo: action.message };
    case "reply_clear":
      return { ...state, replyTo: null };
    case "favorite_set":
      return applyFavoriteSet(state, action);
    case "favorite_error":
      // Reuses the transient banner without touching reaction snapshots.
      return { ...state, actionError: action.error };
    default:
      return undefined;
  }
}
