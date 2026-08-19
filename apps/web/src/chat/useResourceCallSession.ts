import { useCallback, useEffect, useRef, useState } from "react";

import { issueCallToken, type ResourceCallKind } from "./callApi";
import { acquireChatSocket } from "./chatSocket";
import { startResourceCall } from "./resourceCallSignaling";
import type { CallMediaBridge } from "./useCallSignaling";

export type ResourceCallStatus = "idle" | "connecting" | "active" | "error";
export type ResourceCallErrorOperation = "join" | "leave" | null;

export interface ResourceCallTarget {
  kind: ResourceCallKind;
  id: string;
  /** Channel or group display name, for the panel header — never sent to the server. */
  name: string;
  /** Authoritative id resolved by authenticated call.sync during tab handoff. */
  callId?: string;
}

export interface ResourceCallController {
  active: ResourceCallTarget | null;
  callId: string | null;
  status: ResourceCallStatus;
  error: string | null;
  /**
   * Which operation `error` belongs to, so a retry affordance can replay the
   * one that actually failed instead of always defaulting to join() — a
   * leave() failure must retry leave(), never re-join.
   */
  errorOperation: ResourceCallErrorOperation;
  /** Explicit user gesture only — never called on mount/reconnect. */
  join: (target: ResourceCallTarget) => Promise<void>;
  /** Reacquires a token and Room after this tab regains ownership. */
  reconnect: () => Promise<void>;
  /**
   * Disconnects this participant only; never notifies anyone else. Resolves
   * once the local Room cleanup has actually finished. Rejects, without
   * pretending the room was left, if that cleanup itself fails — `active`
   * and `status` stay in place so both the "Sair da chamada" button and a
   * handoff caller (RF-23's direct-call media bridge) can tell cleanup
   * never completed and try again; only a call that actually resolves may
   * treat the Room as gone.
   */
  leave: () => Promise<void>;
}

