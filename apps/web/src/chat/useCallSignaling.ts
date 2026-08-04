import { useCallback, useEffect, useRef, useState } from "react";

import { acquireChatSocket, type ChatSocketHandle } from "./chatSocket";
import { issueCallToken } from "./callApi";
import {
  applyCallEvent,
  initialCallState,
  isTerminalCall,
  parseCallEvent,
  type Call,
  type CallEvent,
  type CallState,
  type CallType,
} from "./callState";
import type { CallMediaSessionController } from "./useCallMedia";

export type CallMediaBridge = Pick<CallMediaSessionController, "startAudio" | "connect" | "stop">;

type CallOperation = "call.start" | "call.accept" | "call.decline" | "call.cancel" | "call.end";

interface PendingCallCommand {
  operation: CallOperation;
  callId?: string;
  requestId?: string;
}

interface ReconnectState {
  mediaStopped: boolean;
}

interface SyncReconciliation extends ReconnectState {
  generation: number;
}

function eventCompletesPending(pending: PendingCallCommand | null, event: CallEvent): boolean {
  if (!pending) return false;
  if (pending.operation === "call.start") {
    return event.call.request_id === pending.requestId;
  }
  if (pending.callId !== event.call.call_id) return false;
  if (isTerminalCall(event.call.status)) return true;
  return pending.operation === "call.accept" && event.call.status === "active";
}

function errorMatchesPending(
  pending: PendingCallCommand | null,
  value: Record<string, unknown>,
): boolean {
  if (!pending || value["operation"] !== pending.operation) return false;
  // Transition errors carry call_id. call.start errors do not echo request_id,
  // so the single in-flight operation is the strongest available correlation.
  return pending.callId === undefined || value["call_id"] === pending.callId;
}

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
  retryMedia: () => Promise<void>;
  clearTerminal: () => void;
}

