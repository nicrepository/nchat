/**
 * useMessages — hook for loading and sending messages in a channel or DM.
 *
 * Security notes:
 * - No tokens are stored or exposed; authentication is handled by authenticatedFetch.
 * - No author_id is sent from the client; the server derives sender identity from the JWT.
 * - AbortController cancels in-flight list and fallback single-message requests
 *   on target change or unmount.
 * - latestTargetRef is updated via useLayoutEffect (no deps) after every render,
 *   synchronously in the same JS task, before any microtask can run. This ensures
 *   stale POST completions are detected reliably regardless of effect scheduling.
 *
 * WebSocket realtime delivery:
 * Connected to /api/chat/ws via useChatWebSocket. Auth uses the Bearer access
 * token passed as the Sec-WebSocket-Protocol subprotocol (browser WebSocket
 * upgrade does not support custom headers; token-in-URL is explicitly rejected
 * by the server). Incoming message.created events carry the full message DTO
 * in evt.payload and are inserted directly into the timeline without an
 * additional GET (dedup by id in reducer). If payload is absent (old server
 * during rolling deploy) a targeted GET is used as fallback.
 * Cleanup happens on unmount and target change via useChatWebSocket's effect.
 */

import { useCallback, useEffect, useLayoutEffect, useMemo, useReducer, useRef } from "react";

import {
  deleteMessage as deleteMessageRequest,
  editMessage as editMessageRequest,
  favoriteMessage,
  fetchChannelMessage,
  fetchChannelMessages,
  fetchDMMessage,
  fetchDMMessages,
  postChannelMessage,
  postDMMessage,
  resolveChannelMessageReferences,
  resolveDMMessageReferences,
  unfavoriteMessage,
} from "./chatApi";
import { normalizeBodyFormat, type Message, type MessagePage } from "./chatTypes";
import {
  useChatWebSocket,
  type WSMessageCreatedEvent,
  type WSMessageUpdatedEvent,
  type WSClientErrorEvent,
  type WSMembersAddedEvent,
  type WSAttachmentStatusEvent,
  type WSPinUpdatedEvent,
  type WSReactionUpdatedEvent,
} from "./useChatWebSocket";

// ── State shape ───────────────────────────────────────────────────────────────

type MessagesStatus = "idle" | "loading" | "ready" | "error";

/**
 * Explicit record of the most recent messages mutation.
 * Used by MessageList's useLayoutEffect to apply the correct scroll strategy
 * without relying on fragile first/last ID comparisons.
 *
 * "ws_append" — message appended from a WebSocket event; MessageList scrolls
 *               to bottom only when the user is already near the bottom.
 */
export type LastMutation = "initial" | "append" | "prepend" | "ws_append" | "none";

const referenceRevalidationMs = 15_000;
const referenceRevalidationBatchSize = 100;

export interface MessagesState {
  status: MessagesStatus;
  messages: Message[];
  /** Opaque cursor for loading older messages; empty string when no older page. */
  nextCursor: string;
  sendError: string | null;
  sending: boolean;
  /** True while an older-page fetch is in progress. */
  loadingMore: boolean;
  /** Describes the most recent change to the messages array for scroll management. */
  lastMutation: LastMutation;
  /** Recoverable realtime fallback error; initial loads and manual retries remain authoritative. */
  realtimeError: string | null;
  /** Feedback for rejected/unsent message actions (reactions, favorites). */
  actionError: string | null;
  /** Reactions snapshot to restore per messageId if the in-flight optimistic toggle is rejected or times out. */
  pendingReactions: Map<string, Message["reactions"]>;
  /** RF-07: message currently selected as the parent quote for the composer. */
  replyTo: Message | null;
}

// ── Send result ───────────────────────────────────────────────────────────────

/**
 * Explicit result returned by sendMessage.
 *
 * "sent"  — POST succeeded and state was updated for the current target.
 * "stale" — target changed before POST resolved/rejected; caller must not
 *            treat this as success or failure for the current target.
 *
 * Current-target failures throw instead of returning a result, preserving
 * the existing draft-retention contract in callers.
 */
export type SendResult = { status: "sent" } | { status: "stale" };

// ── Reducer ───────────────────────────────────────────────────────────────────

type Action =
  | { type: "loading" }
  | { type: "loaded"; page: MessagePage }
  | { type: "error" }
  | { type: "sending" }
  | { type: "sent"; message: Message }
  | { type: "send_error"; error: string }
  | { type: "prepending" }
  | { type: "prepended"; page: MessagePage }
  | { type: "prepend_error" }
  | { type: "ws_received"; message: Message }
  | {
      type: "edit_optimistic";
      messageId: string;
      body: string;
      bodyFormat: Message["bodyFormat"];
      editedAt: string;
    }
  | { type: "edit_confirmed"; message: Message }
  | { type: "edit_revert"; message: Message; optimisticEditedAt: string }
  | { type: "message_updated"; event: WSMessageUpdatedEvent }
  | { type: "message_snapshot"; message: Message; insertIfMissing: boolean }
  | {
      type: "references_refreshed";
      references: Record<string, NonNullable<Message["reference"]>>;
    }
  | { type: "delete_error"; error: string }
  | { type: "reaction_updated"; event: WSReactionUpdatedEvent; actorIsMe: boolean }
  | { type: "reaction_snapshot"; messageId: string; reactions: Message["reactions"] }
  | { type: "reaction_error"; error: string }
  | { type: "reaction_error_clear" }
  | { type: "reply_set"; message: Message }
  | { type: "reply_clear" }
  | { type: "favorite_set"; messageId: string; isFavorited: boolean }
  | { type: "favorite_error"; error: string }
  | { type: "reaction_optimistic"; messageId: string; emoji: string }
  | { type: "reaction_revert"; messageId: string; error: string }
  | { type: "ws_fetch_error"; error: string }
  | { type: "ws_subscription_ready" };

