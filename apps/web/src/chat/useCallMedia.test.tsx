import { act, render, renderHook, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { MediaIntent } from "../calls/callOwnership";
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
  onMicrophoneStateChanged(enabled: boolean): void;
  onParticipantConnected(identity: string, displayName: string): void;
  onParticipantDisconnected(identity: string): void;
  onRemoteVideoAvailabilityChanged(identity: string, available: boolean): void;
  onActiveSpeakersChanged(identities: string[]): void;
  onScreenShareChanged(enabled: boolean): void;
  onLocalScreenShareChanged(element: HTMLMediaElement | null): void;
  onRemoteScreenShareChanged(
    identity: string,
    trackSid: string,
    element: HTMLMediaElement,
    active: boolean,
  ): void;
}

function remoteVideoFor(identity: string): HTMLVideoElement {
  const element = document.createElement("video");
  element.dataset.participantIdentity = identity;
  return element;
}

function remoteAudioFor(identity: string): HTMLAudioElement {
  const element = document.createElement("audio");
  element.dataset.participantIdentity = identity;
  return element;
}

class FakeSession {
  readonly callbacks: SessionCallbacks;
  readonly startAudio = vi.fn(async (): Promise<void> => undefined);
  readonly connect = vi.fn(async (): Promise<void> => undefined);
  readonly enableCamera = vi.fn(async (): Promise<void> => undefined);
  // Mirrors the real adapter contract: confirming the effective microphone
  // state is a side effect of these calls (via onMicrophoneStateChanged),
  // never a value the caller assumes locally.
  readonly enableMicrophone = vi.fn(async (): Promise<void> => {
    this.callbacks.onMicrophoneStateChanged(true);
  });
  readonly setCameraEnabled = vi.fn(async (): Promise<void> => undefined);
  readonly setMicrophoneEnabled = vi.fn(async (enabled: boolean): Promise<void> => {
    this.callbacks.onMicrophoneStateChanged(enabled);
  });
  readonly setScreenShareEnabled = vi.fn(async (enabled: boolean): Promise<void> => {
    this.callbacks.onScreenShareChanged(enabled);
  });
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
  const result = {
    get current() {
      const current = view.result.current;
      return {
        ...current,
        connect: (
          call: Parameters<typeof current.connect>[0],
          token: Parameters<typeof current.connect>[1],
          serverUrl = liveKitServerUrl,
          initialIntent?: Parameters<typeof current.connect>[3],
        ) => current.connect(call, token, serverUrl, initialIntent),
      };
    },
  };
  const getSession = () => {
    if (!session) throw new Error("Session was not created");
    return session;
  };
  return { ...view, result, factory, loader, getSession, sessions };
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
const liveKitServerUrl = "wss://livekit-dev.nic-labs.com";

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
    await act(() => view.result.current.connect(videoCall, "participant-token", liveKitServerUrl));

