import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import LiveKitSpikePage from "./LiveKitSpikePage";
import {
  SpikeMediaError,
  type LiveKitSpikeSession,
  type LiveKitSpikeSessionCallbacks,
  type LiveKitSpikeSessionFactory,
} from "./liveKitSpikeSession";
import type { SpikeTokenResponse, SpikeTokenRequester } from "./mediaSpikeApi";

const tokenResponse: SpikeTokenResponse = {
  serverUrl: "ws://127.0.0.1:7880",
  token: "participant-token",
  room: "spike-1to1",
  identity: "browser-a",
  expiresInSeconds: 300,
};

interface Deferred {
  promise: Promise<void>;
  resolve(): void;
  reject(error: unknown): void;
}

function deferred(): Deferred {
  let resolve!: () => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<void>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function deferredValue<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

class FakeSession implements LiveKitSpikeSession {
  readonly callbacks: LiveKitSpikeSessionCallbacks;
  private localVideo: HTMLVideoElement | null = null;
  readonly startAudio = vi.fn<LiveKitSpikeSession["startAudio"]>(async () => undefined);
  readonly connect = vi.fn<LiveKitSpikeSession["connect"]>(async () => undefined);
  readonly enableCamera = vi.fn<LiveKitSpikeSession["enableCamera"]>(async () => {
    this.attachLocalVideo();
  });
  readonly enableMicrophone = vi.fn<LiveKitSpikeSession["enableMicrophone"]>(async () => undefined);
  readonly setCameraEnabled = vi.fn<LiveKitSpikeSession["setCameraEnabled"]>(
    async (enabled: boolean) => {
      if (enabled) {
        this.attachLocalVideo();
      } else if (this.localVideo) {
        this.callbacks.onElementRemoved(this.localVideo);
        this.localVideo = null;
      }
    },
  );
  readonly setMicrophoneEnabled = vi.fn<LiveKitSpikeSession["setMicrophoneEnabled"]>(
    async () => undefined,
  );
  readonly setScreenShareEnabled = vi.fn<LiveKitSpikeSession["setScreenShareEnabled"]>(
    async () => undefined,
  );
  readonly disconnect = vi.fn<LiveKitSpikeSession["disconnect"]>(async () => undefined);
  readonly listMediaDevices = vi.fn<LiveKitSpikeSession["listMediaDevices"]>(async () => []);
  readonly getActiveDevice = vi.fn<LiveKitSpikeSession["getActiveDevice"]>(() => undefined);
  readonly switchActiveDevice = vi.fn<LiveKitSpikeSession["switchActiveDevice"]>(
    async () => undefined,
  );
  readonly isAudioOutputSupported = vi.fn<LiveKitSpikeSession["isAudioOutputSupported"]>(
    () => true,
  );

  constructor(callbacks: LiveKitSpikeSessionCallbacks) {
    this.callbacks = callbacks;
  }

  private attachLocalVideo(): void {
    const video = document.createElement("video");
    video.dataset.testid = "local-video";
    this.localVideo = video;
    this.callbacks.onLocalElement(video);
  }
}

function setup(configureSession?: (session: FakeSession) => void) {
  let session: FakeSession | undefined;
  const sessionFactory: LiveKitSpikeSessionFactory = (callbacks) => {
    session = new FakeSession(callbacks);
    configureSession?.(session);
    return session;
  };
  const tokenRequester = vi.fn<SpikeTokenRequester>(async () => tokenResponse);
  const view = render(
    <LiveKitSpikePage sessionFactory={sessionFactory} tokenRequester={tokenRequester} />,
  );
  return {
    ...view,
    tokenRequester,
    get session() {
      if (!session) throw new Error("session not created");
      return session;
    },
  };
}

async function connect() {
  const user = userEvent.setup();
  const identity = screen.getByLabelText(/identidade/i);
  await user.clear(identity);
  await user.type(identity, "browser-a");
  await user.click(screen.getByRole("button", { name: /entrar na chamada/i }));
  await screen.findByText("Conectado");
  return user;
}

async function flushAsyncWork(): Promise<void> {
  await act(async () => {
    for (let index = 0; index < 8; index += 1) await Promise.resolve();
  });
}

async function startConnectionWithFakeTimers(): Promise<void> {
  fireEvent.click(screen.getByRole("button", { name: /entrar na chamada/i }));
  await flushAsyncWork();
}

async function expireConnectionTimeout(): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(8000);
  });
}

