import { describe, expect, it, vi } from "vitest";

import { createCommandRegistry } from "./commandRegistry";

describe("CommandRegistry", () => {
  it("executes the semantic action registered for a command", () => {
    const openSearch = vi.fn();
    const registry = createCommandRegistry({
      openSearch,
      openShortcutHelp: vi.fn(),
      previousConversation: vi.fn(),
      nextConversation: vi.fn(),
      historyBack: vi.fn(),
      historyForward: vi.fn(),
    });

    registry.execute("search.open");

    expect(openSearch).toHaveBeenCalledOnce();
  });
});
