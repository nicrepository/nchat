/**
 * What happens to the memoised catalog when the lazily-imported chunk fails.
 *
 * Kept apart from emojiCatalog.test.ts because it needs the chunk itself
 * replaced, which means resetting the module registry between cases — the rest
 * of the catalog suite runs against the real generated data.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const catalogJson = JSON.stringify({
  version: "test",
  emojis: [{ u: "👍", l: "polegar para cima", t: "jóia", g: 1 }],
});

beforeEach(() => vi.resetModules());
afterEach(() => vi.doUnmock("./emojiCatalog.json?raw"));

describe("loadEmojiCatalog", () => {
  it("does not remember a failed load, so a retry reaches the chunk again", async () => {
    let attempts = 0;
    vi.doMock("./emojiCatalog.json?raw", () => {
      attempts += 1;
      if (attempts === 1) throw new Error("chunk unreachable");
      return { default: catalogJson };
    });
    const { loadEmojiCatalog } = await import("./emojiCatalog");

    await expect(loadEmojiCatalog()).rejects.toThrow();
    // The second call must actually load, not replay the cached rejection —
    // otherwise one bad moment breaks the picker for the rest of the session.
    await expect(loadEmojiCatalog()).resolves.toMatchObject({ version: "test" });
    expect(attempts).toBe(2);
  });

  it("still loads the catalog only once when it succeeds", async () => {
    let attempts = 0;
    vi.doMock("./emojiCatalog.json?raw", () => {
      attempts += 1;
      return { default: catalogJson };
    });
    const { loadEmojiCatalog } = await import("./emojiCatalog");

    const [first, second] = await Promise.all([loadEmojiCatalog(), loadEmojiCatalog()]);

    expect(first).toBe(second);
    expect(attempts).toBe(1);
  });

  it("reports nothing as catalogued while the chunk is failing", async () => {
    vi.doMock("./emojiCatalog.json?raw", () => {
      throw new Error("chunk unreachable");
    });
    const { isCatalogedEmoji, loadEmojiCatalog } = await import("./emojiCatalog");

    await expect(loadEmojiCatalog()).rejects.toThrow();

    expect(isCatalogedEmoji("👍")).toBe(false);
  });
});