export function useResourceCallSession(
  media: CallMediaBridge,
  presenceEnabled = true,
): ResourceCallController {
  const [active, setActive] = useState<ResourceCallTarget | null>(null);
  const [status, setStatus] = useState<ResourceCallStatus>("idle");
  const [callId, setCallId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [errorOperation, setErrorOperation] = useState<ResourceCallErrorOperation>(null);
  const activeRef = useRef<ResourceCallTarget | null>(null);
  // Generation-scoped: a pending join is only deduplicable while its
  // generation is still current. leave() bumps the generation without
  // clearing joinPromiseRef synchronously (it only clears once its own
  // cleanup resolves), so without the generation check a join-then-leave-
  // then-rejoin-same-target would find this still-cached stale entry and
  // hand back the superseded (soon-to-be-no-op) promise instead of starting
  // a real new attempt.
  const joinPromiseRef = useRef<{
    generation: number;
    target: ResourceCallTarget;
    promise: Promise<void>;
  } | null>(null);
  // A rejoin of the exact same target reuses the exact same ResourceCallTarget
  // reference, so object identity can't tell "nothing changed since this
  // attempt started" from "a same-target rejoin already superseded it" —
  // join()/leave() each bump this and capture their own number instead.
  const attemptGenerationRef = useRef(0);
  // The in-flight cleanup attempt, real success/failure and all — never a
  // promise pre-resolved to "done" regardless of outcome. join() awaits it
  // before requesting a new Room; a rejection here must reach every awaiter.
  const cleanupPromiseRef = useRef<Promise<void> | null>(null);
  const mediaRef = useRef(media);
  useEffect(() => {
    mediaRef.current = media;
  }, [media]);

  // Never swallows a rejection into a false "cleanup succeeded": a failed
  // media.stop() is a real error, not a completed leave — `cleanup` itself
  // (returned below) still rejects for every caller. Cleared on both
  // outcomes via a two-argument then (not .finally, which would forward the
  // rejection to a new, uncaught promise) so the next leave()/join() starts
  // fresh instead of staying wedged behind an old rejected promise.
  const stopMedia = useCallback((): Promise<void> => {
    const cleanup = mediaRef.current.stop();
    cleanupPromiseRef.current = cleanup;
    const clearIfCurrent = () => {
      if (cleanupPromiseRef.current === cleanup) cleanupPromiseRef.current = null;
    };
    cleanup.then(clearIfCurrent, clearIfCurrent);
    return cleanup;
  }, []);

  const leave = useCallback((): Promise<void> => {
    const generation = ++attemptGenerationRef.current;
    setError(null);
    setErrorOperation(null);
    return stopMedia().then(
      () => {
        // A newer join()/leave() may already have moved past this attempt;
        // only the leave() that is still current gets to clear the room.
        if (attemptGenerationRef.current !== generation) return;
        activeRef.current = null;
        joinPromiseRef.current = null;
        setActive(null);
        setCallId(null);
        setStatus("idle");
        setErrorOperation(null);
      },
      (cleanupError: unknown) => {
        if (attemptGenerationRef.current === generation) {
          setStatus("error");
          setErrorOperation("leave");
          setError("Não foi possível sair da chamada. Tente novamente.");
        }
        throw cleanupError;
      },
    );
  }, [stopMedia]);

  const join = useCallback((target: ResourceCallTarget): Promise<void> => {
    const pending = joinPromiseRef.current;
    if (
      pending &&
      pending.generation === attemptGenerationRef.current &&
      pending.target.kind === target.kind &&
      pending.target.id === target.id
    ) {
      return pending.promise;
    }
    const generation = ++attemptGenerationRef.current;
    activeRef.current = target;
    setActive(target);
    setStatus("connecting");
    setError(null);
    setErrorOperation(null);
    const attempt = (async () => {
      // Never request a new Room before a pending leave()/unmount has
      // actually finished releasing the previous one — and never after
      // one that failed either: its rejection propagates from here.
      const cleanup = cleanupPromiseRef.current;
      if (cleanup) await cleanup;
      if (attemptGenerationRef.current !== generation) return;
      await mediaRef.current.startAudio();
      if (attemptGenerationRef.current !== generation) return;
      const call = target.callId ? { call_id: target.callId } : await startResourceCall(target);
      if (attemptGenerationRef.current !== generation) return;
      setCallId(call.call_id);
      const result = await issueCallToken(call.call_id);
      if (attemptGenerationRef.current !== generation) return;
      // RF-24 follow-up: a resource room is one call, camera and microphone
      // are just controls within it — "audio"/"video" is not a concept of
      // this room. "audio" is passed only because useCallMedia.connect()
      // still uses call_type internally to decide whether to auto-enable the
      // camera (kept for RF-23 compatibility); the camera must never turn on
      // by itself here, so this stays "audio" unconditionally and the user
      // enables the camera explicitly via toggleCamera().
      await mediaRef.current.connect(
        { call_id: call.call_id, call_type: "audio" },
        result.token,
        result.serverUrl,
      );
      if (attemptGenerationRef.current !== generation) return;
      setStatus("active");
    })().catch(() => {
      if (attemptGenerationRef.current !== generation) return;
      setStatus("error");
      setErrorOperation("join");
      setError("Não foi possível entrar na chamada.");
    });
    joinPromiseRef.current = { generation, target, promise: attempt };
    void attempt.finally(() => {
      if (joinPromiseRef.current?.promise === attempt) joinPromiseRef.current = null;
    });
    return attempt;
  }, []);

  const reconnect = useCallback(async (): Promise<void> => {
    const target = activeRef.current;
    if (!target || !callId) return;
    const generation = ++attemptGenerationRef.current;
    setStatus("connecting");
    setError(null);
    setErrorOperation(null);
    try {
      await stopMedia();
      if (attemptGenerationRef.current !== generation) return;
      await mediaRef.current.startAudio();
      const result = await issueCallToken(callId);
      if (attemptGenerationRef.current !== generation) return;
      await mediaRef.current.connect(
        { call_id: callId, call_type: "audio" },
        result.token,
        result.serverUrl,
      );
      if (attemptGenerationRef.current === generation) setStatus("active");
    } catch (reconnectError) {
      if (attemptGenerationRef.current !== generation) return;
      setStatus("error");
      setErrorOperation("join");
      setError("Não foi possível recuperar a chamada.");
      throw reconnectError;
    }
  }, [callId, stopMedia]);

  useEffect(
    () => () => {
      // Invalidates any in-flight join()/leave() attempt so its continuation
      // never touches state after this point.
      attemptGenerationRef.current += 1;
      activeRef.current = null;
      // Best-effort on unmount: nothing left to show a recoverable error to.
      void stopMedia().catch(() => undefined);
    },
    [stopMedia],
  );

  useEffect(() => {
    if (!presenceEnabled || status !== "active" || !callId) return;
    const sendPresence = () => handle.send({ type: "call.presence", call_id: callId });
    const handle = acquireChatSocket({ onOpen: sendPresence });
    sendPresence();
    const heartbeat = window.setInterval(sendPresence, 10_000);
    return () => {
      window.clearInterval(heartbeat);
      handle.release();
    };
  }, [callId, presenceEnabled, status]);

  return { active, callId, status, error, errorOperation, join, reconnect, leave };
}
