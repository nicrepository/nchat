import { type FormEvent, useEffect, useRef, useState } from "react";

import "./LiveKitSpikePage.css";
import {
  createLiveKitSpikeSession,
  SpikeMediaError,
  type LiveKitSpikeSession,
  type LiveKitSpikeSessionCallbacks,
  type LiveKitSpikeSessionFactory,
} from "./liveKitSpikeSession";
import { requestSpikeToken, type SpikeTokenRequester } from "./mediaSpikeApi";

type PageStatus = "disconnected" | "connecting" | "connected" | "error";
type AttemptCancelReason = "none" | "timeout" | "user" | "unmount" | "replaced" | "disconnected";

interface LiveKitSpikePageProps {
  sessionFactory?: LiveKitSpikeSessionFactory;
  tokenRequester?: SpikeTokenRequester;
}

interface ConnectionAttempt {
  readonly id: number;
  readonly controller: AbortController;
  session: LiveKitSpikeSession | null;
  invalidated: boolean;
  cancelReason: AttemptCancelReason;
  timeoutId: number | null;
  disposePromise: Promise<void> | null;
}

const connectionTimeoutMilliseconds = 8000;

const statusLabels: Record<PageStatus, string> = {
  disconnected: "Desconectado",
  connecting: "Conectando",
  connected: "Conectado",
  error: "Erro",
};

