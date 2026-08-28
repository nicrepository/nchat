import { afterEach, describe, expect, it, vi } from "vitest";

import { requestMediaPermission } from "./mediaPermission";

function fakeStream(trackCount: number) {
  const tracks = Array.from({ length: trackCount }, () => ({ stop: vi.fn() }));
  return { getTracks: () => tracks, tracks } as unknown as MediaStream & {
    tracks: { stop: ReturnType<typeof vi.fn> }[];
  };
}

const originalMediaDevices = navigator.mediaDevices;

afterEach(() => {
  Object.defineProperty(navigator, "mediaDevices", {
    value: originalMediaDevices,
    configurable: true,
  });
  vi.restoreAllMocks();
});

function mockGetUserMedia(impl: (constraints: MediaStreamConstraints) => Promise<MediaStream>) {
  Object.defineProperty(navigator, "mediaDevices", {
    value: { getUserMedia: vi.fn(impl) },
    configurable: true,
  });
}

describe("requestMediaPermission", () => {
  it("requests only the microphone for an audio call", async () => {
    const stream = fakeStream(1);
    mockGetUserMedia(async () => stream);

    const result = await requestMediaPermission("audio");

    expect(navigator.mediaDevices.getUserMedia).toHaveBeenCalledExactlyOnceWith({
      audio: true,
      video: false,
    });
    expect(result).toEqual({ ok: true });
  });

  it("requests the microphone and camera for a video call", async () => {
    const stream = fakeStream(2);
    mockGetUserMedia(async () => stream);

    const result = await requestMediaPermission("video");

    expect(navigator.mediaDevices.getUserMedia).toHaveBeenCalledExactlyOnceWith({
      audio: true,
      video: true,
    });
    expect(result).toEqual({ ok: true });
  });

  it("stops every temporary track after a successful preflight", async () => {
    const stream = fakeStream(2);
    mockGetUserMedia(async () => stream);

    await requestMediaPermission("video");

    for (const track of stream.tracks) expect(track.stop).toHaveBeenCalledOnce();
  });

  it.each([
    ["NotAllowedError", "permission_denied"],
    ["PermissionDeniedError", "permission_denied"],
    ["NotFoundError", "device_not_found"],
    ["NotReadableError", "device_unavailable"],
    ["OverconstrainedError", "constraint_unsupported"],
    ["AbortError", "aborted"],
    ["SecurityError", "security_blocked"],
    ["TypeError", "unsupported_browser"],
  ] as const)("normalizes %s into kind %s without leaking the raw error", async (name, kind) => {
    mockGetUserMedia(async () => {
      throw new DOMException("raw browser detail", name);
    });

    const result = await requestMediaPermission("video");

    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("unreachable");
    expect(result.kind).toBe(kind);
    expect(result.message).not.toContain("raw browser detail");
    expect(result.message.length).toBeGreaterThan(0);
  });

  it("distinguishes microphone-only from microphone-and-camera denial messages", async () => {
    mockGetUserMedia(async () => {
      throw new DOMException("denied", "NotAllowedError");
    });

    const audio = await requestMediaPermission("audio");
    const video = await requestMediaPermission("video");

    expect(audio.ok).toBe(false);
    expect(video.ok).toBe(false);
    if (audio.ok || video.ok) throw new Error("unreachable");
    expect(audio.message).not.toBe(video.message);
    expect(audio.message.toLowerCase()).toContain("microfone");
    expect(audio.message.toLowerCase()).not.toContain("câmera");
    expect(video.message.toLowerCase()).toContain("câmera");
  });

  it("reports an unsupported browser when mediaDevices is missing", async () => {
    Object.defineProperty(navigator, "mediaDevices", {
      value: undefined,
      configurable: true,
    });

    const result = await requestMediaPermission("audio");

    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("unreachable");
    expect(result.kind).toBe("unsupported_browser");
  });

  it("normalizes an unrecognized rejection reason without leaking it", async () => {
    mockGetUserMedia(async () => {
      throw new DOMException("odd platform failure", "UnknownVendorError");
    });

    const result = await requestMediaPermission("audio");

    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("unreachable");
    expect(result.kind).toBe("permission_denied");
    expect(result.message).not.toContain("odd platform failure");
    expect(result.message.length).toBeGreaterThan(0);
  });

  it("does not send a token, device id, or stack trace in the error message", async () => {
    mockGetUserMedia(async () => {
      const error = new DOMException("denied", "NotAllowedError");
      (error as unknown as { deviceId?: string }).deviceId = "device-secret-123";
      throw error;
    });

    const result = await requestMediaPermission("audio");

    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("unreachable");
    expect(result.message).not.toContain("device-secret-123");
    expect(result.message).not.toContain("Error");
    expect(result.message).not.toContain("at ");
  });
});
