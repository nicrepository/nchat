import { useCallback, useEffect, useRef, useState } from "react";

import { issueCallToken, type ResourceCallKind } from "./callApi";
import { acquireChatSocket } from "./chatSocket";
import {
  joinResourceCall,
  leaveResourceCall,
  ResourceCallSignalingError,
  startResourceCall,
} from "./resourceCallSignaling";
import type { CallMediaBridge } from "./useCallSignaling";

export type ResourceCallStatus = "idle" | "connecting" | "active" | "error";
export type ResourceCallErrorOperation = "join" | "leave" | null;

/**
 * Internal marker for join()'s own catch handler (issue #622 round 2,
 * post-admission compensation): thrown only when admission already
 * succeeded (a lease now exists server-side) and this attempt's own
 * best-effort call.leave to release it also failed. Never thrown across a
 * public API boundary — join() itself always resolves to `undefined` on any
 * failure, per its existing contract.
 */
class ResourceJoinCompensationFailedError extends Error {
  callId: string;
  participationId: string;
  cause: unknown;

  constructor(callId: string, participationId: string, cause: unknown) {
    super("resource join compensation failed");
    this.name = "ResourceJoinCompensationFailedError";
    this.callId = callId;
    this.participationId = participationId;
    this.cause = cause;
  }
}

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
  /**
   * Explicit user gesture only — never called on mount/reconnect. Resolves
   * with the call_id this attempt actually joined once media is connected,
   * or `undefined` if the attempt failed or was superseded before
   * finishing — never throws. This is the only reliable signal a caller can
   * use to register a fresh participation: `callId` above may already equal
   * this same value from a previous attempt (a legitimate rejoin of the
   * exact same resource call), so React never produces an observable state
   * transition on it that an effect could key off.
   */
  /**
   * `onCallIdResolved`, if given, fires SYNCHRONOUSLY — in the same tick as
   * this hook's own internal `callId` authority, before the very next
   * `await` (issueCallToken) — the instant this attempt's call_id becomes
   * known, whether that was already known (`target.callId`, the common
   * case) or only just resolved from the server (issue #594 adversarial
   * follow-up, round 3: a fresh join whose target has no callId yet, e.g.
   * ChatShell's "start/join group call" flow). This is the only way an
   * external causal-intent tracker (CallSessionProvider's
   * pendingJoinAttemptsRef) can associate itself with the real callId
   * before any cross-tab message has a chance to interleave — waiting for
   * this promise to resolve would already be too late.
   */
  join: (
    target: ResourceCallTarget,
    onCallIdResolved?: (callId: string) => void,
  ) => Promise<string | undefined>;
  /** Reacquires a token and Room after this tab regains ownership. */
  reconnect: () => Promise<void>;
  /**
   * Disconnects this participant only — releases their own server-side
   * participation (issue #569) alongside the local Room cleanup, but never
   * ends the resource call for anyone else and never notifies anyone else
   * directly. Resolves once both have actually finished. Rejects, without
   * pretending the room was left, if either the local cleanup or the
   * server-side release fails — `active` and `status` stay in place so both
   * the "Sair da chamada" button and a handoff caller (RF-23's direct-call
   * media bridge) can tell cleanup never completed and try again; only a
   * call that actually resolves may treat the Room as gone. Safe to call
   * more than once for the same leave: the server-side release is
   * idempotent, so a duplicate/retried leave never corrupts state.
   */
  leave: () => Promise<boolean>;
  /**
   * Converges this session to idle for a participation the server already
   * ended in a DIFFERENT tab (issue #594) — e.g. this tab handed ownership
   * off and a dedicated tab then ran its own real leave(). Never calls
   * leaveResourceCall itself; that round trip already happened wherever the
   * real leave ran — this is purely local state. No-ops unless `callId`
   * still matches this hook's own current callId, so a stale/reordered
   * cross-tab "left" can never clear a rejoin this same hook has since
   * moved on to. Bumps the attempt generation exactly like leave()'s success
   * path, so any join()/reconnect() still in flight for the converged
   * participation aborts instead of resurrecting it.
   */
  convergeRemoteLeave: (callId: string) => void;
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
  // The synchronous, always-current authority for "which callId is this
  // hook's own participation right now" (issue #594 adversarial follow-up).
  // Written in the SAME tick as setCallId(), never only via the `callId`
  // React state closure: a cross-tab "left" can arrive and be accepted by
  // the ordering guard in CallSessionProvider before this hook's own
  // component (or CallSessionProvider's, which holds the `resource` object
  // convergeRemoteLeave lives on) has re-rendered — at which point any
  // closure over the `callId` state variable would still read the OLD
  // value, silently no-op the convergence, and — because the ordering guard
  // already consumed that "left" as accepted — leave this session stuck
  // active forever, since a redelivered duplicate is rejected as stale.
  // Mirrors the participationRecordsRef/participationRecords split in
  // CallSessionProvider.tsx for exactly the same reason: never read a
  // React state value from a callback that must react to something that
  // just happened in the same synchronous instant.
  const callIdRef = useRef<string | null>(null);
  const participationIdRef = useRef<string | null>(null);
  // Generation-scoped, exactly like joinPromiseRef/cleanupPromiseRef below:
  // set the instant THIS attempt (join() or reconnect()) marks itself
  // "connecting", cleared only by whichever attempt's own generation still
  // owns it — so a superseded attempt's own bail-out can never wipe a
  // newer attempt's still-genuine in-flight marker. convergeRemoteLeave
  // (issue #594) reads this to know whether the shared media bridge
  // currently has a live/in-flight connection THIS HOOK is responsible for
  // tearing down — never true for a merely-stale `active`/`callId` left
  // over from a handoff (whose media was already stopped elsewhere), and
  // never true once join()/reconnect() has itself settled either way. That
  // distinction matters: unconditionally stopping the shared media bridge
  // from convergeRemoteLeave would also stop an unrelated, already-active
  // direct 1:1 call that happens to be reusing the same bridge.
  const connectingGenerationRef = useRef<number | null>(null);
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
    promise: Promise<string | undefined>;
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
  // Set by the presence effect below whenever its heartbeat is running;
  // leave() calls this synchronously, before any await, so a heartbeat tick
  // already queued cannot slip a call.presence out after call.leave has been
  // sent (issue #569 follow-up: a late presence must never resurrect a lease
  // this same leave() is releasing). React's own effect cleanup calls the
  // same function on the next render, so whichever runs first does the work
  // and the other is a no-op — see the effect for why that's safe to call
  // twice.
  const stopHeartbeatRef = useRef<(() => void) | null>(null);
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

  const leave = useCallback((): Promise<boolean> => {
    const generation = ++attemptGenerationRef.current;
    // Stops the presence heartbeat before anything else — synchronously,
    // not via setStatus()/setCallId() and a future render. Those only take
    // effect once this leave() resolves, and the heartbeat interval keeps
    // firing for that entire round trip otherwise: a tick landing after
    // call.leave is sent but before the server processes it would resend
    // call.presence and resurrect the lease call.leave just released.
    stopHeartbeatRef.current?.();
    // This leave() is about to run its own stopMedia() below regardless of
    // outcome, so any older attempt's "connecting" marker is moot the
    // instant this generation bump supersedes it — never left dangling for
    // convergeRemoteLeave to misread later as "still connecting".
    connectingGenerationRef.current = null;
    // Only a call that actually reached the server (a callId is known) has
    // anything server-side to release; an abandoned in-flight join never
    // created a lease, so there is nothing to send — the generation bump
    // above already invalidates that attempt's continuation.
    const leavingCallId = callIdRef.current;
    const leavingParticipationId = participationIdRef.current;
    setError(null);
    setErrorOperation(null);
    const mediaCleanup = stopMedia();
    const serverRelease =
      leavingCallId && leavingParticipationId
        ? leaveResourceCall(leavingCallId, leavingParticipationId).then(({ released }) => released)
        : Promise.resolve(false);
    return Promise.all([mediaCleanup, serverRelease]).then(
      ([, released]) => {
        // A newer join()/leave() may already have moved past this attempt;
        // only the leave() that is still current gets to clear the room.
        if (attemptGenerationRef.current !== generation) return false;
        activeRef.current = null;
        joinPromiseRef.current = null;
        callIdRef.current = null;
        participationIdRef.current = null;
        setActive(null);
        setCallId(null);
        setStatus("idle");
        setErrorOperation(null);
        return released;
      },
      (leaveError: unknown) => {
        if (attemptGenerationRef.current === generation) {
          setStatus("error");
          setErrorOperation("leave");
          setError("Não foi possível sair da chamada. Tente novamente.");
        }
        throw leaveError;
      },
    );
  }, [stopMedia]);

  const join = useCallback(
    (
      target: ResourceCallTarget,
      onCallIdResolved?: (callId: string) => void,
    ): Promise<string | undefined> => {
      const previousActive = activeRef.current;
      const previousCallId = callIdRef.current;
      const previousParticipationId = participationIdRef.current;
      if (
        previousActive &&
        cleanupPromiseRef.current === null &&
        (previousActive.kind !== target.kind || previousActive.id !== target.id)
      ) {
        return Promise.resolve(undefined);
      }
      const pending = joinPromiseRef.current;
      if (
        pending &&
        pending.generation === attemptGenerationRef.current &&
        pending.target.kind === target.kind &&
        pending.target.id === target.id
      ) {
        // The already-in-flight attempt this dedups into may already have
        // resolved its own callId (synchronously, before its next await) —
        // this new caller's own callback still deserves that fact.
        if (callIdRef.current) onCallIdResolved?.(callIdRef.current);
        return pending.promise;
      }
      const generation = ++attemptGenerationRef.current;
      activeRef.current = target;
      setActive(target);
      setStatus("connecting");
      // Marks this attempt as the one THIS callId's convergeRemoteLeave, if it
      // fires mid-flight, is responsible for tearing media down for — see the
      // ref's own comment above for why a state-based `status === "connecting"`
      // read would be too stale/coarse for that decision.
      connectingGenerationRef.current = generation;
      setError(null);
      setErrorOperation(null);
      // Set only once THIS attempt's own admission (call.start/call.join)
      // has actually succeeded — distinguishes a post-admission failure
      // (issue #622 round 2: a lease now exists and callId/active were
      // already moved to it, so a rollback is required) from a pre-admission
      // one (busy or otherwise — nothing was ever admitted, so active/callId
      // were never touched by this attempt and the existing pre-#622
      // behavior applies unchanged).
      let admittedThisAttempt = false;
      const attempt = (async () => {
        // Never request a new Room before a pending leave()/unmount has
        // actually finished releasing the previous one — and never after
        // one that failed either: its rejection propagates from here.
        const cleanup = cleanupPromiseRef.current;
        if (cleanup) await cleanup;
        if (attemptGenerationRef.current !== generation) return;
        await mediaRef.current.startAudio();
        if (attemptGenerationRef.current !== generation) return;
        // A known call_id is protected (CallSessionProvider's
        // pendingJoinAttemptsRef) synchronously, before any await — the
        // callback must fire here, before call.join's own round trip, not
        // after: the call_id is already known, so there is no reason to
        // wait for the server to echo it back (issue #622 round 2).
        if (target.callId) onCallIdResolved?.(target.callId);
        // A known call_id is obligatory admission via call.join — it is
        // never synthesised locally: only call.admitted actually creates the
        // participant lease media-service's token now requires (issue
        // #622 round 2). A fresh target (no call_id yet) still goes through
        // call.start, which creates-or-reuses the canonical call AND admits
        // this actor in one round trip.
        const admission = target.callId
          ? await joinResourceCall(target.callId, target)
          : await startResourceCall(target);
        const { call, participationId } = admission;
        // Admission succeeded: a participant lease now exists server-side
        // for call.call_id, regardless of whether this attempt is still the
        // one this hook is tracking.
        if (attemptGenerationRef.current !== generation) {
          // Superseded while the admission round trip was in flight: no
          // other attempt observed or will ever release this lease, so this
          // attempt alone must (issue #622 round 2, post-admission
          // compensation) — best-effort; nothing left here to report to.
          void leaveResourceCall(call.call_id, participationId).catch(() => undefined);
          return;
        }
        setCallId(call.call_id);
        // Written synchronously in this same tick as setCallId() above — see
        // callIdRef's own comment for why the React state alone isn't enough.
        callIdRef.current = call.call_id;
        participationIdRef.current = participationId;
        admittedThisAttempt = true;
        // Fresh-target case: call_id was not known before admission, so this
        // is the first (and only) time onCallIdResolved fires for it.
        if (!target.callId) onCallIdResolved?.(call.call_id);
        try {
          const result = await issueCallToken(call.call_id, participationId);
          if (attemptGenerationRef.current !== generation) {
            void leaveResourceCall(call.call_id, participationId).catch(() => undefined);
            return;
          }
          // RF-24 follow-up: a resource room is one call, camera and
          // microphone are just controls within it — "audio"/"video" is not
          // a concept of this room. "audio" is passed only because
          // useCallMedia.connect() still uses call_type internally to
          // decide whether to auto-enable the camera (kept for RF-23
          // compatibility); the camera must never turn on by itself here,
          // so this stays "audio" unconditionally and the user enables the
          // camera explicitly via toggleCamera().
          await mediaRef.current.connect(
            { call_id: call.call_id, call_type: "audio" },
            result.token,
            result.serverUrl,
          );
          if (attemptGenerationRef.current !== generation) {
            void leaveResourceCall(call.call_id, participationId).catch(() => undefined);
            return undefined;
          }
          if (connectingGenerationRef.current === generation)
            connectingGenerationRef.current = null;
          setStatus("active");
          return call.call_id;
        } catch (postAdmissionError) {
          // Token issuance or media connect failed AFTER admission already
          // created a lease (issue #622 round 2): that lease must never be
          // left orphaned. Only this still-current attempt may compensate —
          // a superseded one would risk releasing a lease a newer attempt
          // (a rejoin of the very same call_id) has since made live again,
          // since a lease is keyed by (user, call), not by frontend attempt.
          if (attemptGenerationRef.current === generation) {
            try {
              await leaveResourceCall(call.call_id, participationId);
            } catch (compensationError) {
              throw new ResourceJoinCompensationFailedError(
                call.call_id,
                participationId,
                compensationError,
              );
            }
          }
          throw postAdmissionError;
        }
      })().catch((joinError: unknown): string | undefined => {
        if (joinError instanceof ResourceJoinCompensationFailedError) {
          // Compensation itself failed: never claim idle while the lease
          // might still be live. Preserve callId/active so the existing
          // "Tentar novamente" affordance retries leave() (idempotent)
          // against the right call, never re-joins.
          if (attemptGenerationRef.current === generation) {
            activeRef.current = target;
            callIdRef.current = joinError.callId;
            participationIdRef.current = joinError.participationId;
            setActive(target);
            setCallId(joinError.callId);
            setStatus("error");
            setErrorOperation("leave");
            setError("Não foi possível encerrar a participação anterior. Tente novamente.");
          }
          return undefined;
        }
        if (attemptGenerationRef.current !== generation) return undefined;
        if (connectingGenerationRef.current === generation) connectingGenerationRef.current = null;
        if (
          joinError instanceof ResourceCallSignalingError &&
          joinError.code === "call_participant_busy"
        ) {
          activeRef.current = previousActive;
          callIdRef.current = previousCallId;
          participationIdRef.current = previousParticipationId;
          setActive(previousActive);
          setCallId(previousCallId);
          setStatus(previousCallId ? "active" : "error");
          setErrorOperation("join");
          setError("Você já está em outra chamada.");
          return undefined;
        }
        if (admittedThisAttempt) {
          // Post-admission failure, compensation already succeeded (the
          // ResourceJoinCompensationFailedError branch above handles the
          // failed-compensation case separately): the lease this attempt
          // created is gone, so active/callId must roll back to whatever
          // this attempt actually had before it started — never left
          // pointing at a lease that no longer exists (issue #622 round 2).
          activeRef.current = previousActive;
          callIdRef.current = previousCallId;
          participationIdRef.current = previousParticipationId;
          setActive(previousActive);
          setCallId(previousCallId);
        }
        // A pre-admission failure never touched active/callId in the first
        // place — unchanged from before #622, active stays this attempt's
        // target so its own retry affordance still targets it.
        setStatus("error");
        setErrorOperation("join");
        setError("Não foi possível entrar na chamada.");
        return undefined;
      });
      joinPromiseRef.current = { generation, target, promise: attempt };
      void attempt.finally(() => {
        if (joinPromiseRef.current?.promise === attempt) joinPromiseRef.current = null;
      });
      return attempt;
    },
    [],
  );

  const convergeRemoteLeave = useCallback(
    (leftCallId: string) => {
      // callIdRef, never the `callId` state closure — see its own comment
      // above for the render-timing race this avoids.
      if (callIdRef.current !== leftCallId) return;
      attemptGenerationRef.current += 1;
      stopHeartbeatRef.current?.();
      activeRef.current = null;
      joinPromiseRef.current = null;
      callIdRef.current = null;
      participationIdRef.current = null;
      // Only ever stop the shared media bridge here when THIS hook's own
      // join()/reconnect() genuinely still has a connect in flight for the
      // participation being converged — never unconditionally: a merely
      // stale `active`/`callId` (the common #594 case, left over from a
      // handoff whose media was already stopped elsewhere) must not risk
      // reaching into a media bridge that could, by now, be servicing an
      // entirely unrelated, currently-active direct 1:1 call instead.
      const hadPendingConnect = connectingGenerationRef.current !== null;
      connectingGenerationRef.current = null;
      setActive(null);
      setCallId(null);
      setStatus("idle");
      setError(null);
      setErrorOperation(null);
      // Safe even while media.connect() is still awaiting the server: the
      // underlying LiveKitSpikeSession.connect() checks its own `disposed`
      // flag (set synchronously by disconnect(), before any await) right
      // after its room.connect() await resolves, and self-disconnects
      // instead of completing — and useCallMedia.connect()'s own
      // sessionRef/generationRef check independently stops it from ever
      // reporting "connected". Never lets a connect that started before
      // this convergence end up left running in the background.
      if (hadPendingConnect) void stopMedia().catch(() => undefined);
    },
    [stopMedia],
  );

  const reconnect = useCallback(async (): Promise<void> => {
    const target = activeRef.current;
    const knownCallId = callIdRef.current;
    if (!target || !knownCallId) return;
    const generation = ++attemptGenerationRef.current;
    setStatus("connecting");
    connectingGenerationRef.current = generation;
    setError(null);
    setErrorOperation(null);
    let admission: Awaited<ReturnType<typeof joinResourceCall>> | null = null;
    try {
      await stopMedia();
      if (attemptGenerationRef.current !== generation) return;
      await mediaRef.current.startAudio();
      if (attemptGenerationRef.current !== generation) return;
      admission = await joinResourceCall(knownCallId, target);
      const { call, participationId } = admission;
      if (attemptGenerationRef.current !== generation) {
        void leaveResourceCall(call.call_id, participationId).catch(() => undefined);
        return;
      }
      callIdRef.current = call.call_id;
      participationIdRef.current = participationId;
      setCallId(call.call_id);
      const result = await issueCallToken(call.call_id, participationId);
      if (attemptGenerationRef.current !== generation) {
        void leaveResourceCall(call.call_id, participationId).catch(() => undefined);
        return;
      }
      await mediaRef.current.connect(
        { call_id: call.call_id, call_type: "audio" },
        result.token,
        result.serverUrl,
      );
      if (attemptGenerationRef.current !== generation) {
        void leaveResourceCall(call.call_id, participationId).catch(() => undefined);
        return;
      }
      if (attemptGenerationRef.current === generation) {
        if (connectingGenerationRef.current === generation) connectingGenerationRef.current = null;
        setStatus("active");
      }
    } catch (reconnectError) {
      if (attemptGenerationRef.current !== generation) return;
      if (admission) {
        try {
          await leaveResourceCall(admission.call.call_id, admission.participationId);
        } catch (compensationError) {
          if (connectingGenerationRef.current === generation)
            connectingGenerationRef.current = null;
          setStatus("error");
          setErrorOperation("leave");
          setError("Não foi possível encerrar a participação anterior. Tente novamente.");
          throw new ResourceJoinCompensationFailedError(
            admission.call.call_id,
            admission.participationId,
            compensationError,
          );
        }
        if (attemptGenerationRef.current !== generation) return;
        participationIdRef.current = null;
      }
      if (connectingGenerationRef.current === generation) connectingGenerationRef.current = null;
      setStatus("error");
      setErrorOperation("join");
      setError("Não foi possível recuperar a chamada.");
      throw reconnectError;
    }
  }, [stopMedia]);

  useEffect(
    () => () => {
      // Invalidates any in-flight join()/leave() attempt so its continuation
      // never touches state after this point.
      attemptGenerationRef.current += 1;
      activeRef.current = null;
      participationIdRef.current = null;
      // Best-effort on unmount: nothing left to show a recoverable error to.
      void stopMedia().catch(() => undefined);
    },
    [stopMedia],
  );

  useEffect(() => {
    const participationId = participationIdRef.current;
    if (!presenceEnabled || status !== "active" || !callId || !participationId) return;
    const sendPresence = () =>
      handle.send({
        type: "call.presence",
        call_id: callId,
        participation_id: participationId,
      });
    const handle = acquireChatSocket({
      onOpen: sendPresence,
      onMessage(data) {
        if (
          data["type"] !== "call.error" ||
          data["operation"] !== "call.presence" ||
          data["call_id"] !== callId ||
          data["code"] !== "call_participation_stale"
        ) {
          return;
        }
        attemptGenerationRef.current += 1;
        stopHeartbeatRef.current?.();
        activeRef.current = null;
        callIdRef.current = null;
        participationIdRef.current = null;
        setActive(null);
        setCallId(null);
        setStatus("idle");
        void stopMedia().catch(() => undefined);
      },
    });
    sendPresence();
    const heartbeat = window.setInterval(sendPresence, 10_000);
    // Idempotent and reusable as both leave()'s synchronous stop and React's
    // own cleanup: whichever runs first tears the heartbeat down, the other
    // finds stopHeartbeatRef already cleared and no-ops.
    const stop = () => {
      window.clearInterval(heartbeat);
      handle.release();
      if (stopHeartbeatRef.current === stop) stopHeartbeatRef.current = null;
    };
    stopHeartbeatRef.current = stop;
    return stop;
  }, [callId, presenceEnabled, status, stopMedia]);

  return {
    active,
    callId,
    status,
    error,
    errorOperation,
    join,
    reconnect,
    leave,
    convergeRemoteLeave,
  };
}