    const session = view.getSession();
    expect(view.loader).toHaveBeenCalledOnce();
    expect(view.factory).toHaveBeenCalledOnce();
    expect(session.startAudio).toHaveBeenCalledOnce();
    expect(session.connect).toHaveBeenCalledWith(liveKitServerUrl, "participant-token");
    expect(session.enableCamera).toHaveBeenCalledOnce();
    expect(session.enableMicrophone).toHaveBeenCalledOnce();
    expect(view.result.current.status).toBe("connected");
    expect(view.result.current.cameraEnabled).toBe(true);
    expect(view.result.current.microphoneEnabled).toBe(true);
  });

  it("keeps an audio-only call free of camera capture", async () => {
    const view = setup();

    await act(() =>
      view.result.current.connect(
        { ...videoCall, call_type: "audio" },
        "participant-token",
        liveKitServerUrl,
      ),
    );

    expect(view.getSession().enableCamera).not.toHaveBeenCalled();
    expect(view.getSession().enableMicrophone).toHaveBeenCalledOnce();
    expect(view.result.current.cameraEnabled).toBe(false);
  });

  describe("recovery connect with a restored media intent (issue #610)", () => {
    const combos: MediaIntent[] = [
      { microphone: true, camera: true },
      { microphone: true, camera: false },
      { microphone: false, camera: true },
      { microphone: false, camera: false },
    ];

    it.each(combos)(
      "applies the exact restored snapshot %o via the boolean setters, never enable/disable",
      async (intent) => {
        const view = setup();

        const applied = await act(() =>
          view.result.current.connect(videoCall, "participant-token", liveKitServerUrl, intent),
        );

        const session = view.getSession();
        expect(session.enableCamera).not.toHaveBeenCalled();
        expect(session.enableMicrophone).not.toHaveBeenCalled();
        expect(session.setCameraEnabled).toHaveBeenCalledExactlyOnceWith(intent.camera);
        expect(session.setMicrophoneEnabled).toHaveBeenCalledExactlyOnceWith(intent.microphone);
        expect(view.result.current.cameraEnabled).toBe(intent.camera);
        expect(view.result.current.microphoneEnabled).toBe(intent.microphone);
        expect(applied).toEqual(intent);
      },
    );

    it("resolves undefined when stop() invalidates the attempt before a recovery connect settles", async () => {
      const loading = deferred<LiveKitSessionFactory>();
      const view = setup(() => vi.fn(() => loading.promise));

      let connecting!: Promise<MediaIntent | undefined>;
      act(() => {
        connecting = view.result.current.connect(videoCall, "participant-token", liveKitServerUrl, {
          microphone: true,
          camera: false,
        });
      });
      await act(() => view.result.current.stop());
      await act(async () => {
        loading.resolve(view.factory);
        expect(await connecting).toBeUndefined();
      });

      expect(view.factory).not.toHaveBeenCalled();
      expect(view.result.current.status).toBe("idle");
    });

    it("resolves the fresh-default snapshot when no initialIntent is supplied", async () => {
      const view = setup();
      const appliedVideo = await act(() =>
        view.result.current.connect(videoCall, "participant-token", liveKitServerUrl),
      );
      expect(appliedVideo).toEqual({ microphone: true, camera: true });

      const audioView = setup();
      const appliedAudio = await act(() =>
        audioView.result.current.connect(
          { ...videoCall, call_type: "audio" },
          "participant-token",
          liveKitServerUrl,
        ),
      );
      expect(appliedAudio).toEqual({ microphone: true, camera: false });
    });
  });

  it("never starts screen share on connect and toggles only from explicit intent", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();

    expect(session.setScreenShareEnabled).not.toHaveBeenCalled();
    await act(() => view.result.current.toggleScreenShare());
    expect(view.result.current.screenShareEnabled).toBe(true);
    await act(() => view.result.current.toggleScreenShare());
    expect(view.result.current.screenShareEnabled).toBe(false);
    expect(session.setScreenShareEnabled.mock.calls).toEqual([[true], [false]]);
  });

  it("stabilizes the active speaker before updating the presentation", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(1000);
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();

    act(() => session.callbacks.onActiveSpeakersChanged(["identity-a"]));
    expect(view.result.current.activeSpeakerId).toBeNull();
    act(() => vi.advanceTimersByTime(399));
    expect(view.result.current.activeSpeakerId).toBeNull();
    act(() => vi.advanceTimersByTime(1));
    expect(view.result.current.activeSpeakerId).toBe("identity-a");

    view.unmount();
    vi.useRealTimers();
  });

  it("binds remote screen share separately from participant camera media", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const element = remoteVideoFor("identity-a");

    act(() =>
      view
        .getSession()
        .callbacks.onRemoteScreenShareChanged("identity-a", "track-a", element, true),
    );
    expect(view.result.current.remoteScreenShare?.identity).toBe("identity-a");
    render(
      <div ref={view.result.current.remoteScreenShare?.bindMedia} data-testid="screen-share" />,
    );
    expect(screen.getByTestId("screen-share")).toContainElement(element);

    act(() =>
      view
        .getSession()
        .callbacks.onRemoteScreenShareChanged("identity-a", "track-a", element, false),
    );
    expect(view.result.current.remoteScreenShare).toBeNull();
  });

  it("binds the local screen-share preview separately from the camera preview", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const element = document.createElement("video");
    render(<div ref={view.result.current.bindLocalScreenShare} data-testid="local-screen-share" />);

    act(() => view.getSession().callbacks.onLocalScreenShareChanged(element));
    expect(screen.getByTestId("local-screen-share")).toContainElement(element);

    act(() => view.getSession().callbacks.onLocalScreenShareChanged(null));
    expect(screen.getByTestId("local-screen-share")).toBeEmptyDOMElement();
  });

  describe("remote screen-share registry (issue #611)", () => {
    it("A start -> B start -> B stop => A becomes visible again", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      const elementA = remoteVideoFor("identity-a");
      const elementB = remoteVideoFor("identity-b");

      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-a", "track-a", elementA, true),
      );
      expect(view.result.current.remoteScreenShare?.identity).toBe("identity-a");

      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-b", "track-b", elementB, true),
      );
      expect(view.result.current.remoteScreenShare?.identity).toBe("identity-b");

      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-b", "track-b", elementB, false),
      );
      expect(view.result.current.remoteScreenShare?.identity).toBe("identity-a");
    });

    it("A start -> B start -> A stop => B remains visible", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      const elementA = remoteVideoFor("identity-a");
      const elementB = remoteVideoFor("identity-b");

      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-a", "track-a", elementA, true),
      );
      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-b", "track-b", elementB, true),
      );

      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-a", "track-a", elementA, false),
      );
      expect(view.result.current.remoteScreenShare?.identity).toBe("identity-b");
    });

    it("a same-participant true republish (new trackSid) keeps the newest element visible; the old track's removal cannot clear it", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      const elementOld = remoteVideoFor("identity-a");
      const elementNew = remoteVideoFor("identity-a");

      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-a", "track-old", elementOld, true),
      );
      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-a", "track-new", elementNew, true),
      );
      expect(view.result.current.remoteScreenShare).not.toBeNull();

      // The OLD publication's removal arrives after the new one already
      // started — it must only ever remove "track-old", never clear the
      // newer, currently-presented "track-new" entry for the same identity.
      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-a", "track-old", elementOld, false),
      );
      expect(view.result.current.remoteScreenShare?.identity).toBe("identity-a");
      render(
        <div ref={view.result.current.remoteScreenShare?.bindMedia} data-testid="screen-share" />,
      );
      expect(screen.getByTestId("screen-share")).toContainElement(elementNew);
    });

    // Issue #611 pre-commit review: trackSid ALONE is not a safe removal
    // key. The adapter's own dedup guard (onTrackSubscribed's
    // `remoteTracks.has(track)`) is keyed by RemoteTrack object identity,
    // not trackSid — so a rebind/replay around reconnect can legitimately
    // deliver a second "add" for the SAME trackSid under a new element
    // before the OLD instance's own stale "remove" (still carrying that
    // same trackSid) arrives. The registry must key removal on the exact
    // element, never on trackSid/identity alone, or a stale removal for the
    // superseded instance would delete the newer one.
    it("a stale removal carrying the SAME trackSid as a superseded element cannot clear the newer one", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      const elementOld = remoteVideoFor("identity-a");
      const elementNew = remoteVideoFor("identity-a");

      // add T1/E1
      act(() => session.callbacks.onRemoteScreenShareChanged("identity-a", "T1", elementOld, true));
      // reconnect/rebind/replay: add T1/E2 (same trackSid, new element)
      act(() => session.callbacks.onRemoteScreenShareChanged("identity-a", "T1", elementNew, true));
      expect(view.result.current.remoteScreenShare).not.toBeNull();

      // stale removal belonging to the OLD RemoteTrack/E1, still carrying T1
      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-a", "T1", elementOld, false),
      );
      // E2 must still be presented — the stale removal must not have
      // deleted the registry entry a newer add just wrote under the same
      // trackSid.
      expect(view.result.current.remoteScreenShare).not.toBeNull();
      render(
        <div ref={view.result.current.remoteScreenShare?.bindMedia} data-testid="screen-share" />,
      );
      expect(screen.getByTestId("screen-share")).toContainElement(elementNew);

      // The real removal for E2 (same trackSid) now correctly clears it.
      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-a", "T1", elementNew, false),
      );
      expect(view.result.current.remoteScreenShare).toBeNull();
    });

    it("presenter disconnect (its screen-share removal) falls back to the remaining active share", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      const elementA = remoteVideoFor("identity-a");
      const elementB = remoteVideoFor("identity-b");
      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-a", "track-a", elementA, true),
      );
      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-b", "track-b", elementB, true),
      );

      // Adapter emits the removal as part of participant-disconnect cleanup.
      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-b", "track-b", elementB, false),
      );

      expect(view.result.current.remoteScreenShare?.identity).toBe("identity-a");
    });

    it("clears to null once every active remote screen share has stopped", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      const elementA = remoteVideoFor("identity-a");
      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-a", "track-a", elementA, true),
      );

      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-a", "track-a", elementA, false),
      );

      expect(view.result.current.remoteScreenShare).toBeNull();
    });

    it("a duplicate/replayed add or remove callback is idempotent", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      const elementA = remoteVideoFor("identity-a");

      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-a", "track-a", elementA, true),
      );
      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-a", "track-a", elementA, true),
      );
      expect(view.result.current.remoteScreenShare?.identity).toBe("identity-a");

      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-a", "track-a", elementA, false),
      );
      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-a", "track-a", elementA, false),
      );
      expect(view.result.current.remoteScreenShare).toBeNull();
    });

    it("swaps the bound container to the newly selected element cleanly", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      const elementA = remoteVideoFor("identity-a");
      const elementB = remoteVideoFor("identity-b");
      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-a", "track-a", elementA, true),
      );
      const { rerender } = render(
        <div ref={view.result.current.remoteScreenShare?.bindMedia} data-testid="screen-share" />,
      );
      expect(screen.getByTestId("screen-share")).toContainElement(elementA);

      act(() =>
        session.callbacks.onRemoteScreenShareChanged("identity-b", "track-b", elementB, true),
      );
      rerender(
        <div ref={view.result.current.remoteScreenShare?.bindMedia} data-testid="screen-share" />,
      );

      expect(screen.getByTestId("screen-share")).toContainElement(elementB);
      expect(screen.getByTestId("screen-share")).not.toContainElement(elementA);
    });
  });

  it("mic/camera stay unaffected when screen-share toggling fails", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    session.setScreenShareEnabled.mockRejectedValueOnce(new Error("publish failed"));

    await act(() => view.result.current.toggleScreenShare());

    expect(view.result.current.microphoneEnabled).toBe(true);
    expect(view.result.current.cameraEnabled).toBe(true);
    expect(view.result.current.screenShareEnabled).toBe(false);
    expect(view.result.current.pendingControl).toBeNull();
    expect(view.result.current.error).not.toBeNull();
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
          resolveMicrophone = () => {
            // The SDK confirms the effective state before the operation
            // settles: the mock mirrors that ordering.
            session.callbacks.onMicrophoneStateChanged(false);
            resolve();
          };
        }),
    );

    let first!: Promise<boolean | undefined>;
    let duplicate!: Promise<boolean | undefined>;
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

  describe("toggle result semantics (issue #610)", () => {
    it("resolves the applied boolean on a normal successful toggle", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));

      const mic = await act(() => view.result.current.toggleMicrophone());
      expect(mic).toBe(false);
      const camera = await act(() => view.result.current.toggleCamera());
      expect(camera).toBe(false);
    });

    it("resolves undefined when the SDK rejects the toggle", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      session.setMicrophoneEnabled.mockRejectedValueOnce(new Error("device busy"));

      const mic = await act(() => view.result.current.toggleMicrophone());
      expect(mic).toBeUndefined();
      expect(view.result.current.error).not.toBeNull();
    });

    it("resolves undefined for a stale toggle superseded by stop() before it settles", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      const pending = deferred<void>();
      session.setMicrophoneEnabled.mockImplementationOnce(() => pending.promise);

      let toggle!: Promise<boolean | undefined>;
      act(() => {
        toggle = view.result.current.toggleMicrophone();
      });
      await act(() => view.result.current.stop());
      await act(async () => {
        pending.resolve();
        expect(await toggle).toBeUndefined();
      });
    });

    it("resolves undefined for a duplicate in-flight toggle that never became the pending operation", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      let resolveMicrophone!: () => void;
      session.setMicrophoneEnabled.mockImplementationOnce(
        () =>
          new Promise<void>((resolve) => {
            resolveMicrophone = () => {
              session.callbacks.onMicrophoneStateChanged(false);
              resolve();
            };
          }),
      );

      let first!: Promise<boolean | undefined>;
      let duplicate!: Promise<boolean | undefined>;
      act(() => {
        first = view.result.current.toggleMicrophone();
        duplicate = view.result.current.toggleMicrophone();
      });
      await act(async () => {
        resolveMicrophone();
        expect(await duplicate).toBeUndefined();
        expect(await first).toBe(false);
      });
    });
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

  it("reports microphone permission denial during connect", async () => {
    const view = setup();
    await act(() => view.result.current.prepare());
    await act(() => view.result.current.startAudio());
    view.getSession().enableMicrophone.mockRejectedValueOnce({ kind: "microphone_denied" });

    let failure: unknown;
    await act(async () => {
      try {
        await view.result.current.connect(
          { ...videoCall, call_type: "audio" },
          "participant-token",
        );
      } catch (error) {
        failure = error;
      }
    });

    expect(failure).toMatchObject({ kind: "microphone_denied" });
    expect(view.result.current.status).toBe("permission-denied");
    expect(view.result.current.error).toBe("Permissão de microfone negada pelo navegador.");
  });

  it("reports an unavailable camera when the camera toggle fails", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    view.getSession().setCameraEnabled.mockRejectedValueOnce({ kind: "camera_unavailable" });

    await act(() => view.result.current.toggleCamera());

    expect(view.result.current.error).toBe("Não foi possível acessar ou alterar a câmera.");
  });

  it("reports a generic connect failure without an SDK error kind", async () => {
    const view = setup();
    await act(() => view.result.current.prepare());
    await act(() => view.result.current.startAudio());
    view.getSession().enableMicrophone.mockRejectedValueOnce(new Error("network down"));

    let failure: unknown;
    await act(async () => {
      try {
        await view.result.current.connect(videoCall, "participant-token");
      } catch (error) {
        failure = error;
      }
    });

    expect(failure).toBeInstanceOf(Error);
    expect(view.result.current.status).toBe("error");
    expect(view.result.current.error).toBe("Não foi possível conectar a mídia da chamada.");
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
        view.result.current.connect(
          { ...videoCall, call_type: callType },
          "participant-token",
          liveKitServerUrl,
        ),
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
        liveKitServerUrl,
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
    expect(view.getSession().connect).toHaveBeenCalledWith(liveKitServerUrl, "fresh-token");
  });

  it("does not create a session when the call ends before the import resolves", async () => {
    const loading = deferred<LiveKitSessionFactory>();
    const view = setup(() => vi.fn(() => loading.promise));

    let connecting!: Promise<MediaIntent | undefined>;
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

    let connecting!: Promise<MediaIntent | undefined>;
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

    let first!: Promise<MediaIntent | undefined>;
    let duplicate!: Promise<MediaIntent | undefined>;
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

  describe("authoritative microphone state", () => {
    it("starts false before any session confirms a state", () => {
      const view = setup();
      expect(view.result.current.microphoneEnabled).toBe(false);
    });

    it("toggles both directions using the confirmed ref, not a stale closure", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      expect(view.result.current.microphoneEnabled).toBe(true);

      await act(() => view.result.current.toggleMicrophone());
      expect(view.getSession().setMicrophoneEnabled).toHaveBeenLastCalledWith(false);
      expect(view.result.current.microphoneEnabled).toBe(false);

      await act(() => view.result.current.toggleMicrophone());
      expect(view.getSession().setMicrophoneEnabled).toHaveBeenLastCalledWith(true);
      expect(view.result.current.microphoneEnabled).toBe(true);
    });

    it("preserves the correct sequence across ten consecutive toggles", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));

      for (let cycle = 0; cycle < 10; cycle += 1) {
        const expected = !view.result.current.microphoneEnabled;
        await act(() => view.result.current.toggleMicrophone());
        expect(view.result.current.microphoneEnabled).toBe(expected);
        expect(view.getSession().setMicrophoneEnabled).toHaveBeenLastCalledWith(expected);
      }
      expect(view.getSession().setMicrophoneEnabled).toHaveBeenCalledTimes(10);
    });

    it("reflects a local TrackMuted-equivalent callback without a toggle in flight", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));

      act(() => view.getSession().callbacks.onMicrophoneStateChanged(false));

      expect(view.result.current.microphoneEnabled).toBe(false);
    });

    it("reflects a local TrackUnmuted-equivalent callback without a toggle in flight", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      act(() => view.getSession().callbacks.onMicrophoneStateChanged(false));

      act(() => view.getSession().callbacks.onMicrophoneStateChanged(true));

      expect(view.result.current.microphoneEnabled).toBe(true);
    });

    it("never changes microphoneEnabled from remote track element callbacks", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const remoteAudio = document.createElement("audio");

      act(() => {
        view.getSession().callbacks.onRemoteElement(remoteAudio);
      });
      expect(view.result.current.microphoneEnabled).toBe(true);

      act(() => {
        view.getSession().callbacks.onElementRemoved(remoteAudio);
      });
      expect(view.result.current.microphoneEnabled).toBe(true);
    });

    it("keeps the last confirmed state when muting fails", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      view.getSession().setMicrophoneEnabled.mockRejectedValueOnce({
        kind: "microphone_unavailable",
      });

      await act(() => view.result.current.toggleMicrophone());

      expect(view.result.current.microphoneEnabled).toBe(true);
      expect(view.result.current.error).toBe("Não foi possível acessar ou alterar o microfone.");
      expect(view.result.current.pendingControl).toBeNull();
    });

    it("keeps the last confirmed state when unmuting fails", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      await act(() => view.result.current.toggleMicrophone());
      expect(view.result.current.microphoneEnabled).toBe(false);
      view.getSession().setMicrophoneEnabled.mockRejectedValueOnce({
        kind: "microphone_unavailable",
      });

      await act(() => view.result.current.toggleMicrophone());

      expect(view.result.current.microphoneEnabled).toBe(false);
      expect(view.result.current.pendingControl).toBeNull();
    });

    it("ignores a stale toggle result after stop", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      const toggling = deferred<void>();
      session.setMicrophoneEnabled.mockReturnValueOnce(toggling.promise);

      let toggle!: Promise<boolean | undefined>;
      act(() => {
        toggle = view.result.current.toggleMicrophone();
      });
      await act(() => view.result.current.stop());
      await act(async () => {
        session.callbacks.onMicrophoneStateChanged(false);
        toggling.resolve();
        await toggle;
      });

      expect(view.result.current.status).toBe("idle");
      expect(view.result.current.microphoneEnabled).toBe(false);
      expect(view.result.current.pendingControl).toBeNull();
    });

    it("ignores a stale toggle result after unmount", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      const toggling = deferred<void>();
      session.setMicrophoneEnabled.mockReturnValueOnce(toggling.promise);

      const toggle = view.result.current.toggleMicrophone();
      view.unmount();
      session.callbacks.onMicrophoneStateChanged(false);
      toggling.resolve();
      await toggle;

      expect(session.disconnect).toHaveBeenCalledOnce();
    });

    it("ignores a stale session's callback once a new call replaces it", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const sessionA = view.getSession();
      const toggling = deferred<void>();
      sessionA.setMicrophoneEnabled.mockReturnValueOnce(toggling.promise);

      let staleToggle!: Promise<boolean | undefined>;
      act(() => {
        staleToggle = view.result.current.toggleMicrophone();
      });
      await act(() => view.result.current.stop());
      await act(() =>
        view.result.current.connect(
          { ...videoCall, call_id: "00000000-0000-4000-8000-000000000900" },
          "fresh-token",
          liveKitServerUrl,
        ),
      );
      expect(view.result.current.microphoneEnabled).toBe(true);

      await act(async () => {
        sessionA.callbacks.onMicrophoneStateChanged(false);
        toggling.resolve();
        await staleToggle;
      });

      expect(view.sessions).toHaveLength(2);
      expect(view.result.current.microphoneEnabled).toBe(true);
    });

    it("resyncs to the real state once Reconnected fires", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();

      act(() => session.callbacks.onReconnecting());
      // Resync happens before the hook is told the connection is restored,
      // mirroring the adapter's own notifyMicrophoneState-then-onReconnected
      // ordering: the server may have muted the track while disconnected.
      act(() => {
        session.callbacks.onMicrophoneStateChanged(false);
        session.callbacks.onReconnected();
      });

      expect(view.result.current.status).toBe("connected");
      expect(view.result.current.microphoneEnabled).toBe(false);
    });

    it("starts a new call with the freshly confirmed state after stop", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      await act(() => view.result.current.toggleMicrophone());
      expect(view.result.current.microphoneEnabled).toBe(false);

      await act(() => view.result.current.stop());
      expect(view.result.current.microphoneEnabled).toBe(false);

      await act(() =>
        view.result.current.connect(
          { ...videoCall, call_id: "00000000-0000-4000-8000-000000000901" },
          "fresh-token",
          liveKitServerUrl,
        ),
      );

      expect(view.result.current.microphoneEnabled).toBe(true);
    });

    it("does not leave pendingControl stuck after onDisconnected fires mid-toggle", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      const toggling = deferred<void>();
      session.setMicrophoneEnabled.mockReturnValueOnce(toggling.promise);

      let toggle!: Promise<boolean | undefined>;
      act(() => {
        toggle = view.result.current.toggleMicrophone();
      });
      act(() => session.callbacks.onDisconnected());
      expect(view.result.current.status).toBe("error");

      await act(async () => {
        session.callbacks.onMicrophoneStateChanged(false);
        toggling.resolve();
        await toggle;
      });

      expect(view.result.current.pendingControl).toBeNull();
    });

    it("keeps remote media attached while the local microphone is toggled", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      const remoteAudio = document.createElement("audio");
      act(() => session.callbacks.onRemoteElement(remoteAudio));
      expect(view.result.current.hasRemoteMedia).toBe(true);

      await act(() => view.result.current.toggleMicrophone());

      expect(view.result.current.hasRemoteMedia).toBe(true);
    });

    it("never calls startAudio as part of toggleMicrophone", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      session.startAudio.mockClear();

      await act(() => view.result.current.toggleMicrophone());

      expect(session.startAudio).not.toHaveBeenCalled();
    });
  });

  describe("pendingControl operation isolation across sessions", () => {
    it("does not let A's late microphone toggle clear B's pending microphone control", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const sessionA = view.getSession();
      const togglingA = deferred<void>();
      sessionA.setMicrophoneEnabled.mockReturnValueOnce(togglingA.promise);

      let toggleA!: Promise<boolean | undefined>;
      act(() => {
        toggleA = view.result.current.toggleMicrophone();
      });
      await act(() => view.result.current.stop());
      await act(() =>
        view.result.current.connect(
          { ...videoCall, call_id: "00000000-0000-4000-8000-000000000910" },
          "fresh-token",
          liveKitServerUrl,
        ),
      );
      const sessionB = view.getSession();
      expect(sessionB).not.toBe(sessionA);

      const togglingB = deferred<void>();
      sessionB.setMicrophoneEnabled.mockReturnValueOnce(togglingB.promise);
      let toggleB!: Promise<boolean | undefined>;
      act(() => {
        toggleB = view.result.current.toggleMicrophone();
      });
      expect(view.result.current.pendingControl).toBe("microphone");

      // A duplicate click while B's toggle is still in flight must stay a
      // no-op: only one setMicrophoneEnabled call for B, ever.
      await act(() => view.result.current.toggleMicrophone());
      expect(sessionB.setMicrophoneEnabled).toHaveBeenCalledTimes(1);

      // A's stale operation resolves after B's has already taken the
      // pendingControl slot. Its finally must not clear B's pending state.
      await act(async () => {
        sessionA.callbacks.onMicrophoneStateChanged(false);
        togglingA.resolve();
        await toggleA;
      });
      expect(view.result.current.pendingControl).toBe("microphone");

      // Only when B's own toggle settles does pendingControl clear.
      await act(async () => {
        togglingB.resolve();
        await toggleB;
      });
      expect(view.result.current.pendingControl).toBeNull();
    });

    it("does not let A's late camera toggle clear B's pending camera control", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const sessionA = view.getSession();
      const togglingA = deferred<void>();
      sessionA.setCameraEnabled.mockReturnValueOnce(togglingA.promise);

      let toggleA!: Promise<boolean | undefined>;
      act(() => {
        toggleA = view.result.current.toggleCamera();
      });
      await act(() => view.result.current.stop());
      await act(() =>
        view.result.current.connect(
          { ...videoCall, call_id: "00000000-0000-4000-8000-000000000911" },
          "fresh-token",
          liveKitServerUrl,
        ),
      );
      const sessionB = view.getSession();
      expect(sessionB).not.toBe(sessionA);

      const togglingB = deferred<void>();
      sessionB.setCameraEnabled.mockReturnValueOnce(togglingB.promise);
      let toggleB!: Promise<boolean | undefined>;
      act(() => {
        toggleB = view.result.current.toggleCamera();
      });
      expect(view.result.current.pendingControl).toBe("camera");

      await act(() => view.result.current.toggleCamera());
      expect(sessionB.setCameraEnabled).toHaveBeenCalledTimes(1);

      await act(async () => {
        togglingA.resolve();
        await toggleA;
      });
      expect(view.result.current.pendingControl).toBe("camera");
      // B's cameraEnabled must reflect only its own operation, never A's.
      expect(view.result.current.cameraEnabled).toBe(true);

      await act(async () => {
        togglingB.resolve();
        await toggleB;
      });
      expect(view.result.current.pendingControl).toBeNull();
      expect(view.result.current.cameraEnabled).toBe(false);
    });

    it("does not let A's late toggle error overwrite B's state while B is still pending", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const sessionA = view.getSession();
      const togglingA = deferred<void>();
      sessionA.setMicrophoneEnabled.mockReturnValueOnce(togglingA.promise);

      let toggleA!: Promise<boolean | undefined>;
      act(() => {
        toggleA = view.result.current.toggleMicrophone();
      });
      await act(() => view.result.current.stop());
      await act(() =>
        view.result.current.connect(
          { ...videoCall, call_id: "00000000-0000-4000-8000-000000000912" },
          "fresh-token",
          liveKitServerUrl,
        ),
      );
      const sessionB = view.getSession();
      const togglingB = deferred<void>();
      sessionB.setMicrophoneEnabled.mockReturnValueOnce(togglingB.promise);
      let toggleB!: Promise<boolean | undefined>;
      act(() => {
        toggleB = view.result.current.toggleMicrophone();
      });

      // A's stale operation fails after B has taken over the pending slot.
      await act(async () => {
        togglingA.reject({ kind: "microphone_unavailable" });
        await toggleA.catch(() => undefined);
      });
      expect(view.result.current.error).toBeNull();
      expect(view.result.current.pendingControl).toBe("microphone");

      await act(async () => {
        sessionB.callbacks.onMicrophoneStateChanged(false);
        togglingB.resolve();
        await toggleB;
      });
      expect(view.result.current.error).toBeNull();
      expect(view.result.current.pendingControl).toBeNull();
      expect(view.result.current.microphoneEnabled).toBe(false);
    });

    it("still clears pendingControl on stop even without a new session replacing it", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const session = view.getSession();
      const toggling = deferred<void>();
      session.setMicrophoneEnabled.mockReturnValueOnce(toggling.promise);

      let toggle!: Promise<boolean | undefined>;
      act(() => {
        toggle = view.result.current.toggleMicrophone();
      });
      expect(view.result.current.pendingControl).toBe("microphone");

      await act(() => view.result.current.stop());
      expect(view.result.current.pendingControl).toBeNull();

      await act(async () => {
        toggling.resolve();
        await toggle;
      });
      expect(view.result.current.pendingControl).toBeNull();
    });

    it("keeps the confirmed microphone state coming only from the LiveKit callback, even across the A/B race", async () => {
      const view = setup();
      await act(() => view.result.current.connect(videoCall, "participant-token"));
      const sessionA = view.getSession();
      const togglingA = deferred<void>();
      sessionA.setMicrophoneEnabled.mockReturnValueOnce(togglingA.promise);

      let toggleA!: Promise<boolean | undefined>;
      act(() => {
        toggleA = view.result.current.toggleMicrophone();
      });
      await act(() => view.result.current.stop());
      await act(() =>
        view.result.current.connect(
          { ...videoCall, call_id: "00000000-0000-4000-8000-000000000913" },
          "fresh-token",
          liveKitServerUrl,
        ),
      );
      expect(view.result.current.microphoneEnabled).toBe(true);

      await act(async () => {
        // A's stale callback must be ignored (generation guard), not just
        // its pendingControl bookkeeping.
        sessionA.callbacks.onMicrophoneStateChanged(false);
        togglingA.resolve();
        await toggleA;
      });
      expect(view.result.current.microphoneEnabled).toBe(true);
    });
  });
});

