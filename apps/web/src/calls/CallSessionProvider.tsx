import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Outlet, useLocation } from "react-router";

import {
  acquireChatSocket,
  releaseConsumerSubscriptions,
  setConsumerSubscriptions,
} from "../chat/chatSocket";
import { parseCallEvent, type Call } from "../chat/callState";
import type { ResourceCallKind } from "../chat/callApi";
import {
  convergeResourceCallEvent,
  convergeResourceSyncNull,
  emptyResourceCallDiscovery,
  pruneResourceCallDiscovery,
  resourceKey,
  type ResourceCallDiscoveryState,
  type ResourceKey,
} from "../chat/resourceCallDiscovery";
import { resolveCall, syncResourceCall } from "../chat/resourceCallSignaling";
import type { Channel, DMConversation } from "../chat/chatTypes";
import { initialsFrom, localParticipantDisplayName } from "../chat/messageDisplay";
import { useSelfProfile } from "../profile/selfProfile";
import { useCallMedia, type CallMediaSessionController } from "../chat/useCallMedia";
import {
  useCallSignaling,
  type CallController,
  type CallMediaBridge,
} from "../chat/useCallSignaling";
import {
  useResourceCallSession,
  type ResourceCallController,
  type ResourceCallTarget,
} from "../chat/useResourceCallSession";
import {
  compareParticipationTokens,
  createOwnershipCoordinator,
  type MediaIntent,
  type OwnershipCoordinator,
  type OwnershipMessage,
  type ParticipationMessageType,
  type ParticipationToken,
} from "./callOwnership";
import { initialPresentation, transition, type PresentationState } from "./callPresentation";
import { emitCallTechnicalEvent } from "./callTelemetry";
import FloatingCallWindow, { type FloatingActiveSpeaker } from "./FloatingCallWindow";
import GlobalCallIndicator from "./GlobalCallIndicator";
import IncomingCallPopup from "./IncomingCallPopup";
import {
  getIncomingCallRingtoneEnabled,
  startIncomingCallRingtone,
  stopIncomingCallRingtone,
} from "./incomingCallRingtone";
import OutgoingCallPopup from "./OutgoingCallPopup";

type OwnerState = "none" | "local" | "remote";

export interface CallDirectory {
  currentUserId: string;
  channels: Channel[];
  dms: DMConversation[];
}

export interface CallSessionContextValue {
  media: CallMediaSessionController;
  calls: CallController;
  resource: ResourceCallController;
  ownerState: OwnerState;
  presentation: PresentationState;
  // True once a dedicated tab (reload, direct open, or an unresponsive
  // owner) has exhausted its recovery attempt without ever reaching local
  // ownership. Resets automatically once ownership does go local.
  dedicatedRecoveryFailed: boolean;
  enableMedia: () => void;
  registerDirectory: (directory: CallDirectory) => void;
  registerIdentity: (status: "loading" | "ready" | "error", retry: () => Promise<void>) => void;
  expand: () => boolean;
  takeOver: () => Promise<boolean>;
  announceDedicated: (callId: string) => void;
  acknowledgeDedicated: (callId: string, connected: boolean) => void;
  releaseDedicated: (callId: string) => Promise<void>;
  releaseDirectTerminal: (callId: string) => Promise<void>;
  /**
   * The dedicated tab's actual exit from a resource/group call (issue
   * #570) — never for RF-23 direct calls, which keep using calls.end().
   * Runs the real participant leave (#569), releases the dedicated
   * ownership lease, and tells every other tab this participation is over
   * so none of them ever reconnect it. Distinct from releaseDedicated,
   * which only moves ownership (minimize) and must leave participation
   * untouched.
   */
  leaveDedicated: (callId: string) => Promise<void>;
  /**
   * The local user's call-presentation identity (issue #612), reused from
   * the shared session-scoped profile cache — never a second GET /auth/me.
   * Exposed on the context so a view-scoped surface (e.g. #642's
   * ActiveResourceCallBar, rendered by ChatMessageArea/ChatShell) can show
   * the local avatar/name without its own profile fetch.
   */
  localIdentity: { name: string; initials: string; avatarUrl?: string };
  /**
   * Minimal, view-scoped exit for a resource-call participation (issue
   * #642) — reuses endResourceParticipation exactly as leaveDedicated
   * already does, never resource.leave() directly, so the same
   * leaving/left cross-tab convergence and ownership semantics apply
   * regardless of whether the user left via the dedicated tab, the
   * FloatingCallWindow, or this.
   */
  leaveResourceParticipation: () => Promise<void>;
  /**
   * The single derived authority for "this resource participation is
   * stable enough to present the compact local-control mode in
   * ActiveResourceCallBar" — null in every other state (connecting,
   * reconnecting, error, remote ownership, a stale/mismatched discovery
   * call_id, or a participation already leaving/left). Non-null carries
   * the exact, proven Call this tab is participating in, so a view-scoped
   * consumer (ChatShell) derives callId/startedAt from the SAME validated
   * source. The bar COEXISTS with FloatingCallWindow — this authority
   * never suppresses or replaces the floating window (#657).
   */
  resourcePresentationCall: Call | null;
  /**
   * The direct-call counterpart of resourcePresentationCall (issue #673) —
   * null in every state except a genuinely active, media-connected,
   * locally-owned 1:1 call. Non-null carries the exact, proven Call
   * (useCallSignaling's own calls.call) this tab is participating in, so a
   * view-scoped consumer (ChatShell's directCallSession, mirroring
   * resourceCallSession) derives callId/callType/peer identity/startedAt
   * from the SAME validated source rather than re-deriving its own guess.
   */
  directPresentationCall: Call | null;
  /**
   * Registers a fresh resource-call participation for callId — the causal
   * counterpart to leaveDedicated/endResourceParticipation (issue #570
   * follow-up). Must be called once, right after resource.join() itself
   * resolves with a call_id, by every real call site of resource.join() —
   * never inferred from resource.callId changing, since a legitimate rejoin
   * of the very same resource call resolves to the very same callId string
   * and produces no observable state transition to key an effect off.
   * Resolves once the generation has actually been allocated from the
   * shared, cross-tab ownership storage — never a locally computed one, so
   * a brand-new or reloaded tab always outranks whatever any other tab
   * still remembers for this callId.
   */
  beginResourceParticipation: (callId: string) => Promise<void>;
  /**
   * The one real call site every FRESH, user-initiated resource join/rejoin
   * must go through — ChatMessageArea's "Chamada"/"Entrar na chamada"
   * actions (issue #594 adversarial follow-up, rounds 2-3; issue #622 round
   * 2 replaced the old resource IncomingCallPopup click with these).
   * Wraps resource.join() with a LOCAL-ONLY, causal "this callId has a
   * fresh attempt in flight" marker, registered synchronously BEFORE join()
   * starts — even when target.callId is still undefined at that instant
   * (the server resolves/reuses it), via join()'s own onCallIdResolved
   * callback — and cleared once join() settles either way. Closes the
   * window between join() starting and beginResourceParticipation()
   * registering the real, higher-ranked ParticipationToken, during which
   * the OLD participation this rejoin is superseding can still have its own
   * (real, legitimately accepted) "left" arrive — without this marker, that
   * "left" would still look "current" to commitParticipation's ordering and
   * would wrongly converge/abort this brand-new, not-yet-registered
   * attempt. Never itself posts anything to other tabs or writes to shared
   * storage — that stays exclusively beginResourceParticipation's job, run
   * only once join() actually confirms success, so a still-failable attempt
   * is never announced as a real participation. NEVER the right call for
   * DedicatedCallPage's own handoff/continuation join — see
   * activateResourceParticipation below.
   */
  joinResourceParticipation: (target: ResourceCallTarget) => Promise<string | undefined>;
  /**
   * DedicatedCallPage/handoff's own entry point (issue #594 adversarial
   * follow-up, round 3) — same join()+beginResourceParticipation shape as
   * joinResourceParticipation, but deliberately never registers the
   * fresh-join-intent marker. DedicatedCallPage's activation effect runs on
   * EVERY successful ownerState -> "local" transition, including a plain
   * handoff continuation reconnecting media for an ALREADY-active
   * participation (issue #570 problem 3) — treating that as a fresh intent
   * would risk masking a real, currently-accepted "left" for the very
   * participation being reconnected to, which is strictly worse than the
   * narrower, deliberately-unprotected race this leaves open.
   */
  activateResourceParticipation: (target: ResourceCallTarget) => Promise<string | undefined>;
  /**
   * Discovery, never participation (issue #622 round 2) — the authoritative
   * "is there an active call here" for one channel/group-DM target, derived
   * purely from lifecycle broadcasts and call.resource.sync answers this
   * provider has converged. Returns null for "no active call" AND for a
   * target this tab has simply never observed yet — the same value; a
   * caller that needs to distinguish "confirmed idle" from "not checked yet"
   * should call requestResourceCallSync itself and await the next render.
   * Never reflects whether the CURRENT USER is participating — that is
   * resource.active/resource.callId, a completely different question.
   */
  getResourceCall: (kind: ResourceCallKind, id: string) => Call | null;
  /**
   * Explicitly (re)requests call.resource.sync for one target and converges
   * the result into discovery — e.g. after a known-call join fails (issue
   * #622 round 2 section 17), so the indicator reflects whatever the server
   * now says instead of the stale pre-failure guess. Fire-and-forget by
   * design: callers read the outcome back through getResourceCall on the
   * next render, never through this function's own return value.
   */
  requestResourceCallSync: (kind: ResourceCallKind, id: string) => void;
}

const CallSessionContext = createContext<CallSessionContextValue | null>(null);
const terminal = new Set(["declined", "cancelled", "timed_out", "ended"]);
// Bounded wait for a live main tab to answer a dedicated tab's "ready"
// announcement with a "handoff" before the dedicated tab claims ownership
// itself (reload, direct open, or an unresponsive/dead owner).
const DEDICATED_READY_TIMEOUT_MS = 5_000;

// eslint-disable-next-line react-refresh/only-export-components
export function useCallSession(): CallSessionContextValue {
  const value = useContext(CallSessionContext);
  if (!value) throw new Error("useCallSession must be used within CallSessionProvider");
  return value;
}

