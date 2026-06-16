/**
 * useMessages — hook for loading and sending messages in a channel or DM.
 *
 * Security notes:
 * - No tokens are stored or exposed; authentication is handled by authenticatedFetch.
 * - No author_id is sent from the client; the server derives sender identity from the JWT.
 * - AbortController cancels in-flight requests on target change or unmount to prevent
 *   stale responses from rendering into the wrong target.
 * - A per-request "nonce" (requestId) guards against cross-target race conditions:
 *   if the target changes while a request is in flight, the stale response is discarded.
 *
 * WebSocket realtime delivery:
 * PREREQUISITE MISSING — ws/handler.go returns 501 Not Implemented.
 * A secure browser-usable WebSocket auth design (auth ticket or same-origin cookie-based
 * upgrade, never token-in-URL) must be implemented before WS can be wired here.
 * Until then, this hook is REST-only.
 */

import { useCallback, useEffect, useReducer, useRef } from "react";

import {
  fetchChannelMessages,
  fetchDMMessages,
  postChannelMessage,
  postDMMessage,
} from "./chatApi";
import type { Message, MessagePage } from "./chatTypes";

// ── State shape ───────────────────────────────────────────────────────────────

type MessagesStatus = "idle" | "loading" | "ready" | "error";

export interface MessagesState {
  status: MessagesStatus;
  messages: Message[];
  /** Opaque cursor for loading older messages; empty string when no older page. */
  nextCursor: string;
  sendError: string | null;
  sending: boolean;
}

// ── Reducer ───────────────────────────────────────────────────────────────────

type Action =
  | { type: "loading" }
  | { type: "loaded"; page: MessagePage }
  | { type: "error" }
  | { type: "sending" }
  | { type: "sent"; message: Message; requestId: string; currentRequestId: string }
  | { type: "send_error"; error: string };

const initialState: MessagesState = {
  status: "idle",
  messages: [],
  nextCursor: "",
  sendError: null,
  sending: false,
};

function reducer(state: MessagesState, action: Action): MessagesState {
  switch (action.type) {
    case "loading":
      return { ...state, status: "loading", sendError: null };
    case "loaded":
      return {
        status: "ready",
        messages: action.page.messages,
        nextCursor: action.page.nextCursor,
        sendError: null,
        sending: false,
      };
    case "error":
      return { ...state, status: "error" };
    case "sending":
      return { ...state, sending: true, sendError: null };
    case "sent":
      // Guard: discard if target has changed since send was initiated.
      if (action.requestId !== action.currentRequestId) return state;
      return {
        ...state,
        messages: [...state.messages, action.message],
        sending: false,
        sendError: null,
      };
    case "send_error":
      return { ...state, sending: false, sendError: action.error };
  }
}

// ── Hook ──────────────────────────────────────────────────────────────────────

interface UseMessagesOptions {
  kind: "channel" | "dm";
  targetId: string;
}

export interface UseMessagesResult {
  state: MessagesState;
  sendMessage: (body: string) => Promise<void>;
  retry: () => void;
}

export function useMessages({ kind, targetId }: UseMessagesOptions): UseMessagesResult {
  const [state, dispatch] = useReducer(reducer, initialState);

  // Track the current fetch request so stale responses are discarded.
  const currentRequestRef = useRef<string>("");
  // AbortController for the active list request.
  const abortRef = useRef<AbortController | null>(null);

  const load = useCallback((id: string, k: "channel" | "dm") => {
    // Cancel previous in-flight request.
    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;

    // Mint a new request ID. Only this exact (kind, id) combo will update state.
    const requestId = `${k}:${id}`;
    currentRequestRef.current = requestId;

    dispatch({ type: "loading" });

    const fetchFn: () => Promise<MessagePage> =
      k === "channel"
        ? () => fetchChannelMessages(id, undefined, ctrl.signal)
        : () => fetchDMMessages(id, undefined, ctrl.signal);

    fetchFn().then(
      (page) => {
        if (currentRequestRef.current !== requestId) return; // stale — discard
        dispatch({ type: "loaded", page });
      },
      (err: unknown) => {
        if (currentRequestRef.current !== requestId) return;
        if (err instanceof Error && err.name === "AbortError") return;
        dispatch({ type: "error" });
      },
    );

    return () => {
      ctrl.abort();
    };
  }, []);

  // Reload when target changes.
  useEffect(() => {
    if (!targetId) return;
    return load(targetId, kind);
  }, [kind, targetId, load]);

  const retry = useCallback(() => {
    if (targetId) load(targetId, kind);
  }, [kind, targetId, load]);

  const sendMessage = useCallback(
    async (body: string): Promise<void> => {
      if (!targetId || !body.trim()) return;

      const requestId = `${kind}:${targetId}`;
      dispatch({ type: "sending" });

      try {
        const sendFn =
          kind === "channel"
            ? () => postChannelMessage(targetId, body)
            : () => postDMMessage(targetId, body);

        const msg = await sendFn();

        // Append the new message only after server confirms; discard if target changed.
        dispatch({
          type: "sent",
          message: msg,
          requestId,
          currentRequestId: currentRequestRef.current,
        });
      } catch (err: unknown) {
        const message = err instanceof Error ? err.message : "Não foi possível enviar a mensagem.";
        dispatch({ type: "send_error", error: message });
      }
    },
    [kind, targetId],
  );

  return { state, sendMessage, retry };
}