export default function LiveKitSpikePage({
  sessionFactory = createLiveKitSpikeSession,
  tokenRequester = requestSpikeToken,
}: LiveKitSpikePageProps) {
  const [roomName, setRoomName] = useState(
    () => import.meta.env.VITE_MEDIA_SPIKE_ROOM ?? "spike-1to1",
  );
  const [identity, setIdentity] = useState(generateBrowserIdentity);
  const [name, setName] = useState("Browser local");
  const [status, setStatus] = useState<PageStatus>("disconnected");
  const [errorMessage, setErrorMessage] = useState("");
  const [cameraEnabled, setCameraEnabledState] = useState(false);
  const [microphoneEnabled, setMicrophoneEnabledState] = useState(false);
  const [hasLocalVideo, setHasLocalVideo] = useState(false);
  const [remoteElementCount, setRemoteElementCount] = useState(0);

  const sessionRef = useRef<LiveKitSpikeSession | null>(null);
  const activeAttemptRef = useRef<ConnectionAttempt | null>(null);
  const connectInFlightRef = useRef(false);
  const nextAttemptIdRef = useRef(0);
  const mountedRef = useRef(true);
  const localMediaRef = useRef<HTMLDivElement>(null);
  const remoteMediaRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    mountedRef.current = true;
    const localMedia = localMediaRef.current;
    const remoteMedia = remoteMediaRef.current;
    return () => {
      mountedRef.current = false;
      const attempt = activeAttemptRef.current;
      const session = attempt?.session ?? sessionRef.current;
      if (attempt) {
        cancelAttempt(attempt, "unmount");
      }
      activeAttemptRef.current = null;
      sessionRef.current = null;
      connectInFlightRef.current = false;
      if (attempt) {
        void disconnectAttempt(attempt);
      } else if (session) {
        void session.disconnect().catch(() => undefined);
      }
      localMedia?.replaceChildren();
      remoteMedia?.replaceChildren();
    };
  }, []);

  function isAttemptCurrent(attempt: ConnectionAttempt): boolean {
    return (
      mountedRef.current &&
      !attempt.invalidated &&
      !attempt.controller.signal.aborted &&
      activeAttemptRef.current === attempt &&
      attempt.session !== null &&
      sessionRef.current === attempt.session
    );
  }

  function invalidateAttempt(
    attempt: ConnectionAttempt,
    reason: AttemptCancelReason = attempt.cancelReason,
  ): void {
    cancelAttempt(attempt, reason);
    if (activeAttemptRef.current === attempt) activeAttemptRef.current = null;
    if (sessionRef.current === attempt.session) sessionRef.current = null;
    connectInFlightRef.current = false;
  }

  async function disposeInvalidAttempt(
    attempt: ConnectionAttempt,
    reason: AttemptCancelReason = attempt.cancelReason,
  ): Promise<void> {
    invalidateAttempt(attempt, reason);
    await disconnectAttempt(attempt);
  }

  function handleAttemptTimeout(attempt: ConnectionAttempt): void {
    if (!isAttemptCurrent(attempt)) return;
    void disposeInvalidAttempt(attempt, "timeout");
    if (
      !mountedRef.current ||
      nextAttemptIdRef.current !== attempt.id ||
      attempt.cancelReason !== "timeout"
    ) {
      return;
    }
    resetMediaState();
    setStatus("error");
    setErrorMessage("A conexão demorou mais que o esperado. Tente novamente.");
  }

  function buildSessionCallbacks(attempt: ConnectionAttempt): LiveKitSpikeSessionCallbacks {
    return {
      onLocalElement(element) {
        if (!isAttemptCurrent(attempt) || !localMediaRef.current) return;
        localMediaRef.current.replaceChildren(element);
        setHasLocalVideo(true);
      },
      onRemoteElement(element) {
        if (!isAttemptCurrent(attempt) || !remoteMediaRef.current) return;
        remoteMediaRef.current.append(element);
        setRemoteElementCount((count) => count + 1);
      },
      onElementRemoved(element) {
        const wasLocal = localMediaRef.current?.contains(element) ?? false;
        const wasRemote = remoteMediaRef.current?.contains(element) ?? false;
        element.remove();
        if (!isAttemptCurrent(attempt)) return;
        if (wasLocal) setHasLocalVideo(false);
        if (wasRemote) setRemoteElementCount((count) => Math.max(0, count - 1));
      },
      onDisconnected() {
        if (!isAttemptCurrent(attempt)) return;
        void disposeInvalidAttempt(attempt, "disconnected");
        resetMediaState();
        setStatus("error");
        setErrorMessage("A conexão com o LiveKit foi encerrada.");
      },
      onReconnecting() {
        if (isAttemptCurrent(attempt)) setStatus("connecting");
      },
      onReconnected() {
        if (isAttemptCurrent(attempt)) setStatus("connected");
      },
      onAudioPlaybackChanged: () => undefined,
      onMicrophoneStateChanged(enabled) {
        if (isAttemptCurrent(attempt)) setMicrophoneEnabledState(enabled);
      },
      // This PoC page only ever shows the single-remote 1:1 spike flow — see
      // media/liveKitSession.ts for the multi-participant (RF-24) consumer.
      onParticipantConnected: () => undefined,
      onParticipantDisconnected: () => undefined,
      onRemoteVideoAvailabilityChanged: () => undefined,
      onActiveSpeakersChanged: () => undefined,
      onScreenShareChanged: () => undefined,
      onRemoteScreenShareChanged: () => undefined,
    };
  }

  function resetMediaState(): void {
    setCameraEnabledState(false);
    setMicrophoneEnabledState(false);
    setHasLocalVideo(false);
    setRemoteElementCount(0);
    localMediaRef.current?.replaceChildren();
    remoteMediaRef.current?.replaceChildren();
  }

  async function handleConnect(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (connectInFlightRef.current) return;

    const previousAttempt = activeAttemptRef.current;
    if (previousAttempt) {
      void disposeInvalidAttempt(previousAttempt, "replaced");
      resetMediaState();
    }

    connectInFlightRef.current = true;
    setStatus("connecting");
    setErrorMessage("");
    const controller = new AbortController();
    const attempt: ConnectionAttempt = {
      id: ++nextAttemptIdRef.current,
      controller,
      session: null,
      invalidated: false,
      cancelReason: "none",
      timeoutId: null,
      disposePromise: null,
    };
    activeAttemptRef.current = attempt;
    attempt.timeoutId = window.setTimeout(
      () => handleAttemptTimeout(attempt),
      connectionTimeoutMilliseconds,
    );
    let stage: "audio" | "token" | "connect" | "camera" | "microphone" = "audio";

    try {
      // Browser autoplay policies require this call to originate from the submit click.
      const session = sessionFactory(buildSessionCallbacks(attempt));
      attempt.session = session;
      sessionRef.current = session;
      if (!isAttemptCurrent(attempt)) {
        await disposeInvalidAttempt(attempt);
        return;
      }
      await session.startAudio();
      if (!isAttemptCurrent(attempt)) {
        await disposeInvalidAttempt(attempt);
        return;
      }
      stage = "token";
      const tokenResponse = await tokenRequester(
        { room: roomName.trim(), identity: identity.trim(), name: name.trim() },
        controller.signal,
      );
      if (!isAttemptCurrent(attempt)) {
        await disposeInvalidAttempt(attempt);
        return;
      }

      stage = "connect";
      await session.connect(tokenResponse.serverUrl, tokenResponse.token);
      if (!isAttemptCurrent(attempt)) {
        await disposeInvalidAttempt(attempt);
        return;
      }
      stage = "camera";
      await session.enableCamera();
      if (!isAttemptCurrent(attempt)) {
        await disposeInvalidAttempt(attempt);
        return;
      }
      setCameraEnabledState(true);
      stage = "microphone";
      await session.enableMicrophone();
      if (!isAttemptCurrent(attempt)) {
        await disposeInvalidAttempt(attempt);
        return;
      }
      setMicrophoneEnabledState(true);
      if (isAttemptCurrent(attempt)) setStatus("connected");
    } catch (error) {
      const mayReportError = isAttemptCurrent(attempt);
      if (mayReportError) await disposeInvalidAttempt(attempt);
      if (
        mayReportError &&
        mountedRef.current &&
        nextAttemptIdRef.current === attempt.id &&
        activeAttemptRef.current === null
      ) {
        resetMediaState();
        setStatus("error");
        setErrorMessage(connectErrorMessage(stage, error));
      }
    } finally {
      clearAttemptTimeout(attempt);
      if (activeAttemptRef.current === attempt) connectInFlightRef.current = false;
    }
  }

  async function handleLeave(): Promise<void> {
    const attempt = activeAttemptRef.current;
    setErrorMessage("");
    resetMediaState();
    setStatus("disconnected");
    if (attempt) await disposeInvalidAttempt(attempt, "user");
  }

  async function toggleCamera(): Promise<void> {
    const attempt = activeAttemptRef.current;
    const session = sessionRef.current;
    if (!attempt || !session || !isAttemptCurrent(attempt)) return;
    const next = !cameraEnabled;
    try {
      await session.setCameraEnabled(next);
      if (!isAttemptCurrent(attempt)) return;
      setCameraEnabledState(next);
      setErrorMessage("");
    } catch (error) {
      if (isAttemptCurrent(attempt)) setErrorMessage(controlErrorMessage("camera", error));
    }
  }

  async function toggleMicrophone(): Promise<void> {
    const attempt = activeAttemptRef.current;
    const session = sessionRef.current;
    if (!attempt || !session || !isAttemptCurrent(attempt)) return;
    const next = !microphoneEnabled;
    try {
      await session.setMicrophoneEnabled(next);
      if (!isAttemptCurrent(attempt)) return;
      setMicrophoneEnabledState(next);
      setErrorMessage("");
    } catch (error) {
      if (isAttemptCurrent(attempt)) {
        setErrorMessage(controlErrorMessage("microphone", error));
      }
    }
  }

  const busy = status === "connecting";
  const connected = status === "connected";

  return (
    <main className="media-spike">
      <header className="media-spike__header">
        <p className="media-spike__eyebrow">Somente desenvolvimento</p>
        <h1>Spike LiveKit — chamada 1:1</h1>
        <p>Prova de conceito descartável para validar áudio e vídeo locais.</p>
      </header>

      <section className="media-spike__panel" aria-label="Configuração da chamada">
        <div className="media-spike__status" data-status={status} aria-live="polite">
          <span aria-hidden="true" />
          {statusLabels[status]}
        </div>
        <form onSubmit={handleConnect}>
          <label>
            Sala
            <input
              value={roomName}
              onChange={(event) => setRoomName(event.target.value)}
              pattern={"[A-Za-z0-9_\\-]{1,64}"}
              maxLength={64}
              required
              disabled={busy || connected}
            />
          </label>
          <label>
            Identidade
            <input
              value={identity}
              onChange={(event) => setIdentity(event.target.value)}
              pattern={"[A-Za-z0-9_\\-]{1,64}"}
              maxLength={64}
              required
              disabled={busy || connected}
            />
          </label>
          <label>
            Nome
            <input
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={64}
              disabled={busy || connected}
            />
          </label>
          <button type="submit" disabled={busy || connected}>
            {busy ? "Conectando…" : "Entrar na chamada"}
          </button>
        </form>
        {errorMessage && <p role="alert">{errorMessage}</p>}
      </section>

      <section className="media-spike__videos" aria-label="Vídeos da chamada">
        <article aria-label="Vídeo local">
          <div className="media-spike__video-label">Você</div>
          <div className="media-spike__media" ref={localMediaRef} />
          {!hasLocalVideo && <p>A câmera local aparecerá após a conexão.</p>}
        </article>
        <article aria-label="Mídia remota">
          <div className="media-spike__video-label">Participante remoto</div>
          <div className="media-spike__media" ref={remoteMediaRef} />
          {remoteElementCount === 0 && <p>Aguardando outro navegador na mesma sala.</p>}
        </article>
      </section>

      <nav className="media-spike__controls" aria-label="Controles da chamada">
        <button type="button" onClick={toggleMicrophone} disabled={!connected}>
          {microphoneEnabled ? "Mutar microfone" : "Ativar microfone"}
        </button>
        <button type="button" onClick={toggleCamera} disabled={!connected}>
          {cameraEnabled ? "Desligar câmera" : "Ligar câmera"}
        </button>
        <button
          type="button"
          className="media-spike__leave"
          onClick={handleLeave}
          disabled={!connected && !busy}
        >
          Sair da chamada
        </button>
      </nav>
    </main>
  );
}

