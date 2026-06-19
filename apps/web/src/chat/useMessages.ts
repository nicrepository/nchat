/**
 * useMessages — hook for loading and sending messages in a channel or DM.
 *
 * Security notes:
 * - No tokens are stored or exposed; authentication is handled by authenticatedFetch.
 * - No author_id is sent from the client; the server derives sender identity from the JWT.
 * - AbortController cancels in-flight list requests on target change or unmount.
 * - latestTargetRef is updated via useLayoutEffect (no deps) after every render,
 *   synchronously in the same JS task, before any microtask can run. This ensures
 *   stale POST completions are detected reliably regardless of effect scheduling.
 *
 * WebSocket realtime delivery:
 * PREREQUISITE MISSING — ws/handler.go returns 501 Not Implemented.
 * A secure browser-usable WebSocket auth design (auth ticket or same-origin cookie-based
 * upgrade, never token-in-URL) must be implemented before WS can be wired here.
 * Until then, this hook is REST-only.
 */

import { useCallback, useEffect, useLayoutEffect, useReducer, useRef } from "react";

import {
  fetchChannelMessages,
  fetchDMMessages,
  postChannelMessage,
  postDMMessage,
} from "./chatApi";
import type { Message, MessagePage } from "./chatTypes";

// ── State shape ───────────────────────────────────────────────────────────────

type MessagesStatus = "idle" | "loading" | "ready" | "error";

/**
 * Explicit record of the most recent messages mutation.
 * Used by MessageList's useLayoutEffect to apply the correct scroll strategy
 * without relying on fragile first/last ID comparisons.
 */
export type LastMutation = "initial" | "append" | "prepend" | "none";

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
  | { type: "prepend_error" };

const initialState: MessagesState = {
  status: "idle",
  messages: [],
  nextCursor: "",
  sendError: null,
  sending: false,
  loadingMore: false,
  lastMutation: "none",
};

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
  }
}

// ── Hook ──────────────────────────────────────────────────────────────────────

interface UseMessagesOptions {
  kind: "channel" | "dm";
  targetId: string;
}

export interface UseMessagesResult {
  state: MessagesState;
  sendMessage: (body: string) => Promise<SendResult>;
  retry: () => void;
  loadMore: () => void;
}

export function useMessages({ kind, targetId }: UseMessagesOptions): UseMessagesResult {
  const [state, dispatch] = useReducer(reducer, initialState);

  // stateRef holds values that stable callbacks (loadMore, sendMessage, load) read
  // after async gaps, so they always see the current target and pagination state.
  // useLayoutEffect (no deps) fires synchronously after every render, before any
  // microtask, ensuring the ref is up-to-date before any async resolution can run.
  const stateRef = useRef({
    target: `${kind}:${targetId}`,
    nextCursor: state.nextCursor,
    loadingMore: state.loadingMore,
  });
  useLayoutEffect(() => {
    stateRef.current.target = `${kind}:${targetId}`;
    stateRef.current.nextCursor = state.nextCursor;
    stateRef.current.loadingMore = state.loadingMore;
  });

  const abortRef = useRef<AbortController | null>(null);
  const loadMoreAbortRef = useRef<AbortController | null>(null);

  const load = useCallback((id: string, k: "channel" | "dm") => {
    abortRef.current?.abort();
    loadMoreAbortRef.current?.abort();
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
        if (stateRef.current.target !== loadKey) return;
        dispatch({ type: "loaded", page });
      },
      (err: unknown) => {
        if (stateRef.current.target !== loadKey) return;
        if (err instanceof Error && err.name === "AbortError") return;
        dispatch({ type: "error" });
      },
    );

    return () => {
      ctrl.abort();
      loadMoreAbortRef.current?.abort();
    };
  }, []);

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
        if (stateRef.current.target !== loadKey) return;
        dispatch({ type: "prepended", page });
      },
      (err: unknown) => {
        stateRef.current.loadingMore = false;
        if (stateRef.current.target !== loadKey) return;
        if (err instanceof Error && err.name === "AbortError") return;
        dispatch({ type: "prepend_error" });
      },
    );
  }, [kind, targetId]);

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

  return { state, sendMessage, retry, loadMore };
}