export default function CallSessionProvider({ children }: { children?: ReactNode }) {
  const location = useLocation();
  const dedicated = location.pathname.startsWith("/call/");
  const role = dedicated ? "dedicated" : "main";
  const media = useCallMedia();
  // Local call-presentation identity (issue #612), reused from the shared
  // session-scoped profile cache — never a second GET /auth/me just for calls.
  const selfProfile = useSelfProfile();
  const selfDisplayName = selfProfile.status === "ready" ? selfProfile.profile.displayName : "";
  const selfAvatarUrl = selfProfile.status === "ready" ? selfProfile.profile.avatarUrl : undefined;
  const localName = localParticipantDisplayName(selfDisplayName);
  // Initials from the raw name, never the "(você)"-suffixed label (issue
  // #612 blocker) — see DedicatedCallPage's identical derivation. Empty/
  // loading falls back to "Você", never "?".
  const localInitials = initialsFrom(selfDisplayName || "Você");
  const [mediaEnabled, setMediaEnabled] = useState(false);
  const [ownerState, setOwnerState] = useState<OwnerState>("none");
  const ownerStateRef = useRef<OwnerState>("none");
  const [dedicatedRecoveryFailed, setDedicatedRecoveryFailed] = useState(false);
  const mediaRef = useRef(media);
  const [presentation, dispatchPresentation] = useReducer(transition, initialPresentation);
  const [directory, setDirectory] = useState<CallDirectory | null>(null);
  const [identity, setIdentity] = useState<{
    status: "loading" | "ready" | "error";
    retry?: () => Promise<void>;
  }>({ status: "loading" });
  // Discovery store (issue #622 round 2) — keyed observations of whether a
  // channel/group-DM has an active call, entirely separate from
  // participation. Populated by the global resource-events subscription
  // below (every channel + group DM the directory knows about, regardless
  // of whether this user is busy elsewhere — #609 only ever blocks this
  // user's own join/start, never their ability to see that a call exists)
  // and by explicit call.resource.sync answers (route-scoped rediscovery,
  // reconnect resync, and post-join-failure reconciliation).
  const [resourceCallDiscovery, setResourceCallDiscovery] = useState<ResourceCallDiscoveryState>(
    emptyResourceCallDiscovery,
  );
  const applyResourceCallEvent = useCallback((kind: ResourceCallKind, id: string, call: Call) => {
    setResourceCallDiscovery((current) => convergeResourceCallEvent(current, kind, id, call));
  }, []);
  const getResourceCall = useCallback(
    (kind: ResourceCallKind, id: string): Call | null =>
      resourceCallDiscovery.get(resourceKey(kind, id))?.call ?? null,
    [resourceCallDiscovery],
  );
  // At most one in-flight call.resource.sync per target at a time — a
  // duplicate trigger (route effect + reconnect + failure-reconciliation all
  // landing close together) converges into the same outcome regardless, so
  // there is nothing to gain from a second concurrent round trip.
  const syncingResourceCallsRef = useRef<Set<ResourceKey>>(new Set());
  const requestResourceCallSync = useCallback((kind: ResourceCallKind, id: string) => {
    const key = resourceKey(kind, id);
    if (syncingResourceCallsRef.current.has(key)) return;
    syncingResourceCallsRef.current.add(key);
    syncResourceCall({ kind, id }).then(
      (result) => {
        syncingResourceCallsRef.current.delete(key);
        setResourceCallDiscovery((current) =>
          result.call
            ? convergeResourceCallEvent(current, kind, id, result.call)
            : convergeResourceSyncNull(current, kind, id, result.observedAt),
        );
      },
      () => {
        syncingResourceCallsRef.current.delete(key);
      },
    );
  }, []);
  // The channel/group-DM the current route is showing, if any — direct DMs
  // are deliberately excluded (issue #622 round 2 section 5: "direct DM não
  // faz resource sync"). Read inside the reconnect handler below via a ref,
  // never the state closure, since that handler is registered once and must
  // still see whichever route is current at the moment the socket reopens.
  const routeResourceMatch = /^\/chat\/(channel|dm)\/([^/]+)/.exec(location.pathname);
  const routeResourceTarget = useMemo(() => {
    if (!routeResourceMatch || !directory) return null;
    const id = decodeURIComponent(routeResourceMatch[2]);
    if (routeResourceMatch[1] === "channel") {
      return directory.channels.some((channel) => channel.id === id)
        ? { kind: "channel" as const, id }
        : null;
    }
    const dm = directory.dms.find((candidate) => candidate.id === id);
    return dm?.type === "group" ? { kind: "dm" as const, id } : null;
  }, [directory, routeResourceMatch]);
  const routeResourceTargetRef = useRef(routeResourceTarget);
  useEffect(() => {
    routeResourceTargetRef.current = routeResourceTarget;
  }, [routeResourceTarget]);
  // Rediscovery on navigation (issue #622 round 2 section 5): every time the
  // route settles on a different channel/group-DM, resync it once — realtime
  // discovery for every OTHER known target still comes from the global
  // broadcast subscription below, this is purely to recover from whatever
  // this tab missed while the target wasn't open (or was never open before).
  useEffect(() => {
    if (routeResourceTarget?.kind && routeResourceTarget.id) {
      requestResourceCallSync(routeResourceTarget.kind, routeResourceTarget.id);
    }
  }, [routeResourceTarget?.kind, routeResourceTarget?.id, requestResourceCallSync]);
  const handoffTimer = useRef<number | null>(null);
  const handoffCall = useRef("");
  const handoffEpoch = useRef(0);
  // User/recovery mic+camera intent (issue #610) — never SDK state. Updated
  // only after a durable writeMediaIntent succeeds, following either a
  // successful ownedMedia.connect or a confirmed user toggle (see
  // wrappedToggleMicrophone/wrappedToggleCamera below). Never cleared on a
  // handoff stop/release, and never touched by server mute, SDK reconnect,
  // or MediaState effects — only replaced when callId genuinely changes.
  const confirmedIntentRef = useRef<{
    callId: string;
    microphone: boolean;
    camera: boolean;
  } | null>(null);
  const reportedMediaCleanupCallRef = useRef("");
  // Which call_id ownedMedia.stop() belongs to, for when NEITHER of stop's
  // two normal sources still knows: getLease() can already be cleared (a
  // connect() failure/supersession releases the lease from inside its own
  // catch, synchronously, before stop() ever runs) and confirmedIntentRef is
  // never set until AFTER a connect fully succeeds — so a terminal event
  // racing a still-in-flight or just-failed connect can reach stop() with
  // both empty. Set at the very start of connect(), before anything that
  // could fail or be superseded, so it survives exactly the failure modes
  // those two don't; never cleared, since a call_id is a UUID never reused
  // (same accepted trade-off as everConnectedRef in useCallSignaling.ts) —
  // only ever overwritten by a later call's own connect(). Without this,
  // ownedMedia.stop() silently fell back to "", and releaseDirectTerminal's
  // own `reportedMediaCleanupCallRef.current !== callId` dedup could never
  // match, double-reporting a track-cleanup that only happened once.
  const stopFallbackCallIdRef = useRef("");
  // The dedicated tab's epoch the current outstanding handoff attempt
  // expects back in an "ack"/"failure" reply. Cleared whenever the attempt
  // reaches a terminal state (ack, failure, or timeout) so a late reply from
  // an abandoned attempt can never be mistaken for the current one.
  const expectedAckEpoch = useRef<number | null>(null);
  const recovery = useRef<Promise<boolean> | null>(null);
  const dedicatedReadyTimer = useRef<number | null>(null);
  // Per-callId participation lifecycle (issue #570 follow-up). "leaving"/
  // "left" block reconnect the same way the old permanent
  // endedResourceCallIds Set did — but scoped to one participation attempt,
  // not the callId forever: a resource call's server-side row is long-lived
  // and reused by every participant who joins it, so callId alone can't
  // mean "never reconnectable again" — a later, legitimate rejoin of the
  // very same callId must work.
  //
  // Ordered by (ParticipationToken, sequence), never wall-clock time — two
  // tabs can legitimately produce identical Date.now() values, and delivery
  // order is not causal order. The token's generation increments once per
  // fresh join of a callId, so a message from an old, already-superseded
  // participation can never outrank a newer one no matter how it collides or
  // reorders in transit; when two tabs race to the SAME generation without a
  // real cross-process lock (allocateParticipationGeneration is best-effort,
  // not a true compare-and-swap — see callOwnership.ts), the token's
  // writerId is what still makes them distinguishable and consistently
  // orderable across every tab, via compareParticipationTokens. `sequence`
  // orders leaving/left/leave-cancelled within one already-agreed-upon
  // participation's own lifecycle. A handoff/minimize never touches this map
  // at all: resource.callId staying set after a handoff is exactly what lets
  // a legitimate later reclaim reconnect, and this map only ever gates that
  // reclaim for a callId whose CURRENT participation attempt is actually
  // leaving/left.
  //
  // Two views of the SAME data, deliberately kept separate (react-hooks/refs
  // forbids reading a ref during render — see commitParticipation below and
  // resourceParticipationPhase further down):
  //
  // - participationRecordsRef is the synchronous authority the state
  //   machine itself computes from. commitParticipation, and everything
  //   that calls it (beginResourceParticipation/continueResourceParticipation),
  //   must read the true latest record even when invoked from a plain
  //   Promise continuation (resource.join() resolving) that is not itself
  //   inside any React-owned batch — a setState updater's "current"
  //   argument only reflects that latest value reliably when no other
  //   update on that same state is already pending, which is exactly the
  //   case a stale read here would silently under-count the next
  //   generation. Never read during render.
  // - participationRecords is a plain React state snapshot of the ref,
  //   updated in lockstep every time the ref changes — this is what render
  //   (resourceParticipationPhase below) and nothing else may read, and
  //   what actually triggers the re-render.
  type ParticipationRecords = Map<
    string,
    { phase: "participating" | "leaving" | "left"; token: ParticipationToken; sequence: number }
  >;
  const participationRecordsRef = useRef<ParticipationRecords>(new Map());
  // Local-only, causal "a fresh join for this callId is in flight and not
  // yet registered" markers (issue #594 adversarial follow-up, rounds 2-3) —
  // see joinResourceParticipation below, the only writer. Never posted to
  // other tabs, never written to shared storage: purely a same-tab guard so
  // the ownership-message effect can tell "this accepted left refers to the
  // OLD participation my new, still-unregistered attempt is superseding"
  // from "this accepted left genuinely applies to my current, already-
  // registered participation" — the latter must still converge normally. A
  // tab can never be the source of a `left` for a callId it hasn't yet
  // registered as participating in (endResourceParticipation only ever runs
  // against an already-`active` participation), so suppressing convergence
  // unconditionally while a callId's entry is present here is exhaustively
  // correct for this window, with no token comparison needed.
  //
  // Reference-counted (a callId -> how many overlapping fresh attempts are
  // currently protecting it), not a bare Set: two overlapping fresh attempts
  // for the same callId (e.g. a rapid double "Entrar" click before the
  // first one's own dedup in useResourceCallSession.join() has registered)
  // must not let the first one's finally-cleanup remove protection the
  // second one still needs.
  //
  // ONLY ever written by a genuinely fresh, user-initiated join/rejoin
  // gesture (ChatMessageArea's "Chamada"/"Entrar na chamada" actions) —
  // DedicatedCallPage's activation-effect join() deliberately
  // never writes here (see activateResourceParticipation below): it runs on
  // every successful ownerState -> "local" transition, including a plain
  // handoff continuation reconnecting media for an ALREADY-active
  // participation (issue #570 problem 3), and a dedicated tab has no
  // reliable local signal to distinguish that from a genuinely fresh
  // rejoin — its own participationRecordsRef starts empty even for a real
  // continuation, since it never observed the original "participating"
  // broadcast. Treating every DedicatedCallPage join() as a fresh intent
  // would risk suppressing a real, currently-accepted "left" for the very
  // participation it is simply reconnecting to — a worse failure than the
  // narrower, deliberately-unprotected race this leaves open.
  const pendingJoinAttemptsRef = useRef<Map<string, number>>(new Map());
  const protectJoinAttempt = useCallback((joinCallId: string) => {
    const map = pendingJoinAttemptsRef.current;
    map.set(joinCallId, (map.get(joinCallId) ?? 0) + 1);
  }, []);
  const unprotectJoinAttempt = useCallback((joinCallId: string) => {
    const map = pendingJoinAttemptsRef.current;
    const count = map.get(joinCallId) ?? 0;
    if (count <= 1) map.delete(joinCallId);
    else map.set(joinCallId, count - 1);
  }, []);
  // Independently initialized to an empty Map — never read from the ref
  // (react-hooks/refs forbids that during render, and this initializer runs
  // at render time on first mount) — the two simply start in the same
  // state the ref itself was just initialized to, and commitParticipation
  // keeps them in lockstep from here on.
  const [participationRecords, setParticipationRecords] = useState<ParticipationRecords>(
    () => new Map(),
  );
  // Applies a participation transition — from an incoming cross-tab message
  // (applyParticipationEvent) or a locally computed one
  // (beginResourceParticipation/continueResourceParticipation) — but only if
  // (token, sequence) is not behind whatever this tab already knows for that
  // callId. That ordering guard is what a bare Set, a naive delete-on-
  // rejoin, or a wall-clock comparison could never express deterministically.
  //
  // Purely local — never writes back to shared storage. Under the per-writer
  // storage keys allocateParticipationGeneration uses (issue #570 follow-up),
  // every writer's own token already persists durably under its own key the
  // moment it is allocated, and getParticipationToken derives "current" as
  // the max across all of them at read time — so there is nothing left for
  // a receiver to converge into storage; a convergence write would in fact
  // be unsafe here, since the only way to "record" a token this tab did not
  // allocate itself would be writing it under another writer's key,
  // destroying the exclusive-ownership property that makes the storage
  // layer race-free without Web Locks.
  const commitParticipation = useCallback(
    (
      callId: string,
      phase: "participating" | "leaving" | "left",
      token: ParticipationToken,
      sequence: number,
    ): boolean => {
      const existing = participationRecordsRef.current.get(callId);
      if (existing) {
        const cmp = compareParticipationTokens(token, existing.token);
        if (cmp < 0) return false;
        if (cmp === 0 && sequence <= existing.sequence) return false;
      }
      const next = new Map(participationRecordsRef.current);
      next.set(callId, { phase, token, sequence });
      participationRecordsRef.current = next;
      setParticipationRecords(next);
      return true;
    },
    [],
  );
  // Returns whether the event actually advanced callId's record (false for a
  // stale/superseded one — see the ordering guard above) so a caller like
  // the "left" handling below can tell a genuinely-applied confirmation from
  // one the ordering already rejected.
  const applyParticipationEvent = useCallback(
    (
      callId: string,
      phase: "participating" | "leaving" | "left",
      token: ParticipationToken,
      sequence: number,
    ): boolean => commitParticipation(callId, phase, token, sequence),
    [commitParticipation],
  );
  useEffect(() => {
    mediaRef.current = media;
  }, [media]);
  const setOwner = useCallback((next: OwnerState) => {
    ownerStateRef.current = next;
    setOwnerState(next);
    if (next === "local") setDedicatedRecoveryFailed(false);
  }, []);
  // Lifecycle lives on a ref, not useState: the coordinator is an imperative
  // resource (BroadcastChannel, storage listener, heartbeat, Web Locks), and
  // its lifetime must be driven by the effect below, not by render. Every
  // other reader goes through getOwnership() below — never through a
  // render-captured value — so nothing can hold a stale closure over an
  // instance the lifecycle effect has already closed.
  const coordinatorRef = useRef<OwnershipCoordinator | null>(null);
  const getOwnership = useCallback((): OwnershipCoordinator => {
    // Only a defensive fallback for a call that races ahead of the effect
    // below (none exists today: every caller is itself inside an effect or
    // a callback, both of which only run after it). Never overwrites a
    // still-open instance and never resurrects a closed one — that is
    // exclusively the lifecycle effect's job.
    return coordinatorRef.current ?? (coordinatorRef.current = createOwnershipCoordinator());
  }, []);

  // Owns the coordinator's create/close lifecycle end to end. Declared
  // before every other effect that touches ownership so it always finishes
  // setting up first (React runs effect setup in declaration order) —
  // including React StrictMode's dev-only mount -> cleanup -> mount probe,
  // which otherwise closes the instance kept in a useState/ref and leaves a
  // second, unrelated mount pass reusing that closed instance (CALLS-546).
  // The probe's cleanup here closes the coordinator; because a closed
  // coordinator must never be reused, this setup recreates a fresh one
  // whenever the current one is closed (the probe) or absent (the very
  // first real mount) — exactly one open instance at a time.
  useEffect(() => {
    if (!coordinatorRef.current || coordinatorRef.current.isClosed()) {
      coordinatorRef.current = createOwnershipCoordinator();
    }
    const instance = coordinatorRef.current;
    return () => instance.close();
  }, []);

  useEffect(
    () =>
      getOwnership().onOwnershipLost(() => {
        emitCallTechnicalEvent("duplicate-owner-detected");
        setOwner("remote");
        dispatchPresentation({ type: "OWNER_LOST" });
        void mediaRef.current.stop().then(() => emitCallTechnicalEvent("track-cleanup"));
      }),
    [getOwnership, setOwner],
  );

  // Storage-failure teardown shared by both toggle wrappers' CONFIRMED-write
  // failure branch (issue #610 privacy-blocker follow-up). By the time this
  // runs, a durable "pending" marker already protects recovery (see
  // wrappedToggleMicrophone/Camera below) regardless of whether ownership
  // is released — so, unlike the previous design, release is now always
  // safe and never conditional/withheld: no future reader can ever treat a
  // "pending" winner as a specific preset (readMediaIntentForLease always
  // maps it to OFF/OFF), so holding the lease hostage buys nothing.
  const stopAndReleaseOnStorageFailure = useCallback(
    async (callId: string) => {
      const ownership = getOwnership();
      await media.stop();
      emitCallTechnicalEvent("track-cleanup");
      ownership.release(callId);
      setOwner("none");
      emitCallTechnicalEvent("join-failure");
    },
    [getOwnership, media, setOwner],
  );

  const ownedMedia = useMemo<CallMediaBridge>(
    () => ({
      startAudio: media.startAudio,
      connect: async (call, token, serverUrl, mode) => {
        if (confirmedIntentRef.current && confirmedIntentRef.current.callId !== call.call_id) {
          confirmedIntentRef.current = null;
        }
        stopFallbackCallIdRef.current = call.call_id;
        const ownership = getOwnership();
        const current = ownership.getLease();
        const lease =
          current?.callId === call.call_id ? current : await ownership.claim(call.call_id, role);
        if (!lease) {
          setOwner("remote");
          throw new Error("call media is owned by another tab");
        }
        setOwner("local");
        // §16 absolute rule: recovery with no valid snapshot degrades to
        // OFF/OFF — never a call_type/active-call fallback. Fresh never
        // restores a historical snapshot as a preset (existing entry
        // defaults apply, handled inside media.connect itself).
        // readMediaIntentForLease (issue #610 audit) only ever resolves this
        // coordinator's own currently-held lease's PROVEN predecessor — a
        // same-epoch or otherwise-unproven writer (a transient concurrent
        // claimant, reachable without Web Locks) is never substituted in.
        const initialIntent: MediaIntent | undefined =
          mode === "recovery"
            ? (ownership.readMediaIntentForLease(call.call_id) ?? {
                microphone: false,
                camera: false,
              })
            : undefined;
        let applied: MediaIntent | undefined;
        try {
          applied = await media.connect(call, token, serverUrl, initialIntent);
        } catch (error) {
          emitCallTechnicalEvent("join-failure");
          ownership.release(call.call_id);
          setOwner("none");
          throw error;
        }
        if (!applied) {
          // Superseded before this attempt's connect settled — not a
          // failure of ownership/storage, just stale.
          emitCallTechnicalEvent("join-failure");
          ownership.release(call.call_id);
          setOwner("none");
          throw new Error("media connection was superseded before it completed");
        }
        // No write-ahead/pending step is needed here (issue #610 audit,
        // documented conclusion): a connect's applied snapshot is either a
        // fresh default (no prior confirmed baseline it could contradict)
        // or a re-persistence of the SAME value recovery just read back
        // from a causally-valid predecessor — never a NEW divergence from
        // a just-confirmed device change the way a mid-call user toggle
        // is. If this write fails, the previous predecessor entry (or
        // nothing) simply remains in place, which is still safe to read
        // later — so the plain §11 fail-closed teardown is sufficient.
        const wrote = ownership.writeMediaIntent(call.call_id, lease, applied, "confirmed");
        if (!wrote.ok) {
          await media.stop();
          emitCallTechnicalEvent("track-cleanup");
          emitCallTechnicalEvent("join-failure");
          ownership.release(call.call_id);
          setOwner("none");
          throw new Error("failed to persist media intent for handoff");
        }
        confirmedIntentRef.current = { callId: call.call_id, ...applied };
        reportedMediaCleanupCallRef.current = "";
        emitCallTechnicalEvent("join-success");
      },
      stop: async () => {
        const callId =
          getOwnership().getLease()?.callId ??
          confirmedIntentRef.current?.callId ??
          stopFallbackCallIdRef.current;
        await media.stop();
        emitCallTechnicalEvent("track-cleanup");
        reportedMediaCleanupCallRef.current = callId;
      },
    }),
    [getOwnership, media, role, setOwner],
  );

  // Write-ahead safety marker (issue #610 privacy-blocker follow-up): a
  // user toggle is the one moment a device can diverge from the last
  // DURABLE confirmed intent, so a "pending" entry describing the desired
  // change is written SYNCHRONOUSLY, fenced by the causal lease, BEFORE
  // the SDK is ever asked to touch the device. If that pre-write fails —
  // for any reason, including the lease no longer being valid — the SDK is
  // never called and the device stays exactly as last confirmed. Once the
  // SDK genuinely applies the change, a "confirmed" write settles it,
  // fenced additionally by the pending write's own revision so a stale,
  // superseded operation can never overwrite a newer one's entry. If the
  // SDK fails/is superseded, or the confirmed commit itself fails, the
  // already-durable "pending" marker is by itself sufficient: any future
  // recovery (readMediaIntentForLease) treats a winning "pending" entry as
  // OFF/OFF, never as a specific preset — safe with or without this tab's
  // OwnerLease surviving the same failure (issue #610 audit: heartbeat
  // writes use the same storage and can fail together with this write).
  const wrappedToggleMicrophone = useCallback(async (): Promise<boolean | undefined> => {
    const lease = getOwnership().getLease();
    const callId = lease?.callId;
    if (!lease || !callId) return undefined;
    const camera =
      confirmedIntentRef.current?.callId === callId
        ? confirmedIntentRef.current.camera
        : media.cameraEnabled;
    const desired: MediaIntent = { microphone: !media.microphoneEnabled, camera };
    const pending = getOwnership().writeMediaIntent(callId, lease, desired, "pending");
    if (!pending.ok) return undefined;

    const result = await media.toggleMicrophone();
    if (result === undefined) return undefined;

    const finalIntent: MediaIntent = { microphone: result, camera };
    const confirmed = getOwnership().writeMediaIntent(callId, lease, finalIntent, "confirmed", {
      expectedRevision: pending.revision,
    });
    if (confirmed.ok) {
      confirmedIntentRef.current = { callId, ...finalIntent };
    } else if (confirmed.reason === "storage-error") {
      await stopAndReleaseOnStorageFailure(callId);
    }
    // confirmed.reason === "stale" means a newer operation already
    // advanced the revision — that operation now owns confirming intent;
    // this one's own SDK result is still returned to the caller, but
    // nothing further is written or torn down here.
    return result;
  }, [getOwnership, media, stopAndReleaseOnStorageFailure]);

  const wrappedToggleCamera = useCallback(async (): Promise<boolean | undefined> => {
    const lease = getOwnership().getLease();
    const callId = lease?.callId;
    if (!lease || !callId) return undefined;
    const microphone =
      confirmedIntentRef.current?.callId === callId
        ? confirmedIntentRef.current.microphone
        : media.microphoneEnabled;
    const desired: MediaIntent = { microphone, camera: !media.cameraEnabled };
    const pending = getOwnership().writeMediaIntent(callId, lease, desired, "pending");
    if (!pending.ok) return undefined;

    const result = await media.toggleCamera();
    if (result === undefined) return undefined;

    const finalIntent: MediaIntent = { microphone, camera: result };
    const confirmed = getOwnership().writeMediaIntent(callId, lease, finalIntent, "confirmed", {
      expectedRevision: pending.revision,
    });
    if (confirmed.ok) {
      confirmedIntentRef.current = { callId, ...finalIntent };
    } else if (confirmed.reason === "storage-error") {
      await stopAndReleaseOnStorageFailure(callId);
    }
    return result;
  }, [getOwnership, media, stopAndReleaseOnStorageFailure]);

  const resource = useResourceCallSession(ownedMedia, ownerState === "local");
  // Pulled out as its own binding so the ownership-subscribe effect below
  // can depend on this specific, permanently-stable callback (issue #594
  // adversarial follow-up) instead of the whole `resource` object, which is
  // a brand-new object literal every render — depending on it directly
  // would unsubscribe/resubscribe the ownership coordinator on every single
  // render for no reason. (No message-loss risk either way: subscribe/
  // unsubscribe are synchronous Set mutations in callOwnership.ts, and a
  // cross-tab message only ever arrives via an async BroadcastChannel
  // event, so there is no window for one to land in between.)
  // resource.join is itself permanently stable (useResourceCallSession's own
  // useCallback has `[]` deps) — pulling it out the same way lets
  // joinResourceParticipation below depend on it directly too.
  const { convergeRemoteLeave, join: joinResourceCall, leave: leaveResourceSession } = resource;
  const directMedia = useMemo<CallMediaBridge>(
    () => ({
      startAudio: ownedMedia.startAudio,
      connect: ownedMedia.connect,
      stop: async () => {
        if (resource.active) return;
        await ownedMedia.stop();
      },
    }),
    [ownedMedia, resource.active],
  );
  const releaseResource = useCallback(async () => {
    if (resource.active) await resource.leave();
  }, [resource]);
  const calls = useCallSignaling(directMedia, mediaEnabled, releaseResource);
  const callsRef = useRef(calls);
  const terminalDirectRecoveryCallRef = useRef("");
  useEffect(() => {
    callsRef.current = calls;
  }, [calls]);
  const directCallBusy = calls.call !== null && !terminal.has(calls.call.status);

  const postParticipation = useCallback(
    (
      callId: string,
      type: ParticipationMessageType,
      token: ParticipationToken,
      sequence: number,
    ) => {
      const ownership = getOwnership();
      ownership.post({
        v: 1,
        type,
        callId,
        tabId: ownership.tabId,
        epoch: ownership.getLease()?.epoch ?? 0,
        generation: token.generation,
        writerId: token.writerId,
        sequence,
      });
    },
    [getOwnership],
  );
  // Registers a *causally real* new join — called only once resource.join()
  // itself resolves with the callId it actually joined (issue #570 follow-
  // up), never inferred from resource.callId changing: a legitimate rejoin
  // of the very same resource call resolves to the very same callId string,
  // and React never produces an observable state transition on an unchanged
  // primitive, so nothing watching resource.callId can ever detect it.
  //
  // The token is allocated from the ownership coordinator's SHARED,
  // persisted, per-callId storage (allocateParticipationGeneration) — never
  // computed from this tab's own local participationRecordsRef, which
  // starts EMPTY in a brand-new or just-reloaded tab. A local-only counter
  // can only ever be monotonic within one CallSessionProvider instance; a
  // fresh instance has no way to know what generation another tab (or its
  // own earlier life, before reload) already reached for THIS callId
  // specifically, and would restart at 1 — indistinguishable from, and
  // rejected as older than, a real prior participation another tab still
  // remembers. Handoff note (issue #570 problem 3): DedicatedCallPage's
  // activation effect calls resource.join() — and therefore this — on
  // EVERY successful activation, including a plain handoff continuation,
  // not only a genuinely fresh join. That is intentional: it never touches
  // "leaving"/"left" (those live exclusively in endResourceParticipation
  // below, reachable only via an explicit user "sair" action), so a handoff
  // can never make any tab believe a server-side leave/rejoin happened —
  // the phase stays "participating" throughout. The resulting generation
  // bump is a harmless, idempotent refresh of the "currently participating"
  // token, not a second real join.
  const beginResourceParticipation = useCallback(
    async (callId: string) => {
      const token = getOwnership().allocateParticipationGeneration(callId);
      commitParticipation(callId, "participating", token, 0);
      postParticipation(callId, "participating", token, 0);
    },
    [commitParticipation, getOwnership, postParticipation],
  );
  // The real call site every FRESH, user-initiated resource join/rejoin must
  // go through (issue #594 adversarial follow-up, rounds 2-3) —
  // ChatMessageArea's "Chamada"/"Entrar na chamada" actions, never
  // DedicatedCallPage (see activateResourceParticipation below and
  // pendingJoinAttemptsRef's own comment). Registers the local-only intent
  // synchronously, BEFORE resource.join() itself starts (never after — that
  // would reopen the exact window this exists to close), and only ever
  // clears it once join() has fully settled: promoted via
  // beginResourceParticipation on success (whose own commitParticipation
  // call is synchronous, so the real, higher-ranked token is already in
  // place the instant this awaits past it — no gap), or simply dropped on
  // failure/supersession, since there is then nothing left to protect.
  //
  // target.callId is undefined for a genuinely fresh join (ChatShell: the
  // server decides/reuses the call_id) — the protection cannot simply key
  // off target.callId in that case, since there is nothing to key off of at
  // the instant of the gesture. Instead it rides useResourceCallSession's
  // own onCallIdResolved callback, which fires SYNCHRONOUSLY the moment the
  // real call_id becomes known inside join() itself (see that hook's own
  // doc comment) — before issueCallToken/media.connect's own await ever
  // gives a cross-tab message a chance to interleave. protectedCallId tracks
  // whichever callId is currently protected by THIS attempt (initially
  // target.callId if already known, reassigned the instant the callback
  // fires) so cleanup always releases the right one.
  const joinResourceParticipation = useCallback(
    async (target: ResourceCallTarget): Promise<string | undefined> => {
      if (
        directCallBusy ||
        (resource.active &&
          (resource.active.kind !== target.kind || resource.active.id !== target.id))
      ) {
        return undefined;
      }
      let protectedCallId: string | null = null;
      if (target.callId) {
        protectedCallId = target.callId;
        protectJoinAttempt(protectedCallId);
      }
      try {
        const joinedCallId = await joinResourceCall(target, "fresh", (resolvedCallId) => {
          if (protectedCallId === resolvedCallId) return;
          if (protectedCallId) unprotectJoinAttempt(protectedCallId);
          protectedCallId = resolvedCallId;
          protectJoinAttempt(protectedCallId);
        });
        if (joinedCallId) {
          try {
            await beginResourceParticipation(joinedCallId);
          } catch {
            // Defensive (issue #622 round 2 adversarial audit, section 20):
            // beginResourceParticipation is designed to never throw —
            // callOwnership.ts's allocateParticipationGeneration/post both
            // fail open to a safe local fallback rather than propagating —
            // but that is another module's invariant, not this one's to
            // assume forever. If it ever did throw, resource.join() has
            // already created a real server-side lease and possibly a
            // connected media room with NO participation token registered
            // anywhere — worse than an ordinary join failure, since no tab
            // (including this one) would know this participation exists to
            // converge or release it later. Real cleanup takes precedence
            // over pretending the join succeeded; matches resource.join()'s
            // own "never throws" contract by resolving undefined here too.
            await leaveResourceSession().catch(() => undefined);
            return undefined;
          }
        } else if (target.callId) {
          // A known-call join failed (issue #622 round 2 section 17): the
          // call may have ended between discovery and this click, or the
          // busy check may have rejected it — either way, resync so the
          // indicator reflects the server's current answer instead of the
          // stale pre-click guess. Safe for busy too: the target is still
          // genuinely active, so this resync only re-confirms it, never
          // clears it.
          requestResourceCallSync(target.kind, target.id);
        }
        return joinedCallId;
      } finally {
        if (protectedCallId) unprotectJoinAttempt(protectedCallId);
      }
    },
    [
      beginResourceParticipation,
      directCallBusy,
      joinResourceCall,
      leaveResourceSession,
      protectJoinAttempt,
      requestResourceCallSync,
      resource.active,
      unprotectJoinAttempt,
    ],
  );
  // DedicatedCallPage/handoff-only entry point (issue #594 adversarial
  // follow-up, round 3) — deliberately NEVER registers a fresh-join-intent
  // (pendingJoinAttemptsRef): see that ref's own comment for why treating
  // every DedicatedCallPage activation as a fresh attempt would risk
  // masking a real, currently-accepted "left" for the participation it is
  // simply reconnecting media for. Still registers the real participation
  // on success, via the exact same beginResourceParticipation used by
  // joinResourceParticipation — issue #570 problem 3's "harmless, idempotent
  // refresh" behavior for a plain continuation is unaffected either way.
  const activateResourceParticipation = useCallback(
    async (target: ResourceCallTarget): Promise<string | undefined> => {
      const joinedCallId = await joinResourceCall(target, "recovery");
      if (joinedCallId) await beginResourceParticipation(joinedCallId);
      return joinedCallId;
    },
    [beginResourceParticipation, joinResourceCall],
  );
  // Advances the CURRENT participation (same token, next sequence) — used
  // by endResourceParticipation below for leaving/left/leave-cancelled,
  // which all belong to the join already registered via
  // beginResourceParticipation, never a new one. Falls back to the shared,
  // read-only token when this tab's own local record is empty — e.g. a tab
  // that took over an EXISTING participation via handoff/reconnect (never
  // its own join()) never locally observed the "participating" broadcast
  // that created it, and must still tag its own leaving/left with the token
  // every other tab already knows, or they would reject it as an older,
  // unrelated participation.
  const continueResourceParticipation = useCallback(
    (
      callId: string,
      phase: "participating" | "leaving" | "left",
      type: ParticipationMessageType,
    ) => {
      const existing = participationRecordsRef.current.get(callId);
      const token = existing?.token ??
        getOwnership().getParticipationToken(callId) ?? {
          generation: 0,
          writerId: getOwnership().tabId,
        };
      const sequence = (existing?.sequence ?? 0) + 1;
      commitParticipation(callId, phase, token, sequence);
      postParticipation(callId, type, token, sequence);
    },
    [commitParticipation, getOwnership, postParticipation],
  );
  // The real participant leave (#569) plus the cross-tab signal that this
  // user's participation is over — distinct from a handoff/minimize, which
  // only ever moves ownership and must leave resource.callId alone so a
  // legitimate reclaim can still reconnect (issue #570). Every genuine
  // "I'm done with this resource call" action funnels through here so no
  // other tab is ever left believing it can reconnect it.
  //
  // "leaving" is posted immediately, before the server round trip, so no
  // tab (including this one) can reconnect/take over while the leave is
  // still pending. "left" only follows once resource.leave() actually
  // resolves with released=true. A stale fence resolves released=false:
  // local stale media is already cleared, but "leave-cancelled" prevents
  // the speculative leaving phase from suppressing the newer tab's lease.
  const endResourceParticipation = useCallback(async (): Promise<void> => {
    if (!resource.active) return;
    const callId = resource.callId;
    if (callId) continueResourceParticipation(callId, "leaving", "leaving");
    try {
      const released = await resource.leave();
      if (!released) {
        if (callId) continueResourceParticipation(callId, "participating", "leave-cancelled");
        return;
      }
    } catch (error) {
      if (callId) continueResourceParticipation(callId, "participating", "leave-cancelled");
      throw error;
    }
    if (callId) continueResourceParticipation(callId, "left", "left");
  }, [continueResourceParticipation, resource]);

  const directActive = calls.call?.status === "active" ? calls.call : null;
  // Render-time read: the state snapshot, never the ref — see the
  // participationRecordsRef/participationRecords comment above.
  const resourceParticipationPhase = resource.callId
    ? participationRecords.get(resource.callId)?.phase
    : undefined;
  const resourceCallReconnectable =
    resourceParticipationPhase !== "leaving" && resourceParticipationPhase !== "left";
  const activeCallId =
    directActive?.call_id ?? (resource.callId && resourceCallReconnectable ? resource.callId : "");
  const resourceTarget = directActive ? null : resource.active;
  // The callId this participation may still reconnect/present as — the SAME
  // authority activeCallId above already reads (resourceCallReconnectable),
  // so "leaving"/"left" clears this synchronously (issue #642 review,
  // blocker 4), before endResourceParticipation's own server round trip
  // ever resolves. Never a second local "isLeaving" flag.
  const resourcePresentationCallId =
    resource.callId && resourceCallReconnectable ? resource.callId : null;
  // The discovery observation for the target this tab is actually
  // participating in — call.admitted and call.accepted are independent,
  // unordered messages (resourceCallSignaling.ts), so discovery can still
  // briefly hold an OLDER call_id for the very same channel/group-DM while
  // this participation's own callId is already current. Only ever used to
  // gate the resourcePresentationCall below — every other resource-call path
  // (join/leave/reconnect) stays entirely independent of discovery.
  const resourceDiscoveredForTarget = resourceTarget
    ? getResourceCall(resourceTarget.kind, resourceTarget.id)
    : null;
  // #657 fix (was #642 review fix) — the single derived authority for
  // "this resource participation is stable enough to present the compact
  // local-control mode in ActiveResourceCallBar" — null in every other state
  // (connecting, reconnecting, error, remote ownership, a stale/mismatched
  // discovery call_id, or a participation already leaving/left). Non-null
  // carries the exact, proven Call this tab is participating in. The bar
  // COEXISTS with FloatingCallWindow — this authority never suppresses or
  // replaces the floating window (issue #657 regression 1 fix).
  const resourcePresentationCall =
    resourcePresentationCallId &&
    resourceDiscoveredForTarget?.status === "active" &&
    resourceDiscoveredForTarget.call_id === resourcePresentationCallId &&
    resource.status === "active" &&
    media.status === "connected" &&
    ownerState === "local"
      ? resourceDiscoveredForTarget
      : null;
  // #673 — the direct-call counterpart of resourcePresentationCall: the
  // single derived authority for "this 1:1 call is stable enough to present
  // the compact persistent bar in the DM view". Unlike a resource call,
  // there is no separate discovery/participation duality to reconcile here —
  // calls.call (useCallSignaling) already only ever holds a direct/"user"
  // call this tab itself is caller or callee of, so directActive alone
  // (status "active", never merely "ringing") plus the same media-connected/
  // local-ownership gate resourcePresentationCall uses is the whole
  // predicate. Null while ringing (IncomingCallPopup/OutgoingCallPopup keep
  // owning that state), connecting, reconnecting, erroring, or while
  // ownership is remote (another tab/FloatingCallWindow owns the surface).
  const directPresentationCall =
    directActive && media.status === "connected" && ownerState === "local" ? directActive : null;

  // Ownership fence for screen-share START only (issue #611) — requirements
  // traceability that only the CURRENT owner of THIS specific call may begin
  // capture, never a fix for the (already-safe, see useCallMedia's
  // PendingMediaOperation/generation guards) late-resolution race. Never
  // treats a lease for some OTHER call as authorization: lease.callId must
  // match this tab's own activeCallId. STOP is never gated — an already-
  // active share must always be stoppable for privacy even if ownership was
  // just lost, since media.stop() (ownership-loss/handoff paths) already
  // tears the whole session down independently of this wrapper. No
  // writeMediaIntent, no storage revision, no recovery snapshot: screen
  // share is never persisted (issue #611 explicitly forbids auto-resume).
  const wrappedToggleScreenShare = useCallback(async (): Promise<void> => {
    if (!media.screenShareEnabled) {
      const lease = getOwnership().getLease();
      if (ownerStateRef.current !== "local" || !lease || lease.callId !== activeCallId) return;
    }
    await media.toggleScreenShare();
  }, [activeCallId, getOwnership, media]);

  // Every external consumer (context value's `media`, the floating/dedicated
  // `controls` object) must read toggles through here — never the raw
  // useCallMedia() functions — so a call UI can never bypass persistence
  // (issue #610 §10) or the screen-share ownership fence above. Internal
  // choke points above (ownedMedia.connect, directMedia.stop) keep using the
  // raw `media` object directly.
  const wrappedMedia = useMemo<CallMediaSessionController>(
    () => ({
      ...media,
      toggleMicrophone: wrappedToggleMicrophone,
      toggleCamera: wrappedToggleCamera,
      toggleScreenShare: wrappedToggleScreenShare,
    }),
    [media, wrappedToggleMicrophone, wrappedToggleCamera, wrappedToggleScreenShare],
  );

  const reconnectLocal = useCallback(async (): Promise<boolean> => {
    try {
      if (resource.callId && resourceCallReconnectable) {
        await resource.reconnect();
      } else if (calls.call?.status === "active") {
        if (calls.mediaActivationRequired) await calls.activateMedia();
        else await calls.retryMedia();
      }
      return mediaRef.current.status !== "error";
    } catch {
      return false;
    }
  }, [calls, resource, resourceCallReconnectable]);

  const restoreOwnership = useCallback(
    (
      callId: string,
      afterEpoch = 0,
      predecessorHint?: { tabId: string; epoch: number },
    ): Promise<boolean> => {
      if (recovery.current) return recovery.current;
      const attempt = (async () => {
        dispatchPresentation({ type: "OWNER_LOST" });
        const localCall = callsRef.current.call;
        const recoveringDirect = localCall?.call_id === callId && localCall.target_type === "user";
        if (recoveringDirect) {
          if (terminalDirectRecoveryCallRef.current === callId) return false;
          let authoritative: Call;
          try {
            authoritative = await resolveCall(callId);
          } catch {
            return false;
          }
          if (authoritative.call_id !== callId || authoritative.status !== "active") {
            terminalDirectRecoveryCallRef.current = callId;
            return false;
          }
          const currentCall = callsRef.current.call;
          if (
            currentCall?.call_id !== callId ||
            currentCall.target_type !== "user" ||
            currentCall.status !== "active"
          ) {
            return false;
          }
        }
        const lease = await getOwnership().claim(callId, "main", afterEpoch, predecessorHint);
        if (!lease) return false;
        if (recoveringDirect) {
          const currentCall = callsRef.current.call;
          if (
            currentCall?.call_id !== callId ||
            currentCall.target_type !== "user" ||
            currentCall.status !== "active"
          ) {
            if (currentCall?.call_id === callId && terminal.has(currentCall.status)) {
              terminalDirectRecoveryCallRef.current = callId;
            }
            getOwnership().release(callId);
            return false;
          }
        }
        emitCallTechnicalEvent("ownership-takeover");
        setOwner("local");
        const connected = await reconnectLocal();
        if (connected) emitCallTechnicalEvent("floating-activated");
        dispatchPresentation({ type: connected ? "RECOVERED" : "FAIL" });
        return connected;
      })().finally(() => {
        if (recovery.current === attempt) recovery.current = null;
      });
      recovery.current = attempt;
      return attempt;
    },
    [getOwnership, reconnectLocal, setOwner],
  );

  useEffect(() => {
    const ownership = getOwnership();
    return ownership.subscribe((message: OwnershipMessage) => {
      // Participation facts (issue #570) are handled before — and
      // independently of — the handoff correlation below: they must be
      // applied for whatever callId they name, never only the one this tab
      // currently considers "active" (that's circular — activeCallId
      // itself depends on participationRecords), and never gated on
      // handoffCall.current either.
      if (
        message.type === "participating" ||
        message.type === "leaving" ||
        message.type === "left" ||
        message.type === "leave-cancelled"
      ) {
        const applied = applyParticipationEvent(
          message.callId,
          message.type === "leave-cancelled" ? "participating" : message.type,
          { generation: message.generation, writerId: message.writerId },
          message.sequence,
        );
        // A "left" that actually advanced this callId's record (never a
        // stale/superseded one) means the real server-side leave already
        // happened in whichever tab sent it. Converge THIS tab's own
        // resource session too, if it still represents that same callId
        // (issue #594) — purely local, never a second call.leave — so the
        // buttons this tab gates on resource.active recover without a
        // reload.
        //
        // Unless a fresh join for this exact callId is already in flight
        // locally and not yet registered (pendingJoinAttemptsRef, issue #594
        // adversarial follow-up): commitParticipation's ordering can only
        // judge this "left" against whatever token it already knows about,
        // which — during that window — is still the OLD participation this
        // new attempt is superseding, not the new one (which has no token
        // yet). Converging here would abort a legitimate rejoin instead of
        // the stale participation it actually refers to.
        if (
          applied &&
          message.type === "left" &&
          !pendingJoinAttemptsRef.current.has(message.callId)
        ) {
          convergeRemoteLeave(message.callId);
        }
        return;
      }
      const callId = activeCallId || handoffCall.current;
      if (message.callId !== callId) return;
      if (role === "main" && message.type === "ready") {
        const lease = ownership.getLease();
        if (!lease || lease.callId !== callId || handoffCall.current) return;
        handoffCall.current = callId;
        handoffEpoch.current = lease.epoch;
        // The dedicated tab claims with afterEpoch = lease.epoch, so in the
        // uncontested case it ends up holding lease.epoch + 1 — that is the
        // only epoch this attempt will accept back in an ack/failure.
        expectedAckEpoch.current = lease.epoch + 1;
        emitCallTechnicalEvent("handoff-start");
        dispatchPresentation({ type: "HANDOFF_START" });
        void mediaRef.current.stop().then(() => {
          ownership.release(callId);
          setOwner("remote");
          ownership.post({
            v: 1,
            type: "handoff",
            callId,
            tabId: ownership.tabId,
            targetTabId: message.tabId,
            epoch: lease.epoch,
          });
          handoffTimer.current = window.setTimeout(() => {
            handoffTimer.current = null;
            handoffCall.current = "";
            // Mark this attempt terminal (aborted) before recovery starts:
            // any ack/failure that still arrives for it must be ignored.
            expectedAckEpoch.current = null;
            emitCallTechnicalEvent("handoff-failure");
            dispatchPresentation({ type: "HANDOFF_TIMEOUT" });
            void restoreOwnership(callId, lease.epoch);
          }, 6_000);
        });
      } else if (
        role === "dedicated" &&
        message.type === "handoff" &&
        message.targetTabId === ownership.tabId
      ) {
        handoffEpoch.current = message.epoch;
        // The "handoff" message IS main's own live report of the lease
        // it just gave up ({tabId: message.tabId, epoch: message.epoch}) —
        // exact, provable predecessor identity (issue #610 audit), so the
        // dedicated tab's recovery connect can restore main's snapshot
        // precisely instead of degrading to OFF/OFF.
        void ownership
          .claim(callId, "dedicated", message.epoch, { tabId: message.tabId, epoch: message.epoch })
          .then((lease) => {
            if (lease) {
              if (dedicatedReadyTimer.current !== null) {
                window.clearTimeout(dedicatedReadyTimer.current);
                dedicatedReadyTimer.current = null;
              }
              setOwner("local");
            } else
              ownership.post({
                v: 1,
                type: "failure",
                callId,
                tabId: ownership.tabId,
                epoch: message.epoch,
              });
          });
      } else if (role === "main" && message.type === "ack") {
        if (message.epoch !== expectedAckEpoch.current) return;
        expectedAckEpoch.current = null;
        if (handoffTimer.current !== null) window.clearTimeout(handoffTimer.current);
        handoffTimer.current = null;
        handoffCall.current = "";
        emitCallTechnicalEvent("handoff-success");
        dispatchPresentation({ type: "HANDOFF_ACK" });
      } else if (role === "main" && message.type === "failure") {
        if (message.epoch !== expectedAckEpoch.current) return;
        expectedAckEpoch.current = null;
        if (handoffTimer.current !== null) window.clearTimeout(handoffTimer.current);
        handoffTimer.current = null;
        handoffCall.current = "";
        emitCallTechnicalEvent("handoff-failure");
        void restoreOwnership(callId, handoffEpoch.current);
      } else if (
        role === "main" &&
        message.type === "released" &&
        ownerStateRef.current !== "local"
      ) {
        // A dedicated tab minimizing back (releaseDedicated) never sends an
        // explicit targeted handoff — this "released" broadcast (posted by
        // release() itself) is the only live signal main gets, and it
        // already carries the exact predecessor identity (issue #610
        // audit) the poll-based restoreOwnership fallback below has no way
        // to prove on its own. restoreOwnership's own in-flight dedup
        // (recovery.current) makes this safe to fire eagerly alongside
        // that poll without ever double-claiming.
        void restoreOwnership(callId, message.epoch, {
          tabId: message.tabId,
          epoch: message.epoch,
        });
      }
    });
  }, [
    activeCallId,
    applyParticipationEvent,
    convergeRemoteLeave,
    getOwnership,
    restoreOwnership,
    role,
    setOwner,
  ]);

  useEffect(() => {
    if (role !== "main" || ownerState !== "remote" || !activeCallId || !handoffEpoch.current)
      return;
    const timer = window.setInterval(() => {
      if (!getOwnership().getOwner(activeCallId)) {
        void restoreOwnership(activeCallId, handoffEpoch.current);
      }
    }, 1_500);
    return () => window.clearInterval(timer);
  }, [activeCallId, getOwnership, ownerState, restoreOwnership, role]);

  useEffect(() => {
    if (mediaEnabled && calls.call?.status === "ringing") void media.prepare();
  }, [calls.call?.status, media, mediaEnabled]);

  useEffect(() => {
    const incoming =
      calls.call?.status === "ringing" && calls.call.callee_id === directory?.currentUserId;
    if (incoming && presentation.mode === "idle") {
      emitCallTechnicalEvent("incoming-shown");
      dispatchPresentation({ type: "INCOMING" });
    }
    if (
      (calls.call?.status === "active" || resource.status === "connecting") &&
      ["idle", "incoming"].includes(presentation.mode)
    ) {
      dispatchPresentation({ type: "CONNECT" });
    }
    if (media.status === "connected" && presentation.mode === "connecting") {
      if (!dedicated) emitCallTechnicalEvent("floating-activated");
      dispatchPresentation({ type: "CONNECTED", dedicated });
    }
    if (media.status === "reconnecting" && presentation.mode.startsWith("active_")) {
      emitCallTechnicalEvent("reconnect");
      dispatchPresentation({ type: "RECONNECTING" });
    }
    if (media.status === "connected" && presentation.mode === "reconnecting") {
      dispatchPresentation({ type: "RECONNECTED" });
    }
    if (
      calls.call &&
      terminal.has(calls.call.status) &&
      !["idle", "ended"].includes(presentation.mode)
    ) {
      emitCallTechnicalEvent("end");
      dispatchPresentation({ type: "END" });
    }
  }, [
    calls.call,
    dedicated,
    directory?.currentUserId,
    media.status,
    presentation.mode,
    resource.status,
  ]);

  // Discovery is directory-purged (issue #622 round 2 section 6): a
  // resource that stopped existing or became inaccessible must not keep a
  // stale "call ativa" indicator forever, since it will never again receive
  // a subscription/broadcast to correct it. Purely cache eviction — the
  // server remains sole authority for whether a join is actually allowed.
  //
  // Adjusted during render (React's "adjust state when a prop changes"
  // pattern, same one HeaderDM's avatar fallback uses below) rather than in
  // an effect: pruning is a pure function of `directory` alone, so deriving
  // it synchronously avoids an extra commit-then-cascade render for every
  // directory update.
  const [prunedForDirectory, setPrunedForDirectory] = useState(directory);
  if (directory !== prunedForDirectory) {
    setPrunedForDirectory(directory);
    if (directory) {
      const known = new Set<ResourceKey>([
        ...directory.channels.map((channel) => resourceKey("channel", channel.id)),
        ...directory.dms.filter((dm) => dm.type === "group").map((dm) => resourceKey("dm", dm.id)),
      ]);
      setResourceCallDiscovery((current) => pruneResourceCallDiscovery(current, known));
    }
  }

  const lastSocketGenerationRef = useRef<number | null>(null);
  useEffect(() => {
    if (!directory) return;
    const consumerId = `global-calls:${getOwnership().tabId}`;
    setConsumerSubscriptions(consumerId, [
      ...directory.channels.map((channel) => ({ kind: "channel" as const, targetId: channel.id })),
      ...directory.dms
        .filter((dm) => dm.type === "group")
        .map((dm) => ({ kind: "dm" as const, targetId: dm.id })),
    ]);
    const handle = acquireChatSocket({
      onOpen(generation) {
        // Rediscovery on reconnect (issue #622 round 2 section 5): a new
        // generation means the socket actually reopened (the very first
        // connection's generation is recorded here too, but the route
        // effect above already covers that initial sync, so only a CHANGE
        // triggers a resync here). Whatever this tab missed while
        // disconnected is exactly what call.resource.sync recovers —
        // never a broadcast this tab could have relied on instead, since it
        // was, by definition, not connected to receive one.
        const previous = lastSocketGenerationRef.current;
        lastSocketGenerationRef.current = generation;
        const target = routeResourceTargetRef.current;
        if (previous !== null && previous !== generation && target) {
          requestResourceCallSync(target.kind, target.id);
        }
      },
      onMessage(value) {
        const event = parseCallEvent(value);
        if (!event || event.target_type === "user") return;
        // Discovery only — never gated on busy/participation/caller
        // identity (issue #622 round 2): observing a resource call is never
        // participating in it, and #609 only ever blocks this user's own
        // join/start, never their ability to see that X exists. The
        // reducer itself owns every ordering/staleness decision.
        applyResourceCallEvent(event.target_type, event.target_id, event.call);
      },
    });
    return () => {
      handle.release();
      releaseConsumerSubscriptions(consumerId);
    };
  }, [applyResourceCallEvent, directory, getOwnership, requestResourceCallSync]);

  // Timers only: the coordinator's own close() lives exclusively in the
  // lifecycle effect above.
  useEffect(
    () => () => {
      if (handoffTimer.current !== null) window.clearTimeout(handoffTimer.current);
      if (dedicatedReadyTimer.current !== null) window.clearTimeout(dedicatedReadyTimer.current);
    },
    [],
  );

  const enableMedia = useCallback(() => setMediaEnabled(true), []);
  const registerDirectory = useCallback((next: CallDirectory) => {
    setDirectory((current) =>
      current?.currentUserId === next.currentUserId &&
      current.channels === next.channels &&
      current.dms === next.dms
        ? current
        : next,
    );
    setMediaEnabled(true);
  }, []);
  const registerIdentity = useCallback(
    (status: "loading" | "ready" | "error", retry: () => Promise<void>) => {
      setIdentity((current) =>
        current.status === status && current.retry === retry ? current : { status, retry },
      );
    },
    [],
  );
  // window.open's return value is not trustworthy evidence that a tab
  // actually opened: with "noopener" some browsers omit the WindowProxy even
  // on success, and that is indistinguishable here from a blocked popup. So
  // this boolean means only "the main tab asked the browser to open the
  // dedicated tab" (the guard above passed) — never "the dedicated tab is
  // confirmed open". Real handoff confirmation is exclusively the
  // ready -> handoff -> ack/failure/timeout protocol elsewhere in this file;
  // a blocked/opaque popup must never change ownerState or release the lease.
  const expand = useCallback(() => {
    if (!activeCallId || ownerStateRef.current !== "local") return false;
    window.open(`/call/${encodeURIComponent(activeCallId)}`, "_blank", "noopener");
    emitCallTechnicalEvent("dedicated-opened");
    return true;
  }, [activeCallId]);
  const takeOver = useCallback(async () => {
    if (!activeCallId) return false;
    const observed = getOwnership().getOwner(activeCallId);
    return restoreOwnership(activeCallId, observed?.epoch ?? 0);
  }, [activeCallId, getOwnership, restoreOwnership]);
  // Dedicated-tab-only recovery: claims ownership directly, bypassing the
  // main-tab handoff reply. Safe against a live owner because it goes
  // through the same callId/epoch-scoped claim() as every other path — a
  // healthy owner's lease simply outlives the guard and the claim fails.
  const attemptDedicatedRecovery = useCallback(
    async (callId: string, afterEpoch: number) => {
      const lease = await getOwnership().claim(callId, "dedicated", afterEpoch);
      if (handoffCall.current !== callId) return;
      if (lease) {
        setOwner("local");
        emitCallTechnicalEvent("ownership-takeover");
      } else {
        emitCallTechnicalEvent("handoff-failure");
        setDedicatedRecoveryFailed(true);
      }
    },
    [getOwnership, setOwner],
  );
  const announceDedicated = useCallback(
    (callId: string) => {
      const ownership = getOwnership();
      handoffCall.current = callId;
      setDedicatedRecoveryFailed(false);
      const owner = ownership.getOwner(callId);
      ownership.post({
        v: 1,
        type: "ready",
        callId,
        tabId: ownership.tabId,
        epoch: owner?.epoch ?? 0,
      });
      if (dedicatedReadyTimer.current !== null) {
        window.clearTimeout(dedicatedReadyTimer.current);
        dedicatedReadyTimer.current = null;
      }
      if (!owner) {
        // No lease at all: nothing to wait for, claim it right away.
        void attemptDedicatedRecovery(callId, 0);
        return;
      }
      dedicatedReadyTimer.current = window.setTimeout(() => {
        dedicatedReadyTimer.current = null;
        if (ownerStateRef.current === "local" || handoffCall.current !== callId) return;
        void attemptDedicatedRecovery(callId, owner.epoch);
      }, DEDICATED_READY_TIMEOUT_MS);
    },
    [attemptDedicatedRecovery, getOwnership],
  );
  const acknowledgeDedicated = useCallback(
    (callId: string, connected: boolean) => {
      const ownership = getOwnership();
      ownership.post({
        v: 1,
        type: connected ? "ack" : "failure",
        callId,
        tabId: ownership.tabId,
        epoch: ownership.getLease()?.epoch ?? handoffEpoch.current,
      });
      if (!connected) {
        ownership.release(callId);
        setOwner("remote");
      }
    },
    [getOwnership, setOwner],
  );
  const releaseDedicatedOwnership = useCallback(
    async (callId: string, emitCleanup: boolean) => {
      // Stop-before-release: the dedicated tab's own Room/tracks must be
      // torn down before it stops being the owner, so a window.close()
      // fallback (SPA navigate in this same tab) never leaves residual
      // media running alongside whatever tab recovers ownership next.
      await mediaRef.current.stop();
      if (emitCleanup) emitCallTechnicalEvent("track-cleanup");
      getOwnership().release(callId);
      setOwner("remote");
    },
    [getOwnership, setOwner],
  );
  const releaseDedicated = useCallback(
    (callId: string) => releaseDedicatedOwnership(callId, true),
    [releaseDedicatedOwnership],
  );
  const releaseDirectTerminal = useCallback(
    async (callId: string) => {
      // useCallSignaling normally completed and reported this cleanup first.
      // A retry after disconnect failure has not, so report the first
      // successful convergence here without duplicating the normal path.
      await releaseDedicatedOwnership(callId, false);
      if (reportedMediaCleanupCallRef.current !== callId) {
        emitCallTechnicalEvent("track-cleanup");
        reportedMediaCleanupCallRef.current = callId;
      }
    },
    [releaseDedicatedOwnership],
  );
  // The dedicated tab's actual "sair" (issue #570) — distinct from
  // releaseDedicated above, which only ever moves ownership (minimize) and
  // must never touch participation. Runs the real participant leave (#569)
  // and broadcasts "leaving"/"left" so every other tab converges instead of
  // later reconnecting a call this user already left, then reuses
  // releaseDedicated itself for the ownership half — same
  // stop-before-release guarantee, no separate implementation of it to
  // drift out of sync.
  const leaveDedicated = useCallback(
    async (callId: string) => {
      await endResourceParticipation();
      await releaseDedicated(callId);
    },
    [endResourceParticipation, releaseDedicated],
  );
  // #642's view-scoped exit: same endResourceParticipation leaveDedicated
  // already reuses above — never resource.leave() directly, so it leaves
  // exactly the same leaving/left cross-tab state a FloatingCallWindow leave
  // would.
  const leaveResourceParticipation = endResourceParticipation;

  const localIdentity = useMemo(
    () => ({ name: localName, initials: localInitials, avatarUrl: selfAvatarUrl }),
    [localName, localInitials, selfAvatarUrl],
  );

  const value = useMemo(
    () => ({
      media: wrappedMedia,
      calls,
      resource,
      ownerState,
      presentation,
      dedicatedRecoveryFailed,
      enableMedia,
      registerDirectory,
      registerIdentity,
      expand,
      takeOver,
      announceDedicated,
      acknowledgeDedicated,
      releaseDedicated,
      releaseDirectTerminal,
      leaveDedicated,
      localIdentity,
      leaveResourceParticipation,
      resourcePresentationCall,
      directPresentationCall,
      beginResourceParticipation,
      joinResourceParticipation,
      activateResourceParticipation,
      getResourceCall,
      requestResourceCallSync,
    }),
    [
      acknowledgeDedicated,
      activateResourceParticipation,
      announceDedicated,
      beginResourceParticipation,
      calls,
      dedicatedRecoveryFailed,
      directPresentationCall,
      enableMedia,
      expand,
      getResourceCall,
      joinResourceParticipation,
      leaveDedicated,
      localIdentity,
      leaveResourceParticipation,
      wrappedMedia,
      ownerState,
      presentation,
      registerDirectory,
      registerIdentity,
      releaseDedicated,
      releaseDirectTerminal,
      requestResourceCallSync,
      resource,
      resourcePresentationCall,
      takeOver,
    ],
  );

  const directIncoming =
    calls.call?.status === "ringing" &&
    (!directory || calls.call.callee_id === directory.currentUserId)
      ? calls.call
      : null;
  const incomingRingtoneCallId =
    !dedicated && directIncoming && getIncomingCallRingtoneEnabled()
      ? directIncoming.call_id
      : null;
  useEffect(() => {
    if (!incomingRingtoneCallId) return;
    startIncomingCallRingtone(incomingRingtoneCallId);
    return stopIncomingCallRingtone;
  }, [incomingRingtoneCallId]);
  // The CALLER's own surface for the exact same call.ringing (issue #615) —
  // authoritative only: gated on calls.call itself, never on calls.pending
  // (a start() preflight that never reaches the server, or one that fails
  // before ringing, must never show this). No target_type check needed:
  // calls.call (useCallSignaling) already only ever holds a direct/"user"
  // call by construction — its onMessage drops any envelope whose
  // target_type isn't "user" before a Call ever reaches this state, and the
  // Call payload's own target_type is undefined for legacy direct rows
  // (directActive/directIncoming above rely on the exact same invariant).
  // Deliberately requires `directory` to already be loaded (unlike
  // directIncoming's permissive `!directory ||` fallback above) — the two
  // conditions must never both hold for the same ringing call, or this and
  // IncomingCallPopup would render simultaneously for it.
  const directOutgoing =
    calls.call?.status === "ringing" &&
    directory &&
    calls.call.caller_id === directory.currentUserId
      ? calls.call
      : null;
  const peerId = directActive
    ? directActive.caller_id === directory?.currentUserId
      ? directActive.callee_id
      : directActive.caller_id
    : directOutgoing
      ? directOutgoing.callee_id
      : (directIncoming?.caller_id ?? "");
  const peer = directory?.dms.find((dm) => dm.counterpart?.userId === peerId)?.counterpart;
  const title = peer?.displayName ?? resourceTarget?.name ?? "Participante";
  // Real identity seed for the floating remote fallback avatar's color —
  // peerId itself (not peer?.userId) so a direct call still gets a stable,
  // real user id even before `directory` has resolved (peer is a directory
  // lookup and can be briefly undefined while directActive already exists).
  // resourceTarget.id covers the group/channel case. `title` is only ever
  // the last resort when neither identity is known yet — never the primary
  // seed, since two different peers can share the same display name.
  const remoteSeed = peerId || resourceTarget?.id || title;
  // Stable seed for the local fallback avatar — the current user's own id.
  // Falls back to a fixed literal only for the brief window before
  // `directory` (fetched by the host page) has resolved; never a new
  // profile fetch just for this.
  const localSeed = directory?.currentUserId ?? "local";
  const participants = media.participants ?? [];
  const participantCount = Math.max(1, participants.length + 1);
  const activeSpeakerParticipant = participants.find(
    (participant) => participant.identity === media.activeSpeakerId,
  );
  const activeSpeaker: FloatingActiveSpeaker | undefined =
    media.activeSpeakerId === directory?.currentUserId
      ? { kind: "local", name: "Você" }
      : directActive && directory && media.activeSpeakerId === peerId
        ? { kind: "direct-remote", name: title }
        : resourceTarget && activeSpeakerParticipant
          ? { kind: "resource-remote", name: activeSpeakerParticipant.displayName }
          : undefined;
  // Compact floating status text (issue #611) — local always takes
  // precedence over remote (matching the dedicated primary-tile tie-break),
  // reusing the same participant displayName lookup already used above.
  // No preview, no second grid — text only.
  const screenShareLabel = media.screenShareEnabled
    ? "Você está compartilhando a tela"
    : media.remoteScreenShare
      ? `${
          participants.find(
            (participant) => participant.identity === media.remoteScreenShare?.identity,
          )?.displayName ?? "Participante"
        } está compartilhando a tela`
      : undefined;
  const controls = {
    microphoneEnabled: media.microphoneEnabled,
    cameraEnabled: media.cameraEnabled,
    screenShareEnabled: media.screenShareEnabled,
    pendingControl: media.pendingControl,
    onMicrophone: wrappedToggleMicrophone,
    onCamera: wrappedToggleCamera,
    onScreenShare: wrappedToggleScreenShare,
    onEnd: directActive
      ? calls.end
      : () => {
          emitCallTechnicalEvent("end");
          void endResourceParticipation().catch(() => undefined);
        },
    devices: {
      mediaDevices: media.mediaDevices,
      activeDeviceId: media.activeDeviceId,
      devicePendingKinds: media.devicePendingKinds,
      deviceError: media.deviceError,
      audioOutputSupported: media.audioOutputSupported,
      onSwitch: media.switchDevice,
    },
  };
  const floatingStatus =
    media.status === "reconnecting"
      ? "reconnecting"
      : media.status === "connected"
        ? "connected"
        : media.status === "error" || media.status === "permission-denied"
          ? "failed"
          : "connecting";
  const globalCallError = calls.error ?? resource.error;

  return (
    <CallSessionContext.Provider value={value}>
      {children ?? <Outlet />}
      {!dedicated && directIncoming && (
        <IncomingCallPopup
          name={peer?.displayName ?? "Participante"}
          avatarUrl={peer?.avatarUrl}
          callType={directIncoming.call_type}
          onAccept={() => {
            emitCallTechnicalEvent("accepted");
            return calls.accept();
          }}
          onReject={() => {
            emitCallTechnicalEvent("rejected");
            return calls.decline();
          }}
          identityStatus={identity.status}
          onRetryIdentity={() => identity.retry?.()}
        />
      )}
      {!dedicated && directOutgoing && (
        <OutgoingCallPopup
          name={peer?.displayName ?? "Participante"}
          avatarUrl={peer?.avatarUrl}
          callType={directOutgoing.call_type}
          // calls.cancelling (never calls.pending — issue #615 blocker
          // follow-up): `pending` alone also covers reconnect/call.sync
          // reconciliation, which would otherwise show "Cancelando…" and
          // disable the button for a call the user never asked to cancel.
          cancelling={calls.cancelling}
          onCancel={() => {
            const cancelled = calls.cancel();
            // Only a command that actually got sent is a real cancel
            // attempt — never emitted for a no-op (offline, already
            // pending, mid-reconnect).
            if (cancelled) emitCallTechnicalEvent("cancelled");
            return cancelled;
          }}
        />
      )}
      {!dedicated &&
        activeCallId &&
        ownerState === "remote" &&
        !(directActive && presentation.mode === "recovering_to_floating") && (
          <GlobalCallIndicator
            title={title}
            participantCount={participantCount}
            onReturn={() => void takeOver()}
          />
        )}
      {!dedicated && activeCallId && ownerState !== "remote" && (
        // #657: FloatingCallWindow is ALWAYS rendered when active+local-owner.
        // ActiveResourceCallBar coexists alongside it — it never replaces the
        // full call surface. Camera, screen share, video, and "Expandir em
        // nova aba" remain available through FloatingCallWindow at all times.
        <FloatingCallWindow
          title={title}
          status={floatingStatus}
          participantCount={participantCount}
          activeSpeaker={activeSpeaker}
          screenShareLabel={screenShareLabel}
          hasRemoteVideo={media.hasRemoteVideo}
          remoteSeed={remoteSeed}
          avatarUrl={peer?.avatarUrl}
          hasLocalVideo={media.hasLocalVideo}
          localSeed={localSeed}
          localName={localName}
          localInitials={localInitials}
          localAvatarUrl={selfAvatarUrl}
          controls={controls}
          onExpand={expand}
          bindLocalMedia={media.bindLocalMedia}
          bindRemoteMedia={media.bindRemoteMedia}
          testId={resourceTarget ? "resource-call-panel" : "floating-call-window"}
          activationRequired={Boolean(directActive && calls.mediaActivationRequired)}
          activationLabel={
            directActive?.call_type === "audio"
              ? "Permitir microfone"
              : "Permitir câmera e microfone"
          }
          onActivate={() => void calls.activateMedia()}
          identityStatus={directActive ? identity.status : "ready"}
          onRetryIdentity={() => identity.retry?.()}
          error={calls.error ?? resource.error ?? media.error}
          onRetry={() =>
            directActive
              ? void calls.retryMedia()
              : void resource.reconnect().catch(() => undefined)
          }
        />
      )}
      {!dedicated && !activeCallId && globalCallError && (
        <p className="call-global-error" role="alert">
          {globalCallError}
        </p>
      )}
    </CallSessionContext.Provider>
  );
}
