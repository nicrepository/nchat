import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const RINGTONE_KEY = "nchat.notifications.calls.ringtone.enabled";

function fakeAudio() {
  return {
    preload: "",
    loop: false,
    currentTime: 8,
    play: vi.fn(() => Promise.resolve()),
    pause: vi.fn(),
  };
}

function stubAudio(factory: () => unknown) {
  const AudioMock = vi.fn(function AudioMock() {
    return factory();
  });
  vi.stubGlobal("Audio", AudioMock);
  return AudioMock;
}

// jsdom has no Web Locks API — every test in this file exercises the
// "always present" fallback that real browsers without navigator.locks also
// take. The two tests in the "presentation lock" block below stub
// navigator.locks directly to cover the coordination path; actual cross-tab
// exclusion is proven end-to-end by call-1to1-ui.spec.ts's two-tab test.
function stubLocks(grantLock: boolean) {
  const request = vi.fn((_name: string, _options: unknown, callback: (lock: unknown) => unknown) =>
    Promise.resolve(callback(grantLock ? {} : null)),
  );
  Object.defineProperty(navigator, "locks", { value: { request }, configurable: true });
  return request;
}

describe("incomingCallRingtone", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.resetModules();
    localStorage.clear();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    localStorage.clear();
    Reflect.deleteProperty(navigator, "locks");
  });

  it("creates the call audio with preload auto and starts immediately", async () => {
    const audio = fakeAudio();
    const AudioMock = stubAudio(() => audio);
    const { startIncomingCallRingtone } = await import("./incomingCallRingtone");

    startIncomingCallRingtone("call-1");

    expect(AudioMock).toHaveBeenCalledWith("/sounds/incoming-call.wav");
    expect(audio.preload).toBe("auto");
    expect(audio.loop).toBe(false);
    expect(audio.currentTime).toBe(0);
    expect(audio.play).toHaveBeenCalledOnce();
  });

  it("repeats deterministically after a comfortable cadence", async () => {
    const audio = fakeAudio();
    stubAudio(() => audio);
    const { RINGTONE_REPEAT_MS, startIncomingCallRingtone } =
      await import("./incomingCallRingtone");

    startIncomingCallRingtone("call-1");
    vi.advanceTimersByTime(RINGTONE_REPEAT_MS - 1);
    expect(audio.play).toHaveBeenCalledOnce();
    vi.advanceTimersByTime(1);
    expect(audio.play).toHaveBeenCalledTimes(2);
  });

  it("is idempotent for the same call id", async () => {
    const audio = fakeAudio();
    stubAudio(() => audio);
    const { RINGTONE_REPEAT_MS, startIncomingCallRingtone } =
      await import("./incomingCallRingtone");

    startIncomingCallRingtone("call-1");
    startIncomingCallRingtone("call-1");

    expect(audio.play).toHaveBeenCalledOnce();
    vi.advanceTimersByTime(RINGTONE_REPEAT_MS);
    expect(audio.play).toHaveBeenCalledTimes(2);
  });

  it("fully stops the previous cycle before starting a new call id", async () => {
    const audio = fakeAudio();
    stubAudio(() => audio);
    const { RINGTONE_REPEAT_MS, startIncomingCallRingtone } =
      await import("./incomingCallRingtone");

    startIncomingCallRingtone("call-1");
    startIncomingCallRingtone("call-2");

    expect(audio.pause).toHaveBeenCalledOnce();
    expect(audio.play).toHaveBeenCalledTimes(2);
    vi.advanceTimersByTime(RINGTONE_REPEAT_MS);
    expect(audio.play).toHaveBeenCalledTimes(3);
  });

  it("stop cancels the timer, pauses and resets currentTime", async () => {
    const audio = fakeAudio();
    stubAudio(() => audio);
    const { RINGTONE_REPEAT_MS, startIncomingCallRingtone, stopIncomingCallRingtone } =
      await import("./incomingCallRingtone");
    startIncomingCallRingtone("call-1");

    stopIncomingCallRingtone();

    expect(audio.pause).toHaveBeenCalledOnce();
    expect(audio.currentTime).toBe(0);
    vi.advanceTimersByTime(RINGTONE_REPEAT_MS * 2);
    expect(audio.play).toHaveBeenCalledOnce();
    expect(() => stopIncomingCallRingtone()).not.toThrow();
  });

  it("swallows autoplay rejection without an unhandled rejection", async () => {
    const audio = fakeAudio();
    audio.play.mockRejectedValue(new DOMException("blocked", "NotAllowedError"));
    stubAudio(() => audio);
    const { startIncomingCallRingtone } = await import("./incomingCallRingtone");

    expect(() => startIncomingCallRingtone("call-1")).not.toThrow();
    await Promise.resolve();
  });

  it("contains synchronous constructor, play, pause and currentTime failures", async () => {
    stubAudio(() => {
      throw new Error("constructor failed");
    });
    let module = await import("./incomingCallRingtone");
    expect(() => module.startIncomingCallRingtone("call-1")).not.toThrow();
    module.stopIncomingCallRingtone();

    vi.resetModules();
    localStorage.clear();
    let currentTimeWrites = 0;
    const audio = {
      preload: "",
      loop: false,
      play: vi.fn(() => {
        throw new Error("play failed");
      }),
      pause: vi.fn(() => {
        throw new Error("pause failed");
      }),
    };
    Object.defineProperty(audio, "currentTime", {
      get: () => 0,
      set: () => {
        currentTimeWrites += 1;
        throw new Error("seek failed");
      },
    });
    stubAudio(() => audio);
    module = await import("./incomingCallRingtone");
    expect(() => module.startIncomingCallRingtone("call-2")).not.toThrow();
    expect(() => module.stopIncomingCallRingtone()).not.toThrow();
    expect(currentTimeWrites).toBeGreaterThan(0);
  });

  it("preview plays one motif with no repeat even when automatic ringtone is off", async () => {
    localStorage.setItem(RINGTONE_KEY, "false");
    const audio = fakeAudio();
    stubAudio(() => audio);
    const { playIncomingCallRingtonePreview } = await import("./incomingCallRingtone");

    playIncomingCallRingtonePreview();
    vi.runAllTimers();

    expect(audio.play).toHaveBeenCalledOnce();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("does not start automatic playback when its independent preference is off", async () => {
    localStorage.setItem(RINGTONE_KEY, "false");
    localStorage.setItem("nchat.notifications.sound.mode", "all");
    const audio = fakeAudio();
    stubAudio(() => audio);
    const { startIncomingCallRingtone } = await import("./incomingCallRingtone");

    const timersBefore = vi.getTimerCount();
    startIncomingCallRingtone("call-1");

    expect(audio.play).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(timersBefore);
  });

  describe("presentation lock", () => {
    it("does not play when another tab already holds the presentation lock", async () => {
      const audio = fakeAudio();
      stubAudio(() => audio);
      const request = stubLocks(false);
      const { startIncomingCallRingtone } = await import("./incomingCallRingtone");

      startIncomingCallRingtone("call-1");
      await vi.waitFor(() => expect(request).toHaveBeenCalledOnce());

      expect(request).toHaveBeenCalledWith(
        "nchat.calls.ringtone.presentation.call-1",
        { ifAvailable: true },
        expect.any(Function),
      );
      expect(audio.play).not.toHaveBeenCalled();
    });

    it("plays while holding the presentation lock and releases it when stopped", async () => {
      const audio = fakeAudio();
      stubAudio(() => audio);
      let released = false;
      const request = vi.fn(
        (_name: string, _options: unknown, callback: (lock: unknown) => unknown) => {
          const held = callback({});
          return Promise.resolve(held).then((value) => {
            released = true;
            return value;
          });
        },
      );
      Object.defineProperty(navigator, "locks", { value: { request }, configurable: true });
      const { startIncomingCallRingtone, stopIncomingCallRingtone } =
        await import("./incomingCallRingtone");

      startIncomingCallRingtone("call-1");
      await vi.waitFor(() => expect(audio.play).toHaveBeenCalledOnce());
      expect(released).toBe(false);

      stopIncomingCallRingtone();
      await vi.waitFor(() => expect(released).toBe(true));
    });
  });
});

