/**
 * Loading a conversation and paging back through it.
 *
 * The two page transitions differ in one way that matters beyond the array: an
 * initial load resets everything the previous conversation left behind, while a
 * prepend must not disturb what the reader is looking at.
 */

import { applyLinkSafetyCorrections } from "./linkSafetyCorrections";
import type { Action, ActionOf, MessagesState } from "./types";

function applyLoaded(state: MessagesState, action: ActionOf<"loaded">): MessagesState {
  return {
    status: "ready",
    messages: action.page.messages.map((message) =>
      applyLinkSafetyCorrections(message, state.linkSafetyCorrections),
    ),
    nextCursor: action.page.nextCursor,
    sendError: null,
    sending: false,
    loadingMore: false,
    lastMutation: "initial",
    realtimeError: null,
    actionError: null,
    pendingReactions: new Map(),
    replyTo: null,
    linkSafetyCorrections: state.linkSafetyCorrections,
  };
}

function applyPrepended(state: MessagesState, action: ActionOf<"prepended">): MessagesState {
  // Prepend older messages; deduplicate by ID to guard against cursor overlaps.
  const existingIds = new Set(state.messages.map((m) => m.id));
  const fresh = action.page.messages
    .filter((m) => !existingIds.has(m.id))
    .map((message) => applyLinkSafetyCorrections(message, state.linkSafetyCorrections));
  // If every message in this page was already present, no DOM change occurs:
  // skip the scroll delta calculation by keeping lastMutation as "none".
  return {
    ...state,
    messages: fresh.length > 0 ? [...fresh, ...state.messages] : state.messages,
    nextCursor: action.page.nextCursor,
    loadingMore: false,
    lastMutation: fresh.length > 0 ? "prepend" : "none",
  };
}

/** Loading a conversation and paging through it. */
export function reduceHistory(state: MessagesState, action: Action): MessagesState | undefined {
  switch (action.type) {
    case "loading":
      // Reset cursor and loadingMore so stale pagination state does not carry over.
      return {
        ...state,
        status: "loading",
        sendError: null,
        sending: false,
        loadingMore: false,
        nextCursor: "",
        lastMutation: "none",
        realtimeError: null,
        actionError: null,
        pendingReactions: new Map(),
        replyTo: null,
      };
    case "loaded":
      return applyLoaded(state, action);
    case "error":
      return { ...state, status: "error", sending: false, lastMutation: "none" };
    case "prepending":
      return { ...state, loadingMore: true, lastMutation: "none" };
    case "prepended":
      return applyPrepended(state, action);
    case "prepend_error":
      return { ...state, loadingMore: false, lastMutation: "none" };
    default:
      return undefined;
  }
}
