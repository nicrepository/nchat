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

export interface MessagesState {
  status: MessagesStatus;
  messages: Message[];
  /** Opaque cursor for loading older messages; empty string when no older page. */
  nextCursor: string;
  sendError: string | null;
  sending: boolean;
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
      // Reset sending when a new target load starts to avoid carry-over from a prior send.
      return { ...state, status: "loading", sendError: null, sending: false };
    case "loaded":
      return {
        status: "ready",
        messages: action.page.messages,
        nextCursor: action.page.nextCursor,
        sendError: null,
        sending: false,
      };
    case "error":
      return { ...state, status: "error", sending: false };
    case "sending":
      return { ...state, sending: true, sendError: null };
    case "sent":
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
  sendMessage: (body: string) => Promise<SendResult>;
  retry: () => void;
}

export function useMessages({ kind, targetId }: UseMessagesOptions): UseMessagesResult {
  const [state, dispatch] = useReducer(reducer, initialState);

  // latestTargetRef always holds the current route target. useLayoutEffect (no deps)
  // fires synchronously after every render in the same JS task, before microtasks.
  // Any stale POST that resolves after a route change will see the updated ref.
  const latestTargetRef = useRef(`${kind}:${targetId}`);
  useLayoutEffect(() => {
    latestTargetRef.current = `${kind}:${targetId}`;
  });

  const abortRef = useRef<AbortController | null>(null);

  const load = useCallback((id: string, k: "channel" | "dm") => {
    abortRef.current?.abort();
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
        if (latestTargetRef.current !== loadKey) return;
        dispatch({ type: "loaded", page });
      },
      (err: unknown) => {
        if (latestTargetRef.current !== loadKey) return;
        if (err instanceof Error && err.name === "AbortError") return;
        dispatch({ type: "error" });
      },
    );

    return () => {
      ctrl.abort();
    };
  }, []);

  useEffect(() => {
    if (!targetId) return;
    return load(targetId, kind);
  }, [kind, targetId, load]);

  const retry = useCallback(() => {
    if (targetId) load(targetId, kind);
  }, [kind, targetId, load]);

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

        if (latestTargetRef.current !== sendKey) return { status: "stale" };
        dispatch({ type: "sent", message: msg });
        return { status: "sent" };
      } catch (err: unknown) {
        // Stale failure: silently discard — do not update state for a previous target.
        if (latestTargetRef.current !== sendKey) return { status: "stale" };
        const message = err instanceof Error ? err.message : "Não foi possível enviar a mensagem.";
        dispatch({ type: "send_error", error: message });
        // Re-throw for current-target failures so callers can preserve the draft.
        throw err;
      }
    },
    [kind, targetId],
  );

  return { state, sendMessage, retry };
}
