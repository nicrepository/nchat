import { describe, expect, it, vi } from "vitest";

import { createLiveKitSessionLoader } from "./liveKitSession";

describe("createLiveKitSessionLoader", () => {
  it("reuses one module import for concurrent callers", async () => {
    const factory = vi.fn();
    const importer = vi.fn(async () => ({ createLiveKitSpikeSession: factory }));
    const load = createLiveKitSessionLoader(importer);

    const first = load();
    const second = load();

    expect(first).toBe(second);
    await expect(first).resolves.toBe(factory);
    await expect(second).resolves.toBe(factory);
    expect(importer).toHaveBeenCalledOnce();
  });

  it("allows retry after a failed module import", async () => {
    const factory = vi.fn();
    const importer = vi
      .fn()
      .mockRejectedValueOnce(new Error("chunk unavailable"))
      .mockResolvedValueOnce({ createLiveKitSpikeSession: factory });
    const load = createLiveKitSessionLoader(importer);

    await expect(load()).rejects.toThrow("chunk unavailable");
    await expect(load()).resolves.toBe(factory);
    expect(importer).toHaveBeenCalledTimes(2);
  });
});
