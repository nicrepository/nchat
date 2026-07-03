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

import { useCallback, useEffect, useLayoutEffect, useReducer, useRef } from "react";

import {
  fetchChannelMessage,
  fetchChannelMessages,
  fetchDMMessage,
  fetchDMMessages,
  postChannelMessage,
  postDMMessage,
} from "./chatApi";
import type { Message, MessagePage } from "./chatTypes";
import {
  useChatWebSocket,
  type WSMessageCreatedEvent,
  type WSClientErrorEvent,
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
  /** Feedback for reaction commands rejected or not sent. */
  reactionError: string | null;
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
  | { type: "reaction_updated"; event: WSReactionUpdatedEvent; actorIsMe: boolean }
  | { type: "reaction_snapshot"; messageId: string; reactions: Message["reactions"] }
  | { type: "reaction_error"; error: string }
  | { type: "reaction_sending" }
  | { type: "ws_fetch_error"; error: string };

const initialState: MessagesState = {
  status: "idle",
  messages: [],
  nextCursor: "",
  sendError: null,
  sending: false,
  loadingMore: false,
  lastMutation: "none",
  realtimeError: null,
  reactionError: null,
};

const realtimeFallbackErrorMessage = "Não foi possível atualizar mensagens em tempo real.";

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
        reactionError: null,
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
        reactionError: null,
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
      const msg = action.message;
      const msgs = state.messages;
      const isNewer =
        msgs.length === 0 ||
        msg.createdAt > msgs[msgs.length - 1].createdAt ||
        (msg.createdAt === msgs[msgs.length - 1].createdAt && msg.id > msgs[msgs.length - 1].id);

      const newMessages = isNewer
        ? [...msgs, msg]
        : [...msgs, msg].sort((a, b) => {
            if (a.createdAt < b.createdAt) return -1;
            if (a.createdAt > b.createdAt) return 1;
            return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
          });

      return {
        ...state,
        messages: newMessages,
        // ws_append: MessageList scrolls to bottom only if the user is already
        // near the bottom, preserving position when reading history.
        // If the message was inserted mid-list (out-of-order), no auto-scroll.
        lastMutation: isNewer ? "ws_append" : "none",
        realtimeError: null,
      };
    }
    case "ws_fetch_error":
      return { ...state, realtimeError: action.error, lastMutation: "none" };
    case "reaction_error":
      return { ...state, reactionError: action.error };
    case "reaction_sending":
      return { ...state, reactionError: null };
    case "reaction_updated": {
      const { reaction } = action.event;
      if (!reaction) return state;
      const index = state.messages.findIndex((message) => message.id === reaction.message_id);
      if (index < 0) return state;
      const message = state.messages[index];
      const previous = new Map(message.reactions.map((item) => [item.emoji, item.reactedByMe]));
      const reactions = reaction.reactions.map((item) => ({
        ...item,
        reactedByMe:
          action.actorIsMe && item.emoji === reaction.emoji
            ? reaction.added
            : (previous.get(item.emoji) ?? false),
      }));
      const messages = [...state.messages];
      messages[index] = { ...message, reactions };
      return {
        ...state,
        messages,
        lastMutation: "none",
        realtimeError: null,
        reactionError: null,
      };
    }
    case "reaction_snapshot": {
      const index = state.messages.findIndex((message) => message.id === action.messageId);
      if (index < 0) return state;
      const messages = [...state.messages];
      messages[index] = { ...messages[index], reactions: action.reactions };
      return {
        ...state,
        messages,
        lastMutation: "none",
        realtimeError: null,
        reactionError: null,
      };
    }
  }
}

// ── Hook ──────────────────────────────────────────────────────────────────────

interface UseMessagesOptions {
  kind: "channel" | "dm";
  targetId: string;
  currentUserId: string;
}

export interface UseMessagesResult {
  state: MessagesState;
  sendMessage: (body: string) => Promise<SendResult>;
  retry: () => void;
  loadMore: () => void;
  toggleReaction: (messageId: string, emoji: string) => void;
}

