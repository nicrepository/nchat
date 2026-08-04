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
  mediaCleanup: Promise<boolean> | null;
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

export function useCallSignaling(media?: CallMediaBridge, mediaEnabled = true): CallController {
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
  const mediaRequestGenerationRef = useRef(0);
  const mediaRetryPromiseRef = useRef<{ callId: string; promise: Promise<void> } | null>(null);
  const mediaCleanupPromiseRef = useRef<Promise<boolean> | null>(null);
  const mediaRef = useRef(media);
  const mediaEnabledRef = useRef(mediaEnabled);

  useEffect(() => {
    mediaRef.current = media;
  }, [media]);

  const requestMedia = useCallback(async (call: Call) => {
    if (
      !mediaEnabledRef.current ||
      call.status !== "active" ||
      mediaCallIdRef.current === call.call_id ||
      mediaRequestCallIdRef.current === call.call_id
    ) {
      return;
    }
    const generation = mediaRequestGenerationRef.current;
    mediaRequestCallIdRef.current = call.call_id;
    const current = () =>
      mediaRequestGenerationRef.current === generation &&
      mediaRequestCallIdRef.current === call.call_id &&
      mediaEnabledRef.current &&
      callRef.current?.call_id === call.call_id &&
      callRef.current.status === "active";
    try {
      if (!current()) return;
      const result = await issueCallToken(call.call_id);
      if (!current()) return;
      await mediaRef.current?.connect(call, result.token);
      if (!current()) return;
      mediaCallIdRef.current = call.call_id;
      setMediaReady(true);
      setError(null);
    } catch {
      if (!current()) return;
      setMediaReady(false);
      setError("Não foi possível preparar a mídia da chamada. Tente novamente.");
    } finally {
      if (
        mediaRequestGenerationRef.current === generation &&
        mediaRequestCallIdRef.current === call.call_id
      ) {
        mediaRequestCallIdRef.current = "";
      }
    }
  }, []);

  const invalidateMediaRequest = useCallback(() => {
    mediaRequestGenerationRef.current += 1;
    mediaRequestCallIdRef.current = "";
    mediaCallIdRef.current = "";
    mediaRetryPromiseRef.current = null;
    setMediaReady(false);
  }, []);

  const stopMedia = useCallback((): Promise<boolean> => {
    invalidateMediaRequest();
    const pending = mediaCleanupPromiseRef.current;
    if (pending) return pending;
    const stopping = (async () => {
      try {
        await mediaRef.current?.stop();
        return true;
      } catch {
        return false;
      }
    })();
    mediaCleanupPromiseRef.current = stopping;
    void stopping.then(() => {
      if (mediaCleanupPromiseRef.current === stopping) mediaCleanupPromiseRef.current = null;
    });
    return stopping;
  }, [invalidateMediaRequest]);

  useEffect(() => {
    mediaEnabledRef.current = mediaEnabled;
    const call = callRef.current;
    if (!mediaEnabled || call?.status !== "active") return;
    const cleanup = mediaCleanupPromiseRef.current;
    if (!cleanup) {
      void requestMedia(call);
      return;
    }
    const mediaGeneration = mediaRequestGenerationRef.current;
    void cleanup.then((cleaned) => {
      if (
        mediaRequestGenerationRef.current !== mediaGeneration ||
        !mediaEnabledRef.current ||
        callRef.current?.call_id !== call.call_id ||
        callRef.current.status !== "active"
      ) {
        return;
      }
      if (!cleaned) {
        setMediaReady(false);
        setError("Não foi possível liberar a mídia da chamada anterior. Tente novamente.");
        return;
      }
      return requestMedia(call);
    });
  }, [mediaEnabled, requestMedia]);

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
        if (reconnect) setPending(true);
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
          const replacesCurrentCall = Boolean(
            reconciliation && currentCall && event.call.call_id !== currentCall.call_id,
          );
          const confirmsCurrentCall = Boolean(
            reconciliation &&
            currentCall &&
            event.call.call_id === currentCall.call_id &&
            event.call.version === currentCall.version &&
            event.call.status === currentCall.status,
          );
          if (nextState.call === currentCall && !confirmsCurrentCall && !replacesCurrentCall)
            return;
          syncReconciliationRef.current = null;
          if (reconciliation) {
            pendingRef.current = null;
            setPending(false);
          } else if (eventCompletesPending(pendingRef.current, event)) {
            pendingRef.current = null;
            setPending(false);
          }
          setError(null);
          const acceptedState = replacesCurrentCall ? { call: event.call } : nextState;
          if (acceptedState.call !== currentCall) {
            callRef.current = acceptedState.call;
            setState(acceptedState);
          }
          let mediaCleanup = reconciliation?.mediaCleanup ?? mediaCleanupPromiseRef.current;
          if (replacesCurrentCall) {
            if (currentCall && !isTerminalCall(currentCall.status)) {
              if (mediaCleanup) invalidateMediaRequest();
              else mediaCleanup = stopMedia();
            } else invalidateMediaRequest();
          }
          if (event.call.status === "active") {
            if (mediaCleanup) {
              const mediaGeneration = mediaRequestGenerationRef.current;
              void mediaCleanup.then((cleaned) => {
                if (
                  closed ||
                  generation !== socketGenerationRef.current ||
                  mediaRequestGenerationRef.current !== mediaGeneration ||
                  !mediaEnabledRef.current ||
                  callRef.current?.call_id !== event.call.call_id ||
                  callRef.current.status !== "active"
                ) {
                  return;
                }
                if (!cleaned) {
                  setMediaReady(false);
                  setError(
                    "Não foi possível liberar a mídia da chamada anterior. Tente novamente.",
                  );
                  return;
                }
                return requestMedia(event.call);
              });
            } else void requestMedia(event.call);
          }
          if (replacesCurrentCall) return;
          if (nextState.call !== currentCall && isTerminalCall(event.call.status)) {
            void stopMedia();
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
            pendingRef.current = null;
            const staleCall = callRef.current;
            callRef.current = null;
            setState(initialCallState);
            setPending(false);
            setError(null);
            invalidateMediaRequest();
            if (staleCall && !isTerminalCall(staleCall.status) && !reconciliation.mediaCleanup) {
              void stopMedia();
            }
            return;
          }
          if (
            operation === "call.sync" &&
            syncReconciliationRef.current?.generation === generation
          ) {
            syncReconciliationRef.current = null;
            pendingRef.current = null;
            setPending(false);
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
        const reconciliation =
          syncReconciliationRef.current?.generation === generation
            ? syncReconciliationRef.current
            : null;
        const terminalCommand =
          pendingCommand?.operation === "call.decline" ||
          pendingCommand?.operation === "call.cancel" ||
          pendingCommand?.operation === "call.end";
        let mediaCleanup = reconciliation?.mediaCleanup ?? mediaCleanupPromiseRef.current;
        if (!mediaCleanup && terminalCommand) mediaCleanup = stopMedia();
        reconnectRef.current = { mediaCleanup };
        syncReconciliationRef.current = null;
        if (pendingCommand || reconciliation) {
          pendingRef.current = null;
          setPending(false);
        }
      },
      onStatus: (status) => {
        if (closed || status !== "failed") return;
        pendingRef.current = null;
        setPending(false);
        setError("Conexão em tempo real indisponível.");
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
      mediaRequestGenerationRef.current += 1;
      mediaRetryPromiseRef.current = null;
      mediaCleanupPromiseRef.current = null;
      handle.release();
      void (async () => {
        try {
          await mediaRef.current?.stop();
        } catch {
          // The owner is gone; the media adapter already invalidated its session.
        }
      })();
    };
  }, [invalidateMediaRequest, requestMedia, stopMedia]);

  const send = useCallback((operation: CallOperation, payload: Record<string, unknown>) => {
    const handle = socketRef.current;
    if (!handle) {
      setError("Conexão em tempo real indisponível.");
      return false;
    }
    if (reconnectRef.current || syncReconciliationRef.current) return false;
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
      if (declined) void stopMedia();
      return declined;
    },
    cancel: () => {
      const cancelled = transition("call.cancel");
      if (cancelled) void stopMedia();
      return cancelled;
    },
    end: () => {
      const ended = transition("call.end");
      if (ended) void stopMedia();
      return ended;
    },
    retryMedia: () => {
      const call = callRef.current;
      const pendingRetry = mediaRetryPromiseRef.current;
      if (call && pendingRetry?.callId === call.call_id) return pendingRetry.promise;
      if (!call || call.status !== "active" || mediaRequestCallIdRef.current === call.call_id) {
        return Promise.resolve();
      }
      setError(null);
      const cleanup = stopMedia();
      const mediaGeneration = mediaRequestGenerationRef.current;
      const retrying = cleanup
        .then((cleaned) => {
          if (
            mediaRequestGenerationRef.current !== mediaGeneration ||
            callRef.current?.call_id !== call.call_id ||
            callRef.current.status !== "active"
          ) {
            return;
          }
          if (!cleaned) {
            setError("Não foi possível liberar a mídia da chamada anterior. Tente novamente.");
            return;
          }
          return requestMedia(call);
        })
        .finally(() => {
          if (mediaRetryPromiseRef.current?.promise === retrying) {
            mediaRetryPromiseRef.current = null;
          }
        });
      mediaRetryPromiseRef.current = { callId: call.call_id, promise: retrying };
      return retrying;
    },
    clearTerminal: () => {
      if (state.call && isTerminalCall(state.call.status)) {
        void stopMedia();
        callRef.current = null;
        setState(initialCallState);
      }
    },
  };
}
