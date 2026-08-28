import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";

import { readEmojiUsage } from "./emojiUsage";
import { useEmojiUsage } from "./useEmojiUsage";

beforeEach(() => localStorage.clear());

describe("useEmojiUsage", () => {
  it("starts from what the reader already had stored", () => {
    localStorage.setItem(
      "nchat_emoji_usage:me",
      JSON.stringify({ v: 1, tone: 2, entries: [{ emoji: "🚀", count: 3, usedAt: 9 }] }),
    );

    const { result } = renderHook(() => useEmojiUsage("me"));

    expect(result.current.usage.tone).toBe(2);
    expect(result.current.usage.entries[0].emoji).toBe("🚀");
  });

  it("records a confirmed reaction and persists it", () => {
    const { result } = renderHook(() => useEmojiUsage("me"));

    act(() => result.current.remember("🎉"));

    expect(result.current.usage.entries[0]).toMatchObject({ emoji: "🎉", count: 1 });
    expect(readEmojiUsage("me").entries[0].emoji).toBe("🎉");
  });

  it("persists a change of skin tone without disturbing the history", () => {
    const { result } = renderHook(() => useEmojiUsage("me"));
    act(() => result.current.remember("🎉"));

    act(() => result.current.changeTone(4));

    expect(result.current.usage.tone).toBe(4);
    expect(result.current.usage.entries[0].emoji).toBe("🎉");
    expect(readEmojiUsage("me")).toEqual(result.current.usage);
  });

  // The preference belongs to whoever is reading: a different reader must never
  // inherit the previous one's emoji.
  it("re-reads when the reader changes without a remount", () => {
    localStorage.setItem(
      "nchat_emoji_usage:other",
      JSON.stringify({ v: 1, tone: 0, entries: [{ emoji: "🐱", count: 1, usedAt: 1 }] }),
    );
    const { result, rerender } = renderHook(({ userId }) => useEmojiUsage(userId), {
      initialProps: { userId: "me" },
    });
    act(() => result.current.remember("🎉"));

    rerender({ userId: "other" });

    expect(result.current.usage.entries.map((entry) => entry.emoji)).toEqual(["🐱"]);
  });
});
