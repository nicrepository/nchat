import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

describe("messageSound", () => {
  let playSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    playSpy = vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
    vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.resetModules();
  });

  it("constructs the underlying Audio with the notification sound URL", async () => {
    const { playMessageSound } = await import("./messageSound");
    playMessageSound();
    // jsdom's Audio maps to an HTMLAudioElement; its src reflects the constructor URL.
    expect(playSpy).toHaveBeenCalledTimes(1);
    const instance = playSpy.mock.instances[0] as HTMLAudioElement;
    expect(instance.getAttribute("src") ?? instance.src).toContain("/sounds/message-received.wav");
  });

  it("resets currentTime and calls play()", async () => {
    const { playMessageSound } = await import("./messageSound");
    playMessageSound();
    const instance = playSpy.mock.instances[0] as HTMLAudioElement;
    expect(instance.currentTime).toBe(0);
    expect(playSpy).toHaveBeenCalledTimes(1);
  });

  it("reuses the same Audio instance across calls", async () => {
    const { playMessageSound } = await import("./messageSound");
    playMessageSound();
    playMessageSound();
    playMessageSound();
    const instances = new Set(playSpy.mock.instances);
    expect(instances.size).toBe(1);
    expect(playSpy).toHaveBeenCalledTimes(3);
  });

  it("does not throw when play() rejects", async () => {
    playSpy.mockRejectedValue(new DOMException("blocked", "NotAllowedError"));
    const { playMessageSound } = await import("./messageSound");
    expect(() => playMessageSound()).not.toThrow();
    // Let the rejection settle so it isn't reported as unhandled.
    await new Promise((resolve) => setTimeout(resolve, 0));
  });

  it("does not throw when play() throws synchronously", async () => {
    playSpy.mockImplementation(() => {
      throw new Error("synchronous failure");
    });
    const { playMessageSound } = await import("./messageSound");
    expect(() => playMessageSound()).not.toThrow();
  });

  it("does not set audio.loop", async () => {
    const { playMessageSound } = await import("./messageSound");
    playMessageSound();
    const instance = playSpy.mock.instances[0] as HTMLAudioElement;
    expect(instance.loop).toBe(false);
  });
});