describe("useCallMedia participants (RF-24)", () => {
  it("tracks a participant present before connect completes, without a track", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();

    act(() => session.callbacks.onParticipantConnected("identity-a", "Pedro Almeida"));

    expect(view.result.current.participants).toEqual([
      {
        identity: "identity-a",
        displayName: "Pedro Almeida",
        hasVideo: false,
        hasAudio: false,
        bindVideo: expect.any(Function),
      },
    ]);
  });

  it("falls back to a generic label when the SDK reports no participant name", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();

    act(() => session.callbacks.onParticipantConnected("identity-a", ""));

    expect(view.result.current.participants[0]).toMatchObject({
      identity: "identity-a",
      displayName: "Participante",
    });
  });

  it("adds video and audio for a participant already known and removes them independently", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    const video = remoteVideoFor("identity-a");
    const audio = remoteAudioFor("identity-a");

    act(() => {
      session.callbacks.onParticipantConnected("identity-a", "Pedro Almeida");
      session.callbacks.onRemoteElement(video);
      session.callbacks.onRemoteElement(audio);
    });
    expect(view.result.current.participants).toEqual([
      {
        identity: "identity-a",
        displayName: "Pedro Almeida",
        hasVideo: true,
        hasAudio: true,
        bindVideo: expect.any(Function),
      },
    ]);

    act(() => session.callbacks.onElementRemoved(video));
    expect(view.result.current.participants).toEqual([
      {
        identity: "identity-a",
        displayName: "Pedro Almeida",
        hasVideo: false,
        hasAudio: true,
        bindVideo: expect.any(Function),
      },
    ]);

    act(() => session.callbacks.onElementRemoved(audio));
    expect(view.result.current.participants[0]).toMatchObject({ hasVideo: false, hasAudio: false });
  });

  it("upserts the participant from a track arriving before ParticipantConnected, then fills in the real name once it arrives (HIGH: late displayName)", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    const video = remoteVideoFor("identity-a");

    act(() => session.callbacks.onRemoteElement(video));

    expect(view.result.current.participants).toEqual([
      {
        identity: "identity-a",
        displayName: "Participante",
        hasVideo: true,
        hasAudio: false,
        bindVideo: expect.any(Function),
      },
    ]);
    const bindVideoBeforeName = view.result.current.participants[0].bindVideo;

    act(() => session.callbacks.onParticipantConnected("identity-a", "Pedro Almeida"));

    // Same identity, same media state, same bindVideo reference — only the
    // name changed. Never a second entry, never a reset of hasVideo.
    expect(view.result.current.participants).toEqual([
      {
        identity: "identity-a",
        displayName: "Pedro Almeida",
        hasVideo: true,
        hasAudio: false,
        bindVideo: expect.any(Function),
      },
    ]);
    expect(view.result.current.participants[0].bindVideo).toBe(bindVideoBeforeName);

    render(
      <div ref={view.result.current.participants[0].bindVideo} data-testid="tile-late-name" />,
    );
    expect(screen.getByTestId("tile-late-name")).toContainElement(video);
  });

  it("never lets an empty or blank name regress an already-known real name (HIGH fix)", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();

    act(() => session.callbacks.onParticipantConnected("identity-a", "Pedro Almeida"));
    expect(view.result.current.participants[0].displayName).toBe("Pedro Almeida");

    act(() => session.callbacks.onParticipantConnected("identity-a", ""));
    expect(view.result.current.participants[0].displayName).toBe("Pedro Almeida");

    act(() => session.callbacks.onParticipantConnected("identity-a", "   "));
    expect(view.result.current.participants[0].displayName).toBe("Pedro Almeida");

    expect(view.result.current.participants).toHaveLength(1);
  });

  it("trims whitespace around a real name instead of treating it as a different name", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();

    act(() => session.callbacks.onParticipantConnected("identity-a", "  Pedro Almeida  "));

    expect(view.result.current.participants[0].displayName).toBe("Pedro Almeida");
    expect(view.result.current.participants).toHaveLength(1);
  });

  it("preserves participant identity and display name across a reconnect, without duplicating the entry", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    act(() => session.callbacks.onParticipantConnected("identity-a", "Pedro Almeida"));

    act(() => session.callbacks.onReconnecting());
    act(() => session.callbacks.onReconnected());

    expect(view.result.current.participants).toEqual([
      {
        identity: "identity-a",
        displayName: "Pedro Almeida",
        hasVideo: false,
        hasAudio: false,
        bindVideo: expect.any(Function),
      },
    ]);
  });

  // RF-25 "ponto crítico": on a LiveKit full reconnect, the SDK itself
  // unwinds every remote participant (firing ParticipantDisconnected for
  // each) before repopulating from the server's fresh join response (firing
  // ParticipantConnected for whoever is actually still there) — see
  // livekit-client Room.handleRestarting/handleSignalRestarted. A participant
  // who left during the outage and one who joined during it are exactly what
  // that unwind-then-repopulate models; this asserts our identity-keyed map
  // reflects the post-reconnect roster with no stale entry and no duplicate,
  // without any additional client-side snapshot/polling reconciliation.
  it("reconciles the roster via ParticipantConnected/Disconnected alone when membership changes during a reconnect", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    act(() => {
      session.callbacks.onParticipantConnected("identity-a", "Pedro Almeida");
      session.callbacks.onParticipantConnected("identity-b", "Maria Silva");
    });

    act(() => session.callbacks.onReconnecting());
    // The SDK's own full-reconnect unwind: every remote participant known
    // before the outage is disconnected first...
    act(() => {
      session.callbacks.onParticipantDisconnected("identity-a");
      session.callbacks.onParticipantDisconnected("identity-b");
    });
    // ...then repopulated from the server's fresh, authoritative list: B left
    // for real during the outage, C joined during it, A is still there.
    act(() => {
      session.callbacks.onParticipantConnected("identity-a", "Pedro Almeida");
      session.callbacks.onParticipantConnected("identity-c", "Ana Lima");
    });
    act(() => session.callbacks.onReconnected());

    expect(view.result.current.participants.map((p) => p.identity)).toEqual([
      "identity-a",
      "identity-c",
    ]);
    expect(view.result.current.status).toBe("connected");
  });

  it("removes a participant on disconnect and keeps the others", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();

    act(() => {
      session.callbacks.onParticipantConnected("identity-a", "Pedro Almeida");
      session.callbacks.onParticipantConnected("identity-b", "Maria Silva");
      session.callbacks.onParticipantDisconnected("identity-a");
    });

    expect(view.result.current.participants.map((p) => p.identity)).toEqual(["identity-b"]);
  });

  it("mounts a participant's video element into its own tile container, never into the flat remote container", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    const video = remoteVideoFor("identity-a");
    act(() => {
      session.callbacks.onParticipantConnected("identity-a", "Pedro Almeida");
      session.callbacks.onRemoteElement(video);
    });

    render(<div ref={view.result.current.participants[0].bindVideo} data-testid="tile-a" />);

    expect(screen.getByTestId("tile-a")).toContainElement(video);
  });

  it("mounts remote audio into bindRemoteAudio independent of bindRemoteMedia", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    const audio = remoteAudioFor("identity-a");

    act(() => session.callbacks.onRemoteElement(audio));
    render(<div ref={view.result.current.bindRemoteAudio} data-testid="audio-sink" />);

    expect(screen.getByTestId("audio-sink")).toContainElement(audio);
  });

  it("clears every participant on stop()", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    act(() => session.callbacks.onParticipantConnected("identity-a", "Pedro Almeida"));
    expect(view.result.current.participants).toHaveLength(1);

    await act(() => view.result.current.stop());

    expect(view.result.current.participants).toEqual([]);
  });
});

