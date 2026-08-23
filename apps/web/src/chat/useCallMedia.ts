import { useCallback, useEffect, useRef, useState, type RefCallback } from "react";

import {
  loadLiveKitSessionFactory,
  type LiveKitSession,
  type LiveKitSessionCallbacks,
  type LiveKitSessionFactory,
  type LiveKitSessionLoader,
} from "../media/liveKitSession";
import { nextStableSpeaker, type StableSpeakerState } from "../calls/activeSpeaker";
import type { MediaIntent } from "../calls/callOwnership";
import type { CallType } from "./callState";

export type CallMediaStatus =
  | "idle"
  | "connecting"
  | "connected"
  | "reconnecting"
  | "permission-denied"
  | "error";

// One resource-room participant (RF-24), keyed by LiveKit participant.identity
// — the canonical user UUID, never participant.sid. Present in this list from
// ParticipantConnected/existing-on-connect through ParticipantDisconnected,
// independent of whether camera/microphone are currently published, so a
// grid tile can render an avatar fallback instead of disappearing.
export interface ParticipantMedia {
  identity: string;
  /** Server-issued LiveKit participant name; falls back to "Participante" when absent. */
  displayName: string;
  hasVideo: boolean;
  hasAudio: boolean;
  bindVideo: RefCallback<HTMLDivElement>;
}

export interface RemoteScreenShare {
  identity: string;
  bindMedia: RefCallback<HTMLDivElement>;
}

const fallbackParticipantDisplayName = "Participante";

/** Trims a raw participant name; "" (including whitespace-only) means "no name known". */
function normalizeParticipantDisplayName(name: string): string {
  return name.trim();
}

export interface CallMediaController {
  status: CallMediaStatus;
  microphoneEnabled: boolean;
  cameraEnabled: boolean;
  hasLocalVideo: boolean;
  hasRemoteMedia: boolean;
  hasRemoteVideo: boolean;
  mediaLoading: boolean;
  audioStarting: boolean;
  audioActivationRequired: boolean;
  error: string | null;
  pendingControl: "microphone" | "camera" | "screen-share" | null;
  activeSpeakerId: string | null;
  screenShareEnabled: boolean;
  remoteScreenShare: RemoteScreenShare | null;
  bindLocalMedia: RefCallback<HTMLDivElement>;
  bindRemoteMedia: RefCallback<HTMLDivElement>;
  // RF-24 grid API. Populated from the same event stream as the legacy
  // fields above but kept independent: a resource room never touches
  // bindRemoteMedia's flat container, and a direct call never touches these.
  participants: ParticipantMedia[];
  bindRemoteAudio: RefCallback<HTMLDivElement>;
  toggleMicrophone: () => Promise<boolean | undefined>;
  toggleCamera: () => Promise<boolean | undefined>;
  toggleScreenShare: () => Promise<void>;
  activateAudio: () => Promise<void>;
}

export interface CallMediaSessionController extends CallMediaController {
  prepare: () => Promise<void>;
  startAudio: () => Promise<void>;
  connect: (
    call: { call_id: string; call_type: CallType },
    token: string,
    serverUrl: string,
    initialIntent?: MediaIntent,
  ) => Promise<MediaIntent | undefined>;
  stop: () => Promise<void>;
}

interface MediaState {
  status: CallMediaStatus;
  microphoneEnabled: boolean;
  cameraEnabled: boolean;
  hasLocalVideo: boolean;
  hasRemoteMedia: boolean;
  hasRemoteVideo: boolean;
  mediaLoading: boolean;
  audioStarting: boolean;
  audioActivationRequired: boolean;
  error: string | null;
  pendingControl: "microphone" | "camera" | "screen-share" | null;
  activeSpeakerId: string | null;
  screenShareEnabled: boolean;
  remoteScreenShare: RemoteScreenShare | null;
  participants: ParticipantMedia[];
}

const initialState: MediaState = {
  status: "idle",
  microphoneEnabled: false,
  cameraEnabled: false,
  hasLocalVideo: false,
  hasRemoteMedia: false,
  hasRemoteVideo: false,
  mediaLoading: false,
  audioStarting: false,
  audioActivationRequired: false,
  error: null,
  pendingControl: null,
  activeSpeakerId: null,
  screenShareEnabled: false,
  remoteScreenShare: null,
  participants: [],
};

interface ParticipantMediaEntry {
  displayName: string;
  hasVideo: boolean;
  hasAudio: boolean;
  videoElement: HTMLMediaElement | null;
  container: HTMLDivElement | null;
}

