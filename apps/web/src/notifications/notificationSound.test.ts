import { existsSync, statSync } from "node:fs";
import { resolve } from "node:path";

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { NotificationSound } from "./notificationSound";

/**
 * The seven keys, written out rather than imported from the module.
 *
 * A test that asks the implementation what its keys are cannot notice one going
 * missing — it would simply assert less. This list is the contract #827 states,
 * and `NOTIFICATION_SOUNDS` is checked against it below.
 */
const EXPECTED_SOUNDS: readonly NotificationSound[] = [
  "message",
  "in-conversation",
  "mention",
  "urgent",
  "incoming-call",
  "call-start",
  "call-end",
];

const EXPECTED_SOURCES: Record<NotificationSound, string> = {
  message: "/sounds/nchat_lumen_message.wav",
  "in-conversation": "/sounds/nchat_lumen_in_conversation.wav",
  mention: "/sounds/nchat_lumen_mention.wav",
  urgent: "/sounds/nchat_lumen_urgent.wav",
  "incoming-call": "/sounds/nchat_lumen_incoming_call.wav",
  "call-start": "/sounds/nchat_lumen_call_start.wav",
  "call-end": "/sounds/nchat_lumen_call_end.wav",
};

function fakeAudio() {
  return {
    preload: "",
    currentTime: 7,
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

/** A fresh module, so the element cache never leaks between tests. */
async function loadPlayer() {
  return import("./notificationSound");
}

describe("notificationSound", () => {
  beforeEach(() => {
    vi.resetModules();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  describe("asset allowlist", () => {
    it("exposes exactly the seven Lumen keys", async () => {
      const { NOTIFICATION_SOUNDS } = await loadPlayer();

      expect([...NOTIFICATION_SOUNDS].sort()).toEqual([...EXPECTED_SOUNDS].sort());
      expect(NOTIFICATION_SOUNDS).toHaveLength(7);
    });

    it("resolves each key to its own Lumen asset", async () => {
      const { notificationSoundSource } = await loadPlayer();

      for (const sound of EXPECTED_SOUNDS) {
        expect(notificationSoundSource(sound)).toBe(EXPECTED_SOURCES[sound]);
      }
      // Seven distinct files: a mapping that quietly pointed two keys at one
      // asset would still pass every "has a source" assertion above.
      const sources = EXPECTED_SOUNDS.map((sound) => notificationSoundSource(sound));
      expect(new Set(sources).size).toBe(7);
    });

    it("ships every mapped asset in the public directory", async () => {
      const { notificationSoundSource } = await loadPlayer();
      // Vitest runs with this package as its root (see vite.config.ts).
      const publicDir = resolve(process.cwd(), "public");

      for (const sound of EXPECTED_SOUNDS) {
        const file = `${publicDir}${notificationSoundSource(sound)}`;
        // Structural only — existence, a regular file, non-empty. Decoding the
        // bytes would be testing the browser, not this mapping.
        expect(existsSync(file), `${sound} -> ${file}`).toBe(true);
        expect(statSync(file).isFile()).toBe(true);
        expect(statSync(file).size).toBeGreaterThan(0);
      }
    });

    it("refuses a key outside the allowlist instead of building a path from it", async () => {
      const { notificationSoundSource, playNotificationSound } = await loadPlayer();
      const AudioMock = stubAudio(fakeAudio);
      const hostile = "../../../etc/passwd" as NotificationSound;

      expect(notificationSoundSource(hostile)).toBeUndefined();
      // Inherited keys are not sources either: "toString" is on Object.prototype.
      expect(notificationSoundSource("toString" as NotificationSound)).toBeUndefined();

      expect(() => playNotificationSound(hostile)).not.toThrow();
      expect(AudioMock).not.toHaveBeenCalled();
    });
  });

  describe("playback", () => {
    it("builds the element once and reuses it for every later play", async () => {
      const audio = fakeAudio();
      const AudioMock = stubAudio(() => audio);
      const { playNotificationSound } = await loadPlayer();

      playNotificationSound("message");
      playNotificationSound("message");
      playNotificationSound("message");

      expect(AudioMock).toHaveBeenCalledOnce();
      expect(AudioMock).toHaveBeenCalledWith("/sounds/nchat_lumen_message.wav");
      expect(audio.preload).toBe("auto");
      expect(audio.play).toHaveBeenCalledTimes(3);
    });

    it("rewinds before each play so a repeat restarts rather than stacking", async () => {
      const audio = fakeAudio();
      stubAudio(() => audio);
      const { playNotificationSound } = await loadPlayer();

      playNotificationSound("mention");
      audio.currentTime = 0.4;
      playNotificationSound("mention");

      expect(audio.currentTime).toBe(0);
    });

    it("caches at most one element per key and never more than seven", async () => {
      const audio = fakeAudio();
      const AudioMock = stubAudio(() => audio);
      const { NOTIFICATION_SOUNDS, playNotificationSound } = await loadPlayer();

      for (let round = 0; round < 5; round += 1) {
        for (const sound of NOTIFICATION_SOUNDS) playNotificationSound(sound);
      }

      // 35 plays, 7 elements: the cache is bounded by the key set, not by calls.
      expect(AudioMock).toHaveBeenCalledTimes(7);
      expect(audio.play).toHaveBeenCalledTimes(35);
    });

    it("keeps each sound independent of the others", async () => {
      const built = new Map<string, ReturnType<typeof fakeAudio>>();
      const AudioMock = vi.fn(function AudioMock(src: string) {
        const audio = fakeAudio();
        built.set(src, audio);
        return audio;
      });
      vi.stubGlobal("Audio", AudioMock);
      const { playNotificationSound, stopNotificationSound } = await loadPlayer();

      playNotificationSound("urgent");
      playNotificationSound("message");
      stopNotificationSound("urgent");

      expect(built.get("/sounds/nchat_lumen_urgent.wav")?.pause).toHaveBeenCalledOnce();
      expect(built.get("/sounds/nchat_lumen_message.wav")?.pause).not.toHaveBeenCalled();
      expect(built.get("/sounds/nchat_lumen_message.wav")?.play).toHaveBeenCalledOnce();
    });
  });

  describe("exclusivity", () => {
    it("silences the other sounds before starting an exclusive one", async () => {
      const built = new Map<string, ReturnType<typeof fakeAudio>>();
      vi.stubGlobal(
        "Audio",
        vi.fn(function AudioMock(src: string) {
          const audio = fakeAudio();
          built.set(src, audio);
          return audio;
        }),
      );
      const { playNotificationSound } = await loadPlayer();

      playNotificationSound("message");
      playNotificationSound("urgent");
      playNotificationSound("incoming-call", { exclusive: true });

      expect(built.get("/sounds/nchat_lumen_message.wav")?.pause).toHaveBeenCalledOnce();
      expect(built.get("/sounds/nchat_lumen_urgent.wav")?.pause).toHaveBeenCalledOnce();
      expect(built.get("/sounds/nchat_lumen_incoming_call.wav")?.play).toHaveBeenCalledOnce();
    });

    it("leaves the other sounds alone when exclusivity is not asked for", async () => {
      const built = new Map<string, ReturnType<typeof fakeAudio>>();
      vi.stubGlobal(
        "Audio",
        vi.fn(function AudioMock(src: string) {
          const audio = fakeAudio();
          built.set(src, audio);
          return audio;
        }),
      );
      const { playNotificationSound } = await loadPlayer();

      playNotificationSound("message");
      playNotificationSound("mention");

      expect(built.get("/sounds/nchat_lumen_message.wav")?.pause).not.toHaveBeenCalled();
    });
  });

  describe("cleanup", () => {
    it("stopNotificationSounds pauses and rewinds everything it holds", async () => {
      const built: ReturnType<typeof fakeAudio>[] = [];
      vi.stubGlobal(
        "Audio",
        vi.fn(function AudioMock() {
          const audio = fakeAudio();
          built.push(audio);
          return audio;
        }),
      );
      const { playNotificationSound, stopNotificationSounds } = await loadPlayer();

      playNotificationSound("message");
      playNotificationSound("incoming-call");
      stopNotificationSounds();

      expect(built).toHaveLength(2);
      for (const audio of built) {
        expect(audio.pause).toHaveBeenCalledOnce();
        expect(audio.currentTime).toBe(0);
      }
    });

    it("dispose silences the active sounds and drops the cache", async () => {
      const audio = fakeAudio();
      const AudioMock = stubAudio(() => audio);
      const { playNotificationSound, disposeNotificationSoundPlayer } = await loadPlayer();

      playNotificationSound("message");
      disposeNotificationSoundPlayer();

      expect(audio.pause).toHaveBeenCalledOnce();
      expect(audio.currentTime).toBe(0);

      // The cache is gone, so the next play builds a new element rather than
      // resurrecting the disposed one.
      playNotificationSound("message");
      expect(AudioMock).toHaveBeenCalledTimes(2);
    });

    it("creates no timer and registers no listener to clean up", async () => {
      vi.useFakeTimers();
      const addEventListener = vi.spyOn(globalThis, "addEventListener");
      stubAudio(fakeAudio);
      try {
        const { playNotificationSound, disposeNotificationSoundPlayer } = await loadPlayer();

        playNotificationSound("message");
        playNotificationSound("incoming-call", { exclusive: true });

        // Repeat cadence, cooldowns and burst control all live in their own
        // owners; nothing here schedules work, so there is nothing to orphan.
        expect(vi.getTimerCount()).toBe(0);
        expect(addEventListener).not.toHaveBeenCalled();
        disposeNotificationSoundPlayer();
        expect(vi.getTimerCount()).toBe(0);
      } finally {
        vi.useRealTimers();
      }
    });

    it("tolerates stop and dispose before anything has ever played", async () => {
      stubAudio(fakeAudio);
      const { stopNotificationSound, stopNotificationSounds, disposeNotificationSoundPlayer } =
        await loadPlayer();

      expect(() => stopNotificationSound("message")).not.toThrow();
      expect(() => stopNotificationSounds()).not.toThrow();
      expect(() => disposeNotificationSoundPlayer()).not.toThrow();
    });
  });

  describe("failure never reaches the caller", () => {
    it("swallows a rejected play() without an unhandled rejection", async () => {
      const unhandled = vi.fn();
      process.on("unhandledRejection", unhandled);
      const audio = fakeAudio();
      audio.play.mockRejectedValue(new DOMException("blocked", "NotAllowedError"));
      stubAudio(() => audio);
      const { playNotificationSound } = await loadPlayer();

      try {
        expect(() => playNotificationSound("message")).not.toThrow();
        await new Promise((resolve) => setTimeout(resolve, 0));
        expect(unhandled).not.toHaveBeenCalled();
      } finally {
        process.off("unhandledRejection", unhandled);
      }
    });

    it("swallows an AbortError the same way and does not retry", async () => {
      const audio = fakeAudio();
      audio.play.mockRejectedValue(new DOMException("interrupted", "AbortError"));
      stubAudio(() => audio);
      const { playNotificationSound } = await loadPlayer();

      playNotificationSound("urgent");
      await new Promise((resolve) => setTimeout(resolve, 0));

      // One attempt per call, forever: a retry loop on a tab that has not been
      // clicked is an infinite one.
      expect(audio.play).toHaveBeenCalledOnce();
    });

    it("survives a play() that returns undefined, as older elements do", async () => {
      const audio = { ...fakeAudio(), play: vi.fn(() => undefined) };
      stubAudio(() => audio);
      const { playNotificationSound } = await loadPlayer();

      expect(() => playNotificationSound("message")).not.toThrow();
      expect(audio.play).toHaveBeenCalledOnce();
    });

    it("survives a constructor that throws, and retries only on the next call", async () => {
      const AudioMock = stubAudio(() => {
        throw new Error("no audio in this browser");
      });
      const { playNotificationSound } = await loadPlayer();

      expect(() => playNotificationSound("message")).not.toThrow();
      expect(() => playNotificationSound("message")).not.toThrow();

      // Nothing was cached, so each call tries once — and never more than once.
      expect(AudioMock).toHaveBeenCalledTimes(2);
    });

    it("survives synchronous play, pause and seek failures", async () => {
      let seekAttempts = 0;
      const audio = {
        preload: "",
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
          seekAttempts += 1;
          throw new Error("seek failed");
        },
      });
      stubAudio(() => audio);
      const { playNotificationSound, stopNotificationSound } = await loadPlayer();

      expect(() => playNotificationSound("message")).not.toThrow();
      expect(() => stopNotificationSound("message")).not.toThrow();

      // A pause that throws must not cost the rewind that follows it.
      expect(seekAttempts).toBeGreaterThanOrEqual(2);
      expect(audio.play).toHaveBeenCalledOnce();
    });
  });
});
