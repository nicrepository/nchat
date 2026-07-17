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
    ParticipantDisconnected: "participantDisconnected",
    Disconnected: "disconnected",
    Reconnecting: "reconnecting",
    Reconnected: "reconnected",
  } as const;
  const kinds = { Audio: "audio", Video: "video" } as const;
  const permissionDenied = "permission-denied";
  const rooms: MockRoom[] = [];

  class MockRoom {
    readonly options: unknown;
    readonly listeners = new Map<string, Set<(...args: unknown[]) => void>>();
    readonly localParticipant = {
      setCameraEnabled: vi.fn(async (): Promise<unknown> => undefined),
      setMicrophoneEnabled: vi.fn(async (): Promise<unknown> => undefined),
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
      for (const callback of this.listeners.get(event) ?? []) callback(...args);
    }
  }

  return {
    events,
    kinds,
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
  Track: { Kind: liveKitMock.kinds },
}));

function callbacks(): LiveKitSpikeSessionCallbacks {
  return {
    onLocalElement: vi.fn(),
    onRemoteElement: vi.fn(),
    onElementRemoved: vi.fn((element: HTMLMediaElement) => element.remove()),
    onDisconnected: vi.fn(),
    onReconnecting: vi.fn(),
    onReconnected: vi.fn(),
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

  it("handles rejected remote audio playback without dropping the element", async () => {
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
    const participantA = { sid: "participant-a" };
    const participantB = { sid: "participant-b" };
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
    expect(handlers.onDisconnected).toHaveBeenCalledOnce();
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
});