export function useCallSignaling(media?: CallMediaBridge): CallController {
  const [state, setState] = useState<CallState>(initialCallState);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [mediaReady, setMediaReady] = useState(false);
  const socketRef = useRef<ChatSocketHandle | null>(null);
  const callRef = useRef<Call | null>(null);
  const pendingRef = useRef<PendingCallCommand | null>(null);
  const socketGenerationRef = useRef(0);
  const reconnectRef = useRef<ReconnectState | null>(null);
  const syncReconciliationRef = useRef<SyncReconciliation | null>(null);
  const mediaCallIdRef = useRef("");
  const mediaRequestCallIdRef = useRef("");
  const mediaRetryPromiseRef = useRef<{ callId: string; promise: Promise<void> } | null>(null);
  const mediaRef = useRef(media);

  useEffect(() => {
    mediaRef.current = media;
  }, [media]);

  const requestMedia = useCallback(async (call: Call) => {
    if (
      call.status !== "active" ||
      mediaCallIdRef.current === call.call_id ||
      mediaRequestCallIdRef.current === call.call_id
    ) {
      return;
    }
    mediaRequestCallIdRef.current = call.call_id;
    try {
      const result = await issueCallToken(call.call_id);
      if (callRef.current?.call_id !== call.call_id || callRef.current.status !== "active") return;
      await mediaRef.current?.connect(call, result.token);
      if (callRef.current?.call_id !== call.call_id || callRef.current.status !== "active") return;
      mediaCallIdRef.current = call.call_id;
      setMediaReady(true);
      setError(null);
    } catch {
      if (callRef.current?.call_id !== call.call_id || callRef.current.status !== "active") return;
      setMediaReady(false);
      setError("Não foi possível preparar a mídia da chamada. Tente novamente.");
    } finally {
      if (mediaRequestCallIdRef.current === call.call_id) mediaRequestCallIdRef.current = "";
    }
  }, []);

  useEffect(() => {
    let closed = false;
    // Call signalling shares the tab's single chat connection: a socket of its
    // own would double the per-user connection cost for no benefit, since both
    // consume the same server-side stream (issue #449).
    const handle = acquireChatSocket({
      onOpen: (generation) => {
        if (closed) return;
        socketGenerationRef.current = generation;
        const reconnect = reconnectRef.current;
        syncReconciliationRef.current = reconnect ? { generation, ...reconnect } : null;
        reconnectRef.current = null;
        // Once per generation: the server replays the caller's current call, so
        // a reconnection cannot leave a stale call on screen.
        handle.send({ type: "call.sync" });
      },
      onMessage: (value, generation) => {
        if (closed || generation !== socketGenerationRef.current) return;
        const event = parseCallEvent(value);
        if (event) {
          const currentCall = callRef.current;
          const reconciliation =
            syncReconciliationRef.current?.generation === generation
              ? syncReconciliationRef.current
              : null;
          const nextState = applyCallEvent({ call: currentCall }, event);
          const confirmsCurrentCall = Boolean(
            reconciliation &&
            currentCall &&
            event.call.call_id === currentCall.call_id &&
            event.call.version === currentCall.version &&
            event.call.status === currentCall.status,
          );
          if (nextState.call === currentCall && !confirmsCurrentCall) return;
          syncReconciliationRef.current = null;
          if (eventCompletesPending(pendingRef.current, event)) {
            pendingRef.current = null;
            setPending(false);
          }
          setError(null);
          if (nextState.call !== currentCall) {
            callRef.current = nextState.call;
            setState(nextState);
          }
          if (event.call.status === "active") void requestMedia(event.call);
          if (nextState.call !== currentCall && isTerminalCall(event.call.status)) {
            mediaCallIdRef.current = "";
            mediaRequestCallIdRef.current = "";
            mediaRetryPromiseRef.current = null;
            setMediaReady(false);
            void mediaRef.current?.stop();
          }
          return;
        }
        if (value["type"] === "call.error") {
          const operation = value["operation"];
          const code = value["code"];
          if (operation === "call.sync" && code === "call_not_found") {
            const reconciliation =
              syncReconciliationRef.current?.generation === generation
                ? syncReconciliationRef.current
                : null;
            if (!reconciliation) return;
            syncReconciliationRef.current = null;
            if (pendingRef.current) return;
            const staleCall = callRef.current;
            callRef.current = null;
            setState(initialCallState);
            setPending(false);
            setError(null);
            mediaCallIdRef.current = "";
            mediaRequestCallIdRef.current = "";
            mediaRetryPromiseRef.current = null;
            setMediaReady(false);
            if (staleCall && !isTerminalCall(staleCall.status) && !reconciliation.mediaStopped) {
              void mediaRef.current?.stop();
            }
            return;
          }
          if (errorMatchesPending(pendingRef.current, value)) {
            pendingRef.current = null;
            setPending(false);
          }
          setError(
            code === "call_rate_limited"
              ? "Muitas tentativas de chamada. Aguarde um minuto."
              : code === "call_invalid_state"
                ? "A chamada já mudou de estado."
                : "Não foi possível concluir a ação da chamada.",
          );
        }
      },
      onClose: (generation) => {
        if (closed || generation !== socketGenerationRef.current || reconnectRef.current !== null) {
          return;
        }
        const pendingCommand = pendingRef.current;
        const mediaStopped =
          pendingCommand?.operation === "call.decline" ||
          pendingCommand?.operation === "call.cancel" ||
          pendingCommand?.operation === "call.end";
        reconnectRef.current = { mediaStopped };
        syncReconciliationRef.current = null;
        if (pendingCommand) {
          pendingRef.current = null;
          setPending(false);
        }
        if (mediaStopped) {
          mediaCallIdRef.current = "";
          mediaRequestCallIdRef.current = "";
          mediaRetryPromiseRef.current = null;
          setMediaReady(false);
        }
      },
    });
    socketRef.current = handle;
    socketGenerationRef.current = handle.generation();

    return () => {
      closed = true;
      socketRef.current = null;
      callRef.current = null;
      pendingRef.current = null;
      reconnectRef.current = null;
      syncReconciliationRef.current = null;
      mediaCallIdRef.current = "";
      mediaRequestCallIdRef.current = "";
      mediaRetryPromiseRef.current = null;
      handle.release();
      void mediaRef.current?.stop();
    };
  }, [requestMedia]);

  const send = useCallback((operation: CallOperation, payload: Record<string, unknown>) => {
    const handle = socketRef.current;
    if (!handle) {
      setError("Conexão em tempo real indisponível.");
      return false;
    }
    if (pendingRef.current) return false;
    pendingRef.current = {
      operation,
      ...(typeof payload["call_id"] === "string" ? { callId: payload["call_id"] } : {}),
      ...(typeof payload["request_id"] === "string" ? { requestId: payload["request_id"] } : {}),
    };
    if (!handle.send({ type: operation, ...payload })) {
      pendingRef.current = null;
      setPending(false);
      setError("Conexão em tempo real indisponível.");
      return false;
    }
    setPending(true);
    setError(null);
    return true;
  }, []);

  const transition = useCallback(
    (type: "call.accept" | "call.decline" | "call.cancel" | "call.end") => {
      const call = callRef.current;
      return call ? send(type, { call_id: call.call_id }) : false;
    },
    [send],
  );

  return {
    call: state.call,
    pending,
    error,
    mediaReady,
    start: (targetUserId, callType) => {
      const started = send("call.start", {
        request_id: crypto.randomUUID(),
        target_user_id: targetUserId,
        call_type: callType,
      });
      if (started) void mediaRef.current?.startAudio();
      return started;
    },
    accept: () => {
      const accepted = transition("call.accept");
      if (accepted) void mediaRef.current?.startAudio();
      return accepted;
    },
    decline: () => {
      const declined = transition("call.decline");
      if (declined) void mediaRef.current?.stop();
      return declined;
    },
    cancel: () => {
      const cancelled = transition("call.cancel");
      if (cancelled) void mediaRef.current?.stop();
      return cancelled;
    },
    end: () => {
      const ended = transition("call.end");
      if (ended) void mediaRef.current?.stop();
      return ended;
    },
    retryMedia: () => {
      const call = callRef.current;
      const pendingRetry = mediaRetryPromiseRef.current;
      if (call && pendingRetry?.callId === call.call_id) return pendingRetry.promise;
      if (!call || call.status !== "active" || mediaRequestCallIdRef.current === call.call_id) {
        return Promise.resolve();
      }
      mediaRequestCallIdRef.current = call.call_id;
      mediaCallIdRef.current = "";
      setMediaReady(false);
      setError(null);
      const retrying = Promise.resolve(mediaRef.current?.stop())
        .catch(() => undefined)
        .then(() => {
          if (callRef.current?.call_id !== call.call_id || callRef.current.status !== "active") {
            return;
          }
          mediaRequestCallIdRef.current = "";
          return requestMedia(call);
        })
        .finally(() => {
          if (mediaRequestCallIdRef.current === call.call_id) mediaRequestCallIdRef.current = "";
          if (mediaRetryPromiseRef.current?.promise === retrying) {
            mediaRetryPromiseRef.current = null;
          }
        });
      mediaRetryPromiseRef.current = { callId: call.call_id, promise: retrying };
      return retrying;
    },
    clearTerminal: () => {
      if (state.call && isTerminalCall(state.call.status)) {
        void mediaRef.current?.stop();
        callRef.current = null;
        setState(initialCallState);
      }
    },
  };
}
