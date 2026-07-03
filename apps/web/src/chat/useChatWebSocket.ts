/**
 * useChatWebSocket — minimal WebSocket hook for realtime message delivery.
 *
 * Auth: passes the Bearer access token as the WebSocket subprotocol so that
 * browser clients (which cannot set arbitrary HTTP headers on the upgrade
 * request) can authenticate without putting the token in the URL query string.
 * The server validates the token but does not echo it back in the response.
 *
 * Security notes:
 * - Token is passed as Sec-WebSocket-Protocol, not in the URL query string.
 * - Token is read from sessionStorage via getAccessToken() — never from state.
 * - onMessage callback is kept in a ref so re-renders don't restart the socket.
 * - Target filtering is applied here AND in the caller (defence-in-depth).
 * - Errors (auth failures, network issues) are not logged; reconnect is bounded
 *   and keeps the token out of the URL.
 * - Connection is cleaned up on unmount or target change.
 */

import { useCallback, useEffect, useLayoutEffect, useRef } from "react";

import { getAccessToken } from "../lib/authSession";

const CHAT_WS_URL =
  (import.meta.env.VITE_CHAT_WS_URL as string | undefined) ??
  `${window.location.protocol === "https:" ? "wss:" : "ws:"}//${window.location.host}/api/chat/ws`;

const RECONNECT_BASE_DELAY_MS = 250;
const RECONNECT_MAX_DELAY_MS = 2_000;

export interface WSMessagePayload {
  id: string;
  workspace_id: string;
  channel_id?: string;
  dm_conversation_id?: string;
  sender_id: string;
  sender_display_name: string;
  sender_email?: string;
  kind: string;
  body_text: string;
  /** Missing means legacy v1 during rolling deploys. */
  body_format?: "v1" | "v2" | "v3";
  status: string;
  is_removed: boolean;
  created_at: string;
  updated_at: string;
  edited_at?: string | null;
  deleted_at?: string | null;
}

export interface WSMessageCreatedEvent {
  /** Optional for compatibility with older servers during rolling deploys. */
  schema_version?: number;
  type: "message.created";
  workspace_id: string;
  target_type: "channel" | "dm";
  target_id: string;
  message_id: string;
  event_id: string;
  created_at: string;
  /** Full message DTO — use this to insert the message without a follow-up GET. */
  payload?: WSMessagePayload;
}

export interface WSReactionUpdatedEvent {
  type: "reaction.updated";
  target_type: "channel" | "dm";
  target_id: string;
  message_id: string;
  reaction?: {
    message_id: string;
    actor_user_id: string;
    emoji: string;
    added: boolean;
    reactions: Array<{ emoji: string; count: number }>;
  };
}

interface UseChatWebSocketOptions {
  kind: "channel" | "dm";
  targetId: string;
  onMessageCreated: (event: WSMessageCreatedEvent) => void;
  onReactionUpdated?: (event: WSReactionUpdatedEvent) => void;
}

export interface ChatWebSocketActions {
  toggleReaction: (messageId: string, emoji: string) => void;
}

export function useChatWebSocket({
  kind,
  targetId,
  onMessageCreated,
  onReactionUpdated,
}: UseChatWebSocketOptions): ChatWebSocketActions {
  // Keep the callback current without restarting the effect.
  const onMessageRef = useRef(onMessageCreated);
  const onReactionRef = useRef(onReactionUpdated);
  const socketRef = useRef<WebSocket | null>(null);
  useLayoutEffect(() => {
    onMessageRef.current = onMessageCreated;
    onReactionRef.current = onReactionUpdated;
  });

  const toggleReaction = useCallback((messageId: string, emoji: string) => {
    const socket = socketRef.current;
    if (!socket || socket.readyState !== WebSocket.OPEN) return;
    socket.send(JSON.stringify({ type: "reaction.toggle", message_id: messageId, emoji }));
  }, []);

  useEffect(() => {
    if (!targetId) return;

    const targetType: "channel" | "dm" = kind === "channel" ? "channel" : "dm";
    let closed = false;
    let ws: WebSocket | null = null;
    let reconnectTimer: number | null = null;
    let reconnectAttempt = 0;

    const clearReconnectTimer = () => {
      if (reconnectTimer === null) return;
      window.clearTimeout(reconnectTimer);
      reconnectTimer = null;
    };

    const scheduleReconnect = () => {
      if (closed || reconnectTimer !== null) return;
      const delay = Math.min(
        RECONNECT_BASE_DELAY_MS * 2 ** reconnectAttempt,
        RECONNECT_MAX_DELAY_MS,
      );
      reconnectAttempt += 1;
      reconnectTimer = window.setTimeout(() => {
        reconnectTimer = null;
        connect();
      }, delay);
    };

    const connect = () => {
      if (closed) return;

      const token = getAccessToken();
      if (!token) return;

      let socket: WebSocket;
      try {
        // Pass the access token as the WebSocket subprotocol.
        // WSTokenMiddleware extracts it as a Bearer token; the server does not echo it back.
        socket = new WebSocket(CHAT_WS_URL, [token]);
      } catch {
        // WebSocket constructor may throw on invalid URL.
        scheduleReconnect();
        return;
      }

      ws = socket;
      socketRef.current = socket;

      socket.onopen = () => {
        if (closed || ws !== socket) return;
        reconnectAttempt = 0;
        socket.send(
          JSON.stringify({ type: "subscribe", target_type: targetType, target_id: targetId }),
        );
      };

      socket.onmessage = (event: MessageEvent<unknown>) => {
        if (closed || ws !== socket) return;
        let data: unknown;
        try {
          data = JSON.parse(event.data as string);
        } catch {
          return;
        }
        if (!data || typeof data !== "object") return;
        const d = data as Record<string, unknown>;
        // Filter: only process events for the active target.
        if (d["target_type"] !== targetType || d["target_id"] !== targetId) return;
        if (d["type"] === "message.created") {
          onMessageRef.current(d as unknown as WSMessageCreatedEvent);
        } else if (d["type"] === "reaction.updated") {
          onReactionRef.current?.(d as unknown as WSReactionUpdatedEvent);
        }
      };

      socket.onerror = () => {
        // Connection errors are expected (auth failures, network issues).
        // No logging — the event contains no useful detail and the token must not be logged.
      };

      socket.onclose = () => {
        if (closed || ws !== socket) return;
        scheduleReconnect();
      };
    };

    connect();

    return () => {
      closed = true;
      clearReconnectTimer();
      if (ws) {
        const socket = ws;
        ws = null;
        socketRef.current = null;
        if (socket.readyState === WebSocket.OPEN) {
          try {
            socket.send(
              JSON.stringify({
                type: "unsubscribe",
                target_type: targetType,
                target_id: targetId,
              }),
            );
          } catch {
            // Ignore send errors during cleanup.
          }
        }
        socket.onopen = null;
        socket.onmessage = null;
        socket.onerror = null;
        socket.onclose = null;
        socket.close();
      }
    };
  }, [kind, targetId]);

  return { toggleReaction };
}
