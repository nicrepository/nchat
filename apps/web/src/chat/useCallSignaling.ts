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
import { requestMediaPermission } from "./mediaPermission";
import type { CallMediaSessionController } from "./useCallMedia";
import { randomId } from "../lib/randomId";
import type { MediaConnectionMode } from "../calls/callOwnership";

// A distinct type from CallMediaSessionController's own `connect` (see
// useCallMedia): that one takes an already-resolved `initialIntent` snapshot
// and knows nothing about ownership/mode. This bridge is the layer
// CallSessionProvider's ownedMedia implements — it takes the causal
// fresh/recovery mode instead, since only the caller here (call.start/
// accept/activateMedia/retryMedia below) knows which one applies; ownedMedia
// resolves `mode` into the actual stored intent (issue #610).
export type CallMediaBridge = {
  startAudio: CallMediaSessionController["startAudio"];
  connect: (
    call: { call_id: string; call_type: CallType },
    token: string,
    serverUrl: string,
    mode: MediaConnectionMode,
  ) => Promise<void>;
  stop: CallMediaSessionController["stop"];
};

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

// A call is only "consented" when this hook itself drove it there via a
// gesture-gated start()/accept()/activateMedia() preflight — never merely
// because an event reported status "active". Scoped to one call_id + type so
// it can't leak to a call that replaces this one, and cleared by
// invalidateMediaRequest (call ends, gets replaced, or sync finds nothing)
// and by unmount. Never persisted.
interface LocalMediaAuthorization {
  callId: string;
  callType: CallType;
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

function isAuthorizedFor(auth: LocalMediaAuthorization | null, call: Call): boolean {
  return auth !== null && auth.callId === call.call_id && auth.callType === call.call_type;
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
  mediaActivationRequired: boolean;
  start: (targetUserId: string, callType: CallType) => boolean;
  accept: () => boolean;
  decline: () => boolean;
  cancel: () => boolean;
  end: () => boolean;
  retryMedia: () => Promise<void>;
  activateMedia: () => Promise<void>;
  clearTerminal: () => void;
}

export function useCallSignaling(
  media?: CallMediaBridge,
  mediaEnabled = true,
  // Called once, right when this call's own media request begins for a
  // confirmed `active` call — before any token is requested. The intended
  // use is transferring shared-Room ownership away from something else
  // (RF-24's resource room) at the earliest point this call is guaranteed
  // to actually need the Room, never earlier (accept() alone is not that
  // guarantee: it can fail preflight or never be delivered) and never later
  // (deep inside connect(), which only runs after a token request already
  // succeeded).
  onBeforeDirectMedia?: (call: Call) => void | Promise<void>,
): CallController {
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
  const onBeforeDirectMediaRef = useRef(onBeforeDirectMedia);
  const mediaEnabledRef = useRef(mediaEnabled);
  const localAuthorizationRef = useRef<LocalMediaAuthorization | null>(null);
  const activateMediaPromiseRef = useRef<{ callId: string; promise: Promise<void> } | null>(null);
  // Causal fresh/recovery classification (issue #610) for the media
  // connection ATTEMPT — never inferred from call_type or generic SDK
  // state. Keyed by call_id so a new call never inherits a stale mode (the
  // callId check at every read site makes this self-invalidating).
  const attemptModeRef = useRef<{ callId: string; mode: MediaConnectionMode } | null>(null);
  // Survives invalidateMediaRequest()/stopMedia() — unlike every other
  // per-call media ref in this file — because "did this call EVER connect
  // successfully before" must outlive a single connect/disconnect cycle for
  // retryMedia's Caso A/B/C classification (§13) to work. Never evicted: a
  // call_id is a UUID never reused, so this only grows for the lifetime of
  // one hook instance (same accepted trade-off as callOwnership.ts's
  // per-writer key spaces).
  const everConnectedRef = useRef<Set<string>>(new Set());
  const [mediaActivationRequired, setMediaActivationRequired] = useState(false);

  useEffect(() => {
    mediaRef.current = media;
  }, [media]);

  useEffect(() => {
    onBeforeDirectMediaRef.current = onBeforeDirectMedia;
  }, [onBeforeDirectMedia]);

  const requestMedia = useCallback(async (call: Call) => {
    if (
      !mediaEnabledRef.current ||
      call.status !== "active" ||
      !isAuthorizedFor(localAuthorizationRef.current, call) ||
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
      // Runs before any network request for this call's own media: it is
      // where ownership of the shared Room is handed to this call, so that
      // ownership never depends on issueCallToken()/connect() ever
      // succeeding — a resource room the caller is leaving stays left
      // (or its cleanup keeps being retried) regardless of what happens to
      // this call's own token/connect below. A rejection here is handled
      // exactly like a token/connect failure: no Room is touched, and the
      // existing recoverable-error/retry path takes over.
      await onBeforeDirectMediaRef.current?.(call);
      if (!current()) return;
      const result = await issueCallToken(call.call_id);
      if (!current()) return;
      const mode: MediaConnectionMode =
        attemptModeRef.current?.callId === call.call_id ? attemptModeRef.current.mode : "fresh";
      await mediaRef.current?.connect(call, result.token, result.serverUrl, mode);
      if (!current()) return;
      everConnectedRef.current.add(call.call_id);
      // The gesture-time startAudio() in accept() may have unlocked a
      // session that onBeforeDirectMedia above just tore down (a resource
      // room's Room A): the browser's autoplay-unlock is per-Room, so the
      // session that just connected — possibly a brand new Room B — needs
      // its own explicit unlock too. Idempotent when no handoff happened.
      void mediaRef.current?.startAudio();
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
    activateMediaPromiseRef.current = null;
    localAuthorizationRef.current = null;
    setMediaReady(false);
    setMediaActivationRequired(false);
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
    if (!isAuthorizedFor(localAuthorizationRef.current, call)) {
      setMediaActivationRequired(true);
      return;
    }
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
        // Resource-room events share the socket but have their own lifecycle
        // controller. Treating one as a direct call would create a second media
        // owner for the same call_id.
        if (event && event.target_type !== "user") return;
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
            // The only two places local media authorization is ever granted:
            // our own call.start reaching the callee (ringing echoes our
            // request_id) or our own call.accept reaching the server
            // (confirmed by the call going active). A reconciled/restored
            // call never takes this branch, so it can never self-authorize.
            const completedOperation = pendingRef.current?.operation;
            if (
              completedOperation === "call.start" ||
              (completedOperation === "call.accept" && event.call.status === "active")
            ) {
              localAuthorizationRef.current = {
                callId: event.call.call_id,
                callType: event.call.call_type,
              };
              attemptModeRef.current = { callId: event.call.call_id, mode: "fresh" };
            }
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
            if (!isAuthorizedFor(localAuthorizationRef.current, event.call)) {
              // Restored/reconciled active call: never a getUserMedia/connect
              // consequence of just seeing "active". The UI must offer an
              // explicit activation action instead (see activateMedia below).
              setMediaActivationRequired(true);
            } else if (mediaCleanup) {
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
          if (operation === "call.sync") {
            // call.sync never has a PendingCallCommand of its own (it isn't
            // sent through send()/sendGated()): its correlation is entirely
            // syncReconciliationRef, scoped to this connection's generation.
            // No match means this reply belongs to an abandoned/older
            // reconciliation attempt, so it is ignored rather than shown.
            const reconciliation =
              syncReconciliationRef.current?.generation === generation
                ? syncReconciliationRef.current
                : null;
            if (!reconciliation) return;
            if (code === "call_not_found") {
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
            syncReconciliationRef.current = null;
            pendingRef.current = null;
            setPending(false);
            setError(
              code === "call_rate_limited"
                ? "Muitas tentativas de chamada. Aguarde um minuto."
                : "Não foi possível concluir a ação da chamada.",
            );
            return;
          }
          // Every remaining call.error answers a normal command (call.start,
          // call.accept, call.decline, call.cancel or call.end): it is only
          // processed if it matches the PendingCallCommand this hook actually
          // has in flight. A stale reply for a command that was already
          // superseded (or one for a different call_id) must not touch
          // pending/error/call/media state at all — including when nothing
          // is pending, since there is nothing here for it to correlate to.
          const pendingCommand = pendingRef.current;
          if (!pendingCommand || !errorMatchesPending(pendingCommand, value)) {
            return;
          }
          if (code === "call_invalid_state" && pendingCommand.operation !== "call.start") {
            // The backend disagrees with the local transition: the call_id we
            // acted on is correlated, but its authoritative state has moved
            // on. call.start has no established call_id to reconcile against
            // (and its own errors never echo request_id, see
            // errorMatchesPending), so it keeps the generic release below;
            // every other transition reconciles through the same call.sync
            // path a reconnect uses, instead of guessing at the new state.
            // The in-flight media cleanup this command already started (see
            // decline/cancel/end below) carries over into the reconciliation
            // so it is never started a second time.
            const mediaCleanup = mediaCleanupPromiseRef.current;
            pendingRef.current = null;
            setError(null);
            syncReconciliationRef.current = { generation, mediaCleanup };
            if (!handle.send({ type: "call.sync" })) {
              syncReconciliationRef.current = null;
              setPending(false);
              setError("Conexão em tempo real indisponível.");
            }
            return;
          }
          pendingRef.current = null;
          setPending(false);
          setError(
            code === "call_rate_limited"
              ? "Muitas tentativas de chamada. Aguarde um minuto."
              : code === "call_invalid_state"
                ? "A chamada já mudou de estado."
                : code === "call_participant_busy"
                  ? "Você ou este usuário já está em outra chamada."
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
      activateMediaPromiseRef.current = null;
      localAuthorizationRef.current = null;
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
    (type: "call.decline" | "call.cancel" | "call.end") => {
      const call = callRef.current;
      if (!call) return false;
      if (
        type === "call.decline" &&
        call.status === "ringing" &&
        pendingRef.current?.operation === "call.accept" &&
        pendingRef.current.callId === call.call_id
      ) {
        // RF-23: the user must be able to decline while accept()'s own
        // getUserMedia preflight is still awaiting the native prompt. Null
        // out the captured pendingCommand here (identity, not just the
        // ref) so sendGated's `pendingRef.current !== pendingCommand` check
        // rejects it whenever the prompt resolves — no call.accept, no
        // stale setPending/setError clobbering the decline below.
        pendingRef.current = null;
      }
      return send(type, { call_id: call.call_id });
    },
    [send],
  );

  // Reserves the pending slot synchronously (so a double click or a
  // simultaneous audio/video click is rejected immediately, same as send()),
  // then runs a getUserMedia preflight tied to the click before call.start or
  // call.accept ever reaches the server or LiveKit connects. Denial clears the
  // reservation without sending anything. A stale preflight (superseded,
  // declined, terminated, or unmounted while the browser prompt was open) is
  // detected by comparing pendingRef against the object captured here: any
  // event, close, or unmount that ends this attempt already nulls it out.
  const sendGated = useCallback(
    (
      operation: "call.start" | "call.accept",
      callType: CallType,
      payload: Record<string, unknown>,
    ): boolean => {
      const handle = socketRef.current;
      if (!handle) {
        setError("Conexão em tempo real indisponível.");
        return false;
      }
      if (reconnectRef.current || syncReconciliationRef.current) return false;
      if (pendingRef.current) return false;
      const pendingCommand: PendingCallCommand = {
        operation,
        ...(typeof payload["call_id"] === "string" ? { callId: payload["call_id"] } : {}),
        ...(typeof payload["request_id"] === "string" ? { requestId: payload["request_id"] } : {}),
      };
      pendingRef.current = pendingCommand;
      setPending(true);
      setError(null);
      void (async () => {
        const result = await requestMediaPermission(callType);
        if (pendingRef.current !== pendingCommand) return;
        if (!result.ok) {
          pendingRef.current = null;
          setPending(false);
          setError(result.message);
          return;
        }
        const currentHandle = socketRef.current;
        if (!currentHandle || !currentHandle.send({ type: operation, ...payload })) {
          pendingRef.current = null;
          setPending(false);
          setError("Conexão em tempo real indisponível.");
        }
      })();
      return true;
    },
    [],
  );

  // Explicit user gesture that grants local authorization for a call this
  // hook never itself started or accepted (restored via reload, reconnect,
  // or call.sync). Runs the same preflight as start()/accept(); only on
  // success does the call become authorized and requestMedia proceed.
  const activateMedia = useCallback((): Promise<void> => {
    const call = callRef.current;
    const pendingActivation = activateMediaPromiseRef.current;
    if (call && pendingActivation?.callId === call.call_id) return pendingActivation.promise;
    if (!call || call.status !== "active" || isAuthorizedFor(localAuthorizationRef.current, call)) {
      return Promise.resolve();
    }
    setError(null);
    const activating = (async () => {
      const result = await requestMediaPermission(call.call_type);
      if (callRef.current?.call_id !== call.call_id || callRef.current.status !== "active") return;
      if (!result.ok) {
        setError(result.message);
        return;
      }
      // If a previous call's media is still tearing down (e.g. this call
      // replaced one that was ending), wait for it exactly like the
      // automatic authorized path does, instead of overlapping sessions.
      const pendingCleanup = mediaCleanupPromiseRef.current;
      if (pendingCleanup) {
        const cleaned = await pendingCleanup;
        if (callRef.current?.call_id !== call.call_id || callRef.current.status !== "active")
          return;
        if (!cleaned) {
          setError("Não foi possível liberar a mídia da chamada anterior. Tente novamente.");
          return;
        }
      }
      localAuthorizationRef.current = { callId: call.call_id, callType: call.call_type };
      attemptModeRef.current = { callId: call.call_id, mode: "recovery" };
      setMediaActivationRequired(false);
      await requestMedia(call);
    })().finally(() => {
      if (activateMediaPromiseRef.current?.promise === activating) {
        activateMediaPromiseRef.current = null;
      }
    });
    activateMediaPromiseRef.current = { callId: call.call_id, promise: activating };
    return activating;
  }, [requestMedia]);

  return {
    call: state.call,
    pending,
    error,
    mediaReady,
    mediaActivationRequired,
    start: (targetUserId, callType) => {
      const started = sendGated("call.start", callType, {
        request_id: randomId(),
        target_user_id: targetUserId,
        call_type: callType,
      });
      if (started) void mediaRef.current?.startAudio();
      return started;
    },
    accept: () => {
      const call = callRef.current;
      if (!call) return false;
      const accepted = sendGated("call.accept", call.call_type, { call_id: call.call_id });
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
          // stopMedia() above invalidated authorization along with every
          // other piece of media state; retrying is itself the explicit
          // gesture that re-authorizes this exact call for this attempt.
          localAuthorizationRef.current = { callId: call.call_id, callType: call.call_type };
          // §13 Caso A/B/C: once this call has EVER connected successfully,
          // every subsequent retry is a recovery, regardless of how the
          // attempt being retried was itself classified. Otherwise (never
          // connected yet) the retry keeps whatever mode the attempt being
          // retried already had — fresh if its first fresh connect never
          // completed, recovery if a recovery attempt failed before
          // connecting.
          const previousMode =
            attemptModeRef.current?.callId === call.call_id ? attemptModeRef.current.mode : "fresh";
          attemptModeRef.current = {
            callId: call.call_id,
            mode: everConnectedRef.current.has(call.call_id) ? "recovery" : previousMode,
          };
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
    activateMedia,
    clearTerminal: () => {
      if (state.call && isTerminalCall(state.call.status)) {
        void stopMedia();
        callRef.current = null;
        setState(initialCallState);
      }
    },
  };
}
