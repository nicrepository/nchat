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
import type { Channel, DMConversation } from "../chat/chatTypes";
import { useCallMedia, type CallMediaSessionController } from "../chat/useCallMedia";
import {
  useCallSignaling,
  type CallController,
  type CallMediaBridge,
} from "../chat/useCallSignaling";
import {
  useResourceCallSession,
  type ResourceCallController,
} from "../chat/useResourceCallSession";
import {
  compareParticipationTokens,
  createOwnershipCoordinator,
  type OwnershipCoordinator,
  type OwnershipMessage,
  type ParticipationMessageType,
  type ParticipationToken,
} from "./callOwnership";
import { initialPresentation, transition, type PresentationState } from "./callPresentation";
import { emitCallTechnicalEvent } from "./callTelemetry";
import FloatingCallWindow from "./FloatingCallWindow";
import GlobalCallIndicator from "./GlobalCallIndicator";
import IncomingCallPopup from "./IncomingCallPopup";

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
  const [incomingResource, setIncomingResource] = useState<Call | null>(null);
  const ignoredResourceCalls = useRef(new Set<string>());
  const handoffTimer = useRef<number | null>(null);
  const handoffCall = useRef("");
  const handoffEpoch = useRef(0);
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
    ) => {
      const existing = participationRecordsRef.current.get(callId);
      if (existing) {
        const cmp = compareParticipationTokens(token, existing.token);
        if (cmp < 0) return;
        if (cmp === 0 && sequence <= existing.sequence) return;
      }
      const next = new Map(participationRecordsRef.current);
      next.set(callId, { phase, token, sequence });
      participationRecordsRef.current = next;
      setParticipationRecords(next);
    },
    [],
  );
  const applyParticipationEvent = useCallback(
    (
      callId: string,
      phase: "participating" | "leaving" | "left",
      token: ParticipationToken,
      sequence: number,
    ) => commitParticipation(callId, phase, token, sequence),
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

  const ownedMedia = useMemo<CallMediaBridge>(
    () => ({
      startAudio: media.startAudio,
      connect: async (call, token, serverUrl) => {
        const ownership = getOwnership();
        const current = ownership.getLease();
        const lease =
          current?.callId === call.call_id ? current : await ownership.claim(call.call_id, role);
        if (!lease) {
          setOwner("remote");
          throw new Error("call media is owned by another tab");
        }
        setOwner("local");
        try {
          await media.connect(call, token, serverUrl);
          emitCallTechnicalEvent("join-success");
        } catch (error) {
          emitCallTechnicalEvent("join-failure");
          ownership.release(call.call_id);
          setOwner("none");
          throw error;
        }
      },
      stop: async () => {
        await media.stop();
        emitCallTechnicalEvent("track-cleanup");
      },
    }),
    [getOwnership, media, role, setOwner],
  );
  const resource = useResourceCallSession(ownedMedia, ownerState === "local");
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
  // resolves; if it rejects instead, "leave-cancelled" restores this exact
  // participation to "participating" everywhere and the rejection
  // propagates so the caller's own retry affordance still fires.
  const endResourceParticipation = useCallback(async (): Promise<void> => {
    if (!resource.active) return;
    const callId = resource.callId;
    if (callId) continueResourceParticipation(callId, "leaving", "leaving");
    try {
      await resource.leave();
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
    (callId: string, afterEpoch = 0): Promise<boolean> => {
      if (recovery.current) return recovery.current;
      const attempt = (async () => {
        dispatchPresentation({ type: "OWNER_LOST" });
        const lease = await getOwnership().claim(callId, "main", afterEpoch);
        if (!lease) return false;
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
        applyParticipationEvent(
          message.callId,
          message.type === "leave-cancelled" ? "participating" : message.type,
          { generation: message.generation, writerId: message.writerId },
          message.sequence,
        );
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
        void ownership.claim(callId, "dedicated", message.epoch).then((lease) => {
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
      }
    });
  }, [activeCallId, applyParticipationEvent, getOwnership, restoreOwnership, role, setOwner]);

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
      onMessage(value) {
        const event = parseCallEvent(value);
        if (!event || event.target_type === "user") return;
        if (terminal.has(event.call.status)) {
          setIncomingResource((current) =>
            current?.call_id === event.call.call_id ? null : current,
          );
        } else if (
          event.call.status === "active" &&
          // Never gated on resource.callId equality (issue #570 follow-up):
          // a stale resource.callId — left set on purpose after a handoff so
          // a legitimate later reclaim can still reconnect — must not
          // permanently suppress this affordance for a callId this tab's
          // current participation has actually left. What matters is
          // whether THIS tab is presently participating, which the
          // synchronous ref (this runs in a socket callback, not render —
          // see the participationRecordsRef comment above) answers.
          participationRecordsRef.current.get(event.call.call_id)?.phase !== "participating" &&
          event.call.caller_id !== directory.currentUserId &&
          !ignoredResourceCalls.current.has(event.call.call_id)
        ) {
          setIncomingResource((current) => {
            if (current?.call_id !== event.call.call_id) emitCallTechnicalEvent("incoming-shown");
            return event.call;
          });
        }
      },
    });
    return () => {
      handle.release();
      releaseConsumerSubscriptions(consumerId);
    };
  }, [directory, getOwnership]);

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
  const releaseDedicated = useCallback(
    async (callId: string) => {
      // Stop-before-release: the dedicated tab's own Room/tracks must be
      // torn down before it stops being the owner, so a window.close()
      // fallback (SPA navigate in this same tab) never leaves residual
      // media running alongside whatever tab recovers ownership next.
      await mediaRef.current.stop();
      emitCallTechnicalEvent("track-cleanup");
      getOwnership().release(callId);
      setOwner("remote");
    },
    [getOwnership, setOwner],
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

  const value = useMemo(
    () => ({
      media,
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
      leaveDedicated,
      beginResourceParticipation,
    }),
    [
      acknowledgeDedicated,
      announceDedicated,
      beginResourceParticipation,
      calls,
      dedicatedRecoveryFailed,
      enableMedia,
      expand,
      leaveDedicated,
      media,
      ownerState,
      presentation,
      registerDirectory,
      registerIdentity,
      releaseDedicated,
      resource,
      takeOver,
    ],
  );

  const directIncoming =
    calls.call?.status === "ringing" &&
    (!directory || calls.call.callee_id === directory.currentUserId)
      ? calls.call
      : null;
  const peerId = directActive
    ? directActive.caller_id === directory?.currentUserId
      ? directActive.callee_id
      : directActive.caller_id
    : (directIncoming?.caller_id ?? "");
  const peer = directory?.dms.find((dm) => dm.counterpart?.userId === peerId)?.counterpart;
  const resourceTarget = directActive ? null : resource.active;
  const title = peer?.displayName ?? resourceTarget?.name ?? "Participante";
  const participants = media.participants ?? [];
  const participantCount = Math.max(1, participants.length + 1);
  const activeSpeakerName =
    media.activeSpeakerId === directory?.currentUserId
      ? "Você"
      : participants.find((participant) => participant.identity === media.activeSpeakerId)
          ?.displayName;
  const controls = {
    microphoneEnabled: media.microphoneEnabled,
    cameraEnabled: media.cameraEnabled,
    screenShareEnabled: media.screenShareEnabled,
    pendingControl: media.pendingControl,
    onMicrophone: media.toggleMicrophone,
    onCamera: media.toggleCamera,
    onScreenShare: media.toggleScreenShare,
    onEnd: directActive
      ? calls.end
      : () => {
          emitCallTechnicalEvent("end");
          void endResourceParticipation().catch(() => undefined);
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
  const incomingTarget = incomingResource
    ? incomingResource.target_type === "channel"
      ? directory?.channels.find((channel) => channel.id === incomingResource.target_id)
      : directory?.dms.find((dm) => dm.id === incomingResource.target_id)
    : null;

  return (
    <CallSessionContext.Provider value={value}>
      {children ?? <Outlet />}
      {!dedicated && directIncoming && (
        <IncomingCallPopup
          name={peer?.displayName ?? "Participante"}
          avatarUrl={peer?.avatarUrl}
          targetKind="user"
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
      {!dedicated && !directIncoming && incomingResource && incomingTarget && (
        <IncomingCallPopup
          name={incomingTarget.name}
          targetKind={incomingResource.target_type as "channel" | "dm"}
          callType={incomingResource.call_type}
          participantCount={1}
          onAccept={() => {
            emitCallTechnicalEvent("accepted");
            setIncomingResource(null);
            // Registers the new participation only once join() itself
            // confirms it actually succeeded (issue #570 follow-up) — never
            // inferred from resource.callId changing, which a rejoin of the
            // very same call_id would never observably do.
            void resource
              .join({
                kind: incomingResource.target_type as "channel" | "dm",
                id: incomingResource.target_id!,
                name: incomingTarget.name,
                callId: incomingResource.call_id,
              })
              .then((joinedCallId) => {
                if (joinedCallId) void beginResourceParticipation(joinedCallId);
              });
          }}
          onReject={() => {
            emitCallTechnicalEvent("rejected");
            ignoredResourceCalls.current.add(incomingResource.call_id);
            setIncomingResource(null);
          }}
        />
      )}
      {!dedicated && activeCallId && ownerState === "remote" && (
        <GlobalCallIndicator
          title={title}
          participantCount={participantCount}
          onReturn={() => void takeOver()}
        />
      )}
      {!dedicated && activeCallId && ownerState !== "remote" && (
        <FloatingCallWindow
          title={title}
          status={floatingStatus}
          participantCount={participantCount}
          activeSpeakerName={activeSpeakerName}
          screenShareActive={media.screenShareEnabled || Boolean(media.remoteScreenShare)}
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
      {!dedicated && !activeCallId && calls.error && (
        <p className="call-global-error" role="alert">
          {calls.error}
        </p>
      )}
    </CallSessionContext.Provider>
  );
}
