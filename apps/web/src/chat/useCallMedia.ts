import { useCallback, useEffect, useRef, useState, type RefCallback } from "react";

import {
  loadLiveKitSessionFactory,
  type LiveKitSession,
  type LiveKitSessionCallbacks,
  type LiveKitSessionFactory,
  type LiveKitSessionLoader,
} from "../media/liveKitSession";
import type { CallType } from "./callState";

export type CallMediaStatus =
  | "idle"
  | "connecting"
  | "connected"
  | "reconnecting"
  | "permission-denied"
  | "error";

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
  pendingControl: "microphone" | "camera" | null;
  bindLocalMedia: RefCallback<HTMLDivElement>;
  bindRemoteMedia: RefCallback<HTMLDivElement>;
  toggleMicrophone: () => Promise<void>;
  toggleCamera: () => Promise<void>;
  activateAudio: () => Promise<void>;
}

export interface CallMediaSessionController extends CallMediaController {
  prepare: () => Promise<void>;
  startAudio: () => Promise<void>;
  connect: (call: { call_id: string; call_type: CallType }, token: string) => Promise<void>;
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
  pendingControl: "microphone" | "camera" | null;
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
};

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
  const connectPromiseRef = useRef<{ callId: string; promise: Promise<void> } | null>(null);
  const audioStartPromiseRef = useRef<{
    generation: number;
    session: LiveKitSession;
    promise: Promise<void>;
  } | null>(null);
  const disconnectPromiseRef = useRef<Promise<void> | null>(null);
  const connectedCallIdRef = useRef("");
  const audioActivationRequiredRef = useRef(false);
  const pendingControlRef = useRef<"microphone" | "camera" | null>(null);
  const localContainerRef = useRef<HTMLDivElement | null>(null);
  const remoteContainerRef = useRef<HTMLDivElement | null>(null);
  const localElementRef = useRef<HTMLMediaElement | null>(null);
  const remoteElementsRef = useRef(new Set<HTMLMediaElement>());

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
          if (!current() || remoteElementsRef.current.has(element)) return;
          remoteElementsRef.current.add(element);
          remoteContainerRef.current?.append(element);
          syncRemoteState();
        },
        onElementRemoved(element) {
          if (!current()) return;
          if (localElementRef.current === element) {
            localElementRef.current = null;
            update({ hasLocalVideo: false });
          }
          remoteElementsRef.current.delete(element);
          element.remove();
          syncRemoteState();
        },
        onDisconnected() {
          if (current()) {
            update({
              status: "error",
              error: "A conexão de mídia foi encerrada. Tente novamente.",
            });
          }
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
      };
    },
    [update],
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
    const loading = loadFactory()
      .then((factory) => {
        if (!mountedRef.current || generationRef.current !== generation) return null;
        if (sessionRef.current) return sessionRef.current;
        const session = factory(callbacksFor(generation));
        sessionRef.current = session;
        disconnectPromiseRef.current = null;
        return session;
      })
      .finally(() => {
        if (sessionPromiseRef.current === loading) sessionPromiseRef.current = null;
      });
    sessionPromiseRef.current = loading;
    return loading;
  }, [callbacksFor, loadFactory]);

  const stop = useCallback(async () => {
    if (disconnectPromiseRef.current) return disconnectPromiseRef.current;
    const session = sessionRef.current;
    sessionRef.current = null;
    sessionPromiseRef.current = null;
    connectPromiseRef.current = null;
    audioStartPromiseRef.current = null;
    connectedCallIdRef.current = "";
    pendingControlRef.current = null;
    audioActivationRequiredRef.current = false;
    generationRef.current += 1;
    clearElements();
    update(initialState);
    if (!session) return;
    const disconnecting = session.disconnect().catch(() => undefined);
    disconnectPromiseRef.current = disconnecting;
    await disconnecting;
    if (disconnectPromiseRef.current === disconnecting) disconnectPromiseRef.current = null;
  }, [clearElements, update]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      void stop();
    };
  }, [stop]);

  const startAudio = useCallback(async () => {
    if (!factoryRef.current) {
      audioActivationRequiredRef.current = true;
      update({ audioActivationRequired: true });
      await prepare();
      return;
    }
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
    (call: { call_id: string; call_type: CallType }, token: string): Promise<void> => {
      if (connectedCallIdRef.current === call.call_id) return Promise.resolve();
      if (connectPromiseRef.current?.callId === call.call_id) {
        return connectPromiseRef.current.promise;
      }
      const generation = generationRef.current;
      const connecting = (async () => {
        let session: LiveKitSession | null = null;
        update({
          ...initialState,
          status: "connecting",
          mediaLoading: !factoryRef.current,
          audioActivationRequired: audioActivationRequiredRef.current,
        });
        try {
          session = await ensureSession();
          if (!session || generationRef.current !== generation) return;
          await session.connect(liveKitServerUrl(), token);
          if (sessionRef.current !== session || generationRef.current !== generation) return;
          if (call.call_type === "video") {
            await session.enableCamera();
            if (sessionRef.current !== session || generationRef.current !== generation) return;
            update({ cameraEnabled: true });
          }
          await session.enableMicrophone();
          if (sessionRef.current !== session || generationRef.current !== generation) return;
          connectedCallIdRef.current = call.call_id;
          update({ status: "connected", microphoneEnabled: true, error: null });
        } catch (error) {
          if (generationRef.current === generation) {
            if (session && sessionRef.current === session) {
              sessionRef.current = null;
              generationRef.current += 1;
              clearElements();
              await session.disconnect().catch(() => undefined);
            }
            connectedCallIdRef.current = "";
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
    [clearElements, ensureSession, update],
  );

  const toggleMicrophone = useCallback(async () => {
    const session = sessionRef.current;
    if (!session || pendingControlRef.current) return;
    pendingControlRef.current = "microphone";
    update({ pendingControl: "microphone", error: null });
    try {
      const enabled = !state.microphoneEnabled;
      await session.setMicrophoneEnabled(enabled);
      if (sessionRef.current === session) update({ microphoneEnabled: enabled });
    } catch (error) {
      if (sessionRef.current === session) {
        update({ error: mediaErrorMessage(error, "microphone") });
      }
    } finally {
      if (pendingControlRef.current === "microphone") {
        pendingControlRef.current = null;
        update({ pendingControl: null });
      }
    }
  }, [state.microphoneEnabled, update]);

  const toggleCamera = useCallback(async () => {
    const session = sessionRef.current;
    if (!session || pendingControlRef.current) return;
    pendingControlRef.current = "camera";
    update({ pendingControl: "camera", error: null });
    try {
      const enabled = !state.cameraEnabled;
      await session.setCameraEnabled(enabled);
      if (sessionRef.current === session) update({ cameraEnabled: enabled });
    } catch (error) {
      if (sessionRef.current === session) {
        update({ error: mediaErrorMessage(error, "camera") });
      }
    } finally {
      if (pendingControlRef.current === "camera") {
        pendingControlRef.current = null;
        update({ pendingControl: null });
      }
    }
  }, [state.cameraEnabled, update]);

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
    toggleMicrophone,
    toggleCamera,
    activateAudio: startAudio,
    prepare,
    startAudio,
    connect,
    stop,
  };
}

function liveKitServerUrl(): string {
  const url = new URL("/livekit", window.location.origin);
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  return url.toString().replace(/\/$/, "");
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