/** Applies a local toggle to a message's reaction list, mirroring server semantics for count/reactedByMe. */
function toggleOptimisticReaction(
  reactions: Message["reactions"],
  emoji: string,
): Message["reactions"] {
  const index = reactions.findIndex((item) => item.emoji === emoji);
  if (index < 0) {
    return [...reactions, { emoji, count: 1, reactedByMe: true }];
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

function insertMessageChronologically(
  messages: Message[],
  message: Message,
): { messages: Message[]; isNewer: boolean } {
  const isNewer =
    messages.length === 0 ||
    message.createdAt > messages[messages.length - 1].createdAt ||
    (message.createdAt === messages[messages.length - 1].createdAt &&
      message.id > messages[messages.length - 1].id);
  return {
    messages: isNewer
      ? [...messages, message]
      : [...messages, message].sort((a, b) => {
          if (a.createdAt < b.createdAt) return -1;
          if (a.createdAt > b.createdAt) return 1;
          return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
        }),
    isNewer,
  };
}

const initialState: MessagesState = {
  status: "idle",
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

const realtimeFallbackErrorMessage = "Não foi possível atualizar mensagens em tempo real.";
const reactionConfirmTimeoutMs = 8_000;

function reducer(state: MessagesState, action: Action): MessagesState {
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
      return {
        status: "ready",
        messages: action.page.messages,
        nextCursor: action.page.nextCursor,
        sendError: null,
        sending: false,
        loadingMore: false,
        lastMutation: "initial",
        realtimeError: null,
        actionError: null,
        pendingReactions: new Map(),
        replyTo: null,
      };
    case "error":
      return { ...state, status: "error", sending: false, lastMutation: "none" };
    case "sending":
      return { ...state, sending: true, sendError: null };
    case "sent": {
      // Deduplicate: a realtime event or a prior send might have already added this message.
      const alreadyPresent = state.messages.some((m) => m.id === action.message.id);
      return {
        ...state,
        messages: alreadyPresent ? state.messages : [...state.messages, action.message],
        sending: false,
        sendError: null,
        lastMutation: alreadyPresent ? "none" : "append",
        realtimeError: null,
        replyTo: null,
      };
    }
    case "send_error":
      return { ...state, sending: false, sendError: action.error };
    case "prepending":
      return { ...state, loadingMore: true, lastMutation: "none" };
    case "prepended": {
      // Prepend older messages; deduplicate by ID to guard against cursor overlaps.
      const existingIds = new Set(state.messages.map((m) => m.id));
      const fresh = action.page.messages.filter((m) => !existingIds.has(m.id));
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
    case "prepend_error":
      return { ...state, loadingMore: false, lastMutation: "none" };
    case "ws_received": {
      // Dedup: if the message is already present (e.g. our own POST response
      // arrived before the WS event), this is a pure no-op.
      const alreadyPresent = state.messages.some((m) => m.id === action.message.id);
      if (alreadyPresent) return { ...state, realtimeError: null };

      // Insert in stable (createdAt, id) order to handle out-of-order delivery.
      // Most WS messages are newer than all existing ones, so a quick tail-check
      // avoids a full sort in the common case.
      const insertion = insertMessageChronologically(state.messages, action.message);

      return {
        ...state,
        messages: insertion.messages,
        // ws_append: MessageList scrolls to bottom only if the user is already
        // near the bottom, preserving position when reading history.
        // If the message was inserted mid-list (out-of-order), no auto-scroll.
        lastMutation: insertion.isNewer ? "ws_append" : "none",
        realtimeError: null,
      };
    }
    case "edit_optimistic": {
      const messages = state.messages.map((message) =>
        message.id === action.messageId
          ? {
              ...message,
              bodyText: action.body,
              bodyFormat: action.bodyFormat,
              isEdited: true,
              editCount: message.editCount + 1,
              editedAt: action.editedAt,
              updatedAt: action.editedAt,
            }
          : message,
      );
      return { ...state, messages, lastMutation: "none" };
    }
    case "edit_confirmed":
      return {
        ...state,
        messages: state.messages.map((message) =>
          message.id === action.message.id &&
          !message.isRemoved &&
          action.message.editCount >= message.editCount
            ? {
                ...message,
                bodyText: action.message.bodyText,
                bodyFormat: action.message.bodyFormat,
                editedAt: action.message.editedAt,
                updatedAt: action.message.updatedAt,
                editCount: action.message.editCount,
                isEdited: action.message.isEdited,
              }
            : message,
        ),
        lastMutation: "none",
      };
    case "edit_revert":
      return {
        ...state,
        messages: state.messages.map((message) =>
          message.id === action.message.id &&
          !message.isRemoved &&
          message.editedAt === action.optimisticEditedAt
            ? action.message
            : message,
        ),
        lastMutation: "none",
      };
    case "message_updated": {
      const update = action.event.message_update;
      if (!update) return state;
      const removed = update.is_removed === true || update.status === "deleted";
      const deletedAt = update.deleted_at ?? update.updated_at ?? null;
      return {
        ...state,
        messages: state.messages.map((message) => {
          if (message.id === update.message_id) {
            if (message.isRemoved || removed) {
              return removed
                ? {
                    ...message,
                    bodyText: "",
                    quoted: undefined,
                    reactions: [],
                    status: "deleted" as const,
                    isRemoved: true,
                    deletedAt,
                    updatedAt: update.updated_at ?? deletedAt ?? message.updatedAt,
                  }
                : message;
            }
            return update.edit_count < message.editCount
              ? message
              : {
                  ...message,
                  bodyText: update.body,
                  bodyFormat: normalizeBodyFormat(update.body_format),
                  editedAt: update.edited_at,
                  updatedAt: update.updated_at ?? update.edited_at,
                  editCount: update.edit_count,
                  isEdited: update.is_edited,
                };
          }
          if (removed && message.quoted?.id === update.message_id) {
            return {
              ...message,
              quoted: { ...message.quoted, bodyText: "", isRemoved: true, deletedAt },
            };
          }
          return message;
        }),
        replyTo: removed && state.replyTo?.id === update.message_id ? null : state.replyTo,
        lastMutation: "none",
        realtimeError: null,
      };
    }
    case "message_snapshot": {
      const removed = action.message.isRemoved || action.message.status === "deleted";
      const snapshot = removed
        ? { ...action.message, bodyText: "", quoted: undefined, reactions: [] }
        : action.message;
      const alreadyPresent = state.messages.some((message) => message.id === snapshot.id);
      let insertedAsNewer = false;
      let messages = state.messages.map((message) => {
        if (message.id === snapshot.id) return message.isRemoved && !removed ? message : snapshot;
        if (removed && message.quoted?.id === snapshot.id) {
          return {
            ...message,
            quoted: {
              ...message.quoted,
              bodyText: "",
              isRemoved: true,
              deletedAt: snapshot.deletedAt ?? snapshot.updatedAt,
            },
          };
        }
        return message;
      });
      if (!alreadyPresent && action.insertIfMissing) {
        const insertion = insertMessageChronologically(messages, snapshot);
        messages = insertion.messages;
        insertedAsNewer = insertion.isNewer;
      }
      return {
        ...state,
        messages,
        replyTo: removed && state.replyTo?.id === snapshot.id ? null : state.replyTo,
        lastMutation: insertedAsNewer ? "ws_append" : "none",
        realtimeError: null,
      };
    }
    case "references_refreshed":
      return {
        ...state,
        messages: state.messages.map((message) => {
          const reference = action.references[message.id];
          return reference && message.reference ? { ...message, reference } : message;
        }),
      };
    case "delete_error":
      return { ...state, actionError: action.error };
    case "ws_fetch_error":
      return { ...state, realtimeError: action.error, lastMutation: "none" };
    case "ws_subscription_ready":
      return { ...state, realtimeError: null };
    case "reaction_error": {
      // Server-level errors (rate limit, feature unavailable) aren't scoped to a
      // single message, so every optimistic toggle still in flight is reverted.
      if (state.pendingReactions.size === 0) {
        return { ...state, actionError: action.error };
      }
      const messages = state.messages.map((message) => {
        const snapshot = state.pendingReactions.get(message.id);
        return snapshot ? { ...message, reactions: snapshot } : message;
      });
      return { ...state, messages, actionError: action.error, pendingReactions: new Map() };
    }
    case "reaction_error_clear":
      return { ...state, actionError: null };
    case "reply_set":
      return { ...state, replyTo: action.message };
    case "reply_clear":
      return { ...state, replyTo: null };
    case "favorite_set": {
      const index = state.messages.findIndex((message) => message.id === action.messageId);
      if (index < 0) return state;
      const messages = [...state.messages];
      messages[index] = { ...messages[index], isFavorited: action.isFavorited };
      return { ...state, messages };
    }
    case "favorite_error":
      // Reuses the transient banner without touching reaction snapshots.
      return { ...state, actionError: action.error };
    case "reaction_optimistic": {
      const index = state.messages.findIndex((message) => message.id === action.messageId);
      if (index < 0) return state;
      const message = state.messages[index];
      const pendingReactions = new Map(state.pendingReactions);
      if (!pendingReactions.has(action.messageId)) {
        pendingReactions.set(action.messageId, message.reactions);
      }
      const messages = [...state.messages];
      messages[index] = {
        ...message,
        reactions: toggleOptimisticReaction(message.reactions, action.emoji),
      };
      return { ...state, messages, pendingReactions, actionError: null };
    }
    case "reaction_revert": {
      const snapshot = state.pendingReactions.get(action.messageId);
      if (!snapshot) return { ...state, actionError: action.error };
      const index = state.messages.findIndex((message) => message.id === action.messageId);
      const messages =
        index < 0
          ? state.messages
          : state.messages.map((message, i) =>
              i === index ? { ...message, reactions: snapshot } : message,
            );
      const pendingReactions = new Map(state.pendingReactions);
      pendingReactions.delete(action.messageId);
      return { ...state, messages, pendingReactions, actionError: action.error };
    }
    case "reaction_updated": {
      const { reaction } = action.event;
      if (!reaction) return state;
      const index = state.messages.findIndex((message) => message.id === reaction.message_id);
      if (index < 0) return state;
      const message = state.messages[index];
      // Reconcile against the pre-optimistic baseline (not the current, possibly
      // still-unconfirmed optimistic guess) so an update from another actor doesn't
      // inherit our own not-yet-confirmed toggle as ground truth.
      const baseline = state.pendingReactions.get(reaction.message_id) ?? message.reactions;
      const previous = new Map(baseline.map((item) => [item.emoji, item.reactedByMe]));
      const reactions = reaction.reactions.map((item) => ({
        ...item,
        reactedByMe:
          action.actorIsMe && item.emoji === reaction.emoji
            ? reaction.added
            : (previous.get(item.emoji) ?? false),
      }));
      const messages = [...state.messages];
      messages[index] = { ...message, reactions };
      const pendingReactions = new Map(state.pendingReactions);
      pendingReactions.delete(reaction.message_id);
      return {
        ...state,
        messages,
        pendingReactions,
        lastMutation: "none",
        realtimeError: null,
        actionError: null,
      };
    }
    case "reaction_snapshot": {
      const index = state.messages.findIndex((message) => message.id === action.messageId);
      if (index < 0) return state;
      const messages = [...state.messages];
      messages[index] = { ...messages[index], reactions: action.reactions };
      const pendingReactions = new Map(state.pendingReactions);
      pendingReactions.delete(action.messageId);
      return {
        ...state,
        messages,
        pendingReactions,
        lastMutation: "none",
        realtimeError: null,
        actionError: null,
      };
    }
  }
}

// ── Hook ──────────────────────────────────────────────────────────────────────

interface UseMessagesOptions {
  kind: "channel" | "dm";
  targetId: string;
  currentUserId: string;
  /** Direct-navigation target resolved through the authorized single-message GET. */
  focusMessageId?: string;
  onOwnReactionConfirmed?: (emoji: string) => void;
  /** RF-05: called on a pin.updated event for the active target (refetch pins). */
  onPinUpdated?: (event: WSPinUpdatedEvent) => void;
  /**
   * Issue #398: called on a members.added event for the active target.
   *
   * Routed through this hook rather than a second socket because the connection
   * and its subscriptions already live here; opening another one to hear about
   * membership would double the WebSocket count per conversation.
   */
  onMembersAdded?: (event: WSMembersAddedEvent) => void;
  /**
   * RF-22: called on an attachment.status event for the active target.
   *
   * Routed through this hook for the same reason members.added is — the
   * connection and its subscriptions already live here — and filtered to the
   * active target, so a verdict for another conversation cannot make this one
   * refetch.
   */
  onAttachmentStatus?: (event: WSAttachmentStatusEvent) => void;
  onMessageRemoved?: () => void;
}

export interface UseMessagesResult {
  state: MessagesState;
  sendMessage: (body: string, referencedMessageId?: string) => Promise<SendResult>;
  retry: () => void;
  loadMore: () => void;
  selectReply: (message: Message) => void;
  cancelReply: () => void;
  toggleReaction: (messageId: string, emoji: string) => void;
  toggleFavorite: (messageId: string, isFavorited: boolean) => void;
  editMessageLocal: (
    messageId: string,
    body: string,
    bodyFormat: Message["bodyFormat"],
  ) => Promise<Message>;
  deleteMessageLocal: (messageId: string) => Promise<void>;
}

export function useMessages({
  kind,
  targetId,
  currentUserId,
  focusMessageId,
  onOwnReactionConfirmed,
  onPinUpdated,
  onMembersAdded,
  onAttachmentStatus,
  onMessageRemoved,
}: UseMessagesOptions): UseMessagesResult {
  const [state, dispatch] = useReducer(reducer, initialState);

  useEffect(() => {
    if (!state.actionError) return;
    const timer = window.setTimeout(() => dispatch({ type: "reaction_error_clear" }), 5_000);
    return () => window.clearTimeout(timer);
  }, [state.actionError]);

  // stateRef holds values that stable callbacks (loadMore, sendMessage, load) read
  // after async gaps, so they always see the current target and pagination state.
  // useLayoutEffect (no deps) fires synchronously after every render, before any
  // microtask, ensuring the ref is up-to-date before any async resolution can run.
  const stateRef = useRef({
    target: `${kind}:${targetId}`,
    nextCursor: state.nextCursor,
    loadingMore: state.loadingMore,
    messages: state.messages,
    replyTo: state.replyTo,
  });
  useLayoutEffect(() => {
    stateRef.current.target = `${kind}:${targetId}`;
    stateRef.current.nextCursor = state.nextCursor;
    stateRef.current.loadingMore = state.loadingMore;
    stateRef.current.messages = state.messages;
    stateRef.current.replyTo = state.replyTo;
  });

  const abortRef = useRef<AbortController | null>(null);
  const loadMoreAbortRef = useRef<AbortController | null>(null);
  const wsFallbackAbortRefs = useRef<Map<string, AbortController>>(new Map());
  const deletedMessageTombstonesRef = useRef<Map<string, string | null>>(new Map());
  const deletedMessageTombstoneTargetRef = useRef(`${kind}:${targetId}`);
  const pendingReactionTimersRef = useRef<Map<string, number>>(new Map());
  const onMessageRemovedRef = useRef(onMessageRemoved);
  useLayoutEffect(() => {
    onMessageRemovedRef.current = onMessageRemoved;
  }, [onMessageRemoved]);
  const clearReactionTimer = useCallback((messageId: string) => {
    const timer = pendingReactionTimersRef.current.get(messageId);
    if (timer !== undefined) {
      window.clearTimeout(timer);
      pendingReactionTimersRef.current.delete(messageId);
    }
  }, []);

  const abortWsFallbacks = useCallback(() => {
    for (const ctrl of wsFallbackAbortRefs.current.values()) ctrl.abort();
    wsFallbackAbortRefs.current.clear();
  }, []);

  const startWsFallback = useCallback((key: string) => {
    wsFallbackAbortRefs.current.get(key)?.abort();
    const ctrl = new AbortController();
    wsFallbackAbortRefs.current.set(key, ctrl);
    return ctrl;
  }, []);

  const cancelWsFallback = useCallback((key: string) => {
    const ctrl = wsFallbackAbortRefs.current.get(key);
    ctrl?.abort();
    wsFallbackAbortRefs.current.delete(key);
  }, []);

  const finishWsFallback = useCallback((key: string, ctrl: AbortController) => {
    if (wsFallbackAbortRefs.current.get(key) === ctrl) {
      wsFallbackAbortRefs.current.delete(key);
    }
  }, []);

  const sanitizeTombstonedMessage = useCallback((message: Message): Message => {
    if (deletedMessageTombstonesRef.current.has(message.id)) {
      return {
        ...message,
        bodyText: "",
        quoted: undefined,
        reactions: [],
        isRemoved: true,
        status: "deleted",
        deletedAt: deletedMessageTombstonesRef.current.get(message.id) ?? message.deletedAt,
      };
    }
    if (message.quoted && deletedMessageTombstonesRef.current.has(message.quoted.id)) {
      return {
        ...message,
        quoted: {
          ...message.quoted,
          bodyText: "",
          isRemoved: true,
          deletedAt:
            deletedMessageTombstonesRef.current.get(message.quoted.id) ?? message.quoted.deletedAt,
        },
      };
    }
    return message;
  }, []);

  const isCurrentTarget = useCallback((loadKey: string) => {
    return stateRef.current.target === loadKey;
  }, []);

  const isMessageRendered = useCallback((messageId: string) => {
    return stateRef.current.messages.some((message) => message.id === messageId);
  }, []);

  const load = useCallback(
    (id: string, k: "channel" | "dm") => {
      const loadKey = `${k}:${id}`;
      abortRef.current?.abort();
      loadMoreAbortRef.current?.abort();
      abortWsFallbacks();
      if (deletedMessageTombstoneTargetRef.current !== loadKey) {
        deletedMessageTombstonesRef.current.clear();
        deletedMessageTombstoneTargetRef.current = loadKey;
      }
      const ctrl = new AbortController();
      abortRef.current = ctrl;

      dispatch({ type: "loading" });

      const fetchFn: () => Promise<MessagePage> =
        k === "channel"
          ? () => fetchChannelMessages(id, undefined, ctrl.signal)
          : () => fetchDMMessages(id, undefined, ctrl.signal);

      const fetchPage = async (): Promise<MessagePage> => {
        const page = await fetchFn();
        if (!focusMessageId || page.messages.some((message) => message.id === focusMessageId)) {
          return page;
        }
        try {
          const focused =
            k === "channel"
              ? await fetchChannelMessage(id, focusMessageId, ctrl.signal)
              : await fetchDMMessage(id, focusMessageId, ctrl.signal);
          return {
            ...page,
            messages: insertMessageChronologically(page.messages, focused).messages,
          };
        } catch {
          // Invalid, removed, or inaccessible focus IDs never reveal which case occurred.
          return page;
        }
      };

      fetchPage().then(
        (page) => {
          if (!isCurrentTarget(loadKey)) return;
          dispatch({
            type: "loaded",
            page: { ...page, messages: page.messages.map(sanitizeTombstonedMessage) },
          });
        },
        (err: unknown) => {
          if (!isCurrentTarget(loadKey)) return;
          if (err instanceof Error && err.name === "AbortError") return;
          dispatch({ type: "error" });
        },
      );

      return () => {
        ctrl.abort();
        loadMoreAbortRef.current?.abort();
        abortWsFallbacks();
        for (const timer of pendingReactionTimersRef.current.values()) window.clearTimeout(timer);
        pendingReactionTimersRef.current.clear();
      };
    },
    [abortWsFallbacks, focusMessageId, isCurrentTarget, sanitizeTombstonedMessage],
  );

  useEffect(() => {
    if (!targetId) return;
    return load(targetId, kind);
  }, [kind, targetId, load]);

  const referencedMessageIDs = useMemo(
    () => state.messages.filter((message) => message.reference).map((message) => message.id),
    [state.messages],
  );
  const referencedMessageIDsKey = referencedMessageIDs.join(",");

  useEffect(() => {
    if (!targetId || !referencedMessageIDsKey) return;
    const loadKey = `${kind}:${targetId}`;
    const allMessageIDs = referencedMessageIDsKey.split(",");
    const messageIDs = allMessageIDs.slice(-referenceRevalidationBatchSize);
    const overflowMessageIDs = allMessageIDs.slice(0, -referenceRevalidationBatchSize);
    if (overflowMessageIDs.length > 0) {
      dispatch({
        type: "references_refreshed",
        references: Object.fromEntries(
          overflowMessageIDs.map((messageID) => [messageID, { available: false }]),
        ),
      });
    }
    let generation = 0;
    let activeController: AbortController | null = null;
    let disposed = false;
    let revalidationScheduled = false;

    const revalidate = () => {
      generation += 1;
      const currentGeneration = generation;
      activeController?.abort();
      const controller = new AbortController();
      activeController = controller;
      const request =
        kind === "channel"
          ? resolveChannelMessageReferences(targetId, messageIDs, controller.signal)
          : resolveDMMessageReferences(targetId, messageIDs, controller.signal);
      void request.then(
        (references) => {
          if (disposed || currentGeneration !== generation || !isCurrentTarget(loadKey)) return;
          dispatch({
            type: "references_refreshed",
            references: Object.fromEntries(
              messageIDs.map((messageID) => [
                messageID,
                references[messageID] ?? { available: false },
              ]),
            ),
          });
        },
        () => {
          if (disposed || currentGeneration !== generation || !isCurrentTarget(loadKey)) return;
          dispatch({
            type: "references_refreshed",
            references: Object.fromEntries(
              messageIDs.map((messageID) => [messageID, { available: false }]),
            ),
          });
        },
      );
    };
    const scheduleRevalidation = () => {
      if (revalidationScheduled) return;
      revalidationScheduled = true;
      queueMicrotask(() => {
        revalidationScheduled = false;
        if (!disposed) revalidate();
      });
    };

    const timer = window.setInterval(revalidate, referenceRevalidationMs);
    window.addEventListener("focus", scheduleRevalidation);
    const onVisibilityChange = () => {
      if (document.visibilityState === "visible") scheduleRevalidation();
    };
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      disposed = true;
      generation += 1;
      activeController?.abort();
      window.clearInterval(timer);
      window.removeEventListener("focus", scheduleRevalidation);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [isCurrentTarget, kind, referencedMessageIDsKey, targetId]);

  const retry = useCallback(() => {
    if (targetId) load(targetId, kind);
  }, [kind, targetId, load]);

  const loadMore = useCallback(() => {
    const { nextCursor, loadingMore } = stateRef.current;
    if (!nextCursor || loadingMore) return;

    // Update the in-flight flag synchronously — before any async work — so that a
    // second loadMore() call in the same microtask tick (e.g., two IO callbacks
    // fired before the next React render) fails the guard above and does not
    // dispatch a duplicate fetch. The flag is cleared in both success and error paths.
    stateRef.current.loadingMore = true;

    const loadKey = `${kind}:${targetId}`;

    loadMoreAbortRef.current?.abort();
    const ctrl = new AbortController();
    loadMoreAbortRef.current = ctrl;

    dispatch({ type: "prepending" });

    const fetchFn: () => Promise<MessagePage> =
      kind === "channel"
        ? () => fetchChannelMessages(targetId, nextCursor, ctrl.signal)
        : () => fetchDMMessages(targetId, nextCursor, ctrl.signal);

    fetchFn().then(
      (page) => {
        stateRef.current.loadingMore = false;
        if (!isCurrentTarget(loadKey)) return;
        dispatch({
          type: "prepended",
          page: { ...page, messages: page.messages.map(sanitizeTombstonedMessage) },
        });
      },
      (err: unknown) => {
        stateRef.current.loadingMore = false;
        if (!isCurrentTarget(loadKey)) return;
        if (err instanceof Error && err.name === "AbortError") return;
        dispatch({ type: "prepend_error" });
      },
    );
  }, [kind, targetId, isCurrentTarget, sanitizeTombstonedMessage]);

  const sendMessage = useCallback(
    async (body: string, referencedMessageId?: string): Promise<SendResult> => {
      if (!targetId || !body.trim()) return { status: "stale" };

      const sendKey = `${kind}:${targetId}`;
      const parentMessageId = stateRef.current.replyTo?.id;
      dispatch({ type: "sending" });

      try {
        const sendFn =
          kind === "channel"
            ? () => postChannelMessage(targetId, body, parentMessageId, referencedMessageId)
            : () => postDMMessage(targetId, body, parentMessageId, referencedMessageId);

        const msg = await sendFn();

        if (stateRef.current.target !== sendKey) return { status: "stale" };
        dispatch({ type: "sent", message: sanitizeTombstonedMessage(msg) });
        return { status: "sent" };
      } catch (err: unknown) {
        // Stale failure: silently discard — do not update state for a previous target.
        if (stateRef.current.target !== sendKey) return { status: "stale" };
        const message = err instanceof Error ? err.message : "Não foi possível enviar a mensagem.";
        dispatch({ type: "send_error", error: message });
        // Re-throw for current-target failures so callers can preserve the draft.
        throw err;
      }
    },
    [kind, sanitizeTombstonedMessage, targetId],
  );

  // Handle incoming message.created WS events.
  //
  // Primary path: use evt.payload (full DTO from server) to insert the message
  // directly — no additional GET required.
  //
  // Fallback path: if payload is absent (old server version during rolling deploy),
  // fall back to a targeted GET to avoid silent message loss.
  //
  // Target check: events for other channels/DMs are ignored (defence-in-depth on
  // top of the WS hook's own filter).
  const handleWsMessageCreated = useCallback(
    (evt: WSMessageCreatedEvent) => {
      const loadKey = `${kind}:${targetId}`;

      // Double-check target (ws hook already filters, but guard here too).
      if (evt.target_id !== targetId) return;

      if (evt.payload) {
        cancelWsFallback(`created:${evt.message_id}`);
        // Build Message from the full DTO carried in the event.
        const p = evt.payload;
        const removed = p.is_removed || p.status === "deleted" || Boolean(p.deleted_at);
        const msg: Message = {
          id: p.id,
          senderId: p.sender_id,
          senderDisplayName: p.sender_display_name,
          senderEmail: p.sender_email ?? "",
          kind: p.kind as Message["kind"],
          bodyText: removed ? "" : p.body_text,
          bodyFormat: normalizeBodyFormat(p.body_format),
          isRemoved: removed,
          status: removed ? "deleted" : "active",
          deletedAt: p.deleted_at ?? null,
          createdAt: p.created_at,
          updatedAt: p.updated_at,
          isEdited: Boolean(p.edited_at),
          editCount: 0,
          editedAt: p.edited_at ?? undefined,
          reactions: [],
          // WS create events never carry the caller's favorite state; a message
          // just created cannot be favorited yet.
          isFavorited: false,
          isForwarded: p.is_forwarded === true,
          quoted:
            !removed && p.quoted
              ? {
                  id: p.quoted.id,
                  authorId: p.quoted.author_id,
                  bodyText: p.quoted.body ?? "",
                  bodyFormat: normalizeBodyFormat(p.quoted.body_format),
                  isRemoved: p.quoted.is_removed ?? false,
                  deletedAt: p.quoted.deleted_at ?? null,
                  createdAt: p.quoted.created_at ?? "",
                }
              : undefined,
        };
        if (!isCurrentTarget(loadKey)) return;
        dispatch({ type: "ws_received", message: sanitizeTombstonedMessage(msg) });
        return;
      }

      // Fallback: payload absent — fetch the message by ID.
      const fallbackKey = `created:${evt.message_id}`;
      const ctrl = startWsFallback(fallbackKey);
      const fetchFn =
        kind === "channel"
          ? () => fetchChannelMessage(targetId, evt.message_id, ctrl.signal)
          : () => fetchDMMessage(targetId, evt.message_id, ctrl.signal);

      fetchFn().then(
        (msg) => {
          finishWsFallback(fallbackKey, ctrl);
          if (ctrl.signal.aborted) return;
          if (!isCurrentTarget(loadKey)) return;
          dispatch({ type: "ws_received", message: sanitizeTombstonedMessage(msg) });
        },
        (err: unknown) => {
          finishWsFallback(fallbackKey, ctrl);
          if (!isCurrentTarget(loadKey)) return;
          if (err instanceof Error && err.name === "AbortError") return;
          dispatch({ type: "ws_fetch_error", error: realtimeFallbackErrorMessage });
        },
      );
    },
    [
      cancelWsFallback,
      finishWsFallback,
      kind,
      targetId,
      isCurrentTarget,
      sanitizeTombstonedMessage,
      startWsFallback,
    ],
  );

  const fetchReactionSnapshot = useCallback(
    (messageId: string) => {
      const fallbackKey = `reaction:${messageId}`;
      const ctrl = startWsFallback(fallbackKey);
      const loadKey = `${kind}:${targetId}`;
      const fetchFn =
        kind === "channel"
          ? () => fetchChannelMessage(targetId, messageId, ctrl.signal)
          : () => fetchDMMessage(targetId, messageId, ctrl.signal);
      fetchFn().then(
        (message) => {
          finishWsFallback(fallbackKey, ctrl);
          if (ctrl.signal.aborted) return;
          if (!isCurrentTarget(loadKey)) return;
          clearReactionTimer(message.id);
          dispatch({
            type: "reaction_snapshot",
            messageId: message.id,
            reactions: message.reactions,
          });
        },
        (err: unknown) => {
          finishWsFallback(fallbackKey, ctrl);
          if (err instanceof Error && err.name === "AbortError") return;
          if (isCurrentTarget(loadKey)) {
            dispatch({ type: "ws_fetch_error", error: realtimeFallbackErrorMessage });
          }
        },
      );
    },
    [clearReactionTimer, finishWsFallback, isCurrentTarget, kind, startWsFallback, targetId],
  );

  const handleReactionUpdated = useCallback(
    (event: WSReactionUpdatedEvent) => {
      if (event.target_id !== targetId || !isMessageRendered(event.message_id)) return;
      if (!event.reaction) {
        fetchReactionSnapshot(event.message_id);
        return;
      }
      const actorIsMe = event.reaction.actor_user_id === currentUserId;
      if (actorIsMe) onOwnReactionConfirmed?.(event.reaction.emoji);
      clearReactionTimer(event.message_id);
      dispatch({
        type: "reaction_updated",
        event,
        actorIsMe,
      });
    },
    [
      clearReactionTimer,
      currentUserId,
      fetchReactionSnapshot,
      isMessageRendered,
      onOwnReactionConfirmed,
      targetId,
    ],
  );

  const fetchMessageUpdateSnapshot = useCallback(
    (messageId: string, insertIfMissing: boolean) => {
      const fallbackKey = `updated:${messageId}`;
      const ctrl = startWsFallback(fallbackKey);
      const loadKey = `${kind}:${targetId}`;
      const fetchFn =
        kind === "channel"
          ? () => fetchChannelMessage(targetId, messageId, ctrl.signal)
          : () => fetchDMMessage(targetId, messageId, ctrl.signal);
      void fetchFn().then(
        (message) => {
          finishWsFallback(fallbackKey, ctrl);
          if (ctrl.signal.aborted || !isCurrentTarget(loadKey)) return;
          const removed = message.isRemoved || message.status === "deleted";
          if (removed) {
            deletedMessageTombstonesRef.current.set(
              message.id,
              message.deletedAt ?? message.updatedAt,
            );
          }
          const snapshot = sanitizeTombstonedMessage(message);
          dispatch({ type: "message_snapshot", message: snapshot, insertIfMissing });
          if (snapshot.isRemoved) onMessageRemovedRef.current?.();
        },
        (error: unknown) => {
          finishWsFallback(fallbackKey, ctrl);
          if (error instanceof Error && error.name === "AbortError") return;
          if (isCurrentTarget(loadKey)) {
            dispatch({ type: "ws_fetch_error", error: realtimeFallbackErrorMessage });
          }
        },
      );
    },
    [finishWsFallback, isCurrentTarget, kind, sanitizeTombstonedMessage, startWsFallback, targetId],
  );

  const handleMessageUpdated = useCallback(
    (event: WSMessageUpdatedEvent) => {
      if (event.target_id !== targetId) return;
      if (event.message_update) {
        const messageId = event.message_update.message_id;
        if (event.message_update.is_removed || event.message_update.status === "deleted") {
          deletedMessageTombstonesRef.current.set(
            messageId,
            event.message_update.deleted_at ?? event.message_update.updated_at ?? null,
          );
        }
        cancelWsFallback(`updated:${messageId}`);
        const createdFallbackKey = `created:${messageId}`;
        if (wsFallbackAbortRefs.current.has(createdFallbackKey) && !isMessageRendered(messageId)) {
          cancelWsFallback(createdFallbackKey);
          fetchMessageUpdateSnapshot(messageId, true);
        }
        dispatch({ type: "message_updated", event });
        if (event.message_update.is_removed || event.message_update.status === "deleted") {
          onMessageRemovedRef.current?.();
        }
        return;
      }

      const messageId = event.message_id;
      if (!messageId) return;
      const insertIfMissing = wsFallbackAbortRefs.current.has(`created:${messageId}`);
      fetchMessageUpdateSnapshot(messageId, insertIfMissing);
    },
    [cancelWsFallback, fetchMessageUpdateSnapshot, isMessageRendered, targetId],
  );

  const handleReactionError = useCallback((event: WSClientErrorEvent) => {
    const messages: Record<string, string> = {
      rate_limited: "Muitas reações em sequência. Aguarde um minuto e tente novamente.",
      temporarily_unavailable: "Reações temporariamente indisponíveis.",
    };
    for (const timer of pendingReactionTimersRef.current.values()) window.clearTimeout(timer);
    pendingReactionTimersRef.current.clear();
    dispatch({
      type: "reaction_error",
      error: messages[event.code] ?? "Não foi possível atualizar a reação.",
    });
  }, []);

  const handleSubscriptionError = useCallback((event: WSClientErrorEvent) => {
    dispatch({
      type: "ws_fetch_error",
      error:
        event.code === "room_access_denied"
          ? "Não foi possível acessar as atualizações em tempo real desta conversa."
          : realtimeFallbackErrorMessage,
    });
  }, []);

  const handleSubscribed = useCallback(() => {
    dispatch({ type: "ws_subscription_ready" });
  }, []);

  // RF-05: keep the pin callback in a ref so it never restarts the socket.
  const onPinUpdatedRef = useRef(onPinUpdated);
  useLayoutEffect(() => {
    onPinUpdatedRef.current = onPinUpdated;
  });
  const handlePinUpdated = useCallback(
    (event: WSPinUpdatedEvent) => {
      if (event.target_type !== kind || event.target_id !== targetId) return;
      onPinUpdatedRef.current?.(event);
    },
    [kind, targetId],
  );

  // Same ref treatment as the pin callback, and for the same reason: the panel
  // recreates its handler on every render, and letting that restart the socket
  // would drop and re-establish every subscription.
  const onMembersAddedRef = useRef(onMembersAdded);
  useLayoutEffect(() => {
    onMembersAddedRef.current = onMembersAdded;
  });
  const handleMembersAdded = useCallback(
    (event: WSMembersAddedEvent) => {
      if (event.target_type !== kind || event.target_id !== targetId) return;
      onMembersAddedRef.current?.(event);
    },
    [kind, targetId],
  );

  // Same ref treatment again, same reason.
  const onAttachmentStatusRef = useRef(onAttachmentStatus);
  useLayoutEffect(() => {
    onAttachmentStatusRef.current = onAttachmentStatus;
  });
  const handleAttachmentStatus = useCallback(
    (event: WSAttachmentStatusEvent) => {
      if (event.target_type !== kind || event.target_id !== targetId) return;
      onAttachmentStatusRef.current?.(event);
    },
    [kind, targetId],
  );

  const { toggleReaction: sendReactionToggle } = useChatWebSocket({
    kind,
    targetId,
    onMessageCreated: handleWsMessageCreated,
    onMessageUpdated: handleMessageUpdated,
    onReactionUpdated: handleReactionUpdated,
    onPinUpdated: handlePinUpdated,
    onMembersAdded: handleMembersAdded,
    onAttachmentStatus: handleAttachmentStatus,
    onReactionError: handleReactionError,
    onSubscriptionError: handleSubscriptionError,
    onSubscribed: handleSubscribed,
  });

  const toggleReaction = useCallback(
    (messageId: string, emoji: string) => {
      dispatch({ type: "reaction_optimistic", messageId, emoji });
      if (!sendReactionToggle(messageId, emoji)) {
        dispatch({
          type: "reaction_revert",
          messageId,
          error: "Conexão em tempo real indisponível. Tente novamente.",
        });
        return;
      }
      clearReactionTimer(messageId);
      const timer = window.setTimeout(() => {
        pendingReactionTimersRef.current.delete(messageId);
        dispatch({
          type: "reaction_revert",
          messageId,
          error: "Não foi possível confirmar a reação. Tente novamente.",
        });
      }, reactionConfirmTimeoutMs);
      pendingReactionTimersRef.current.set(messageId, timer);
    },
    [clearReactionTimer, sendReactionToggle],
  );

  const selectReply = useCallback((message: Message) => {
    dispatch({ type: "reply_set", message });
  }, []);

  const cancelReply = useCallback(() => {
    dispatch({ type: "reply_clear" });
  }, []);

  // RF-06: REST round-trip confirms before the flag flips — no optimistic
  // update; a failure reuses the transient reaction error banner.
  // ponytail: no in-flight dedupe; a double click just repeats an idempotent call.
  const toggleFavorite = useCallback(
    (messageId: string, isFavorited: boolean) => {
      const apply = isFavorited ? favoriteMessage : unfavoriteMessage;
      void apply(messageId)
        .then(() => {
          if (isMessageRendered(messageId)) {
            dispatch({ type: "favorite_set", messageId, isFavorited });
          }
        })
        .catch(() => {
          dispatch({
            type: "favorite_error",
            error: "Não foi possível atualizar o favorito. Tente novamente.",
          });
        });
    },
    [isMessageRendered],
  );

  const editMessageLocal = useCallback(
    async (messageId: string, body: string, bodyFormat: Message["bodyFormat"]) => {
      const previous = stateRef.current.messages.find((message) => message.id === messageId);
      if (!previous) throw new Error("Mensagem não encontrada.");
      const editKey = `${kind}:${targetId}`;
      const editedAt = new Date().toISOString();
      dispatch({ type: "edit_optimistic", messageId, body, bodyFormat, editedAt });
      try {
        const version = bodyFormat === "v3" ? 3 : bodyFormat === "v2" ? 2 : 1;
        const updated = await editMessageRequest(messageId, body, version);
        if (stateRef.current.target === editKey)
          dispatch({ type: "edit_confirmed", message: updated });
        return updated;
      } catch (error) {
        if (stateRef.current.target === editKey)
          dispatch({ type: "edit_revert", message: previous, optimisticEditedAt: editedAt });
        throw error;
      }
    },
    [kind, targetId],
  );

  const deleteMessageLocal = useCallback(
    async (messageId: string) => {
      const deleteKey = `${kind}:${targetId}`;
      try {
        const deleted = await deleteMessageRequest(messageId);
        if (stateRef.current.target === deleteKey) {
          deletedMessageTombstonesRef.current.set(
            deleted.id,
            deleted.deletedAt ?? deleted.updatedAt,
          );
          dispatch({
            type: "message_snapshot",
            message: sanitizeTombstonedMessage(deleted),
            insertIfMissing: false,
          });
          onMessageRemovedRef.current?.();
        }
      } catch (error) {
        if (stateRef.current.target === deleteKey) {
          dispatch({
            type: "delete_error",
            error: "Não foi possível excluir a mensagem. Tente novamente.",
          });
        }
        throw error;
      }
    },
    [kind, sanitizeTombstonedMessage, targetId],
  );

  return {
    state,
    sendMessage,
    retry,
    loadMore,
    selectReply,
    cancelReply,
    toggleReaction,
    toggleFavorite,
    editMessageLocal,
    deleteMessageLocal,
  };
}
