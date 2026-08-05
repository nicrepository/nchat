import { act, render, renderHook, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { LiveKitSessionFactory, LiveKitSessionLoader } from "../media/liveKitSession";
import { useCallMedia } from "./useCallMedia";

const liveKitAdapterLoaded = vi.hoisted(() => vi.fn());

vi.mock("../mediaSpike/liveKitSpikeSession", () => {
  liveKitAdapterLoaded();
  return { createLiveKitSpikeSession: vi.fn() };
});

interface SessionCallbacks {
  onLocalElement(element: HTMLMediaElement): void;
  onRemoteElement(element: HTMLMediaElement): void;
  onElementRemoved(element: HTMLMediaElement): void;
  onDisconnected(): void;
  onReconnecting(): void;
  onReconnected(): void;
  onAudioPlaybackChanged(canPlaybackAudio: boolean): void;
}

class FakeSession {
  readonly callbacks: SessionCallbacks;
  readonly startAudio = vi.fn(async (): Promise<void> => undefined);
  readonly connect = vi.fn(async (): Promise<void> => undefined);
  readonly enableCamera = vi.fn(async (): Promise<void> => undefined);
  readonly enableMicrophone = vi.fn(async (): Promise<void> => undefined);
  readonly setCameraEnabled = vi.fn(async (): Promise<void> => undefined);
  readonly setMicrophoneEnabled = vi.fn(async (): Promise<void> => undefined);
  readonly disconnect = vi.fn(async (): Promise<void> => undefined);

  constructor(callbacks: SessionCallbacks) {
    this.callbacks = callbacks;
  }
}

function setup(makeLoader?: (factory: LiveKitSessionFactory) => LiveKitSessionLoader) {
  let session: FakeSession | undefined;
  const sessions: FakeSession[] = [];
  const factory: LiveKitSessionFactory = vi.fn((callbacks: SessionCallbacks) => {
    session = new FakeSession(callbacks);
    sessions.push(session);
    return session;
  });
  const loader = makeLoader?.(factory) ?? vi.fn(async () => factory);
  const view = renderHook(() => useCallMedia(loader));
  const getSession = () => {
    if (!session) throw new Error("Session was not created");
    return session;
  };
  return { ...view, factory, loader, getSession, sessions };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

const videoCall = {
  call_id: "00000000-0000-4000-8000-000000000801",
  call_type: "video" as const,
};

beforeEach(() => {
  vi.clearAllMocks();
  window.history.replaceState({}, "", "/chat/dm/example");
});

describe("useCallMedia", () => {
  it("does not load LiveKit during an ordinary ChatShell media render", () => {
    const view = setup();
    const defaultView = renderHook(() => useCallMedia());

    expect(view.loader).not.toHaveBeenCalled();
    expect(view.factory).not.toHaveBeenCalled();
    expect(liveKitAdapterLoaded).not.toHaveBeenCalled();

    defaultView.unmount();
  });

  it("connects one video session and enables the requested local tracks", async () => {
    const view = setup();

    await act(() => view.result.current.prepare());
    await act(() => view.result.current.startAudio());
    await act(() => view.result.current.connect(videoCall, "participant-token"));

    const session = view.getSession();
    expect(view.loader).toHaveBeenCalledOnce();
    expect(view.factory).toHaveBeenCalledOnce();
    expect(session.startAudio).toHaveBeenCalledOnce();
    expect(session.connect).toHaveBeenCalledWith(
      "ws://localhost:3000/livekit",
      "participant-token",
    );
    expect(session.enableCamera).toHaveBeenCalledOnce();
    expect(session.enableMicrophone).toHaveBeenCalledOnce();
    expect(view.result.current.status).toBe("connected");
    expect(view.result.current.cameraEnabled).toBe(true);
    expect(view.result.current.microphoneEnabled).toBe(true);
  });

  it("keeps an audio-only call free of camera capture", async () => {
    const view = setup();

    await act(() =>
      view.result.current.connect({ ...videoCall, call_type: "audio" }, "participant-token"),
    );

    expect(view.getSession().enableCamera).not.toHaveBeenCalled();
    expect(view.getSession().enableMicrophone).toHaveBeenCalledOnce();
    expect(view.result.current.cameraEnabled).toBe(false);
  });

  it("attaches and removes local and remote media without storing SDK objects in React state", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    render(
      <>
        <div ref={view.result.current.bindLocalMedia} data-testid="local" />
        <div ref={view.result.current.bindRemoteMedia} data-testid="remote" />
      </>,
    );
    const localVideo = document.createElement("video");
    const remoteVideo = document.createElement("video");
    const remoteAudio = document.createElement("audio");

    act(() => {
      session.callbacks.onLocalElement(localVideo);
      session.callbacks.onRemoteElement(remoteVideo);
      session.callbacks.onRemoteElement(remoteAudio);
    });

    expect(screen.getByTestId("local")).toContainElement(localVideo);
    expect(screen.getByTestId("remote")).toContainElement(remoteVideo);
    expect(screen.getByTestId("remote")).toContainElement(remoteAudio);
    expect(view.result.current.hasLocalVideo).toBe(true);
    expect(view.result.current.hasRemoteVideo).toBe(true);
    expect(view.result.current.hasRemoteMedia).toBe(true);

    act(() => {
      session.callbacks.onElementRemoved(localVideo);
      session.callbacks.onElementRemoved(remoteVideo);
      session.callbacks.onElementRemoved(remoteAudio);
    });

    expect(view.result.current.hasLocalVideo).toBe(false);
    expect(view.result.current.hasRemoteVideo).toBe(false);
    expect(view.result.current.hasRemoteMedia).toBe(false);
  });

  it("toggles microphone and camera and ignores a duplicate in-flight control action", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    let resolveMicrophone!: () => void;
    session.setMicrophoneEnabled.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          resolveMicrophone = resolve;
        }),
    );

    let first!: Promise<void>;
    let duplicate!: Promise<void>;
    act(() => {
      first = view.result.current.toggleMicrophone();
      duplicate = view.result.current.toggleMicrophone();
    });
    await act(async () => {
      resolveMicrophone();
      await Promise.all([first, duplicate]);
    });
    await act(() => view.result.current.toggleCamera());

    expect(session.setMicrophoneEnabled).toHaveBeenCalledExactlyOnceWith(false);
    expect(session.setCameraEnabled).toHaveBeenCalledExactlyOnceWith(false);
    expect(view.result.current.microphoneEnabled).toBe(false);
    expect(view.result.current.cameraEnabled).toBe(false);
  });

  it("reports permission denial without exposing the SDK error", async () => {
    const view = setup();
    await act(() => view.result.current.prepare());
    await act(() => view.result.current.startAudio());
    view.getSession().enableCamera.mockRejectedValueOnce({ kind: "camera_denied" });

    let failure: unknown;
    await act(async () => {
      try {
        await view.result.current.connect(videoCall, "participant-token");
      } catch (error) {
        failure = error;
      }
    });

    expect(failure).toMatchObject({ kind: "camera_denied" });
    expect(view.result.current.status).toBe("permission-denied");
    expect(view.result.current.error).toBe("Permissão de câmera negada pelo navegador.");
  });

  it("keeps active controls available after a camera toggle fails", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    view.getSession().setCameraEnabled.mockRejectedValueOnce({ kind: "camera_denied" });

    await act(() => view.result.current.toggleCamera());

    expect(view.result.current.status).toBe("connected");
    expect(view.result.current.error).toBe("Permissão de câmera negada pelo navegador.");
    expect(view.result.current.pendingControl).toBeNull();
  });

  it("reflects reconnect events and disconnects idempotently", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();

    act(() => session.callbacks.onReconnecting());
    expect(view.result.current.status).toBe("reconnecting");
    act(() => session.callbacks.onReconnected());
    expect(view.result.current.status).toBe("connected");
    act(() => session.callbacks.onDisconnected());
    expect(view.result.current.status).toBe("error");

    await act(() => Promise.all([view.result.current.stop(), view.result.current.stop()]));
    expect(session.disconnect).toHaveBeenCalledOnce();
  });

  it("disconnects and removes media when the owner unmounts", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();

    view.unmount();
    await act(async () => undefined);

    expect(session.disconnect).toHaveBeenCalledOnce();
  });

  it("requires a new user action when LiveKit was not ready for startAudio", async () => {
    const loading = deferred<LiveKitSessionFactory>();
    const view = setup(() => vi.fn(() => loading.promise));

    let first!: Promise<void>;
    act(() => {
      first = view.result.current.startAudio();
    });
    expect(view.loader).toHaveBeenCalledOnce();
    expect(view.factory).not.toHaveBeenCalled();

    await act(async () => {
      loading.resolve(view.factory);
      await first;
    });

    expect(view.factory).not.toHaveBeenCalled();
    expect(view.result.current.audioActivationRequired).toBe(true);

    let activation!: Promise<void>;
    act(() => {
      activation = view.result.current.startAudio();
      expect(view.getSession().startAudio).toHaveBeenCalledOnce();
    });
    await act(() => activation);
    expect(view.result.current.audioActivationRequired).toBe(false);
  });

  it.each(["audio", "video"] as const)(
    "reflects LiveKit playback status during an %s call",
    async (callType) => {
      const view = setup();
      await act(() =>
        view.result.current.connect({ ...videoCall, call_type: callType }, "participant-token"),
      );

      act(() => view.getSession().callbacks.onAudioPlaybackChanged(false));
      expect(view.result.current.audioActivationRequired).toBe(true);

      act(() => view.getSession().callbacks.onAudioPlaybackChanged(true));
      expect(view.result.current.audioActivationRequired).toBe(false);
    },
  );

  it("keeps failed audio activation recoverable and allows retry", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    session.startAudio.mockRejectedValueOnce(new DOMException("blocked", "NotAllowedError"));

    await act(() => view.result.current.startAudio());

    expect(view.result.current.audioStarting).toBe(false);
    expect(view.result.current.audioActivationRequired).toBe(true);
    expect(view.result.current.error).toBe(
      "O navegador bloqueou o áudio. Ative o áudio novamente.",
    );

    await act(() => view.result.current.startAudio());
    expect(session.startAudio).toHaveBeenCalledTimes(2);
    expect(view.result.current.audioActivationRequired).toBe(false);
  });

  it("deduplicates concurrent audio activation attempts", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const starting = deferred<void>();
    view.getSession().startAudio.mockReturnValueOnce(starting.promise);

    let first!: Promise<void>;
    let duplicate!: Promise<void>;
    act(() => {
      first = view.result.current.startAudio();
      duplicate = view.result.current.startAudio();
    });

    expect(view.getSession().startAudio).toHaveBeenCalledOnce();
    expect(view.result.current.audioStarting).toBe(true);

    await act(async () => {
      starting.resolve(undefined);
      await Promise.all([first, duplicate]);
    });
    expect(view.result.current.audioStarting).toBe(false);
  });

  it("ignores a pending startAudio resolution after stop", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const starting = deferred<void>();
    view.getSession().startAudio.mockReturnValueOnce(starting.promise);
    act(() => view.getSession().callbacks.onAudioPlaybackChanged(false));

    let activation!: Promise<void>;
    act(() => {
      activation = view.result.current.startAudio();
    });
    await act(() => view.result.current.stop());
    await act(async () => {
      starting.resolve(undefined);
      await activation;
    });

    expect(view.result.current.status).toBe("idle");
    expect(view.result.current.audioStarting).toBe(false);
    expect(view.result.current.audioActivationRequired).toBe(false);
    expect(view.result.current.error).toBeNull();
  });

  it("ignores a pending startAudio rejection after stop", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const starting = deferred<void>();
    view.getSession().startAudio.mockReturnValueOnce(starting.promise);

    let activation!: Promise<void>;
    act(() => {
      activation = view.result.current.startAudio();
    });
    await act(() => view.result.current.stop());
    await act(async () => {
      starting.reject(new DOMException("blocked", "NotAllowedError"));
      await activation;
    });

    expect(view.result.current.status).toBe("idle");
    expect(view.result.current.audioStarting).toBe(false);
    expect(view.result.current.audioActivationRequired).toBe(false);
    expect(view.result.current.error).toBeNull();
  });

  it("ignores a pending startAudio result after unmount", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const starting = deferred<void>();
    const session = view.getSession();
    session.startAudio.mockReturnValueOnce(starting.promise);
    const activation = view.result.current.startAudio();

    view.unmount();
    starting.resolve(undefined);
    await activation;

    expect(session.disconnect).toHaveBeenCalledOnce();
  });

  it("does not let an old startAudio result overwrite a new call", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const oldSession = view.getSession();
    const starting = deferred<void>();
    oldSession.startAudio.mockReturnValueOnce(starting.promise);
    const oldActivation = view.result.current.startAudio();

    await act(() => view.result.current.stop());
    await act(() =>
      view.result.current.connect(
        { ...videoCall, call_id: "00000000-0000-4000-8000-000000000802" },
        "fresh-token",
      ),
    );
    const newSession = view.getSession();
    act(() => newSession.callbacks.onAudioPlaybackChanged(false));

    await act(async () => {
      starting.resolve(undefined);
      await oldActivation;
    });

    expect(view.sessions).toHaveLength(2);
    expect(view.result.current.status).toBe("connected");
    expect(view.result.current.audioActivationRequired).toBe(true);
    expect(view.result.current.error).toBeNull();
  });

  it("reports a controlled load error and retries the failed import", async () => {
    const view = setup((factory) => {
      const loader = vi
        .fn<LiveKitSessionLoader>()
        .mockRejectedValueOnce(new Error("chunk unavailable"))
        .mockResolvedValueOnce(factory);
      return loader;
    });

    await act(async () => {
      await expect(view.result.current.connect(videoCall, "participant-token")).rejects.toThrow(
        "chunk unavailable",
      );
    });
    expect(view.result.current.status).toBe("error");
    expect(view.result.current.error).toBe("Não foi possível carregar os recursos da chamada.");

    await act(() => view.result.current.connect(videoCall, "fresh-token"));
    expect(view.loader).toHaveBeenCalledTimes(2);
    expect(view.getSession().connect).toHaveBeenCalledWith(
      "ws://localhost:3000/livekit",
      "fresh-token",
    );
  });

  it("does not create a session when the call ends before the import resolves", async () => {
    const loading = deferred<LiveKitSessionFactory>();
    const view = setup(() => vi.fn(() => loading.promise));

    let connecting!: Promise<void>;
    act(() => {
      connecting = view.result.current.connect(videoCall, "participant-token");
    });
    await act(() => view.result.current.stop());
    await act(async () => {
      loading.resolve(view.factory);
      await connecting;
    });

    expect(view.factory).not.toHaveBeenCalled();
    expect(view.result.current.status).toBe("idle");
  });

  it("does not create a session after unmount while the import is pending", async () => {
    const loading = deferred<LiveKitSessionFactory>();
    const view = setup(() => vi.fn(() => loading.promise));

    let connecting!: Promise<void>;
    act(() => {
      connecting = view.result.current.connect(videoCall, "participant-token");
    });
    view.unmount();
    loading.resolve(view.factory);
    await connecting;

    expect(view.factory).not.toHaveBeenCalled();
  });

  it("reuses one session and connection for duplicate connect calls", async () => {
    const view = setup();
    const connecting = deferred<void>();
    await act(() => view.result.current.prepare());
    await act(() => view.result.current.startAudio());
    view.getSession().connect.mockImplementationOnce(() => connecting.promise);

    let first!: Promise<void>;
    let duplicate!: Promise<void>;
    act(() => {
      first = view.result.current.connect(videoCall, "participant-token");
      duplicate = view.result.current.connect(videoCall, "participant-token");
    });
    await act(async () => {
      connecting.resolve();
      await Promise.all([first, duplicate]);
    });

    expect(view.factory).toHaveBeenCalledOnce();
    expect(view.getSession().connect).toHaveBeenCalledOnce();
  });
});