describe("useCallMedia remote camera mute/unmute (RF-24 code review achado 1)", () => {
  it("keeps the tile but flips hasVideo=false on a remote camera TrackMuted, and restores it on TrackUnmuted", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    const video = remoteVideoFor("identity-a");
    act(() => {
      session.callbacks.onParticipantConnected("identity-a", "Pedro Almeida");
      session.callbacks.onRemoteElement(video);
    });
    expect(view.result.current.participants).toEqual([
      {
        identity: "identity-a",
        displayName: "Pedro Almeida",
        hasVideo: true,
        hasAudio: false,
        bindVideo: expect.any(Function),
      },
    ]);

    act(() => session.callbacks.onRemoteVideoAvailabilityChanged("identity-a", false));

    expect(view.result.current.participants).toHaveLength(1);
    expect(view.result.current.participants[0]).toMatchObject({
      identity: "identity-a",
      hasVideo: false,
    });

    act(() => session.callbacks.onRemoteVideoAvailabilityChanged("identity-a", true));

    expect(view.result.current.participants[0]).toMatchObject({
      identity: "identity-a",
      hasVideo: true,
    });
  });

  it("mute clears the tile's mounted element so no frozen frame shows behind the fallback", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    const video = remoteVideoFor("identity-a");
    act(() => {
      session.callbacks.onParticipantConnected("identity-a", "Pedro Almeida");
      session.callbacks.onRemoteElement(video);
    });
    render(<div ref={view.result.current.participants[0].bindVideo} data-testid="tile-a" />);
    expect(screen.getByTestId("tile-a")).toContainElement(video);

    act(() => session.callbacks.onRemoteVideoAvailabilityChanged("identity-a", false));
    expect(screen.getByTestId("tile-a")).toBeEmptyDOMElement();

    act(() => session.callbacks.onRemoteVideoAvailabilityChanged("identity-a", true));
    expect(screen.getByTestId("tile-a")).toContainElement(video);
  });

  it("does not fabricate hasVideo=true on unmute when no video element was ever attached", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    act(() => session.callbacks.onParticipantConnected("identity-a", "Pedro Almeida"));

    act(() => session.callbacks.onRemoteVideoAvailabilityChanged("identity-a", true));

    expect(view.result.current.participants[0]).toMatchObject({ hasVideo: false });
  });

  it("a remote microphone mute/unmute never touches hasVideo", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    const video = remoteVideoFor("identity-a");
    const audio = remoteAudioFor("identity-a");
    act(() => {
      session.callbacks.onParticipantConnected("identity-a", "Pedro Almeida");
      session.callbacks.onRemoteElement(video);
      session.callbacks.onRemoteElement(audio);
    });

    // Local mic mute callback: distinct channel from the new one, must not
    // be confused with a remote camera event.
    act(() => session.callbacks.onMicrophoneStateChanged(false));

    expect(view.result.current.participants[0]).toMatchObject({ hasVideo: true, hasAudio: true });
  });

  it("ignores a camera-availability event for a participant that never connected", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();

    act(() => session.callbacks.onRemoteVideoAvailabilityChanged("ghost", false));

    expect(view.result.current.participants).toEqual([]);
  });
});

