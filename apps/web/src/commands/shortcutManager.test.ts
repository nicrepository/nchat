import { describe, expect, it } from "vitest";

import { resolveShortcut, shouldIgnoreShortcutTarget } from "./shortcutManager";

describe("ShortcutManager", () => {
  it("resolves Mod shortcuts for either Ctrl or Cmd", () => {
    expect(resolveShortcut({ key: "k", ctrlKey: true }, ["global"])).toBe("search.open");
    expect(resolveShortcut({ key: "K", metaKey: true }, ["global"])).toBe("search.open");
  });

  it("only resolves shortcuts whose scope is active", () => {
    expect(resolveShortcut({ key: "ArrowUp", altKey: true }, ["composer"])).toBeNull();
    expect(resolveShortcut({ key: "ArrowUp", altKey: true }, ["global"])).toBe(
      "conversation.previous",
    );
  });

  it("maps horizontal keys to conversation history instead of sidebar order", () => {
    expect(resolveShortcut({ key: "ArrowLeft", altKey: true }, ["global"])).toBe(
      "conversation.historyBack",
    );
    expect(resolveShortcut({ key: "ArrowRight", altKey: true }, ["global"])).toBe(
      "conversation.historyForward",
    );
  });

  it("leaves native editing shortcuts alone inside editable controls", () => {
    for (const tagName of ["input", "textarea", "select"]) {
      expect(shouldIgnoreShortcutTarget(document.createElement(tagName))).toBe(true);
    }

    const editable = document.createElement("div");
    editable.contentEditable = "true";
    expect(shouldIgnoreShortcutTarget(editable)).toBe(true);
  });
});
