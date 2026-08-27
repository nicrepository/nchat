import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  emptyEmojiUsage,
  frequentEmojis,
  readEmojiUsage,
  recentEmojis,
  recordEmojiUse,
  storeEmojiTone,
  type EmojiUsage,
} from "./emojiUsage";

const userId = "me-123";
const key = `nchat_emoji_usage:${userId}`;

function store(value: unknown): void {
  localStorage.setItem(key, typeof value === "string" ? value : JSON.stringify(value));
}

beforeEach(() => localStorage.clear());
afterEach(() => vi.restoreAllMocks());

describe("readEmojiUsage", () => {
  it("reports no history for a reader who has never reacted", () => {
    expect(readEmojiUsage(userId)).toEqual(emptyEmojiUsage);
  });

  it("reports no history when there is no reader yet", () => {
    expect(readEmojiUsage("")).toEqual(emptyEmojiUsage);
  });

  it("reads back what was written", () => {
    store({ v: 1, tone: 3, entries: [{ emoji: "🚀", count: 2, usedAt: 10 }] });
    expect(readEmojiUsage(userId)).toEqual({
      tone: 3,
      entries: [{ emoji: "🚀", count: 2, usedAt: 10 }],
    });
  });

  // Everything below is a value the app never wrote. None of it may reach the
  // picker, and none of it may throw on the way there.
  it.each([
    ["unparseable", "{not json"],
    ["not an object", JSON.stringify(42)],
    ["null", JSON.stringify(null)],
    ["another schema version", JSON.stringify({ v: 99, tone: 0, entries: [] })],
    ["entries that are not a list", JSON.stringify({ v: 1, tone: 0, entries: "🚀" })],
  ])("treats a %s value as no history", (_name, value) => {
    store(value);
    expect(readEmojiUsage(userId)).toEqual(emptyEmojiUsage);
  });

  it("drops individual entries that are not the shape it writes", () => {
    store({
      v: 1,
      tone: 0,
      entries: [
        { emoji: "🚀", count: 1, usedAt: 1 },
        { emoji: "", count: 1, usedAt: 1 },
        { emoji: "🎉", count: 0, usedAt: 1 },
        { emoji: "🎉", count: Number.NaN, usedAt: 1 },
        { emoji: "🎉", count: 1, usedAt: "ontem" },
        "🎉",
        null,
      ],
    });
    expect(readEmojiUsage(userId).entries).toEqual([{ emoji: "🚀", count: 1, usedAt: 1 }]);
  });

  it("clamps a tone outside the range Unicode defines", () => {
    store({ v: 1, tone: 42, entries: [] });
    expect(readEmojiUsage(userId).tone).toBe(0);
    store({ v: 1, tone: "escura", entries: [] });
    expect(readEmojiUsage(userId).tone).toBe(0);
  });

  it("keeps the stored list bounded however large the stored value is", () => {
    store({
      v: 1,
      tone: 0,
      entries: Array.from({ length: 200 }, (_, i) => ({ emoji: `e${i}`, count: 1, usedAt: i })),
    });
    expect(readEmojiUsage(userId).entries).toHaveLength(40);
  });
});

describe("recordEmojiUse", () => {
  it("puts the emoji first and counts it", () => {
    let usage = recordEmojiUse(userId, "🚀", emptyEmojiUsage);
    usage = recordEmojiUse(userId, "🎉", usage);
    usage = recordEmojiUse(userId, "🚀", usage);

    expect(usage.entries.map((entry) => entry.emoji)).toEqual(["🚀", "🎉"]);
    expect(usage.entries[0].count).toBe(2);
    expect(readEmojiUsage(userId).entries[0].emoji).toBe("🚀");
  });

  it("never grows past the stored limit", () => {
    let usage: EmojiUsage = emptyEmojiUsage;
    for (let i = 0; i < 60; i += 1) usage = recordEmojiUse(userId, `e${i}`, usage);
    expect(usage.entries).toHaveLength(40);
    expect(usage.entries[0].emoji).toBe("e59");
  });

  it("ignores a use it cannot attribute", () => {
    expect(recordEmojiUse("", "🚀", emptyEmojiUsage)).toBe(emptyEmojiUsage);
    expect(recordEmojiUse(userId, "", emptyEmojiUsage)).toBe(emptyEmojiUsage);
  });

  // A preference is best-effort: a browser refusing to store it must not be
  // able to refuse the reaction.
  it("still reports the new history when storage refuses to write", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("quota", "QuotaExceededError");
    });
    expect(recordEmojiUse(userId, "🚀", emptyEmojiUsage).entries[0].emoji).toBe("🚀");
  });
});

describe("recents and frequents", () => {
  const usage: EmojiUsage = {
    tone: 0,
    entries: [
      { emoji: "🚀", count: 1, usedAt: 30 },
      { emoji: "🎉", count: 5, usedAt: 20 },
      { emoji: "👍", count: 2, usedAt: 10 },
    ],
  };

  it("lists recents most recent first", () => {
    expect(recentEmojis(usage, 2)).toEqual(["🚀", "🎉"]);
  });

  // A "most used" list built from single uses is a worse copy of "recent", so
  // an emoji only appears once it has actually been reached for twice.
  it("lists frequents by count, ignoring one-off uses", () => {
    expect(frequentEmojis(usage, 5)).toEqual(["🎉", "👍"]);
    expect(frequentEmojis(emptyEmojiUsage, 5)).toEqual([]);
  });
});

describe("storeEmojiTone", () => {
  it("persists the tone without disturbing the history", () => {
    const usage = recordEmojiUse(userId, "🚀", emptyEmojiUsage);
    const toned = storeEmojiTone(userId, 4, usage);
    expect(toned.tone).toBe(4);
    expect(readEmojiUsage(userId)).toEqual(toned);
  });

  it("refuses a tone Unicode does not define", () => {
    expect(storeEmojiTone(userId, 9, emptyEmojiUsage).tone).toBe(0);
  });
});
