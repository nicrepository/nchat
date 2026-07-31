import { useCallback, useEffect, useRef, useState } from "react";

import { getAccessToken } from "../lib/authSession";
import { issueCallToken } from "./callApi";
import {
  applyCallEvent,
  initialCallState,
  isTerminalCall,
  parseCallEvent,
  type Call,
  type CallState,
  type CallType,
} from "./callState";

const CHAT_WS_URL =
  (import.meta.env.VITE_CHAT_WS_URL as string | undefined) ??
  `${window.location.protocol === "https:" ? "wss:" : "ws:"}//${window.location.host}/api/chat/ws`;
const CHAT_WS_SUBPROTOCOL = "nchat.v1";
const RECONNECT_MAX_DELAY_MS = 2_000;

export interface CallController {
  call: Call | null;
  pending: boolean;
  error: string | null;
  mediaReady: boolean;
  start: (targetUserId: string, callType: CallType) => boolean;
  accept: () => boolean;
  decline: () => boolean;
  cancel: () => boolean;
  end: () => boolean;
  retryMedia: () => void;
  clearTerminal: () => void;
}

export function useCallSignaling(): CallController {
  const [state, setState] = useState<CallState>(initialCallState);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [mediaReady, setMediaReady] = useState(false);
  const socketRef = useRef<WebSocket | null>(null);
  const callRef = useRef<Call | null>(null);
  const pendingRef = useRef(false);
  const mediaTokenRef = useRef<{ callId: string; token: string; expiresAt: string } | null>(null);

  useEffect(() => {
    callRef.current = state.call;
  }, [state.call]);

  const requestMedia = useCallback(async (call: Call) => {
    if (call.status !== "active" || mediaTokenRef.current?.callId === call.call_id) return;
    try {
      const result = await issueCallToken(call.call_id);
      if (callRef.current?.call_id !== call.call_id || callRef.current.status !== "active") return;
      mediaTokenRef.current = { callId: call.call_id, ...result };
      setMediaReady(true);
      setError(null);
    } catch {
      setMediaReady(false);
      setError("Não foi possível preparar a mídia da chamada. Tente novamente.");
    }
  }, []);

  useEffect(() => {
    let closed = false;
    let reconnectAttempt = 0;
    let reconnectTimer: number | null = null;

    const connect = () => {
      const token = getAccessToken();
      if (closed || !token) return;
      let socket: WebSocket;
      try {
        socket = new WebSocket(CHAT_WS_URL, [token, CHAT_WS_SUBPROTOCOL]);
      } catch {
        reconnectTimer = window.setTimeout(connect, RECONNECT_MAX_DELAY_MS);
        return;
      }
      socketRef.current = socket;
      socket.onopen = () => {
        if (closed || socketRef.current !== socket) return;
        reconnectAttempt = 0;
        socket.send(JSON.stringify({ type: "call.sync" }));
      };
      socket.onmessage = (message) => {
        let value: unknown;
        try {
          value = JSON.parse(String(message.data));
        } catch {
          return;
        }
        const event = parseCallEvent(value);
        if (event) {
          pendingRef.current = false;
          setPending(false);
          setError(null);
          setState((current) => applyCallEvent(current, event));
          if (event.call.status === "active") void requestMedia(event.call);
          if (isTerminalCall(event.call.status)) {
            mediaTokenRef.current = null;
            setMediaReady(false);
          }
          return;
        }
        if (
          value &&
          typeof value === "object" &&
          (value as Record<string, unknown>).type === "call.error"
        ) {
          pendingRef.current = false;
          setPending(false);
          const code = (value as Record<string, unknown>).code;
          setError(
            code === "call_rate_limited"
              ? "Muitas tentativas de chamada. Aguarde um minuto."
              : code === "call_invalid_state"
                ? "A chamada já mudou de estado."
                : "Não foi possível concluir a ação da chamada.",
          );
        }
      };
      socket.onclose = () => {
        if (closed || socketRef.current !== socket) return;
        socketRef.current = null;
        const delay = Math.min(250 * 2 ** reconnectAttempt, RECONNECT_MAX_DELAY_MS);
        reconnectAttempt += 1;
        reconnectTimer = window.setTimeout(connect, delay);
      };
      socket.onerror = () => {};
    };

    connect();
    return () => {
      closed = true;
      if (reconnectTimer !== null) window.clearTimeout(reconnectTimer);
      const socket = socketRef.current;
      socketRef.current = null;
      socket?.close();
    };
  }, [requestMedia]);

  const send = useCallback((payload: Record<string, unknown>) => {
    const socket = socketRef.current;
    if (!socket || socket.readyState !== WebSocket.OPEN || pendingRef.current) {
      setError("Conexão em tempo real indisponível.");
      return false;
    }
    pendingRef.current = true;
    setPending(true);
    setError(null);
    socket.send(JSON.stringify(payload));
    return true;
  }, []);

  const transition = useCallback(
    (type: "call.accept" | "call.decline" | "call.cancel" | "call.end") => {
      const call = callRef.current;
      return call ? send({ type, call_id: call.call_id }) : false;
    },
    [send],
  );

  return {
    call: state.call,
    pending,
    error,
    mediaReady,
    start: (targetUserId, callType) =>
      send({
        type: "call.start",
        request_id: crypto.randomUUID(),
        target_user_id: targetUserId,
        call_type: callType,
      }),
    accept: () => transition("call.accept"),
    decline: () => transition("call.decline"),
    cancel: () => transition("call.cancel"),
    end: () => transition("call.end"),
    retryMedia: () => {
      const call = callRef.current;
      if (call) void requestMedia(call);
    },
    clearTerminal: () => {
      if (state.call && isTerminalCall(state.call.status)) setState(initialCallState);
    },
  };
}