// Unique identity for one control operation. Compared by reference (===),
// never by its "control" field alone, so a toggle from a superseded session
// or generation can never be mistaken for the currently pending one.
interface PendingMediaOperation {
  readonly control: "microphone" | "camera" | "screen-share";
  readonly session: LiveKitSession;
  readonly generation: number;
}

export function useCallMedia(
  loadSessionFactory: LiveKitSessionLoader = loadLiveKitSessionFactory,
): CallMediaSessionController {
  const [state, setState] = useState(initialState);
  const mountedRef = useRef(true);
  const generationRef = useRef(0);
  const factoryRef = useRef<LiveKitSessionFactory | null>(null);
  const factoryPromiseRef = useRef<Promise<LiveKitSessionFactory> | null>(null);
  const sessionRef = useRef<LiveKitSession | null>(null);
  const sessionPromiseRef = useRef<Promise<LiveKitSession | null> | null>(null);
  const connectPromiseRef = useRef<{
    callId: string;
    promise: Promise<MediaIntent | undefined>;
  } | null>(null);
  const audioStartPromiseRef = useRef<{
    generation: number;
    session: LiveKitSession;
    promise: Promise<void>;
  } | null>(null);
  const disconnectPromiseRef = useRef<Promise<void> | null>(null);
  // The session a failed stop() is still trying to disconnect. Kept set only
  // across a rejection (cleared on success) so a retried stop() calls
  // disconnect() again on the very same Room instead of finding sessionRef
  // already nulled and silently no-op'ing — see stop() below.
  const disconnectingSessionRef = useRef<LiveKitSession | null>(null);
  const connectedCallIdRef = useRef("");
  const audioActivationRequiredRef = useRef(false);
  // Identity of the in-flight control operation (not just its kind): guards
  // against a stale toggle from a superseded session/generation clearing or
  // overwriting the pending state that belongs to a newer operation (RF-23
  // race: toggle on session A resolves after stop()+reconnect created B).
  const pendingControlRef = useRef<PendingMediaOperation | null>(null);
  // Authoritative local microphone state: only ever written from the
  // adapter's onMicrophoneStateChanged callback (SDK-confirmed), never from
  // toggle intent. toggleMicrophone reads this ref (not React state) to
  // avoid acting on a stale closure.
  const microphoneEnabledRef = useRef(false);
  const screenShareEnabledRef = useRef(false);
  const speakerStateRef = useRef<StableSpeakerState | null>(null);
  const speakerTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const localContainerRef = useRef<HTMLDivElement | null>(null);
  const remoteContainerRef = useRef<HTMLDivElement | null>(null);
  const localElementRef = useRef<HTMLMediaElement | null>(null);
  const remoteElementsRef = useRef(new Set<HTMLMediaElement>());
  // RF-24 grid bookkeeping, independent of the legacy flat container above.
  const remoteAudioContainerRef = useRef<HTMLDivElement | null>(null);
  const remoteAudioElementsRef = useRef(new Set<HTMLMediaElement>());
  const participantsMapRef = useRef(new Map<string, ParticipantMediaEntry>());
  const participantOrderRef = useRef<string[]>([]);
  const participantBindRef = useRef(new Map<string, RefCallback<HTMLDivElement>>());
  const remoteScreenShareElementRef = useRef<HTMLMediaElement | null>(null);
  const remoteScreenShareContainerRef = useRef<HTMLDivElement | null>(null);
  const remoteScreenShareIdentityRef = useRef<string | null>(null);

  const update = useCallback((patch: Partial<MediaState>) => {
    if (mountedRef.current) setState((current) => ({ ...current, ...patch }));
  }, []);

  const clearElements = useCallback(() => {
    localElementRef.current?.remove();
    localElementRef.current = null;
    for (const element of remoteElementsRef.current) element.remove();
    remoteElementsRef.current.clear();
    localContainerRef.current?.replaceChildren();
    remoteContainerRef.current?.replaceChildren();

    for (const element of remoteAudioElementsRef.current) element.remove();
    remoteAudioElementsRef.current.clear();
    remoteAudioContainerRef.current?.replaceChildren();
    participantsMapRef.current.clear();
    participantOrderRef.current = [];
    participantBindRef.current.clear();
    remoteScreenShareElementRef.current?.remove();
    remoteScreenShareElementRef.current = null;
    remoteScreenShareContainerRef.current?.replaceChildren();
    remoteScreenShareIdentityRef.current = null;
    if (speakerTimerRef.current !== null) clearTimeout(speakerTimerRef.current);
    speakerTimerRef.current = null;
    speakerStateRef.current = null;
  }, []);

  const bindRemoteScreenShare = useCallback<RefCallback<HTMLDivElement>>((element) => {
    remoteScreenShareContainerRef.current = element;
    if (element && remoteScreenShareElementRef.current) {
      element.replaceChildren(remoteScreenShareElementRef.current);
    }
  }, []);

  const bindVideoFor = useCallback((identity: string): RefCallback<HTMLDivElement> => {
    let bind = participantBindRef.current.get(identity);
    if (!bind) {
      bind = (container) => {
        const entry = participantsMapRef.current.get(identity);
        if (!entry) return;
        entry.container = container;
        if (container && entry.videoElement) container.replaceChildren(entry.videoElement);
      };
      participantBindRef.current.set(identity, bind);
    }
    return bind;
  }, []);

  const syncParticipants = useCallback(() => {
    const participants: ParticipantMedia[] = participantOrderRef.current.map((identity) => {
      const entry = participantsMapRef.current.get(identity);
      return {
        identity,
        displayName: entry?.displayName ?? fallbackParticipantDisplayName,
        hasVideo: entry?.hasVideo ?? false,
        hasAudio: entry?.hasAudio ?? false,
        bindVideo: bindVideoFor(identity),
      };
    });
    update({ participants });
  }, [bindVideoFor, update]);

  // displayName defaults to "" (never seen it yet — e.g. a track arriving
  // before the SDK's own participant-connected callback) and is normalized to
  // the visible fallback here, once, so every other reader of the map can
  // trust displayName is always non-empty.
  //
  // identity is, and stays, the only key: a track event and a
  // ParticipantConnected event can arrive in either order for the same
  // participant, so this must upsert rather than create-or-ignore. An
  // already-known entry only ever has its name *improved* — a normalized,
  // non-empty name overwrites a fallback or an older name, but an empty/
  // blank one (e.g. the nameless call from onRemoteElement, or a later event
  // that genuinely carries none) never regresses an already-known real name
  // back to the fallback. hasVideo/hasAudio/videoElement/container are
  // untouched here; only displayName is ever revised in place.
  const ensureParticipant = useCallback((identity: string, displayName = "") => {
    const normalized = normalizeParticipantDisplayName(displayName);
    const entry = participantsMapRef.current.get(identity);
    if (entry) {
      if (normalized !== "" && normalized !== entry.displayName) entry.displayName = normalized;
      return;
    }
    participantsMapRef.current.set(identity, {
      displayName: normalized !== "" ? normalized : fallbackParticipantDisplayName,
      hasVideo: false,
      hasAudio: false,
      videoElement: null,
      container: null,
    });
    participantOrderRef.current.push(identity);
  }, []);

  const removeParticipant = useCallback((identity: string) => {
    participantsMapRef.current.delete(identity);
    participantBindRef.current.delete(identity);
    participantOrderRef.current = participantOrderRef.current.filter((id) => id !== identity);
  }, []);

  // Invalidates a session the SDK itself declared dead (RoomEvent.Disconnected,
  // never Reconnecting/Reconnected), so a later connect()/createReadySession()
  // for a new call never inherits stale bookkeeping pointing at a Room that no
  // longer exists. Mirrors stop()'s synchronous bookkeeping reset, but leaves
  // React state to the caller (onDisconnected sets its own status/error in the
  // same update as clearing every other field) and never awaits — this runs
  // inside a synchronous adapter callback.
  const invalidateDeadSession = useCallback(
    (generation: number) => {
      if (generationRef.current !== generation) return;
      const session = sessionRef.current;
      sessionRef.current = null;
      sessionPromiseRef.current = null;
      connectPromiseRef.current = null;
      audioStartPromiseRef.current = null;
      connectedCallIdRef.current = "";
      pendingControlRef.current = null;
      audioActivationRequiredRef.current = false;
      microphoneEnabledRef.current = false;
      screenShareEnabledRef.current = false;
      generationRef.current += 1;
      clearElements();
      if (session && !disconnectPromiseRef.current) {
        disconnectPromiseRef.current = session.disconnect().catch(() => undefined);
      }
    },
    [clearElements],
  );

  const bindRemoteAudio = useCallback<RefCallback<HTMLDivElement>>((element) => {
    remoteAudioContainerRef.current = element;
    if (element) element.replaceChildren(...remoteAudioElementsRef.current);
  }, []);

  const callbacksFor = useCallback(
    (generation: number): LiveKitSessionCallbacks => {
      const current = () => mountedRef.current && generationRef.current === generation;
      const syncRemoteState = () => {
        if (!current()) return;
        const elements = [...remoteElementsRef.current];
        update({
          hasRemoteMedia: elements.length > 0,
          hasRemoteVideo: elements.some((element) => element instanceof HTMLVideoElement),
        });
      };
      return {
        onLocalElement(element) {
          if (!current()) return;
          localElementRef.current?.remove();
          localElementRef.current = element;
          localContainerRef.current?.replaceChildren(element);
          update({ hasLocalVideo: element instanceof HTMLVideoElement });
        },
        onRemoteElement(element) {
          if (!current()) return;
          if (!remoteElementsRef.current.has(element)) {
            remoteElementsRef.current.add(element);
            remoteContainerRef.current?.append(element);
            syncRemoteState();
          }

          const identity = element.dataset.participantIdentity;
          if (!identity) return;
          ensureParticipant(identity);
          const entry = participantsMapRef.current.get(identity);
          if (!entry) return;
          if (element instanceof HTMLVideoElement) {
            entry.videoElement = element;
            entry.hasVideo = true;
            if (entry.container) entry.container.replaceChildren(element);
          } else {
            entry.hasAudio = true;
            if (!remoteAudioElementsRef.current.has(element)) {
              remoteAudioElementsRef.current.add(element);
              remoteAudioContainerRef.current?.append(element);
            }
          }
          syncParticipants();
        },
        onElementRemoved(element) {
          if (!current()) return;
          if (localElementRef.current === element) {
            localElementRef.current = null;
            update({ hasLocalVideo: false });
          }
          remoteElementsRef.current.delete(element);
          remoteAudioElementsRef.current.delete(element);
          if (remoteScreenShareElementRef.current === element) {
            remoteScreenShareElementRef.current = null;
            remoteScreenShareIdentityRef.current = null;
            update({ remoteScreenShare: null });
          }
          element.remove();
          syncRemoteState();

          const identity = element.dataset.participantIdentity;
          if (!identity) return;
          const entry = participantsMapRef.current.get(identity);
          if (!entry) return;
          if (entry.videoElement === element) {
            entry.videoElement = null;
            entry.hasVideo = false;
          } else if (!(element instanceof HTMLVideoElement)) {
            entry.hasAudio = false;
          }
          syncParticipants();
        },
        onParticipantConnected(identity, displayName) {
          if (!current()) return;
          ensureParticipant(identity, displayName);
          syncParticipants();
        },
        onParticipantDisconnected(identity) {
          if (!current()) return;
          removeParticipant(identity);
          if (
            speakerStateRef.current?.candidate === identity ||
            speakerStateRef.current?.visible === identity
          ) {
            if (speakerTimerRef.current !== null) clearTimeout(speakerTimerRef.current);
            speakerTimerRef.current = null;
            speakerStateRef.current = null;
            update({ activeSpeakerId: null });
          }
          syncParticipants();
        },
        // Camera mute keeps the publication (and the attached element) alive
        // — this is never a track removal, so the element/entry.videoElement
        // is deliberately preserved for an instant restore on unmute, only
        // the tile's visible content and hasVideo flag change.
        onRemoteVideoAvailabilityChanged(identity, available) {
          if (!current()) return;
          const entry = participantsMapRef.current.get(identity);
          if (!entry) return;
          if (available && entry.videoElement) {
            entry.hasVideo = true;
            if (entry.container) entry.container.replaceChildren(entry.videoElement);
          } else {
            entry.hasVideo = false;
            entry.container?.replaceChildren();
          }
          syncParticipants();
        },
        onDisconnected() {
          if (!current()) return;
          // Terminal SDK-level disconnect: the Room is dead. Without fully
          // invalidating it here, connectedCallIdRef/sessionRef/participants
          // would keep pointing at it, so a retry with the same call_id would
          // hit the "already connected" dedup in connect() and never
          // actually reconnect. bumps generationRef so any late callback
          // from the dead session is ignored by every other current() guard.
          invalidateDeadSession(generation);
          update({
            ...initialState,
            status: "error",
            error: "A conexão de mídia foi encerrada. Tente novamente.",
          });
        },
        onReconnecting() {
          if (current()) update({ status: "reconnecting" });
        },
        onReconnected() {
          if (current()) update({ status: "connected", error: null });
        },
        onAudioPlaybackChanged(canPlaybackAudio) {
          if (!current()) return;
          const audioActivationRequired = !canPlaybackAudio;
          audioActivationRequiredRef.current = audioActivationRequired;
          update({
            audioActivationRequired,
            ...(canPlaybackAudio ? { error: null } : {}),
          });
        },
        onMicrophoneStateChanged(enabled) {
          if (!current()) return;
          microphoneEnabledRef.current = enabled;
          update({ microphoneEnabled: enabled });
        },
        onActiveSpeakersChanged(identities) {
          if (!current()) return;
          const previous = speakerStateRef.current;
          const candidate = identities[0] ?? null;
          const next = nextStableSpeaker(previous, candidate, Date.now(), 400);
          speakerStateRef.current = next;
          if (next.visible !== (previous?.visible ?? null)) {
            update({ activeSpeakerId: next.visible });
          }
          if (speakerTimerRef.current !== null) clearTimeout(speakerTimerRef.current);
          speakerTimerRef.current = null;
          if (next.visible === candidate) return;
          speakerTimerRef.current = setTimeout(
            () => {
              speakerTimerRef.current = null;
              if (!current() || !speakerStateRef.current) return;
              const settled = nextStableSpeaker(
                speakerStateRef.current,
                speakerStateRef.current.candidate,
                Date.now(),
                400,
              );
              const changed = settled.visible !== speakerStateRef.current.visible;
              speakerStateRef.current = settled;
              if (changed) update({ activeSpeakerId: settled.visible });
            },
            Math.max(0, 400 - (Date.now() - next.since)),
          );
        },
        onScreenShareChanged(enabled) {
          if (!current()) return;
          screenShareEnabledRef.current = enabled;
          update({ screenShareEnabled: enabled });
        },
        onRemoteScreenShareChanged(identity, element) {
          if (!current()) return;
          if (!element) {
            if (remoteScreenShareIdentityRef.current !== identity) return;
            remoteScreenShareElementRef.current = null;
            remoteScreenShareIdentityRef.current = null;
            remoteScreenShareContainerRef.current?.replaceChildren();
            update({ remoteScreenShare: null });
            return;
          }
          remoteScreenShareElementRef.current = element;
          remoteScreenShareIdentityRef.current = identity;
          remoteScreenShareContainerRef.current?.replaceChildren(element);
          update({ remoteScreenShare: { identity, bindMedia: bindRemoteScreenShare } });
        },
      };
    },
    [
      bindRemoteScreenShare,
      ensureParticipant,
      invalidateDeadSession,
      removeParticipant,
      syncParticipants,
      update,
    ],
  );

  const loadFactory = useCallback((): Promise<LiveKitSessionFactory> => {
    if (factoryRef.current) return Promise.resolve(factoryRef.current);
    if (factoryPromiseRef.current) return factoryPromiseRef.current;
    const generation = generationRef.current;
    update({ mediaLoading: true, error: null });
    const loading = loadSessionFactory()
      .then((factory) => {
        factoryRef.current = factory;
        if (mountedRef.current && generationRef.current === generation) {
          update({ mediaLoading: false });
        }
        return factory;
      })
      .catch((error: unknown) => {
        if (mountedRef.current && generationRef.current === generation) {
          update({
            status: "error",
            mediaLoading: false,
            error: "Não foi possível carregar os recursos da chamada.",
          });
        }
        throw error;
      })
      .finally(() => {
        if (factoryPromiseRef.current === loading) factoryPromiseRef.current = null;
      });
    factoryPromiseRef.current = loading;
    return loading;
  }, [loadSessionFactory, update]);

  const prepare = useCallback(async () => {
    await loadFactory().catch(() => undefined);
  }, [loadFactory]);

  const createReadySession = useCallback((): LiveKitSession | null => {
    if (sessionRef.current) return sessionRef.current;
    if (!factoryRef.current || !mountedRef.current) return null;
    const session = factoryRef.current(callbacksFor(generationRef.current));
    sessionRef.current = session;
    disconnectPromiseRef.current = null;
    return session;
  }, [callbacksFor]);

  const ensureSession = useCallback(async (): Promise<LiveKitSession | null> => {
    if (sessionRef.current) return sessionRef.current;
    if (sessionPromiseRef.current) return sessionPromiseRef.current;
    const generation = generationRef.current;
    const loading = (async () => {
      // Never start a new Room while the previous one is still tearing down:
      // stop()/invalidateDeadSession() clear sessionRef synchronously but the
      // underlying session.disconnect() keeps running in the background, so
      // without this wait a leave()-then-join() (or an RF-23 accept arriving
      // while an RF-24 resource room is still releasing camera/mic) could
      // construct a second Room before the first one is gone.
      const pendingDisconnect = disconnectPromiseRef.current;
      // A rejected previous disconnect only needs to be *settled* here, not
      // successful: the real failure already reaches stop()'s own caller.
      if (pendingDisconnect) await pendingDisconnect.catch(() => undefined);
      // A settled-but-failed disconnect leaves the old Room parked in
      // disconnectingSessionRef (see stop()): its cleanup is still
      // unconfirmed, so no new Room may be created until an explicit
      // stop()/leave() retries that disconnect for real and it succeeds.
      if (disconnectingSessionRef.current) {
        throw new Error("A sessão de mídia anterior ainda não foi encerrada.");
      }
      if (!mountedRef.current || generationRef.current !== generation) return null;
      if (sessionRef.current) return sessionRef.current;
      const factory = await loadFactory();
      if (!mountedRef.current || generationRef.current !== generation) return null;
      if (sessionRef.current) return sessionRef.current;
      const session = factory(callbacksFor(generation));
      sessionRef.current = session;
      disconnectPromiseRef.current = null;
      return session;
    })().finally(() => {
      if (sessionPromiseRef.current === loading) sessionPromiseRef.current = null;
    });
    sessionPromiseRef.current = loading;
    return loading;
  }, [callbacksFor, loadFactory]);

  // The one persistent, retryable teardown contract for any session whose
  // cleanup must be confirmed before a new Room may exist — used by both
  // stop() (RF-24 leave, RF-23 handoff) and connect()'s own failure path
  // (Security Review: a session that reached LiveKit or enabled camera/mic
  // before failing is exactly as sensitive as one stop() tears down, so it
  // must never get a cheaper best-effort disconnect). Success releases the
  // session; failure keeps it in disconnectingSessionRef so a later retry
  // calls disconnect() again on the very same Room instead of forgetting it.
  const trackedDisconnect = useCallback((session: LiveKitSession): Promise<void> => {
    disconnectingSessionRef.current = session;
    const cleanup = session.disconnect();
    disconnectPromiseRef.current = cleanup;
    cleanup.then(
      () => {
        if (disconnectPromiseRef.current === cleanup) disconnectPromiseRef.current = null;
        if (disconnectingSessionRef.current === session) disconnectingSessionRef.current = null;
      },
      () => {
        // Kept for retry: the next stop() must call disconnect() again on
        // this same session instead of finding no session at all.
        if (disconnectPromiseRef.current === cleanup) disconnectPromiseRef.current = null;
      },
    );
    return cleanup;
  }, []);

  // An explicit stop() is a cleanup guarantee (RF-24 leave, RF-23 handoff):
  // its caller relies on a resolved stop() meaning the Room is actually
  // gone, so a real LiveKitSession.disconnect() rejection must reject stop()
  // too — never silently swallowed into a false success. Bookkeeping (refs,
  // participants, React state) is still reset synchronously up front either
  // way, since the old Room must never be reused regardless of how its
  // disconnect ends up settling.
  const stop = useCallback((): Promise<void> => {
    const inFlight = disconnectPromiseRef.current;
    if (inFlight) return inFlight;
    // Falls back to the session a previous, failed stop() was still
    // disconnecting: sessionRef is already null by then, but the Room
    // instance itself must be retried, not silently skipped.
    const session = sessionRef.current ?? disconnectingSessionRef.current;
    sessionRef.current = null;
    sessionPromiseRef.current = null;
    connectPromiseRef.current = null;
    audioStartPromiseRef.current = null;
    connectedCallIdRef.current = "";
    pendingControlRef.current = null;
    audioActivationRequiredRef.current = false;
    microphoneEnabledRef.current = false;
    screenShareEnabledRef.current = false;
    generationRef.current += 1;
    clearElements();
    update(initialState);
    if (!session) return Promise.resolve();
    return trackedDisconnect(session);
  }, [clearElements, trackedDisconnect, update]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      // Best-effort: the owner is gone, nothing left to retry a rejection.
      void stop().catch(() => undefined);
    };
  }, [stop]);

  const startAudio = useCallback(async () => {
    if (!factoryRef.current) {
      audioActivationRequiredRef.current = true;
      update({ audioActivationRequired: true });
      await prepare();
      return;
    }
    // Same rule as ensureSession(): never create a session while the
    // previous one is still tearing down.
    const generationBeforeWait = generationRef.current;
    const pendingDisconnect = disconnectPromiseRef.current;
    if (pendingDisconnect) await pendingDisconnect.catch(() => undefined);
    // Same rule as ensureSession(): an unconfirmed cleanup blocks a new Room
    // here too. startAudio() never auto-retries the failed disconnect —
    // that stays an explicit stop()/leave() action — so this just no-ops.
    if (disconnectingSessionRef.current) return;
    if (!mountedRef.current || generationRef.current !== generationBeforeWait) return;
    const session = createReadySession();
    if (!session) return;
    const generation = generationRef.current;
    const pending = audioStartPromiseRef.current;
    if (pending?.generation === generation && pending.session === session) {
      return pending.promise;
    }
    const starting = session.startAudio();
    const current = () =>
      mountedRef.current &&
      generationRef.current === generation &&
      sessionRef.current === session &&
      audioStartPromiseRef.current?.promise === operation;
    const operation = starting
      .then(
        () => {
          if (!current()) return;
          audioActivationRequiredRef.current = false;
          update({ audioActivationRequired: false, error: null });
        },
        () => {
          if (!current()) return;
          audioActivationRequiredRef.current = true;
          update({
            audioActivationRequired: true,
            error: "O navegador bloqueou o áudio. Ative o áudio novamente.",
          });
        },
      )
      .finally(() => {
        if (!current()) return;
        audioStartPromiseRef.current = null;
        update({ audioStarting: false });
      });
    audioStartPromiseRef.current = { generation, session, promise: operation };
    update({ audioStarting: true, error: null });
    return operation;
  }, [createReadySession, prepare, update]);

  const connect = useCallback(
    (
      call: { call_id: string; call_type: CallType },
      token: string,
      serverUrl: string,
      initialIntent?: MediaIntent,
    ): Promise<MediaIntent | undefined> => {
      if (connectedCallIdRef.current === call.call_id) return Promise.resolve(undefined);
      if (connectPromiseRef.current?.callId === call.call_id) {
        return connectPromiseRef.current.promise;
      }
      const generation = generationRef.current;
      const connecting = (async (): Promise<MediaIntent | undefined> => {
        let session: LiveKitSession | null = null;
        microphoneEnabledRef.current = false;
        update({
          ...initialState,
          status: "connecting",
          mediaLoading: !factoryRef.current,
          audioActivationRequired: audioActivationRequiredRef.current,
        });
        try {
          session = await ensureSession();
          if (!session || generationRef.current !== generation) return undefined;
          await session.connect(serverUrl, token);
          if (sessionRef.current !== session || generationRef.current !== generation) {
            return undefined;
          }
          if (initialIntent) {
            // Recovery (issue #610): apply the restored snapshot EXACTLY —
            // never enable-then-disable, never getUserMedia for a device
            // whose restored intent is false. Both setters take the target
            // boolean directly, unlike enableCamera/enableMicrophone below.
            await session.setCameraEnabled(initialIntent.camera);
            if (sessionRef.current !== session || generationRef.current !== generation) {
              return undefined;
            }
            update({ cameraEnabled: initialIntent.camera });
            await session.setMicrophoneEnabled(initialIntent.microphone);
            if (sessionRef.current !== session || generationRef.current !== generation) {
              return undefined;
            }
            connectedCallIdRef.current = call.call_id;
            update({ status: "connected", error: null });
            return { microphone: initialIntent.microphone, camera: initialIntent.camera };
          }
          // Fresh (issue #610): unchanged entry defaults.
          let cameraEnabled = false;
          if (call.call_type === "video") {
            await session.enableCamera();
            if (sessionRef.current !== session || generationRef.current !== generation) {
              return undefined;
            }
            update({ cameraEnabled: true });
            cameraEnabled = true;
          }
          await session.enableMicrophone();
          if (sessionRef.current !== session || generationRef.current !== generation) {
            return undefined;
          }
          connectedCallIdRef.current = call.call_id;
          // microphoneEnabled itself was already confirmed by
          // onMicrophoneStateChanged as part of enableMicrophone() above.
          update({ status: "connected", error: null });
          return { microphone: true, camera: cameraEnabled };
        } catch (error) {
          if (generationRef.current === generation) {
            if (session && sessionRef.current === session) {
              sessionRef.current = null;
              generationRef.current += 1;
              clearElements();
              // Tracked, not best-effort: this session may already have
              // reached LiveKit or enabled camera/mic, so a rejection here
              // must leave it in disconnectingSessionRef for a real retry —
              // never lost the way a bare .catch(() => undefined) would.
              // The primary connect failure below is what this call
              // actually reports; the cleanup outcome is only swallowed
              // here to avoid overriding it, never discarded from state.
              await trackedDisconnect(session).catch(() => undefined);
            }
            connectedCallIdRef.current = "";
            microphoneEnabledRef.current = false;
            const denied = mediaErrorKind(error)?.endsWith("_denied") ?? false;
            update({
              ...initialState,
              status: denied ? "permission-denied" : "error",
              audioActivationRequired: audioActivationRequiredRef.current,
              error: session
                ? mediaErrorMessage(error, "connect")
                : "Não foi possível carregar os recursos da chamada.",
            });
          }
          throw error;
        }
      })();
      connectPromiseRef.current = { callId: call.call_id, promise: connecting };
      const clearPendingConnection = () => {
        if (connectPromiseRef.current?.promise === connecting) {
          connectPromiseRef.current = null;
        }
      };
      void connecting.then(clearPendingConnection, clearPendingConnection);
      return connecting;
    },
    [clearElements, ensureSession, trackedDisconnect, update],
  );

  const toggleMicrophone = useCallback(async (): Promise<boolean | undefined> => {
    const session = sessionRef.current;
    if (!session || pendingControlRef.current) return undefined;
    const operation: PendingMediaOperation = {
      control: "microphone",
      session,
      generation: generationRef.current,
    };
    pendingControlRef.current = operation;
    update({ pendingControl: "microphone", error: null });
    const next = !microphoneEnabledRef.current;
    try {
      // Intent is the inverse of the last SDK-confirmed state (ref), never a
      // stale React closure: onMicrophoneStateChanged keeps the ref current,
      // including corrections from reconnection or a remote/server mute.
      await session.setMicrophoneEnabled(next);
      // The confirmed value itself is applied by onMicrophoneStateChanged,
      // which fires synchronously inside setMicrophoneEnabled above.
      if (pendingControlRef.current !== operation) return undefined;
      return microphoneEnabledRef.current;
    } catch (error) {
      if (pendingControlRef.current === operation) {
        update({ error: mediaErrorMessage(error, "microphone") });
      }
      return undefined;
    } finally {
      if (pendingControlRef.current === operation) {
        pendingControlRef.current = null;
        update({ pendingControl: null });
      }
    }
  }, [update]);

  const toggleCamera = useCallback(async (): Promise<boolean | undefined> => {
    const session = sessionRef.current;
    if (!session || pendingControlRef.current) return undefined;
    const operation: PendingMediaOperation = {
      control: "camera",
      session,
      generation: generationRef.current,
    };
    pendingControlRef.current = operation;
    update({ pendingControl: "camera", error: null });
    const enabled = !state.cameraEnabled;
    try {
      await session.setCameraEnabled(enabled);
      if (pendingControlRef.current !== operation) return undefined;
      update({ cameraEnabled: enabled });
      return enabled;
    } catch (error) {
      if (pendingControlRef.current === operation) {
        update({ error: mediaErrorMessage(error, "camera") });
      }
      return undefined;
    } finally {
      if (pendingControlRef.current === operation) {
        pendingControlRef.current = null;
        update({ pendingControl: null });
      }
    }
  }, [state.cameraEnabled, update]);

  const toggleScreenShare = useCallback(async () => {
    const session = sessionRef.current;
    if (!session || pendingControlRef.current) return;
    const operation: PendingMediaOperation = {
      control: "screen-share",
      session,
      generation: generationRef.current,
    };
    pendingControlRef.current = operation;
    update({ pendingControl: "screen-share", error: null });
    try {
      await session.setScreenShareEnabled(!screenShareEnabledRef.current);
    } catch {
      if (pendingControlRef.current === operation) {
        update({ error: "Não foi possível alterar o compartilhamento de tela." });
      }
    } finally {
      if (pendingControlRef.current === operation) {
        pendingControlRef.current = null;
        update({ pendingControl: null });
      }
    }
  }, [update]);

  const bindLocalMedia = useCallback<RefCallback<HTMLDivElement>>((element) => {
    localContainerRef.current = element;
    if (element && localElementRef.current) element.replaceChildren(localElementRef.current);
  }, []);

  const bindRemoteMedia = useCallback<RefCallback<HTMLDivElement>>((element) => {
    remoteContainerRef.current = element;
    if (element) element.replaceChildren(...remoteElementsRef.current);
  }, []);

  return {
    ...state,
    bindLocalMedia,
    bindRemoteMedia,
    bindRemoteAudio,
    toggleMicrophone,
    toggleCamera,
    toggleScreenShare,
    activateAudio: startAudio,
    prepare,
    startAudio,
    connect,
    stop,
  };
}

function mediaErrorKind(error: unknown): string | undefined {
  if (!error || typeof error !== "object" || !("kind" in error)) return undefined;
  return typeof error.kind === "string" ? error.kind : undefined;
}

function mediaErrorMessage(error: unknown, action: "connect" | "camera" | "microphone"): string {
  switch (mediaErrorKind(error)) {
    case "camera_denied":
      return "Permissão de câmera negada pelo navegador.";
    case "microphone_denied":
      return "Permissão de microfone negada pelo navegador.";
    case "camera_unavailable":
      return "Não foi possível acessar ou alterar a câmera.";
    case "microphone_unavailable":
      return "Não foi possível acessar ou alterar o microfone.";
    default:
      return action === "connect"
        ? "Não foi possível conectar a mídia da chamada."
        : action === "camera"
          ? "Não foi possível alterar a câmera."
          : "Não foi possível alterar o microfone.";
  }
}
