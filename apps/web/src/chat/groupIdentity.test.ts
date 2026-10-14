import { describe, expect, it } from "vitest";

import {
  AUTOMATIC_IDENTITY,
  identityIncomplete,
  identityOf,
  persistedEmoji,
  withIdentityMode,
} from "./groupIdentity";

describe("groupIdentity (issue #1026)", () => {
  it("reads a stored emoji as Emoji mode and its absence as Automático", () => {
    expect(identityOf("🚀")).toEqual({ mode: "emoji", emoji: "🚀" });
    expect(identityOf(undefined)).toBe(AUTOMATIC_IDENTITY);
    expect(identityOf("")).toBe(AUTOMATIC_IDENTITY);
  });

  it("drops the emoji when switching to Automático, so nothing of it is persisted", () => {
    const next = withIdentityMode({ mode: "emoji", emoji: "🎉" }, "auto");
    expect(next).toEqual({ mode: "auto" });
    expect(persistedEmoji(next)).toBeUndefined();
  });

  it("keeps a chosen emoji when Emoji is selected again, and starts empty from Automático", () => {
    const chosen = { mode: "emoji" as const, emoji: "🎉" };
    expect(withIdentityMode(chosen, "emoji")).toBe(chosen);
    expect(withIdentityMode(AUTOMATIC_IDENTITY, "emoji")).toEqual({ mode: "emoji" });
  });

  it("persists the emoji only in Emoji mode", () => {
    expect(persistedEmoji({ mode: "emoji", emoji: "👩‍💻" })).toBe("👩‍💻");
    expect(persistedEmoji({ mode: "auto", emoji: "👩‍💻" })).toBeUndefined();
  });

  it("is incomplete only in Emoji mode with nothing picked", () => {
    expect(identityIncomplete({ mode: "emoji" })).toBe(true);
    expect(identityIncomplete({ mode: "emoji", emoji: "🎉" })).toBe(false);
    expect(identityIncomplete(AUTOMATIC_IDENTITY)).toBe(false);
  });
});
