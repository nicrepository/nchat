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
    olderPagesSettled: 0,
    lastMutation: "initial",
    realtimeError: null,
    actionError: null,
    pendingReactions: new Map(),
    replyTo: null,
    linkSafetyCorrections: state.linkSafetyCorrections,
    pendingEdits: new Map(),
    syncError: null,
  };
}

/**
 * True when an older-page answer still belongs to this timeline: it was asked
 * for with the cursor the timeline holds now. A replacement (issue #947) moves
 * the cursor, so an answer requested before it can never splice an old range
 * into the new list or point the next page back into it.
 */
function continuesTimeline(state: MessagesState, cursor: string): boolean {
  return state.nextCursor === cursor;
}

function applyPrepended(state: MessagesState, action: ActionOf<"prepended">): MessagesState {
  if (!continuesTimeline(state, action.cursor)) return state;
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
    olderPagesSettled: state.olderPagesSettled + 1,
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
        // What was owed or pending belonged to the conversation being left.
        pendingEdits: new Map(),
        syncError: null,
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
    case "denied":
      // Clears messages rather than just flipping status: a client that had
      // already loaded history before losing access must not keep showing it
      // underneath the access-denied state (issue #475).
      return {
        ...state,
        status: "denied",
        pendingEdits: new Map(),
        syncError: null,
        messages: [],
        nextCursor: "",
        sendError: null,
        sending: false,
        loadingMore: false,
        lastMutation: "none",
        realtimeError: null,
        actionError: null,
        pendingReactions: new Map(),
        replyTo: null,
      };
    case "prepending":
      return { ...state, loadingMore: true, lastMutation: "none" };
    case "prepended":
      return applyPrepended(state, action);
    case "prepend_error":
      if (!continuesTimeline(state, action.cursor)) return state;
      return {
        ...state,
        loadingMore: false,
        olderPagesSettled: state.olderPagesSettled + 1,
        lastMutation: "none",
      };
    default:
      return undefined;
  }
}