describe("incoming call ringtone preference", () => {
  beforeEach(() => {
    vi.resetModules();
    localStorage.clear();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    localStorage.clear();
  });

  it("defaults on and falls back on for invalid or failed reads", async () => {
    const { getIncomingCallRingtoneEnabled } = await import("./incomingCallRingtone");
    expect(getIncomingCallRingtoneEnabled()).toBe(true);
    localStorage.setItem(RINGTONE_KEY, "invalid");
    expect(getIncomingCallRingtoneEnabled()).toBe(true);
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("unavailable");
    });
    expect(getIncomingCallRingtoneEnabled()).toBe(true);
  });

  it("persists true and false under the call-only key", async () => {
    const { getIncomingCallRingtoneEnabled, setIncomingCallRingtoneEnabled } =
      await import("./incomingCallRingtone");
    localStorage.setItem("nchat.notifications.sound.mode", "off");
    setIncomingCallRingtoneEnabled(false);
    expect(getIncomingCallRingtoneEnabled()).toBe(false);
    expect(localStorage.getItem(RINGTONE_KEY)).toBe("false");
    expect(localStorage.getItem("nchat.notifications.sound.mode")).toBe("off");
    setIncomingCallRingtoneEnabled(true);
    expect(getIncomingCallRingtoneEnabled()).toBe(true);
  });

  it("does not throw when persistence fails", async () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("unavailable");
    });
    const { setIncomingCallRingtoneEnabled } = await import("./incomingCallRingtone");
    expect(() => setIncomingCallRingtoneEnabled(false)).not.toThrow();
  });
});