export function useMessages({
  kind,
  targetId,
  currentUserId,
}: UseMessagesOptions): UseMessagesResult {
  const [state, dispatch] = useReducer(reducer, initialState);

  // stateRef holds values that stable callbacks (loadMore, sendMessage, load) read
  // after async gaps, so they always see the current target and pagination state.
  // useLayoutEffect (no deps) fires synchronously after every render, before any
  // microtask, ensuring the ref is up-to-date before any async resolution can run.
  const stateRef = useRef({
    target: `${kind}:${targetId}`,
    nextCursor: state.nextCursor,
    loadingMore: state.loadingMore,
    messages: state.messages,
  });
  useLayoutEffect(() => {
    stateRef.current.target = `${kind}:${targetId}`;
    stateRef.current.nextCursor = state.nextCursor;
    stateRef.current.loadingMore = state.loadingMore;
    stateRef.current.messages = state.messages;
  });

  const abortRef = useRef<AbortController | null>(null);
  const loadMoreAbortRef = useRef<AbortController | null>(null);
  const wsFallbackAbortRef = useRef<AbortController | null>(null);

  const isCurrentTarget = useCallback((loadKey: string) => {
    return stateRef.current.target === loadKey;
  }, []);

  const isMessageRendered = useCallback((messageId: string) => {
    return stateRef.current.messages.some((message) => message.id === messageId);
  }, []);

  const load = useCallback(
    (id: string, k: "channel" | "dm") => {
      abortRef.current?.abort();
      loadMoreAbortRef.current?.abort();
      wsFallbackAbortRef.current?.abort();
      const ctrl = new AbortController();
      abortRef.current = ctrl;

      const loadKey = `${k}:${id}`;
      dispatch({ type: "loading" });

      const fetchFn: () => Promise<MessagePage> =
        k === "channel"
          ? () => fetchChannelMessages(id, undefined, ctrl.signal)
          : () => fetchDMMessages(id, undefined, ctrl.signal);

      fetchFn().then(
        (page) => {
          if (!isCurrentTarget(loadKey)) return;
          dispatch({ type: "loaded", page });
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
        wsFallbackAbortRef.current?.abort();
      };
    },
    [isCurrentTarget],
  );

  useEffect(() => {
    if (!targetId) return;
    return load(targetId, kind);
  }, [kind, targetId, load]);

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
        dispatch({ type: "prepended", page });
      },
      (err: unknown) => {
        stateRef.current.loadingMore = false;
        if (!isCurrentTarget(loadKey)) return;
        if (err instanceof Error && err.name === "AbortError") return;
        dispatch({ type: "prepend_error" });
      },
    );
  }, [kind, targetId, isCurrentTarget]);

  const sendMessage = useCallback(
    async (body: string): Promise<SendResult> => {
      if (!targetId || !body.trim()) return { status: "stale" };

      const sendKey = `${kind}:${targetId}`;
      dispatch({ type: "sending" });

      try {
        const sendFn =
          kind === "channel"
            ? () => postChannelMessage(targetId, body)
            : () => postDMMessage(targetId, body);

        const msg = await sendFn();

        if (stateRef.current.target !== sendKey) return { status: "stale" };
        dispatch({ type: "sent", message: msg });
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
    [kind, targetId],
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
        wsFallbackAbortRef.current?.abort();
        // Build Message from the full DTO carried in the event.
        const p = evt.payload;
        const msg: Message = {
          id: p.id,
          senderId: p.sender_id,
          senderDisplayName: p.sender_display_name,
          senderEmail: p.sender_email ?? "",
          kind: p.kind as Message["kind"],
          bodyText: p.body_text,
          bodyFormat: p.body_format === "v3" ? "v3" : p.body_format === "v2" ? "v2" : "v1",
          isRemoved: p.is_removed,
          status: p.status as Message["status"],
          createdAt: p.created_at,
          updatedAt: p.updated_at,
          reactions: [],
        };
        if (!isCurrentTarget(loadKey)) return;
        dispatch({ type: "ws_received", message: msg });
        return;
      }

      // Fallback: payload absent — fetch the message by ID.
      wsFallbackAbortRef.current?.abort();
      const ctrl = new AbortController();
      wsFallbackAbortRef.current = ctrl;
      const fetchFn =
        kind === "channel"
          ? () => fetchChannelMessage(targetId, evt.message_id, ctrl.signal)
          : () => fetchDMMessage(targetId, evt.message_id, ctrl.signal);

      fetchFn().then(
        (msg) => {
          if (!isCurrentTarget(loadKey)) return;
          dispatch({ type: "ws_received", message: msg });
        },
        (err: unknown) => {
          if (!isCurrentTarget(loadKey)) return;
          if (err instanceof Error && err.name === "AbortError") return;
          dispatch({ type: "ws_fetch_error", error: realtimeFallbackErrorMessage });
        },
      );
    },
    [kind, targetId, isCurrentTarget],
  );

  const fetchReactionSnapshot = useCallback(
    (messageId: string) => {
      wsFallbackAbortRef.current?.abort();
      const ctrl = new AbortController();
      wsFallbackAbortRef.current = ctrl;
      const loadKey = `${kind}:${targetId}`;
      const fetchFn =
        kind === "channel"
          ? () => fetchChannelMessage(targetId, messageId, ctrl.signal)
          : () => fetchDMMessage(targetId, messageId, ctrl.signal);
      fetchFn().then(
        (message) => {
          if (!isCurrentTarget(loadKey)) return;
          dispatch({
            type: "reaction_snapshot",
            messageId: message.id,
            reactions: message.reactions,
          });
        },
        (err: unknown) => {
          if (err instanceof Error && err.name === "AbortError") return;
          if (isCurrentTarget(loadKey)) {
            dispatch({ type: "ws_fetch_error", error: realtimeFallbackErrorMessage });
          }
        },
      );
    },
    [isCurrentTarget, kind, targetId],
  );

  const handleReactionUpdated = useCallback(
    (event: WSReactionUpdatedEvent) => {
      if (event.target_id !== targetId || !isMessageRendered(event.message_id)) return;
      if (!event.reaction) {
        fetchReactionSnapshot(event.message_id);
        return;
      }
      dispatch({
        type: "reaction_updated",
        event,
        actorIsMe: event.reaction.actor_user_id === currentUserId,
      });
    },
    [currentUserId, fetchReactionSnapshot, isMessageRendered, targetId],
  );

  const handleReactionError = useCallback((event: WSClientErrorEvent) => {
    const messages: Record<string, string> = {
      rate_limited: "Muitas reações em sequência. Aguarde um minuto e tente novamente.",
      temporarily_unavailable: "Reações temporariamente indisponíveis.",
    };
    dispatch({
      type: "reaction_error",
      error: messages[event.code] ?? "Não foi possível atualizar a reação.",
    });
  }, []);

  const { toggleReaction: sendReactionToggle } = useChatWebSocket({
    kind,
    targetId,
    onMessageCreated: handleWsMessageCreated,
    onReactionUpdated: handleReactionUpdated,
    onReactionError: handleReactionError,
  });

  const toggleReaction = useCallback(
    (messageId: string, emoji: string) => {
      dispatch({ type: "reaction_sending" });
      if (!sendReactionToggle(messageId, emoji)) {
        dispatch({
          type: "reaction_error",
          error: "Conexão em tempo real indisponível. Tente novamente.",
        });
      }
    },
    [sendReactionToggle],
  );

  return { state, sendMessage, retry, loadMore, toggleReaction };
}
