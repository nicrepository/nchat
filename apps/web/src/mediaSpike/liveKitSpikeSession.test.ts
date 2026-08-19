import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  createLiveKitSpikeSession,
  SpikeMediaError,
  type LiveKitSpikeSessionCallbacks,
} from "./liveKitSpikeSession";

const liveKitMock = vi.hoisted(() => {
  const events = {
    TrackSubscribed: "trackSubscribed",
    TrackUnsubscribed: "trackUnsubscribed",
    ParticipantConnected: "participantConnected",
    ParticipantDisconnected: "participantDisconnected",
    Disconnected: "disconnected",
    Reconnecting: "reconnecting",
    Reconnected: "reconnected",
    AudioPlaybackStatusChanged: "audioPlaybackChanged",
    TrackMuted: "trackMuted",
    TrackUnmuted: "trackUnmuted",
    ActiveSpeakersChanged: "activeSpeakersChanged",
    LocalTrackUnpublished: "localTrackUnpublished",
  } as const;
  const kinds = { Audio: "audio", Video: "video" } as const;
  const sources = {
    Camera: "camera",
    Microphone: "microphone",
    ScreenShare: "screen_share",
  } as const;
  const permissionDenied = "permission-denied";
  const rooms: MockRoom[] = [];

  interface MockPublication {
    source: string;
    isMuted: boolean;
    isSubscribed: boolean;
  }

  interface MockRemoteParticipant {
    identity: string;
    name?: string;
    getTrackPublication: (source: string) => MockPublication | undefined;
  }

  class MockRoom {
    readonly options: unknown;
    readonly listeners = new Map<string, Set<(...args: unknown[]) => void>>();
    readonly remoteParticipants = new Map<string, MockRemoteParticipant>();
    // Once a participant instance has been disconnected, a stale/queued
    // TrackSubscribed replay for that exact instance must never resurrect it
    // — only a genuinely new instance (real SDK reentry) may occupy the
    // identity slot again. Tracked by object identity, not the identity
    // string, so a legitimate reentry is unaffected.
    private readonly disconnectedInstances = new WeakSet<object>();
    readonly localParticipant = {
      setCameraEnabled: vi.fn(async (): Promise<unknown> => undefined),
      setMicrophoneEnabled: vi.fn(async (): Promise<unknown> => undefined),
      setScreenShareEnabled: vi.fn(async (): Promise<unknown> => undefined),
      getTrackPublication: vi.fn((): unknown => undefined),
    };
    readonly connect = vi.fn(async (): Promise<unknown> => undefined);
    readonly startAudio = vi.fn(async () => undefined);
    readonly disconnect = vi.fn(async () => undefined);
    readonly removeAllListeners = vi.fn();

    constructor(options: unknown) {
      this.options = options;
      rooms.push(this);
    }

    on(event: string, callback: (...args: never[]) => void): this {
      const callbacks = this.listeners.get(event) ?? new Set();
      callbacks.add(callback as (...args: unknown[]) => void);
      this.listeners.set(event, callbacks);
      return this;
    }

    off(event: string, callback: (...args: never[]) => void): this {
      this.listeners.get(event)?.delete(callback as (...args: unknown[]) => void);
      return this;
    }

    emit(event: string, ...args: unknown[]): void {
      // Mirrors the real Room: remoteParticipants always holds whichever
      // instance is current for an identity. Auto-syncing it here (instead
      // of every test call site managing it by hand) is what lets the
      // adapter's isCurrentParticipant() reference check work the same way
      // it does against the real SDK.
      if (event === events.TrackSubscribed) {
        const participant = args[2] as { identity: string } | undefined;
        if (
          participant &&
          !this.disconnectedInstances.has(participant) &&
          !this.remoteParticipants.has(participant.identity)
        ) {
          this.remoteParticipants.set(participant.identity, participant as MockRemoteParticipant);
        }
      } else if (event === events.ParticipantDisconnected) {
        const participant = args[0] as { identity: string } | undefined;
        if (participant) {
          this.disconnectedInstances.add(participant);
          if (this.remoteParticipants.get(participant.identity) === participant) {
            this.remoteParticipants.delete(participant.identity);
          }
        }
      }
      for (const callback of this.listeners.get(event) ?? []) callback(...args);
    }
  }

  return {
    events,
    kinds,
    sources,
    permissionDenied,
    rooms,
    MockRoom,
    getFailure: vi.fn((error: unknown) =>
      (error as { deviceFailure?: string }).deviceFailure === permissionDenied
        ? permissionDenied
        : "other",
    ),
  };
});

vi.mock("livekit-client", () => ({
  MediaDeviceFailure: {
    PermissionDenied: liveKitMock.permissionDenied,
    getFailure: liveKitMock.getFailure,
  },
  Room: liveKitMock.MockRoom,
  RoomEvent: liveKitMock.events,
  Track: { Kind: liveKitMock.kinds, Source: liveKitMock.sources },
}));

function callbacks(): LiveKitSpikeSessionCallbacks {
  return {
    onLocalElement: vi.fn(),
    onRemoteElement: vi.fn(),
    onElementRemoved: vi.fn((element: HTMLMediaElement) => element.remove()),
    onDisconnected: vi.fn(),
    onReconnecting: vi.fn(),
    onReconnected: vi.fn(),
    onAudioPlaybackChanged: vi.fn(),
    onMicrophoneStateChanged: vi.fn(),
    onParticipantConnected: vi.fn(),
    onParticipantDisconnected: vi.fn(),
    onRemoteVideoAvailabilityChanged: vi.fn(),
    onActiveSpeakersChanged: vi.fn(),
    onScreenShareChanged: vi.fn(),
    onRemoteScreenShareChanged: vi.fn(),
  };
}

function micPublication(isMuted: boolean) {
  return { isMuted, source: liveKitMock.sources.Microphone };
}

function cameraPublication(isMuted: boolean, isSubscribed = true) {
  return { isMuted, isSubscribed, source: liveKitMock.sources.Camera };
}

/** A remote participant known to the Room but not necessarily seen via a TrackSubscribed event. */
function fakeRemoteParticipant(
  identity: string,
  camera?: { isMuted: boolean; isSubscribed?: boolean },
  name?: string,
) {
  return {
    identity,
    name,
    getTrackPublication: (source: string) =>
      source === liveKitMock.sources.Camera && camera
        ? {
            source: liveKitMock.sources.Camera,
            isMuted: camera.isMuted,
            isSubscribed: camera.isSubscribed ?? true,
          }
        : undefined,
  };
}