describe("useCallMedia session serialization (RF-24 code review achado 4)", () => {
  it("disconnected terminal invalidates the session so a retry with the same call_id reconnects for real", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const firstSession = view.getSession();
    expect(view.factory).toHaveBeenCalledOnce();

    act(() => firstSession.callbacks.onDisconnected());
    expect(view.result.current.status).toBe("error");

    await act(() => view.result.current.connect(videoCall, "participant-token"));

    expect(view.factory).toHaveBeenCalledTimes(2);
    const secondSession = view.getSession();
    expect(secondSession).not.toBe(firstSession);
    expect(secondSession.connect).toHaveBeenCalledOnce();
    expect(view.result.current.status).toBe("connected");
  });

  it("clears participants on a terminal disconnect and does not carry them into the rejoin", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const firstSession = view.getSession();
    act(() => firstSession.callbacks.onParticipantConnected("identity-a", "Pedro Almeida"));
    expect(view.result.current.participants).toHaveLength(1);

    act(() => firstSession.callbacks.onDisconnected());
    expect(view.result.current.participants).toEqual([]);

    await act(() =>
      view.result.current.connect(
        { ...videoCall, call_id: "00000000-0000-4000-8000-000000000809" },
        "fresh-token",
      ),
    );
    const secondSession = view.getSession();
    act(() => secondSession.callbacks.onParticipantConnected("identity-b", "Maria Silva"));

    expect(view.result.current.participants.map((p) => p.identity)).toEqual(["identity-b"]);
  });

  it("a stale callback from a session invalidated by a terminal disconnect never changes the new session's state", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const firstSession = view.getSession();
    act(() => firstSession.callbacks.onDisconnected());

    await act(() =>
      view.result.current.connect(
        { ...videoCall, call_id: "00000000-0000-4000-8000-000000000810" },
        "fresh-token",
      ),
    );
    expect(view.result.current.status).toBe("connected");
    // connect() itself confirms the mic on for the new (second) session.
    expect(view.result.current.microphoneEnabled).toBe(true);

    // Late events from the dead Room object (e.g. a queued microtask that ran
    // after invalidation): must be ignored by the generation guard, so an
    // opposite value from the stale session must not flip the new session's
    // confirmed state, and a stale participant must not reappear.
    act(() => firstSession.callbacks.onMicrophoneStateChanged(false));
    act(() => firstSession.callbacks.onParticipantConnected("ghost-from-old-room", "Ghost"));

    expect(view.result.current.microphoneEnabled).toBe(true);
    expect(view.result.current.participants).toEqual([]);
  });

  it("never starts a new Room before the previous one's disconnect has actually resolved", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const firstSession = view.getSession();
    let resolveDisconnect!: () => void;
    firstSession.disconnect.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          resolveDisconnect = resolve;
        }),
    );

    const stopping = view.result.current.stop();
    let connectResolved = false;
    const connecting = view.result.current
      .connect({ ...videoCall, call_id: "00000000-0000-4000-8000-000000000811" }, "fresh-token")
      .then(() => {
        connectResolved = true;
      });

    // Give pending microtasks a chance to run; the second Room must not be
    // constructed yet because the first one's disconnect() has not resolved.
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(view.factory).toHaveBeenCalledOnce();
    expect(connectResolved).toBe(false);

    await act(async () => {
      resolveDisconnect();
      await stopping;
      await connecting;
    });

    expect(view.factory).toHaveBeenCalledTimes(2);
    expect(connectResolved).toBe(true);
  });
});

