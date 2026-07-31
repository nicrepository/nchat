import { useCallback, useEffect, useRef, useState } from "react";

import { acquireChatSocket, type ChatSocketHandle } from "./chatSocket";
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
  const socketRef = useRef<ChatSocketHandle | null>(null);
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
    // Call signalling shares the tab's single chat connection: a socket of its
    // own would double the per-user connection cost for no benefit, since both
    // consume the same server-side stream (issue #449).
    const handle = acquireChatSocket({
      onOpen: () => {
        if (closed) return;
        // Once per generation: the server replays the caller's current call, so
        // a reconnection cannot leave a stale call on screen.
        handle.send({ type: "call.sync" });
      },
      onMessage: (value) => {
        if (closed) return;
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
        if (value["type"] === "call.error") {
          pendingRef.current = false;
          setPending(false);
          const code = value["code"];
          setError(
            code === "call_rate_limited"
              ? "Muitas tentativas de chamada. Aguarde um minuto."
              : code === "call_invalid_state"
                ? "A chamada já mudou de estado."
                : "Não foi possível concluir a ação da chamada.",
          );
        }
      },
    });
    socketRef.current = handle;

    return () => {
      closed = true;
      socketRef.current = null;
      handle.release();
    };
  }, [requestMedia]);

  const send = useCallback((payload: Record<string, unknown>) => {
    const handle = socketRef.current;
    if (!handle || pendingRef.current || !handle.send(payload)) {
      setError("Conexão em tempo real indisponível.");
      return false;
    }
    pendingRef.current = true;
    setPending(true);
    setError(null);
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