function createTrack(
  kind: "audio" | "video",
  element: HTMLMediaElement = document.createElement(kind),
) {
  return {
    kind,
    attach: vi.fn(() => element),
    detach: vi.fn(() => [element]),
  };
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

function setup() {
  const handlers = callbacks();
  const session = createLiveKitSpikeSession(handlers);
  const room = liveKitMock.rooms.at(-1);
  if (!room) throw new Error("Room was not constructed");
  return { handlers, room, session };
}

describe("createLiveKitSpikeSession", () => {
  beforeEach(() => {
    liveKitMock.rooms.length = 0;
    vi.clearAllMocks();
  });

  it("connects with the token and registers only the adapter callbacks", async () => {
    const { room, session } = setup();

    await session.startAudio();
    await session.connect("ws://127.0.0.1:7880", "participant-token");

    expect(room.options).toEqual({ adaptiveStream: true, dynacast: true });
    expect(room.startAudio).toHaveBeenCalledOnce();
    expect(room.connect).toHaveBeenCalledWith("ws://127.0.0.1:7880", "participant-token", {
      autoSubscribe: true,
    });
    expect([...room.listeners.keys()]).toEqual(Object.values(liveKitMock.events));
    for (const event of Object.values(liveKitMock.events)) {
      expect(room.listeners.get(event)).toHaveLength(1);
    }
  });

  it("attaches one local preview and toggles camera and microphone", async () => {
    const { handlers, room, session } = setup();
    const localVideo = document.createElement("video");
    const localTrack = createTrack("video", localVideo);
    room.localParticipant.setCameraEnabled.mockResolvedValue({ track: localTrack });

    await session.enableCamera();
    await session.enableCamera();
    await session.enableMicrophone();
    await session.setMicrophoneEnabled(false);
    await session.setMicrophoneEnabled(true);
    await session.setCameraEnabled(false);

    expect(room.localParticipant.setCameraEnabled).toHaveBeenNthCalledWith(1, true);
    expect(localTrack.attach).toHaveBeenCalledOnce();
    expect(localVideo.autoplay).toBe(true);
    expect(localVideo.muted).toBe(true);
    expect(localVideo.playsInline).toBe(true);
    expect(handlers.onLocalElement).toHaveBeenCalledOnce();
    expect(room.localParticipant.setMicrophoneEnabled.mock.calls).toEqual([
      [true],
      [false],
      [true],
    ]);
    expect(handlers.onElementRemoved).toHaveBeenCalledWith(localVideo);
  });

  it("forwards active speakers and controls screen share explicitly", async () => {
    const { handlers, room, session } = setup();

    room.emit(liveKitMock.events.ActiveSpeakersChanged, [
      { identity: "identity-a" },
      { identity: "identity-b" },
    ]);
    await session.setScreenShareEnabled(true);
    await session.setScreenShareEnabled(false);

    expect(handlers.onActiveSpeakersChanged).toHaveBeenCalledWith(["identity-a", "identity-b"]);
    expect(room.localParticipant.setScreenShareEnabled.mock.calls).toEqual([[true], [false]]);
    expect(handlers.onScreenShareChanged).toHaveBeenNthCalledWith(1, true);
    expect(handlers.onScreenShareChanged).toHaveBeenNthCalledWith(2, false);
  });

  it("reports native screen-share termination", () => {
    const { handlers, room } = setup();

    room.emit(
      liveKitMock.events.LocalTrackUnpublished,
      { source: liveKitMock.sources.ScreenShare },
      room.localParticipant,
    );

    expect(handlers.onScreenShareChanged).toHaveBeenCalledWith(false);
  });

  it("keeps remote screen share separate from participant camera video", () => {
    const { handlers, room } = setup();
    const participant = { sid: "participant-a", identity: "identity-a" };
    const element = document.createElement("video");
    const track = createTrack("video", element);
    const publication = { source: liveKitMock.sources.ScreenShare };

    room.emit(liveKitMock.events.TrackSubscribed, track, publication, participant);
    expect(handlers.onRemoteScreenShareChanged).toHaveBeenCalledWith("identity-a", element);
    expect(handlers.onRemoteElement).not.toHaveBeenCalled();

    room.emit(liveKitMock.events.TrackUnsubscribed, track, publication, participant);
    expect(handlers.onRemoteScreenShareChanged).toHaveBeenLastCalledWith("identity-a", null);
  });

  it("attaches remote video and audio once per track", async () => {
    const { handlers, room } = setup();
    const participant = { sid: "participant-a" };
    const video = document.createElement("video");
    const audio = document.createElement("audio");
    audio.play = vi.fn(async () => undefined);
    const videoTrack = createTrack("video", video);
    const audioTrack = createTrack("audio", audio);

    room.emit(liveKitMock.events.TrackSubscribed, videoTrack, {}, participant);
    room.emit(liveKitMock.events.TrackSubscribed, videoTrack, {}, participant);
    room.emit(liveKitMock.events.TrackSubscribed, audioTrack, {}, participant);
    await Promise.resolve();

    expect(videoTrack.attach).toHaveBeenCalledOnce();
    expect(audioTrack.attach).toHaveBeenCalledOnce();
    expect(audio.play).toHaveBeenCalledOnce();
    expect(handlers.onRemoteElement).toHaveBeenCalledTimes(2);
    expect(handlers.onRemoteElement).toHaveBeenCalledWith(video);
    expect(handlers.onRemoteElement).toHaveBeenCalledWith(audio);
  });

  it("tags every remote element with the participant identity, never the internal sid", async () => {
    const { room } = setup();
    const participant = {
      sid: "participant-a-sid",
      identity: "11111111-1111-4111-8111-111111111111",
    };
    const video = document.createElement("video");
    const videoTrack = createTrack("video", video);

    room.emit(liveKitMock.events.TrackSubscribed, videoTrack, {}, participant);

    expect(video.dataset.participantIdentity).toBe(participant.identity);
  });

  it("reports participants already in the room once connect() completes, with their display name", async () => {
    const { handlers, room, session } = setup();
    room.remoteParticipants.set(
      "a",
      fakeRemoteParticipant("identity-a", undefined, "Pedro Almeida"),
    );
    room.remoteParticipants.set("b", fakeRemoteParticipant("identity-b", undefined, "Maria Silva"));

    await session.connect("ws://127.0.0.1:7880", "participant-token");

    expect(handlers.onParticipantConnected).toHaveBeenCalledWith("identity-a", "Pedro Almeida");
    expect(handlers.onParticipantConnected).toHaveBeenCalledWith("identity-b", "Maria Silva");
    expect(handlers.onParticipantConnected).toHaveBeenCalledTimes(2);
  });

  it("reports an existing participant with no name as an empty display name, never the identity", async () => {
    const { handlers, room, session } = setup();
    room.remoteParticipants.set("a", fakeRemoteParticipant("identity-a"));

    await session.connect("ws://127.0.0.1:7880", "participant-token");

    expect(handlers.onParticipantConnected).toHaveBeenCalledExactlyOnceWith("identity-a", "");
  });

  it("reports a participant joining after connect (with display name) and one leaving", () => {
    const { handlers, room } = setup();

    room.emit(liveKitMock.events.ParticipantConnected, {
      sid: "c-sid",
      identity: "identity-c",
      name: "Ana Lima",
    });
    expect(handlers.onParticipantConnected).toHaveBeenCalledExactlyOnceWith(
      "identity-c",
      "Ana Lima",
    );

    room.emit(liveKitMock.events.ParticipantDisconnected, { sid: "c-sid", identity: "identity-c" });
    expect(handlers.onParticipantDisconnected).toHaveBeenCalledExactlyOnceWith("identity-c");
  });

  it("does not report existing participants when connect() completes after disposal", async () => {
    const { handlers, room, session } = setup();
    room.remoteParticipants.set("a", fakeRemoteParticipant("identity-a"));
    const connecting = session.connect("ws://127.0.0.1:7880", "participant-token");
    await session.disconnect();
    await connecting;

    expect(handlers.onParticipantConnected).not.toHaveBeenCalled();
  });

  it("reports rejected remote audio playback without dropping the element", async () => {
    const { handlers, room } = setup();
    const audio = document.createElement("audio");
    audio.play = vi.fn(async () => {
      throw new DOMException("autoplay blocked", "NotAllowedError");
    });
    const audioTrack = createTrack("audio", audio);

    room.emit(liveKitMock.events.TrackSubscribed, audioTrack, {}, { sid: "participant-a" });
    await Promise.resolve();
    await Promise.resolve();

    expect(handlers.onRemoteElement).toHaveBeenCalledWith(audio);
    expect(handlers.onElementRemoved).not.toHaveBeenCalled();
    expect(handlers.onAudioPlaybackChanged).toHaveBeenCalledExactlyOnceWith(false);
  });

  it("forwards playback status without duplicating its listener after reconnect", () => {
    const { handlers, room } = setup();

    room.emit(liveKitMock.events.AudioPlaybackStatusChanged, false);
    room.emit(liveKitMock.events.Reconnecting);
    room.emit(liveKitMock.events.Reconnected);
    room.emit(liveKitMock.events.AudioPlaybackStatusChanged, true);

    expect(handlers.onAudioPlaybackChanged).toHaveBeenNthCalledWith(1, false);
    expect(handlers.onAudioPlaybackChanged).toHaveBeenNthCalledWith(2, true);
    expect(room.listeners.get(liveKitMock.events.AudioPlaybackStatusChanged)).toHaveLength(1);
  });

  it("unsubscribes only the matching track", () => {
    const { handlers, room } = setup();
    const participant = { sid: "participant-a" };
    const firstElement = document.createElement("video");
    const secondElement = document.createElement("audio");
    secondElement.play = vi.fn(async () => undefined);
    const firstTrack = createTrack("video", firstElement);
    const secondTrack = createTrack("audio", secondElement);
    room.emit(liveKitMock.events.TrackSubscribed, firstTrack, {}, participant);
    room.emit(liveKitMock.events.TrackSubscribed, secondTrack, {}, participant);

    room.emit(liveKitMock.events.TrackUnsubscribed, firstTrack, {}, participant);

    expect(firstTrack.detach).toHaveBeenCalledOnce();
    expect(secondTrack.detach).not.toHaveBeenCalled();
    expect(handlers.onElementRemoved).toHaveBeenCalledTimes(1);
    expect(handlers.onElementRemoved).toHaveBeenCalledWith(firstElement);
  });

  it("removes every track for the disconnected participant and preserves others", () => {
    const { handlers, room } = setup();
    const participantA = { sid: "participant-a", identity: "identity-a" };
    const participantB = { sid: "participant-b", identity: "identity-b" };
    const videoA = document.createElement("video");
    const audioA = document.createElement("audio");
    const videoB = document.createElement("video");
    audioA.play = vi.fn(async () => undefined);
    const videoTrackA = createTrack("video", videoA);
    const audioTrackA = createTrack("audio", audioA);
    const videoTrackB = createTrack("video", videoB);
    room.emit(liveKitMock.events.TrackSubscribed, videoTrackA, {}, participantA);
    room.emit(liveKitMock.events.TrackSubscribed, audioTrackA, {}, participantA);
    room.emit(liveKitMock.events.TrackSubscribed, videoTrackB, {}, participantB);

    room.emit(liveKitMock.events.ParticipantDisconnected, participantA);

    expect(handlers.onElementRemoved).toHaveBeenCalledTimes(2);
    expect(handlers.onElementRemoved).toHaveBeenCalledWith(videoA);
    expect(handlers.onElementRemoved).toHaveBeenCalledWith(audioA);
    expect(
      vi.mocked(handlers.onElementRemoved).mock.calls.some(([element]) => element === videoB),
    ).toBe(false);
    room.emit(liveKitMock.events.TrackUnsubscribed, videoTrackB, {}, participantB);
    expect(handlers.onElementRemoved).toHaveBeenCalledWith(videoB);
  });

  it("forwards connection lifecycle events only before disposal", async () => {
    const { handlers, room, session } = setup();

    room.emit(liveKitMock.events.Reconnecting);
    room.emit(liveKitMock.events.Reconnected);
    room.emit(liveKitMock.events.Disconnected);

    expect(handlers.onReconnecting).toHaveBeenCalledOnce();
    expect(handlers.onReconnected).toHaveBeenCalledOnce();
    expect(handlers.onDisconnected).toHaveBeenCalledOnce();

    await session.disconnect();
    room.emit(liveKitMock.events.Disconnected);
    room.emit(liveKitMock.events.AudioPlaybackStatusChanged, false);
    expect(handlers.onDisconnected).toHaveBeenCalledOnce();
    expect(handlers.onAudioPlaybackChanged).not.toHaveBeenCalled();
  });

  it("cleans up idempotently without removing internal Room listeners", async () => {
    const { handlers, room, session } = setup();
    const localVideo = document.createElement("video");
    const localTrack = createTrack("video", localVideo);
    const remoteVideo = document.createElement("video");
    const remoteTrack = createTrack("video", remoteVideo);
    room.localParticipant.setCameraEnabled.mockResolvedValue({ track: localTrack });
    await session.enableCamera();
    room.emit(liveKitMock.events.TrackSubscribed, remoteTrack, {}, { sid: "participant-a" });

    await Promise.all([session.disconnect(), session.disconnect()]);

    expect(room.disconnect).toHaveBeenCalledOnce();
    expect(room.removeAllListeners).not.toHaveBeenCalled();
    expect(room.listeners.size).toBe(Object.values(liveKitMock.events).length);
    for (const event of Object.values(liveKitMock.events)) {
      expect(room.listeners.get(event)).toHaveLength(0);
    }
    expect(handlers.onElementRemoved).toHaveBeenCalledWith(localVideo);
    expect(handlers.onElementRemoved).toHaveBeenCalledWith(remoteVideo);

    const lateTrack = createTrack("video");
    room.emit(liveKitMock.events.TrackSubscribed, lateTrack, {}, { sid: "participant-a" });
    expect(lateTrack.attach).not.toHaveBeenCalled();
  });

  it.each([
    ["camera", "enableCamera", "setCameraEnabled", "camera_denied"],
    ["microphone", "enableMicrophone", "setMicrophoneEnabled", "microphone_denied"],
  ] as const)("maps a denied %s device failure", async (_device, method, sdkMethod, kind) => {
    const { room, session } = setup();
    room.localParticipant[sdkMethod].mockRejectedValueOnce({
      deviceFailure: liveKitMock.permissionDenied,
    });

    await expect(session[method]()).rejects.toEqual(expect.any(SpikeMediaError));
    await expect(session[method]()).resolves.toBeUndefined();
    expect(liveKitMock.getFailure).toHaveBeenCalled();

    room.localParticipant[sdkMethod].mockRejectedValueOnce(new Error("device unavailable"));
    await expect(session[method]()).rejects.toMatchObject({
      kind: kind.replace("denied", "unavailable"),
    });
  });

  it("cleans partial camera state when attaching the local preview fails", async () => {
    const { handlers, room, session } = setup();
    const track = createTrack("video");
    track.attach.mockImplementationOnce(() => {
      throw new Error("attach failed");
    });
    room.localParticipant.setCameraEnabled.mockResolvedValue({ track });

    await expect(session.enableCamera()).rejects.toMatchObject({ kind: "camera_unavailable" });

    expect(room.localParticipant.setCameraEnabled).toHaveBeenLastCalledWith(false);
    expect(handlers.onLocalElement).not.toHaveBeenCalled();
  });

  it("keeps every public operation inert after disposal", async () => {
    const { room, session } = setup();
    await session.disconnect();

    await session.startAudio();
    await session.connect("ws://127.0.0.1:7880", "late-token");
    await session.enableCamera();
    await session.enableMicrophone();
    await session.setCameraEnabled(true);
    await session.setMicrophoneEnabled(true);

    expect(room.startAudio).not.toHaveBeenCalled();
    expect(room.connect).not.toHaveBeenCalled();
    expect(room.localParticipant.setCameraEnabled).not.toHaveBeenCalled();
    expect(room.localParticipant.setMicrophoneEnabled).not.toHaveBeenCalled();
  });

  it("disconnects again when a pending Room.connect completes after disposal", async () => {
    const { room, session } = setup();
    const connected = deferredValue<unknown>();
    room.connect.mockReturnValueOnce(connected.promise);

    const connecting = session.connect("ws://127.0.0.1:7880", "participant-token");
    await session.disconnect();
    connected.resolve(undefined);
    await connecting;

    expect(room.disconnect).toHaveBeenCalledTimes(2);
    expect(room.disconnect).toHaveBeenNthCalledWith(1, true);
    expect(room.disconnect).toHaveBeenNthCalledWith(2, true);
  });

  it("undoes camera enable that completes during disposal", async () => {
    const { handlers, room, session } = setup();
    const publication = deferredValue<unknown>();
    const track = createTrack("video");
    room.localParticipant.setCameraEnabled.mockReturnValueOnce(publication.promise);

    const enabling = session.enableCamera();
    await session.disconnect();
    publication.resolve({ track });
    await enabling;

    expect(room.localParticipant.setCameraEnabled.mock.calls).toEqual([[true], [false]]);
    expect(track.attach).not.toHaveBeenCalled();
    expect(handlers.onLocalElement).not.toHaveBeenCalled();
  });

  it("undoes microphone enable that completes during disposal", async () => {
    const { room, session } = setup();
    const enabled = deferredValue<unknown>();
    room.localParticipant.setMicrophoneEnabled.mockReturnValueOnce(enabled.promise);

    const enabling = session.enableMicrophone();
    await session.disconnect();
    enabled.resolve(undefined);
    await enabling;

    expect(room.localParticipant.setMicrophoneEnabled.mock.calls).toEqual([[true], [false]]);
  });

  it.each([true, false])(
    "does not publish a pending camera toggle after disposal: enabled=%s",
    async (enabled) => {
      const { handlers, room, session } = setup();
      const publication = deferredValue<unknown>();
      const track = createTrack("video");
      room.localParticipant.setCameraEnabled.mockReturnValueOnce(publication.promise);

      const toggling = session.setCameraEnabled(enabled);
      await session.disconnect();
      publication.resolve({ track });
      await toggling;

      expect(track.attach).not.toHaveBeenCalled();
      expect(handlers.onLocalElement).not.toHaveBeenCalled();
      expect(room.localParticipant.setCameraEnabled).toHaveBeenNthCalledWith(1, enabled);
      expect(room.localParticipant.setCameraEnabled).toHaveBeenCalledTimes(enabled ? 2 : 1);
      if (enabled) {
        expect(room.localParticipant.setCameraEnabled).toHaveBeenLastCalledWith(false);
      }
    },
  );

  it.each([true, false])(
    "does not publish a pending microphone toggle after disposal: enabled=%s",
    async (enabled) => {
      const { room, session } = setup();
      const toggled = deferredValue<unknown>();
      room.localParticipant.setMicrophoneEnabled.mockReturnValueOnce(toggled.promise);

      const toggling = session.setMicrophoneEnabled(enabled);
      await session.disconnect();
      toggled.resolve(undefined);
      await toggling;

      expect(room.localParticipant.setMicrophoneEnabled).toHaveBeenNthCalledWith(1, enabled);
      expect(room.localParticipant.setMicrophoneEnabled).toHaveBeenCalledTimes(enabled ? 2 : 1);
      if (enabled) {
        expect(room.localParticipant.setMicrophoneEnabled).toHaveBeenLastCalledWith(false);
      }
    },
  );

  it("ignores adapter events emitted by Room while disposal is in progress", async () => {
    const { handlers, room, session } = setup();
    const participant = { sid: "participant-a" };
    const track = createTrack("video");
    room.emit(liveKitMock.events.TrackSubscribed, track, {}, participant);
    room.disconnect.mockImplementationOnce(async () => {
      room.emit(liveKitMock.events.TrackUnsubscribed, track, {}, participant);
      room.emit(liveKitMock.events.ParticipantDisconnected, participant);
      room.emit(liveKitMock.events.Disconnected);
      room.emit(liveKitMock.events.Reconnecting);
      room.emit(liveKitMock.events.Reconnected);
    });
    vi.clearAllMocks();

    await session.disconnect();

    expect(handlers.onDisconnected).not.toHaveBeenCalled();
    expect(handlers.onReconnecting).not.toHaveBeenCalled();
    expect(handlers.onReconnected).not.toHaveBeenCalled();
    expect(handlers.onElementRemoved).toHaveBeenCalledOnce();
    expect(track.detach).toHaveBeenCalledOnce();
  });

  it("ignores unsupported and unknown remote tracks", () => {
    const { handlers, room } = setup();
    const unsupportedTrack = createTrack("video");
    unsupportedTrack.kind = "data" as "video";
    const unknownTrack = createTrack("video");

    room.emit(liveKitMock.events.TrackSubscribed, unsupportedTrack, {}, { sid: "participant-a" });
    room.emit(liveKitMock.events.TrackUnsubscribed, unknownTrack, {}, { sid: "participant-a" });

    expect(unsupportedTrack.attach).not.toHaveBeenCalled();
    expect(unknownTrack.detach).not.toHaveBeenCalled();
    expect(handlers.onRemoteElement).not.toHaveBeenCalled();
    expect(handlers.onElementRemoved).not.toHaveBeenCalled();
  });

  it("handles an absent camera publication and a normal enabled toggle", async () => {
    const { handlers, room, session } = setup();
    room.localParticipant.setCameraEnabled.mockResolvedValueOnce(undefined);
    await session.enableCamera();
    expect(handlers.onLocalElement).not.toHaveBeenCalled();

    const track = createTrack("video");
    room.localParticipant.setCameraEnabled.mockResolvedValueOnce({ track });
    await session.setCameraEnabled(true);

    expect(track.attach).toHaveBeenCalledOnce();
    expect(handlers.onLocalElement).toHaveBeenCalledOnce();
  });

  it("preserves the original failure when best-effort device cleanup also fails", async () => {
    const camera = setup();
    const track = createTrack("video");
    track.attach.mockImplementationOnce(() => {
      throw new Error("attach failed");
    });
    camera.room.localParticipant.setCameraEnabled
      .mockResolvedValueOnce({ track })
      .mockRejectedValueOnce(new Error("disable camera failed"));

    await expect(camera.session.setCameraEnabled(true)).rejects.toMatchObject({
      kind: "camera_unavailable",
    });

    const microphone = setup();
    const enabled = deferredValue<unknown>();
    microphone.room.localParticipant.setMicrophoneEnabled
      .mockReturnValueOnce(enabled.promise)
      .mockRejectedValueOnce(new Error("disable microphone failed"));
    const enabling = microphone.session.enableMicrophone();
    await microphone.session.disconnect();
    enabled.resolve(undefined);

    await expect(enabling).resolves.toBeUndefined();
  });

  it("runs adapter cleanup even when Room.disconnect rejects", async () => {
    const { handlers, room, session } = setup();
    const track = createTrack("video");
    room.emit(liveKitMock.events.TrackSubscribed, track, {}, { sid: "participant-a" });
    room.disconnect.mockRejectedValueOnce(new Error("disconnect failed"));

    await expect(session.disconnect()).rejects.toThrow("disconnect failed");

    expect(handlers.onElementRemoved).toHaveBeenCalledOnce();
    expect(track.detach).toHaveBeenCalledOnce();
    for (const event of Object.values(liveKitMock.events)) {
      expect(room.listeners.get(event)).toHaveLength(0);
    }
  });

  describe("disconnect() retry after a rejected attempt (RF-24 quinta correção)", () => {
    it("dedupes two concurrent disconnect() calls onto the same pending attempt", async () => {
      const { room, session } = setup();
      const attempt = deferredValue<undefined>();
      room.disconnect.mockReturnValueOnce(attempt.promise);

      const first = session.disconnect();
      const second = session.disconnect();
      expect(room.disconnect).toHaveBeenCalledOnce();

      attempt.resolve(undefined);
      await Promise.all([first, second]);

      expect(room.disconnect).toHaveBeenCalledOnce();
    });

    it("calls room.disconnect(true) a second time for real after the first attempt rejects, and the retry can succeed", async () => {
      const { room, session } = setup();
      room.disconnect.mockRejectedValueOnce(new Error("disconnect failed"));

      await expect(session.disconnect()).rejects.toThrow("disconnect failed");
      expect(room.disconnect).toHaveBeenCalledTimes(1);

      await expect(session.disconnect()).resolves.toBeUndefined();
      expect(room.disconnect).toHaveBeenCalledTimes(2);
    });

    it("stays idempotent after a successful disconnect: no further room.disconnect(true) calls", async () => {
      const { room, session } = setup();

      await session.disconnect();
      expect(room.disconnect).toHaveBeenCalledOnce();

      await session.disconnect();
      await session.disconnect();

      expect(room.disconnect).toHaveBeenCalledOnce();
    });
  });

  describe("local microphone state", () => {
    it("enableMicrophone reports the confirmed local state", async () => {
      const { handlers, room, session } = setup();
      room.localParticipant.getTrackPublication.mockReturnValue(micPublication(false));

      await session.enableMicrophone();

      expect(handlers.onMicrophoneStateChanged).toHaveBeenCalledExactlyOnceWith(true);
    });

    it("setMicrophoneEnabled(false) confirms the local microphone is muted", async () => {
      const { handlers, room, session } = setup();
      room.localParticipant.getTrackPublication.mockReturnValue(micPublication(true));

      await session.setMicrophoneEnabled(false);

      expect(handlers.onMicrophoneStateChanged).toHaveBeenCalledExactlyOnceWith(false);
    });

    it("setMicrophoneEnabled(true) confirms the local microphone is enabled", async () => {
      const { handlers, room, session } = setup();
      room.localParticipant.getTrackPublication.mockReturnValue(micPublication(false));

      await session.setMicrophoneEnabled(true);

      expect(handlers.onMicrophoneStateChanged).toHaveBeenCalledExactlyOnceWith(true);
    });

    it("notifies false when RoomEvent.TrackMuted targets the local microphone", () => {
      const { handlers, room } = setup();

      room.emit(
        liveKitMock.events.TrackMuted,
        micPublication(true),
        room.localParticipant as never,
      );

      expect(handlers.onMicrophoneStateChanged).toHaveBeenCalledExactlyOnceWith(false);
    });

    it("notifies true when RoomEvent.TrackUnmuted targets the local microphone", () => {
      const { handlers, room } = setup();

      room.emit(
        liveKitMock.events.TrackUnmuted,
        micPublication(false),
        room.localParticipant as never,
      );

      expect(handlers.onMicrophoneStateChanged).toHaveBeenCalledExactlyOnceWith(true);
    });

    it("ignores RoomEvent.TrackMuted for a remote participant's microphone", () => {
      const { handlers, room } = setup();

      room.emit(liveKitMock.events.TrackMuted, micPublication(true), { sid: "participant-b" });

      expect(handlers.onMicrophoneStateChanged).not.toHaveBeenCalled();
    });

    it("ignores RoomEvent.TrackUnmuted for a remote participant's microphone", () => {
      const { handlers, room } = setup();

      room.emit(liveKitMock.events.TrackUnmuted, micPublication(false), { sid: "participant-b" });

      expect(handlers.onMicrophoneStateChanged).not.toHaveBeenCalled();
    });

    it("ignores a local mute/unmute event for the camera source", () => {
      const { handlers, room } = setup();
      const cameraPublication = { isMuted: true, source: liveKitMock.sources.Camera };

      room.emit(liveKitMock.events.TrackMuted, cameraPublication, room.localParticipant as never);
      room.emit(liveKitMock.events.TrackUnmuted, cameraPublication, room.localParticipant as never);

      expect(handlers.onMicrophoneStateChanged).not.toHaveBeenCalled();
      // The local camera going in and out of mute is not a remote participant
      // event either — attachLocalVideo()/setCameraEnabled() alone drive it.
      expect(handlers.onRemoteVideoAvailabilityChanged).not.toHaveBeenCalled();
    });

    it("reports a remote camera mute as unavailable without touching microphone state", () => {
      const { handlers, room } = setup();
      const participant = { sid: "participant-a-sid", identity: "identity-a" };
      const cameraPublication = { isMuted: true, source: liveKitMock.sources.Camera };

      room.emit(liveKitMock.events.TrackMuted, cameraPublication, participant as never);

      expect(handlers.onRemoteVideoAvailabilityChanged).toHaveBeenCalledExactlyOnceWith(
        "identity-a",
        false,
      );
      expect(handlers.onMicrophoneStateChanged).not.toHaveBeenCalled();
      expect(handlers.onElementRemoved).not.toHaveBeenCalled();
    });

    it("reports a remote camera unmute as available again", () => {
      const { handlers, room } = setup();
      const participant = { sid: "participant-a-sid", identity: "identity-a" };
      const cameraPublication = { isMuted: false, source: liveKitMock.sources.Camera };

      room.emit(liveKitMock.events.TrackUnmuted, cameraPublication, participant as never);

      expect(handlers.onRemoteVideoAvailabilityChanged).toHaveBeenCalledExactlyOnceWith(
        "identity-a",
        true,
      );
      expect(handlers.onMicrophoneStateChanged).not.toHaveBeenCalled();
    });

    it("ignores a remote microphone mute/unmute for onRemoteVideoAvailabilityChanged", () => {
      const { handlers, room } = setup();
      const participant = { sid: "participant-a-sid", identity: "identity-a" };
      const micPub = { isMuted: true, source: liveKitMock.sources.Microphone };

      room.emit(liveKitMock.events.TrackMuted, micPub, participant as never);
      room.emit(liveKitMock.events.TrackUnmuted, micPub, participant as never);

      expect(handlers.onRemoteVideoAvailabilityChanged).not.toHaveBeenCalled();
    });

    it("ignores camera mute/unmute emitted after disposal", async () => {
      const { handlers, room, session } = setup();
      await session.disconnect();
      vi.clearAllMocks();
      const participant = { sid: "participant-a-sid", identity: "identity-a" };
      const cameraPublication = { isMuted: true, source: liveKitMock.sources.Camera };

      room.emit(liveKitMock.events.TrackMuted, cameraPublication, participant as never);

      expect(handlers.onRemoteVideoAvailabilityChanged).not.toHaveBeenCalled();
    });
  });

  describe("late join reads publication.isMuted (RF-24 code review round 2, achado 2)", () => {
    it("A: a remote participant already present with a muted camera shows hasVideo=false to a late joiner", async () => {
      const { handlers, room, session } = setup();
      // Not yet subscribed at connect() time — this is the ordering LiveKit
      // does not guarantee: TrackSubscribed for this pre-existing publication
      // arrives after connect() resolves, so the per-event check (not the
      // post-connect resync) is what must catch the already-muted state. The
      // Room always hands the adapter the same participant instance it
      // already has on file — the late event reuses that instance, not a
      // fresh literal, matching how the real SDK behaves.
      const participant = fakeRemoteParticipant("identity-b", {
        isMuted: true,
        isSubscribed: false,
      });
      room.remoteParticipants.set("identity-b", participant);
      const video = document.createElement("video");
      const videoTrack = createTrack("video", video);

      await session.connect("ws://127.0.0.1:7880", "participant-token");
      expect(handlers.onRemoteVideoAvailabilityChanged).not.toHaveBeenCalled();

      room.emit(
        liveKitMock.events.TrackSubscribed,
        videoTrack,
        cameraPublication(true),
        participant,
      );

      expect(handlers.onRemoteElement).toHaveBeenCalledWith(video);
      expect(handlers.onRemoteVideoAvailabilityChanged).toHaveBeenCalledExactlyOnceWith(
        "identity-b",
        false,
      );
    });

    it("A2: a camera publication already muted at subscribe time (participant joining normally) never reports available", () => {
      const { handlers, room } = setup();
      const video = document.createElement("video");
      const videoTrack = createTrack("video", video);

      room.emit(liveKitMock.events.TrackSubscribed, videoTrack, cameraPublication(true), {
        sid: "participant-a-sid",
        identity: "identity-a",
      });

      expect(handlers.onRemoteElement).toHaveBeenCalledWith(video);
      expect(handlers.onRemoteVideoAvailabilityChanged).toHaveBeenCalledExactlyOnceWith(
        "identity-a",
        false,
      );
    });

    it("B: an unmuted camera publication at subscribe time never reports unavailable", () => {
      const { handlers, room } = setup();
      const video = document.createElement("video");
      const videoTrack = createTrack("video", video);

      room.emit(liveKitMock.events.TrackSubscribed, videoTrack, cameraPublication(false), {
        sid: "participant-a-sid",
        identity: "identity-a",
      });

      expect(handlers.onRemoteElement).toHaveBeenCalledWith(video);
      expect(handlers.onRemoteVideoAvailabilityChanged).not.toHaveBeenCalled();
    });

    it("C: reconnect resyncs to muted when the publication went muted during the interruption", () => {
      const { handlers, room } = setup();
      room.remoteParticipants.set(
        "participant-a",
        fakeRemoteParticipant("identity-a", { isMuted: true }),
      );

      room.emit(liveKitMock.events.Reconnected);

      expect(handlers.onRemoteVideoAvailabilityChanged).toHaveBeenCalledWith("identity-a", false);
    });

    it("D: reconnect resyncs to available when the publication came back unmuted", () => {
      const { handlers, room } = setup();
      room.remoteParticipants.set(
        "participant-a",
        fakeRemoteParticipant("identity-a", { isMuted: false }),
      );

      room.emit(liveKitMock.events.Reconnected);

      expect(handlers.onRemoteVideoAvailabilityChanged).toHaveBeenCalledWith("identity-a", true);
    });

    it("E: reconnect resync never reports for a microphone publication", () => {
      const { handlers, room } = setup();
      room.remoteParticipants.set("participant-a", {
        identity: "identity-a",
        getTrackPublication: (source: string) =>
          source === liveKitMock.sources.Microphone
            ? { source: liveKitMock.sources.Microphone, isMuted: true, isSubscribed: true }
            : undefined,
      });

      room.emit(liveKitMock.events.Reconnected);

      expect(handlers.onRemoteVideoAvailabilityChanged).not.toHaveBeenCalled();
    });

    it("resync after connect() only reports subscribed camera publications, never an unsubscribed one", async () => {
      const { handlers, room, session } = setup();
      room.remoteParticipants.set(
        "participant-a",
        fakeRemoteParticipant("identity-a", { isMuted: true, isSubscribed: false }),
      );

      await session.connect("ws://127.0.0.1:7880", "participant-token");

      expect(handlers.onRemoteVideoAvailabilityChanged).not.toHaveBeenCalled();
    });

    it("resync after connect() reports every remote participant's current camera state", async () => {
      const { handlers, room, session } = setup();
      room.remoteParticipants.set(
        "participant-a",
        fakeRemoteParticipant("identity-a", { isMuted: true }),
      );
      room.remoteParticipants.set(
        "participant-b",
        fakeRemoteParticipant("identity-b", { isMuted: false }),
      );

      await session.connect("ws://127.0.0.1:7880", "participant-token");

      expect(handlers.onRemoteVideoAvailabilityChanged).toHaveBeenCalledWith("identity-a", false);
      expect(handlers.onRemoteVideoAvailabilityChanged).toHaveBeenCalledWith("identity-b", true);
    });
  });

  describe("stale track events from a participant that no longer holds that identity slot", () => {
    it("ignores a track subscription that arrives after its participant already disconnected", () => {
      const { handlers, room } = setup();
      const participant = { sid: "participant-a-sid", identity: "identity-a" };
      const firstTrack = createTrack("video", document.createElement("video"));
      room.emit(
        liveKitMock.events.TrackSubscribed,
        firstTrack,
        cameraPublication(false),
        participant,
      );
      expect(handlers.onRemoteElement).toHaveBeenCalledTimes(1);

      room.emit(liveKitMock.events.ParticipantDisconnected, participant);
      vi.mocked(handlers.onRemoteElement).mockClear();

      const lateTrack = createTrack("video", document.createElement("video"));
      room.emit(
        liveKitMock.events.TrackSubscribed,
        lateTrack,
        cameraPublication(false),
        participant,
      );

      expect(lateTrack.attach).not.toHaveBeenCalled();
      expect(handlers.onRemoteElement).not.toHaveBeenCalled();
    });

    it("accepts a legitimate reentry under the same identity with a new participant instance", () => {
      const { handlers, room } = setup();
      const oldParticipant = { sid: "sid-old", identity: "user-x" };
      const newParticipant = { sid: "sid-new", identity: "user-x" };
      const oldTrack = createTrack("video", document.createElement("video"));
      const newVideo = document.createElement("video");
      const newTrack = createTrack("video", newVideo);

      room.emit(
        liveKitMock.events.TrackSubscribed,
        oldTrack,
        cameraPublication(false),
        oldParticipant,
      );
      room.emit(liveKitMock.events.ParticipantDisconnected, oldParticipant);
      vi.mocked(handlers.onRemoteElement).mockClear();

      room.emit(
        liveKitMock.events.TrackSubscribed,
        newTrack,
        cameraPublication(false),
        newParticipant,
      );

      expect(handlers.onRemoteElement).toHaveBeenCalledExactlyOnceWith(newVideo);
    });

    it("keeps ignoring the old SID's late event after a same-identity reentry, without duplicating or evicting the new tile", () => {
      const { handlers, room } = setup();
      const oldParticipant = { sid: "sid-old", identity: "user-x" };
      const newParticipant = { sid: "sid-new", identity: "user-x" };
      const oldTrack = createTrack("video", document.createElement("video"));
      const newVideo = document.createElement("video");
      const newTrack = createTrack("video", newVideo);
      room.emit(
        liveKitMock.events.TrackSubscribed,
        oldTrack,
        cameraPublication(false),
        oldParticipant,
      );
      room.emit(liveKitMock.events.ParticipantDisconnected, oldParticipant);
      room.emit(
        liveKitMock.events.TrackSubscribed,
        newTrack,
        cameraPublication(false),
        newParticipant,
      );
      vi.mocked(handlers.onRemoteElement).mockClear();
      vi.mocked(handlers.onElementRemoved).mockClear();

      // A retransmitted/queued TrackSubscribed for the old SID, arriving
      // after the same identity already reconnected as a new instance.
      const staleLateTrack = createTrack("video", document.createElement("video"));
      room.emit(
        liveKitMock.events.TrackSubscribed,
        staleLateTrack,
        cameraPublication(false),
        oldParticipant,
      );

      expect(staleLateTrack.attach).not.toHaveBeenCalled();
      expect(handlers.onRemoteElement).not.toHaveBeenCalled();
      expect(handlers.onElementRemoved).not.toHaveBeenCalled();

      // The new tile is unaffected and still owns its element.
      room.emit(
        liveKitMock.events.TrackUnsubscribed,
        newTrack,
        cameraPublication(false),
        newParticipant,
      );
      expect(handlers.onElementRemoved).toHaveBeenCalledExactlyOnceWith(newVideo);
    });
  });

  describe("authoritative microphone state (unaffected legacy suite continuation)", () => {
    it("preserves the mute/unmute sequence across five consecutive cycles", async () => {
      const { handlers, room, session } = setup();
      const expected: boolean[] = [];
      for (let cycle = 0; cycle < 5; cycle += 1) {
        const enabled = cycle % 2 === 0;
        room.localParticipant.getTrackPublication.mockReturnValueOnce(micPublication(!enabled));
        await session.setMicrophoneEnabled(enabled);
        expected.push(enabled);
      }

      expect(
        vi.mocked(handlers.onMicrophoneStateChanged).mock.calls.map(([value]) => value),
      ).toEqual(expected);
    });

    it("ignores TrackMuted/TrackUnmuted emitted after disposal", async () => {
      const { handlers, room, session } = setup();
      await session.disconnect();
      vi.clearAllMocks();

      room.emit(
        liveKitMock.events.TrackMuted,
        micPublication(true),
        room.localParticipant as never,
      );
      room.emit(
        liveKitMock.events.TrackUnmuted,
        micPublication(false),
        room.localParticipant as never,
      );

      expect(handlers.onMicrophoneStateChanged).not.toHaveBeenCalled();
    });

    it("removes the TrackMuted/TrackUnmuted listeners on disconnect", async () => {
      const { room, session } = setup();

      await session.disconnect();

      expect(room.listeners.get(liveKitMock.events.TrackMuted)).toHaveLength(0);
      expect(room.listeners.get(liveKitMock.events.TrackUnmuted)).toHaveLength(0);
    });

    it("does not touch remote elements when the local microphone is muted", async () => {
      const { handlers, room, session } = setup();
      const remoteAudio = document.createElement("audio");
      remoteAudio.play = vi.fn(async () => undefined);
      const remoteTrack = createTrack("audio", remoteAudio);
      room.emit(liveKitMock.events.TrackSubscribed, remoteTrack, {}, { sid: "participant-b" });
      await Promise.resolve();
      vi.mocked(handlers.onElementRemoved).mockClear();
      room.localParticipant.getTrackPublication.mockReturnValue(micPublication(true));

      await session.setMicrophoneEnabled(false);

      expect(handlers.onElementRemoved).not.toHaveBeenCalled();
      expect(remoteTrack.detach).not.toHaveBeenCalled();
    });

    it("resyncs the effective microphone state after Reconnected", () => {
      const { handlers, room } = setup();
      room.localParticipant.getTrackPublication.mockReturnValue(micPublication(true));

      room.emit(liveKitMock.events.Reconnected);

      expect(handlers.onMicrophoneStateChanged).toHaveBeenCalledExactlyOnceWith(false);
      expect(handlers.onReconnected).toHaveBeenCalledOnce();
    });

    it("reports a disabled state when the microphone publication is absent", async () => {
      const { handlers, room, session } = setup();
      room.localParticipant.getTrackPublication.mockReturnValue(undefined);

      await session.setMicrophoneEnabled(true);

      expect(handlers.onMicrophoneStateChanged).toHaveBeenCalledExactlyOnceWith(false);
    });
  });

  describe("two independent rooms (isolation)", () => {
    // No real LiveKit media server is available in this test environment, so
    // two genuinely independent participants cannot be exercised end-to-end
    // (see e2e/messaging/call-1to1-ui.spec.ts for the documented e2e gap).
    // This simulates the next best thing: two separate adapter instances,
    // each backed by its own mocked Room/localParticipant, to prove muting
    // one never touches the other's participant or elements.
    it("muting participant A's microphone never calls B's localParticipant or touches B's elements", async () => {
      const a = setup();
      const b = setup();
      const remoteVideoOnB = document.createElement("video");
      const remoteTrackOnB = createTrack("video", remoteVideoOnB);
      b.room.emit(liveKitMock.events.TrackSubscribed, remoteTrackOnB, {}, { sid: "participant-a" });
      a.room.localParticipant.getTrackPublication.mockReturnValue(micPublication(true));

      await a.session.setMicrophoneEnabled(false);

      expect(a.room.localParticipant.setMicrophoneEnabled).toHaveBeenCalledWith(false);
      expect(b.room.localParticipant.setMicrophoneEnabled).not.toHaveBeenCalled();
      expect(b.room.localParticipant.setCameraEnabled).not.toHaveBeenCalled();
      expect(b.room.disconnect).not.toHaveBeenCalled();
      expect(b.room.startAudio).not.toHaveBeenCalled();
      expect(b.handlers.onElementRemoved).not.toHaveBeenCalled();
      expect(b.handlers.onMicrophoneStateChanged).not.toHaveBeenCalled();
      expect(remoteTrackOnB.detach).not.toHaveBeenCalled();
    });
  });
});