describe("useCallMedia stop() real disconnect failure (RF-24 quarta correção)", () => {
  it("rejects stop() when LiveKitSession.disconnect() itself rejects, instead of resolving", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    session.disconnect.mockRejectedValueOnce(new Error("disconnect failed"));

    await expect(view.result.current.stop()).rejects.toThrow("disconnect failed");

    expect(session.disconnect).toHaveBeenCalledOnce();
  });

  it("clears refs/state synchronously even when the underlying disconnect rejects", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    session.disconnect.mockRejectedValueOnce(new Error("disconnect failed"));

    await act(async () => {
      await view.result.current.stop().catch(() => undefined);
    });

    expect(view.result.current.status).toBe("idle");
    expect(view.result.current.participants).toEqual([]);
  });

  it("does not leave disconnectPromiseRef stuck: a retried stop() calls disconnect() again on the same Room and can succeed", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const session = view.getSession();
    session.disconnect.mockRejectedValueOnce(new Error("disconnect failed"));

    await expect(view.result.current.stop()).rejects.toThrow("disconnect failed");
    await expect(view.result.current.stop()).resolves.toBeUndefined();

    expect(session.disconnect).toHaveBeenCalledTimes(2);
  });

  it("never starts a new Room while a rejected disconnect's cleanup is still pending", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const firstSession = view.getSession();
    let rejectDisconnect!: (error: unknown) => void;
    firstSession.disconnect.mockImplementationOnce(
      () =>
        new Promise<void>((_resolve, reject) => {
          rejectDisconnect = reject;
        }),
    );

    const stopping = view.result.current.stop().catch(() => undefined);
    const connecting = view.result.current.connect(
      { ...videoCall, call_id: "00000000-0000-4000-8000-000000000812" },
      "fresh-token",
    );
    connecting.catch(() => undefined);

    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(view.factory).toHaveBeenCalledOnce();

    await act(async () => {
      rejectDisconnect(new Error("disconnect failed"));
      await stopping;
    });

    // The rejection has now fully settled, but cleanup was never confirmed:
    // a second Room must still not exist.
    expect(view.factory).toHaveBeenCalledOnce();
  });

  it("does not produce an unhandled rejection when stop() rejects during unmount", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    view.getSession().disconnect.mockRejectedValueOnce(new Error("disconnect failed"));

    view.unmount();
    await act(async () => undefined);
  });

  // ── HIGH (quinta correção): a Room whose cleanup rejected must permanently
  // block a new Room — across connect(), startAudio(), and ensureSession() —
  // until an explicit stop()/leave() retries the disconnect and it succeeds.
  it("Cenário 1 — connect() rejects and never creates Room B while Room A's cleanup is unconfirmed", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const roomA = view.getSession();
    roomA.disconnect.mockRejectedValueOnce(new Error("disconnect failed"));

    await view.result.current.stop().catch(() => undefined);
    expect(view.factory).toHaveBeenCalledOnce();

    await expect(
      view.result.current.connect(
        { ...videoCall, call_id: "00000000-0000-4000-8000-000000000813" },
        "fresh-token",
      ),
    ).rejects.toThrow();

    expect(view.factory).toHaveBeenCalledOnce();
    expect(view.sessions).toHaveLength(1);
  });

  it("Cenário 2 — startAudio() does not create Room B while Room A's cleanup is unconfirmed", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const roomA = view.getSession();
    roomA.disconnect.mockRejectedValueOnce(new Error("disconnect failed"));

    await view.result.current.stop().catch(() => undefined);
    expect(view.factory).toHaveBeenCalledOnce();

    await act(() => view.result.current.startAudio());

    expect(view.factory).toHaveBeenCalledOnce();
    expect(view.sessions).toHaveLength(1);
  });

  it("Cenário 3 — a successful retry of the failed disconnect releases the block so Room B can be created", async () => {
    const view = setup();
    await act(() => view.result.current.connect(videoCall, "participant-token"));
    const roomA = view.getSession();
    roomA.disconnect.mockRejectedValueOnce(new Error("disconnect failed"));

    await view.result.current.stop().catch(() => undefined);
    await expect(
      view.result.current.connect(
        { ...videoCall, call_id: "00000000-0000-4000-8000-000000000814" },
        "fresh-token",
      ),
    ).rejects.toThrow();
    expect(view.factory).toHaveBeenCalledOnce();

    // Explicit retry: the second stop() call must hit the SAME Room A
    // instance again, and this time it resolves.
    await view.result.current.stop();
    expect(roomA.disconnect).toHaveBeenCalledTimes(2);

    await act(() =>
      view.result.current.connect(
        { ...videoCall, call_id: "00000000-0000-4000-8000-000000000815" },
        "fresh-token",
      ),
    );

    expect(view.factory).toHaveBeenCalledTimes(2);
    expect(view.sessions).toHaveLength(2);
    expect(view.sessions[1]).not.toBe(roomA);
    expect(view.result.current.status).toBe("connected");
  });
});