function clearAttemptTimeout(attempt: ConnectionAttempt): void {
  if (attempt.timeoutId === null) return;
  window.clearTimeout(attempt.timeoutId);
  attempt.timeoutId = null;
}

function cancelAttempt(attempt: ConnectionAttempt, reason: AttemptCancelReason): void {
  clearAttemptTimeout(attempt);
  if (attempt.invalidated) return;
  attempt.cancelReason = reason;
  attempt.invalidated = true;
  attempt.controller.abort();
}

function disconnectAttempt(attempt: ConnectionAttempt): Promise<void> {
  if (attempt.disposePromise) return attempt.disposePromise;
  attempt.disposePromise = (async () => {
    try {
      await attempt.session?.disconnect();
    } catch {
      // Teardown is best effort; stale cleanup errors must not replace current UI state.
    }
  })();
  return attempt.disposePromise;
}

function generateBrowserIdentity(): string {
  const suffix =
    typeof crypto.randomUUID === "function"
      ? crypto.randomUUID().slice(0, 8)
      : Math.random().toString(36).slice(2, 10);
  return `browser-${suffix}`;
}

function connectErrorMessage(
  stage: "audio" | "token" | "connect" | "camera" | "microphone",
  error: unknown,
): string {
  if (stage === "audio") return "Não foi possível habilitar a reprodução de áudio.";
  if (stage === "token") return "Não foi possível obter o token da chamada.";
  if (stage === "connect") return "LiveKit indisponível ou conexão recusada.";
  return controlErrorMessage(stage, error);
}

function controlErrorMessage(device: "camera" | "microphone", error: unknown): string {
  if (error instanceof SpikeMediaError) {
    if (error.kind === "camera_denied") return "Permissão de câmera negada pelo navegador.";
    if (error.kind === "microphone_denied") return "Permissão de microfone negada pelo navegador.";
  }
  return device === "camera"
    ? "Não foi possível acessar ou alterar a câmera."
    : "Não foi possível acessar ou alterar o microfone.";
}