describe("LiveKitSpikePage", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("renders a development-only disconnected screen without requesting media", () => {
    const view = setup();
    expect(screen.getByRole("heading", { name: /spike livekit/i })).toBeInTheDocument();
    expect(screen.getByText(/somente desenvolvimento/i)).toBeInTheDocument();
    expect(screen.getByText("Desconectado")).toBeInTheDocument();
    expect(screen.getByLabelText(/sala/i)).toHaveAttribute("pattern", "[A-Za-z0-9_\\-]{1,64}");
    expect(screen.getByLabelText(/identidade/i)).toHaveAttribute(
      "pattern",
      "[A-Za-z0-9_\\-]{1,64}",
    );
    expect(view.tokenRequester).not.toHaveBeenCalled();
  });

  it("requests a token, connects, controls media, attaches tracks, and leaves", async () => {
    const view = setup();
    const user = await connect();

    expect(view.tokenRequester).toHaveBeenCalledWith(
      expect.objectContaining({ room: "spike-1to1", identity: "browser-a" }),
      expect.any(AbortSignal),
    );
    expect(view.session.startAudio).toHaveBeenCalledOnce();
    expect(view.session.connect).toHaveBeenCalledWith("ws://127.0.0.1:7880", "participant-token");
    expect(view.session.enableCamera).toHaveBeenCalledOnce();
    expect(view.session.enableMicrophone).toHaveBeenCalledOnce();
    expect(screen.getByLabelText("Vídeo local").querySelector("video")).toBeInTheDocument();

    const remoteVideo = document.createElement("video");
    act(() => view.session.callbacks.onRemoteElement(remoteVideo));
    expect(screen.getByLabelText("Mídia remota").querySelector("video")).toBe(remoteVideo);

    act(() => view.session.callbacks.onElementRemoved(remoteVideo));
    expect(screen.getByLabelText("Mídia remota").querySelector("video")).not.toBeInTheDocument();
    expect(screen.getByText(/aguardando outro navegador/i)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /mutar microfone/i }));
    expect(view.session.setMicrophoneEnabled).toHaveBeenCalledWith(false);
    expect(screen.getByRole("button", { name: /ativar microfone/i })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /desligar câmera/i }));
    expect(view.session.setCameraEnabled).toHaveBeenCalledWith(false);
    expect(screen.getByRole("button", { name: /ligar câmera/i })).toBeInTheDocument();
    expect(screen.getByLabelText("Vídeo local").querySelector("video")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /ligar câmera/i }));
    expect(view.session.setCameraEnabled).toHaveBeenCalledWith(true);
    expect(screen.getByLabelText("Vídeo local").querySelector("video")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /sair da chamada/i }));
    expect(view.session.disconnect).toHaveBeenCalledOnce();
    expect(screen.getByText("Desconectado")).toBeInTheDocument();
  });

  it("shows a comprehensible token endpoint error", async () => {
    const tokenRequester = vi.fn<SpikeTokenRequester>(async () => {
      throw new Error("endpoint failed");
    });
    render(<LiveKitSpikePage tokenRequester={tokenRequester} />);

    await userEvent.click(screen.getByRole("button", { name: /entrar na chamada/i }));

    expect(await screen.findByText("Erro")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(/obter o token/i);
  });

  it("keeps the page disconnected when the user leaves during token loading", async () => {
    const tokenRequester = vi.fn<SpikeTokenRequester>(
      (_request, signal) =>
        new Promise((_resolve, reject) => {
          signal?.addEventListener("abort", () =>
            reject(new DOMException("request aborted", "AbortError")),
          );
        }),
    );
    render(<LiveKitSpikePage tokenRequester={tokenRequester} />);
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: /entrar na chamada/i }));
    expect(screen.getByText("Conectando")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /sair da chamada/i }));

    await waitFor(() => expect(screen.getByText("Desconectado")).toBeInTheDocument());
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it.each([
    ["camera_denied", "enableCamera", /permissão de câmera negada/i],
    ["microphone_denied", "enableMicrophone", /permissão de microfone negada/i],
  ] as const)("reports %s and disconnects partial media", async (kind, method, message) => {
    const view = setup((session) => {
      session[method].mockRejectedValueOnce(new SpikeMediaError(kind));
    });
    await userEvent.click(screen.getByRole("button", { name: /entrar na chamada/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(message);
    expect(view.session.disconnect).toHaveBeenCalledOnce();
  });

  it("reports LiveKit unavailability and an unexpected disconnect", async () => {
    let session: FakeSession | undefined;
    const factory: LiveKitSpikeSessionFactory = (callbacks) => {
      session = new FakeSession(callbacks);
      session.connect.mockRejectedValueOnce(new Error("server unavailable"));
      return session;
    };
    const tokenRequester = vi.fn<SpikeTokenRequester>(async () => tokenResponse);
    const first = render(
      <LiveKitSpikePage sessionFactory={factory} tokenRequester={tokenRequester} />,
    );
    await userEvent.click(screen.getByRole("button", { name: /entrar na chamada/i }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/livekit indisponível/i);
    first.unmount();

    const view = setup();
    await connect();
    act(() => view.session.callbacks.onDisconnected());
    expect(screen.getByText("Erro")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(/conexão.*encerrada/i);
    await waitFor(() => expect(view.session.disconnect).toHaveBeenCalledOnce());
  });

  it("disconnects and stops the active session when unmounted", async () => {
    const view = setup();
    await connect();
    view.unmount();
    await waitFor(() => expect(view.session.disconnect).toHaveBeenCalledOnce());
  });

  it("does not enable media when leaving during session.connect", async () => {
    const connectDeferred = deferred();
    const view = setup((session) => {
      session.connect.mockImplementationOnce(() => connectDeferred.promise);
    });
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: /entrar na chamada/i }));
    await waitFor(() => expect(view.session.connect).toHaveBeenCalledOnce());
    await user.click(screen.getByRole("button", { name: /sair da chamada/i }));
    expect(screen.getByText("Desconectado")).toBeInTheDocument();

    await act(async () => connectDeferred.resolve());

    expect(view.session.enableCamera).not.toHaveBeenCalled();
    expect(view.session.enableMicrophone).not.toHaveBeenCalled();
    expect(view.session.disconnect).toHaveBeenCalled();
    expect(screen.getByText("Desconectado")).toBeInTheDocument();
  });

  it("does not enable the microphone when leaving during enableCamera", async () => {
    const cameraDeferred = deferred();
    const view = setup((session) => {
      session.enableCamera.mockImplementationOnce(() => cameraDeferred.promise);
    });
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: /entrar na chamada/i }));
    await waitFor(() => expect(view.session.enableCamera).toHaveBeenCalledOnce());
    await user.click(screen.getByRole("button", { name: /sair da chamada/i }));

    await act(async () => cameraDeferred.resolve());

    expect(view.session.enableMicrophone).not.toHaveBeenCalled();
    expect(screen.getByText("Desconectado")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /ligar câmera/i })).toBeDisabled();
  });

  it("remains disconnected when leaving during enableMicrophone", async () => {
    const microphoneDeferred = deferred();
    const view = setup((session) => {
      session.enableMicrophone.mockImplementationOnce(() => microphoneDeferred.promise);
    });
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: /entrar na chamada/i }));
    await waitFor(() => expect(view.session.enableMicrophone).toHaveBeenCalledOnce());
    await user.click(screen.getByRole("button", { name: /sair da chamada/i }));

    await act(async () => microphoneDeferred.resolve());

    expect(screen.getByText("Desconectado")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /ativar microfone/i })).toBeDisabled();
    expect(view.session.disconnect).toHaveBeenCalled();
  });

  it("invalidates media activation after an unexpected disconnect", async () => {
    const cameraDeferred = deferred();
    const view = setup((session) => {
      session.enableCamera.mockImplementationOnce(() => cameraDeferred.promise);
    });

    await userEvent.click(screen.getByRole("button", { name: /entrar na chamada/i }));
    await waitFor(() => expect(view.session.enableCamera).toHaveBeenCalledOnce());
    act(() => view.session.callbacks.onDisconnected());
    expect(screen.getByText("Erro")).toBeInTheDocument();

    await act(async () => cameraDeferred.resolve());

    expect(view.session.enableMicrophone).not.toHaveBeenCalled();
    expect(screen.getByText("Erro")).toBeInTheDocument();
    expect(screen.queryByText("Conectado")).not.toBeInTheDocument();
  });

  it("ignores a pending connection after unmount without React updates", async () => {
    const connectDeferred = deferred();
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const view = setup((session) => {
      session.connect.mockImplementationOnce(() => connectDeferred.promise);
    });

    await userEvent.click(screen.getByRole("button", { name: /entrar na chamada/i }));
    await waitFor(() => expect(view.session.connect).toHaveBeenCalledOnce());
    view.unmount();
    await act(async () => connectDeferred.resolve());

    expect(view.session.disconnect).toHaveBeenCalled();
    expect(view.session.enableCamera).not.toHaveBeenCalled();
    expect(view.session.enableMicrophone).not.toHaveBeenCalled();
    expect(consoleError).not.toHaveBeenCalled();
  });

  it("allows only the replacement session to complete", async () => {
    const firstConnect = deferred();
    const sessions: FakeSession[] = [];
    const sessionFactory: LiveKitSpikeSessionFactory = (callbacks) => {
      const session = new FakeSession(callbacks);
      if (sessions.length === 0) {
        session.connect.mockImplementationOnce(() => firstConnect.promise);
      }
      sessions.push(session);
      return session;
    };
    render(
      <LiveKitSpikePage
        sessionFactory={sessionFactory}
        tokenRequester={vi.fn<SpikeTokenRequester>(async () => tokenResponse)}
      />,
    );
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: /entrar na chamada/i }));
    await waitFor(() => expect(sessions[0]?.connect).toHaveBeenCalledOnce());
    await user.click(screen.getByRole("button", { name: /sair da chamada/i }));
    await user.click(screen.getByRole("button", { name: /entrar na chamada/i }));
    await screen.findByText("Conectado");

    await act(async () => firstConnect.resolve());

    expect(sessions[0]?.enableCamera).not.toHaveBeenCalled();
    expect(sessions[0]?.disconnect).toHaveBeenCalled();
    expect(sessions[1]?.enableCamera).toHaveBeenCalledOnce();
    expect(sessions[1]?.enableMicrophone).toHaveBeenCalledOnce();
    expect(screen.getByText("Conectado")).toBeInTheDocument();
  });

  it("ignores onDisconnected from a replaced session", async () => {
    const sessions: FakeSession[] = [];
    const sessionFactory: LiveKitSpikeSessionFactory = (callbacks) => {
      const session = new FakeSession(callbacks);
      sessions.push(session);
      return session;
    };
    render(
      <LiveKitSpikePage
        sessionFactory={sessionFactory}
        tokenRequester={vi.fn<SpikeTokenRequester>(async () => tokenResponse)}
      />,
    );
    const user = await connect();
    await user.click(screen.getByRole("button", { name: /sair da chamada/i }));
    await user.click(screen.getByRole("button", { name: /entrar na chamada/i }));
    await screen.findByText("Conectado");
    const secondRemote = document.createElement("video");
    act(() => sessions[1]?.callbacks.onRemoteElement(secondRemote));
    const staleLocal = document.createElement("video");
    const staleRemote = document.createElement("audio");
    const staleRemoved = document.createElement("video");

    act(() => {
      sessions[0]?.callbacks.onLocalElement(staleLocal);
      sessions[0]?.callbacks.onRemoteElement(staleRemote);
      sessions[0]?.callbacks.onElementRemoved(staleRemoved);
      sessions[0]?.callbacks.onDisconnected();
    });

    expect(screen.getByText("Conectado")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Mídia remota").querySelector("video")).toBe(secondRemote);
    expect(screen.getByLabelText("Vídeo local").contains(staleLocal)).toBe(false);
    expect(screen.getByLabelText("Mídia remota").contains(staleRemote)).toBe(false);
    expect(sessions[1]?.disconnect).not.toHaveBeenCalled();
  });

  it("accepts reconnect events only from the active session", async () => {
    const view = setup();
    const user = await connect();

    act(() => view.session.callbacks.onReconnecting());
    expect(screen.getByText("Conectando")).toBeInTheDocument();
    act(() => view.session.callbacks.onReconnected());
    expect(screen.getByText("Conectado")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /sair da chamada/i }));
    act(() => {
      view.session.callbacks.onReconnecting();
      view.session.callbacks.onReconnected();
    });
    expect(screen.getByText("Desconectado")).toBeInTheDocument();
  });

  it("does not request a token when leaving during startAudio", async () => {
    const audioDeferred = deferred();
    const view = setup((session) => {
      session.startAudio.mockImplementationOnce(() => audioDeferred.promise);
    });
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: /entrar na chamada/i }));
    await waitFor(() => expect(view.session.startAudio).toHaveBeenCalledOnce());
    await user.click(screen.getByRole("button", { name: /sair da chamada/i }));
    await act(async () => audioDeferred.resolve());

    expect(view.tokenRequester).not.toHaveBeenCalled();
    expect(view.session.connect).not.toHaveBeenCalled();
    expect(screen.getByText("Desconectado")).toBeInTheDocument();
  });

  it("ignores a token response that arrives after leaving", async () => {
    const tokenDeferred = deferredValue<SpikeTokenResponse>();
    let session: FakeSession | undefined;
    const sessionFactory: LiveKitSpikeSessionFactory = (callbacks) => {
      session = new FakeSession(callbacks);
      return session;
    };
    render(
      <LiveKitSpikePage
        sessionFactory={sessionFactory}
        tokenRequester={vi.fn(() => tokenDeferred.promise)}
      />,
    );
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: /entrar na chamada/i }));
    await screen.findByText("Conectando");
    await user.click(screen.getByRole("button", { name: /sair da chamada/i }));
    await act(async () => tokenDeferred.resolve(tokenResponse));

    expect(session?.connect).not.toHaveBeenCalled();
    expect(session?.disconnect).toHaveBeenCalled();
    expect(screen.getByText("Desconectado")).toBeInTheDocument();
  });

  it.each([
    ["camera", "setCameraEnabled", /desligar câmera/i],
    ["microphone", "setMicrophoneEnabled", /mutar microfone/i],
  ] as const)("ignores a pending %s toggle after leaving", async (_device, method, button) => {
    const toggleDeferred = deferred();
    const view = setup();
    const user = await connect();
    view.session[method].mockImplementationOnce(() => toggleDeferred.promise);

    await user.click(screen.getByRole("button", { name: button }));
    await waitFor(() => expect(view.session[method]).toHaveBeenCalledOnce());
    await user.click(screen.getByRole("button", { name: /sair da chamada/i }));
    await act(async () => toggleDeferred.resolve());

    expect(screen.getByText("Desconectado")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /ligar câmera/i })).toBeDisabled();
    expect(screen.getByRole("button", { name: /ativar microfone/i })).toBeDisabled();
  });

  it("reports generic camera and microphone control failures", async () => {
    const view = setup();
    const user = await connect();
    view.session.setCameraEnabled.mockRejectedValueOnce(new Error("camera failed"));
    view.session.setMicrophoneEnabled.mockRejectedValueOnce(
      new SpikeMediaError("microphone_unavailable"),
    );

    await user.click(screen.getByRole("button", { name: /desligar câmera/i }));
    expect(screen.getByRole("alert")).toHaveTextContent(/alterar a câmera/i);
    await user.click(screen.getByRole("button", { name: /mutar microfone/i }));
    expect(screen.getByRole("alert")).toHaveTextContent(/alterar o microfone/i);
  });

  it("reports an audio playback initialization failure", async () => {
    const view = setup((session) => {
      session.startAudio.mockRejectedValueOnce(new Error("audio blocked"));
    });

    await userEvent.click(screen.getByRole("button", { name: /entrar na chamada/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/reprodução de áudio/i);
    expect(view.session.disconnect).toHaveBeenCalledOnce();
  });

  describe("connection timeout", () => {
    beforeEach(() => {
      vi.useFakeTimers();
    });

    afterEach(() => {
      vi.useRealTimers();
    });

    it("times out a pending token request and allows an immediate retry", async () => {
      const firstToken = deferredValue<SpikeTokenResponse>();
      const sessions: FakeSession[] = [];
      let firstSignal: AbortSignal | undefined;
      const sessionFactory: LiveKitSpikeSessionFactory = (callbacks) => {
        const session = new FakeSession(callbacks);
        sessions.push(session);
        return session;
      };
      const tokenRequester = vi
        .fn<SpikeTokenRequester>()
        .mockImplementationOnce((_request, signal) => {
          firstSignal = signal;
          return firstToken.promise;
        })
        .mockResolvedValue(tokenResponse);
      render(<LiveKitSpikePage sessionFactory={sessionFactory} tokenRequester={tokenRequester} />);

      await startConnectionWithFakeTimers();
      expect(screen.getByText("Conectando")).toBeInTheDocument();
      expect(firstSignal?.aborted).toBe(false);

      await expireConnectionTimeout();

      expect(firstSignal?.aborted).toBe(true);
      expect(screen.getByText("Erro")).toBeInTheDocument();
      expect(screen.getByRole("alert")).toHaveTextContent(/demorou mais que o esperado/i);
      expect(sessions[0]?.disconnect).toHaveBeenCalledOnce();
      expect(screen.getByLabelText("Vídeo local").querySelector("video")).toBeNull();
      expect(screen.getByLabelText("Mídia remota").querySelector("video,audio")).toBeNull();

      await startConnectionWithFakeTimers();
      expect(screen.getByText("Conectado")).toBeInTheDocument();

      await act(async () => firstToken.resolve(tokenResponse));

      expect(screen.getByText("Conectado")).toBeInTheDocument();
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(sessions[0]?.connect).not.toHaveBeenCalled();
      expect(sessions[0]?.disconnect).toHaveBeenCalledOnce();
      expect(sessions[1]?.disconnect).not.toHaveBeenCalled();
    });

    it("times out a pending session.connect without enabling media", async () => {
      const connectDeferred = deferred();
      const view = setup((session) => {
        session.connect.mockImplementationOnce(() => connectDeferred.promise);
      });

      await startConnectionWithFakeTimers();
      expect(view.session.connect).toHaveBeenCalledOnce();

      await expireConnectionTimeout();

      expect(screen.getByText("Erro")).toBeInTheDocument();
      expect(screen.getByRole("alert")).toHaveTextContent(/demorou mais que o esperado/i);
      expect(view.session.disconnect).toHaveBeenCalledOnce();
      expect(view.session.enableCamera).not.toHaveBeenCalled();
      expect(view.session.enableMicrophone).not.toHaveBeenCalled();

      await act(async () => connectDeferred.resolve());

      expect(screen.getByText("Erro")).toBeInTheDocument();
      expect(screen.queryByText("Conectado")).not.toBeInTheDocument();
      expect(view.session.enableCamera).not.toHaveBeenCalled();
      expect(view.session.disconnect).toHaveBeenCalledOnce();
    });

    it("times out pending camera activation and removes partial media", async () => {
      const cameraDeferred = deferred();
      const view = setup((session) => {
        session.enableCamera.mockImplementationOnce(() => {
          session.callbacks.onLocalElement(document.createElement("video"));
          return cameraDeferred.promise;
        });
      });

      await startConnectionWithFakeTimers();
      const remoteAudio = document.createElement("audio");
      act(() => view.session.callbacks.onRemoteElement(remoteAudio));
      expect(screen.getByLabelText("Vídeo local").querySelector("video")).not.toBeNull();
      expect(screen.getByLabelText("Mídia remota").querySelector("audio")).toBe(remoteAudio);

      await expireConnectionTimeout();

      expect(view.session.disconnect).toHaveBeenCalledOnce();
      expect(view.session.enableMicrophone).not.toHaveBeenCalled();
      expect(screen.getByLabelText("Vídeo local").querySelector("video")).toBeNull();
      expect(screen.getByLabelText("Mídia remota").querySelector("audio")).toBeNull();
      expect(screen.getByText("Erro")).toBeInTheDocument();

      await act(async () => cameraDeferred.resolve());

      expect(view.session.enableMicrophone).not.toHaveBeenCalled();
      expect(screen.getByText("Erro")).toBeInTheDocument();
    });

    it("times out pending microphone activation and removes the camera preview", async () => {
      const microphoneDeferred = deferred();
      const view = setup((session) => {
        session.enableMicrophone.mockImplementationOnce(() => microphoneDeferred.promise);
      });

      await startConnectionWithFakeTimers();
      expect(view.session.enableMicrophone).toHaveBeenCalledOnce();
      expect(screen.getByLabelText("Vídeo local").querySelector("video")).not.toBeNull();

      await expireConnectionTimeout();

      expect(view.session.disconnect).toHaveBeenCalledOnce();
      expect(screen.getByLabelText("Vídeo local").querySelector("video")).toBeNull();
      expect(screen.getByRole("button", { name: /ligar câmera/i })).toBeDisabled();
      expect(screen.getByRole("button", { name: /ativar microfone/i })).toBeDisabled();
      expect(screen.getByText("Erro")).toBeInTheDocument();

      await act(async () => microphoneDeferred.resolve());

      expect(screen.getByText("Erro")).toBeInTheDocument();
      expect(screen.queryByText("Conectado")).not.toBeInTheDocument();
    });

    it("cancels the timer when the user leaves before timeout", async () => {
      const tokenRequester = vi.fn<SpikeTokenRequester>(
        (_request, signal) =>
          new Promise((_resolve, reject) => {
            signal?.addEventListener("abort", () =>
              reject(new DOMException("request aborted", "AbortError")),
            );
          }),
      );
      let session: FakeSession | undefined;
      render(
        <LiveKitSpikePage
          sessionFactory={(callbacks) => {
            session = new FakeSession(callbacks);
            return session;
          }}
          tokenRequester={tokenRequester}
        />,
      );
      await startConnectionWithFakeTimers();

      fireEvent.click(screen.getByRole("button", { name: /sair da chamada/i }));
      await flushAsyncWork();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10000);
      });

      expect(screen.getByText("Desconectado")).toBeInTheDocument();
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(session?.disconnect).toHaveBeenCalledOnce();
    });

    it("cancels the timer and avoids updates after unmount", async () => {
      const tokenDeferred = deferredValue<SpikeTokenResponse>();
      const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
      const view = setup();
      view.tokenRequester.mockImplementationOnce(() => tokenDeferred.promise);

      await startConnectionWithFakeTimers();
      view.unmount();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10000);
        tokenDeferred.resolve(tokenResponse);
      });

      expect(view.session.disconnect).toHaveBeenCalledOnce();
      expect(view.session.connect).not.toHaveBeenCalled();
      expect(consoleError).not.toHaveBeenCalled();
    });

    it("does not let the old attempt timer affect its replacement", async () => {
      const firstConnect = deferred();
      const sessions: FakeSession[] = [];
      const sessionFactory: LiveKitSpikeSessionFactory = (callbacks) => {
        const session = new FakeSession(callbacks);
        if (sessions.length === 0) {
          session.connect.mockImplementationOnce(() => firstConnect.promise);
        }
        sessions.push(session);
        return session;
      };
      render(
        <LiveKitSpikePage
          sessionFactory={sessionFactory}
          tokenRequester={vi.fn<SpikeTokenRequester>(async () => tokenResponse)}
        />,
      );

      await startConnectionWithFakeTimers();
      fireEvent.click(screen.getByRole("button", { name: /sair da chamada/i }));
      await flushAsyncWork();
      await startConnectionWithFakeTimers();
      expect(screen.getByText("Conectado")).toBeInTheDocument();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(10000);
        firstConnect.resolve();
      });

      expect(screen.getByText("Conectado")).toBeInTheDocument();
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(sessions[0]?.disconnect).toHaveBeenCalledOnce();
      expect(sessions[1]?.disconnect).not.toHaveBeenCalled();
    });

    it("cancels the timer after a successful connection", async () => {
      const view = setup();

      await startConnectionWithFakeTimers();
      expect(screen.getByText("Conectado")).toBeInTheDocument();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(10000);
      });

      expect(screen.getByText("Conectado")).toBeInTheDocument();
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(view.session.disconnect).not.toHaveBeenCalled();
    });
  });
});
