/**
 * useMessages — hook for loading and sending messages in a channel or DM.
 *
 * Security notes:
 * - No tokens are stored or exposed; authentication is handled by authenticatedFetch.
 * - No author_id is sent from the client; the server derives sender identity from the JWT.
 * - AbortController cancels in-flight list requests on target change or unmount.
 * - latestTargetRef tracks the current route target. It is updated via useLayoutEffect
 *   (no deps, runs after every render) which fires synchronously in the same JS task as
 *   the render, before any microtasks. This means it is always updated before any pending
 *   Promise callbacks (e.g. a stale POST) can run. Any async send path that captures a
 *   sendKey at call time and compares against latestTargetRef.current after its await
 *   will correctly discard results from a previous target.
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

  // latestTargetRef always holds the current route target. It is updated via
  // useLayoutEffect (no deps array) which fires synchronously after every render,
  // in the same JS task, before any microtask callbacks can run. This guarantees
  // that by the time any pending Promise (e.g. a stale POST) resolves, the ref
  // already reflects the new target — closing the race window that exists when
  // using useEffect (which is deferred and can fire after microtasks).
  const latestTargetRef = useRef(`${kind}:${targetId}`);
  useLayoutEffect(() => {
    latestTargetRef.current = `${kind}:${targetId}`;
  });

  // AbortController for the active list request.
  const abortRef = useRef<AbortController | null>(null);

  const load = useCallback((id: string, k: "channel" | "dm") => {
    // Cancel previous in-flight list request.
    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;

    // Capture the load target key; compared against latestTargetRef to discard stale results.
    const loadKey = `${k}:${id}`;

    dispatch({ type: "loading" });

    const fetchFn: () => Promise<MessagePage> =
      k === "channel"
        ? () => fetchChannelMessages(id, undefined, ctrl.signal)
        : () => fetchDMMessages(id, undefined, ctrl.signal);

    fetchFn().then(
      (page) => {
        if (latestTargetRef.current !== loadKey) return; // stale — discard
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

      // Capture the send target key synchronously at call time. After each await,
      // compare against latestTargetRef.current — which useLayoutEffect keeps current —
      // to detect whether the user has navigated to a different target.
      const sendKey = `${kind}:${targetId}`;
      dispatch({ type: "sending" });

      try {
        const sendFn =
          kind === "channel"
            ? () => postChannelMessage(targetId, body)
            : () => postDMMessage(targetId, body);

        const msg = await sendFn();

        // Discard if target changed while POST was in-flight.
        if (latestTargetRef.current !== sendKey) return;
        dispatch({ type: "sent", message: msg });
      } catch (err: unknown) {
        // Discard stale error — do not update state for a target the user already left.
        if (latestTargetRef.current !== sendKey) return;
        const message = err instanceof Error ? err.message : "Não foi possível enviar a mensagem.";
        dispatch({ type: "send_error", error: message });
        // Re-throw so callers (e.g. Composer) know the send failed and can preserve draft.
        throw err;
      }
    },
    [kind, targetId],
  );

  return { state, sendMessage, retry };
}