describe("useCallMedia connect() failure cleanup (Security Review pós-Code Quality)", () => {
  // Camera already reached LiveKit before microphone failed, so Room A is a
  // genuinely sensitive session — not "connect never touched media" — when
  // its own cleanup attempt then also rejects.
  async function setupPartialConnectFailure() {
    const view = setup();
    await act(() => view.result.current.prepare());
    await act(() => view.result.current.startAudio());
    const roomA = view.getSession();
    roomA.enableMicrophone.mockRejectedValueOnce(new Error("microphone unavailable"));
    roomA.disconnect.mockRejectedValueOnce(new Error("disconnect failed"));

    await act(async () => {
      await expect(view.result.current.connect(videoCall, "participant-token")).rejects.toThrow();
    });

    expect(roomA.enableCamera).toHaveBeenCalledOnce();
    expect(roomA.enableMicrophone).toHaveBeenCalledOnce();
    expect(roomA.disconnect).toHaveBeenCalledOnce();
    return { view, roomA };
  }

  it("does not create another Room when cleanup after a partial connect failure (camera on, microphone denied) is rejected", async () => {
    const { view, roomA } = await setupPartialConnectFailure();

    await expect(
      view.result.current.connect(
        { ...videoCall, call_id: "00000000-0000-4000-8000-000000000820" },
        "token-b",
      ),
    ).rejects.toThrow();

    expect(view.factory).toHaveBeenCalledOnce();
    expect(view.sessions).toHaveLength(1);
    expect(view.sessions[0]).toBe(roomA);
  });

  it("lets a resolved retry of Room A's cleanup create Room B, without leaking Room A's state or elements into it", async () => {
    const { view, roomA } = await setupPartialConnectFailure();
    await view.result.current
      .connect({ ...videoCall, call_id: "00000000-0000-4000-8000-000000000821" }, "token-b")
      .catch(() => undefined);
    expect(view.factory).toHaveBeenCalledOnce();

    // Explicit retry (the only way to release the block): stop() must call
    // disconnect() again on the very same Room A instance.
    await view.result.current.stop();
    expect(roomA.disconnect).toHaveBeenCalledTimes(2);

    await act(() =>
      view.result.current.connect(
        { ...videoCall, call_id: "00000000-0000-4000-8000-000000000822" },
        "fresh-token",
      ),
    );

    expect(view.factory).toHaveBeenCalledTimes(2);
    expect(view.sessions).toHaveLength(2);
    const roomB = view.sessions[1];
    expect(roomB).not.toBe(roomA);
    // No stale target/state carried over from A into B.
    expect(view.result.current.status).toBe("connected");
    expect(view.result.current.participants).toEqual([]);
    expect(roomB.connect).toHaveBeenCalledWith(liveKitServerUrl, "fresh-token");
    expect(roomA.connect).not.toHaveBeenCalledWith(liveKitServerUrl, "fresh-token");
  });

  it("does not expose LiveKit internals (token, server URL, room/session identifiers) in the surfaced error", async () => {
    const { view } = await setupPartialConnectFailure();

    expect(view.result.current.error).toBe("Não foi possível conectar a mídia da chamada.");
    expect(view.result.current.error).not.toContain("participant-token");
    expect(view.result.current.error).not.toContain(liveKitServerUrl);
  });
});
