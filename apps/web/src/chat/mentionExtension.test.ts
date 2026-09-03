import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  destroy: vi.fn(),
  fetch: vi.fn(),
  keyDown: vi.fn(),
  updateProps: vi.fn(),
}));

vi.mock("./chatApi", () => ({ fetchMentionCandidates: mocks.fetch }));
vi.mock("@tiptap/react", () => ({
  ReactRenderer: class {
    element = document.createElement("div");
    ref = { onKeyDown: mocks.keyDown };
    updateProps = mocks.updateProps;
    destroy = mocks.destroy;
  },
}));

import { createMentionExtension } from "./mentionExtension";

type Suggestion = {
  items: (props: {
    query: string;
    editor: { storage: Record<string, unknown> };
  }) => unknown[] | Promise<unknown[]>;
  render: () => {
    onStart: (props: Record<string, unknown>) => void;
    onUpdate: (props: Record<string, unknown>) => void;
    onKeyDown: (props: { event: KeyboardEvent }) => boolean;
    onExit: () => void;
  };
};

function options() {
  return createMentionExtension().options as unknown as {
    renderText: (props: { node: { attrs: Record<string, unknown> } }) => string;
    renderHTML: (props: {
      node: { attrs: Record<string, unknown> };
      options: { HTMLAttributes: Record<string, unknown> };
    }) => [string, Record<string, unknown>, string];
    suggestion: Suggestion;
  };
}

beforeEach(() => vi.clearAllMocks());
afterEach(() => document.querySelectorAll(".mention-popup").forEach((element) => element.remove()));

describe("mentionExtension", () => {
  it("returns no items outside channels and falls back to empty on API failure", async () => {
    const suggestion = options().suggestion;

    expect(suggestion.items({ query: "a", editor: { storage: {} } })).toEqual([]);

    mocks.fetch.mockRejectedValueOnce(new Error("network"));
    await expect(
      suggestion.items({
        query: "a".repeat(80),
        editor: {
          storage: { mentionTargetContext: { target: { kind: "channel", id: "channel-1" } } },
        },
      }),
    ).resolves.toEqual([]);
    expect(mocks.fetch).toHaveBeenCalledWith(
      { kind: "channel", id: "channel-1" },
      "a".repeat(64),
      expect.any(AbortSignal),
    );
  });

  it("renders labels with id fallback", () => {
    const extensionOptions = options();
    expect(extensionOptions.renderText({ node: { attrs: { label: "Ana", id: "user-1" } } })).toBe(
      "@Ana",
    );
    expect(extensionOptions.renderText({ node: { attrs: { label: null, id: "user-1" } } })).toBe(
      "@user-1",
    );
    expect(
      extensionOptions.renderHTML({
        node: { attrs: { label: null, id: "user-1", mentionType: "user" } },
        options: { HTMLAttributes: { class: "chat-mention" } },
      })[2],
    ).toBe("@user-1");
  });

  it("repositions on update and handles keyboard lifecycle branches", () => {
    const lifecycle = options().suggestion.render();
    const firstRect = { left: 10, bottom: 20 } as DOMRect;
    const secondRect = { left: 30, bottom: 40 } as DOMRect;
    const props = { editor: {}, clientRect: () => firstRect };

    lifecycle.onUpdate(props);
    expect(lifecycle.onKeyDown({ event: new KeyboardEvent("keydown", { key: "Escape" }) })).toBe(
      true,
    );
    lifecycle.onExit();

    lifecycle.onStart(props);
    const popup = document.querySelector(".mention-popup") as HTMLElement;
    expect(popup.style.left).toBe("10px");
    expect(popup.style.top).toBe("26px");

    lifecycle.onUpdate({ ...props, clientRect: () => secondRect });
    expect(mocks.updateProps).toHaveBeenCalled();
    expect(popup.style.left).toBe("30px");
    expect(popup.style.top).toBe("46px");

    mocks.keyDown.mockReturnValueOnce(true);
    expect(lifecycle.onKeyDown({ event: new KeyboardEvent("keydown", { key: "ArrowDown" }) })).toBe(
      true,
    );
    expect(lifecycle.onKeyDown({ event: new KeyboardEvent("keydown", { key: "Escape" }) })).toBe(
      true,
    );
    expect(document.body.contains(popup)).toBe(false);

    lifecycle.onExit();
    expect(mocks.destroy).toHaveBeenCalledOnce();
  });

  it("handles a missing popup rectangle", () => {
    const lifecycle = options().suggestion.render();
    lifecycle.onStart({ editor: {}, clientRect: null });
    const popup = document.querySelector(".mention-popup") as HTMLElement;
    expect(popup.style.left).toBe("");
    lifecycle.onExit();
  });

  it("flips the popup above the caret when there isn't enough room below the viewport", () => {
    const lifecycle = options().suggestion.render();
    // Caret near the bottom of the viewport (default jsdom innerHeight 768),
    // popup taller than the remaining space below it.
    const rect = { left: 10, top: 700, bottom: 720 } as DOMRect;
    lifecycle.onStart({ editor: {}, clientRect: () => rect });

    const popup = document.querySelector(".mention-popup") as HTMLElement;
    Object.defineProperty(popup, "offsetHeight", { value: 240, configurable: true });
    Object.defineProperty(popup, "offsetWidth", { value: 220, configurable: true });
    lifecycle.onUpdate({ editor: {}, clientRect: () => rect });

    // Flipped above: top - popupHeight - 6 = 700 - 240 - 6 = 454.
    expect(popup.style.top).toBe("454px");
    lifecycle.onExit();
  });

  it("clamps the popup within the right edge of the viewport", () => {
    const lifecycle = options().suggestion.render();
    // Caret near the right edge (default jsdom innerWidth 1024).
    const rect = { left: 1000, top: 20, bottom: 40 } as DOMRect;
    lifecycle.onStart({ editor: {}, clientRect: () => rect });

    const popup = document.querySelector(".mention-popup") as HTMLElement;
    Object.defineProperty(popup, "offsetWidth", { value: 220, configurable: true });
    lifecycle.onUpdate({ editor: {}, clientRect: () => rect });

    // Clamped: min(1000, 1024 - 220 - 8) = 796.
    expect(popup.style.left).toBe("796px");
    lifecycle.onExit();
  });

  it("debounces rapid item requests into a single underlying fetch call", async () => {
    vi.useFakeTimers();
    try {
      mocks.fetch.mockResolvedValue([{ mentionType: "user", id: "user-1", label: "Joao" }]);
      const suggestion = options().suggestion;
      const editor = {
        storage: { mentionTargetContext: { target: { kind: "channel", id: "channel-1" } } },
      };

      // Simulate a fast typing burst: "j", "jo", "joa", "joao".
      const pending = [
        suggestion.items({ query: "j", editor }),
        suggestion.items({ query: "jo", editor }),
        suggestion.items({ query: "joa", editor }),
        suggestion.items({ query: "joao", editor }),
      ];

      expect(mocks.fetch).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(150);

      expect(mocks.fetch).toHaveBeenCalledTimes(1);
      expect(mocks.fetch).toHaveBeenCalledWith(
        { kind: "channel", id: "channel-1" },
        "joao",
        expect.any(AbortSignal),
      );
      await Promise.all(pending);
    } finally {
      vi.useRealTimers();
    }
  });

  it("aborts an in-flight fetch when the popup exits", async () => {
    vi.useFakeTimers();
    try {
      let capturedSignal: AbortSignal | undefined;
      mocks.fetch.mockImplementation((_target: unknown, _query: string, signal?: AbortSignal) => {
        capturedSignal = signal;
        return new Promise(() => {});
      });
      const suggestion = options().suggestion;
      const lifecycle = suggestion.render();
      const editor = {
        storage: { mentionTargetContext: { target: { kind: "channel", id: "channel-1" } } },
      };

      const pending = suggestion.items({ query: "a", editor });
      await vi.advanceTimersByTimeAsync(150);
      expect(mocks.fetch).toHaveBeenCalledTimes(1);
      expect(capturedSignal?.aborted).toBe(false);

      lifecycle.onExit();
      expect(capturedSignal?.aborted).toBe(true);
      await expect(pending).resolves.toEqual([]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("discards a channel-A debounce request when the user switches to channel B before it fires", async () => {
    vi.useFakeTimers();
    try {
      const suggestion = options().suggestion;
      const lifecycle = suggestion.render();
      const channelA = {
        storage: { mentionTargetContext: { target: { kind: "channel", id: "channel-A" } } },
      };

      // User types "@x" in channel A, scheduling a debounced fetch...
      const pendingA = suggestion.items({ query: "x", editor: channelA });
      // ...then switches to channel B, which tears down the popup (onExit)
      // before the debounce fires — channel A's request must be discarded,
      // not resolve with stale/foreign-channel suggestions.
      lifecycle.onExit();
      await vi.advanceTimersByTimeAsync(150);

      expect(mocks.fetch).not.toHaveBeenCalled();
      await expect(pendingA).resolves.toEqual([]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("appends a synthetic @all candidate when the channel query matches", async () => {
    vi.useFakeTimers();
    try {
      mocks.fetch.mockResolvedValue([{ mentionType: "user", id: "user-1", label: "Ana" }]);
      const suggestion = options().suggestion;
      const editor = {
        storage: { mentionTargetContext: { target: { kind: "channel", id: "channel-1" } } },
      };

      const pending = suggestion.items({ query: "al", editor });
      await vi.advanceTimersByTimeAsync(150);

      await expect(pending).resolves.toEqual([
        { mentionType: "user", id: "user-1", label: "Ana" },
        { mentionType: "all", id: "00000000-0000-0000-0000-000000000000", label: "all" },
      ]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not offer @all once the query no longer matches it", async () => {
    vi.useFakeTimers();
    try {
      mocks.fetch.mockResolvedValue([{ mentionType: "user", id: "user-1", label: "Ana" }]);
      const suggestion = options().suggestion;
      const editor = {
        storage: { mentionTargetContext: { target: { kind: "channel", id: "channel-1" } } },
      };

      const pending = suggestion.items({ query: "ana", editor });
      await vi.advanceTimersByTimeAsync(150);

      await expect(pending).resolves.toEqual([{ mentionType: "user", id: "user-1", label: "Ana" }]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("appends a synthetic @all candidate for a group DM query match (issue #776)", async () => {
    vi.useFakeTimers();
    try {
      mocks.fetch.mockResolvedValue([{ mentionType: "user", id: "user-1", label: "Ana" }]);
      const suggestion = options().suggestion;
      const editor = {
        storage: { mentionTargetContext: { target: { kind: "dm", id: "group-1" } } },
      };

      const pending = suggestion.items({ query: "al", editor });
      await vi.advanceTimersByTimeAsync(150);

      await expect(pending).resolves.toEqual([
        { mentionType: "user", id: "user-1", label: "Ana" },
        { mentionType: "all", id: "00000000-0000-0000-0000-000000000000", label: "all" },
      ]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not offer @all for a group DM once the query no longer matches it", async () => {
    vi.useFakeTimers();
    try {
      mocks.fetch.mockResolvedValue([{ mentionType: "user", id: "user-1", label: "Ana" }]);
      const suggestion = options().suggestion;
      const editor = {
        storage: { mentionTargetContext: { target: { kind: "dm", id: "group-1" } } },
      };

      const pending = suggestion.items({ query: "ana", editor });
      await vi.advanceTimersByTimeAsync(150);

      await expect(pending).resolves.toEqual([{ mentionType: "user", id: "user-1", label: "Ana" }]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("ignores an aborted response even when the transport resolves it late", async () => {
    vi.useFakeTimers();
    try {
      let resolveFirst!: (items: unknown[]) => void;
      mocks.fetch
        .mockReturnValueOnce(new Promise((resolve) => (resolveFirst = resolve)))
        .mockResolvedValueOnce([{ mentionType: "user", id: "new", label: "Caio" }]);
      const suggestion = options().suggestion;
      const editor = {
        storage: { mentionTargetContext: { target: { kind: "channel", id: "channel-1" } } },
      };

      const first = suggestion.items({ query: "c", editor });
      await vi.advanceTimersByTimeAsync(150);
      const latest = suggestion.items({ query: "ca", editor });
      resolveFirst([{ mentionType: "user", id: "old", label: "Carlos" }]);
      await Promise.resolve();
      await vi.advanceTimersByTimeAsync(150);

      await expect(Promise.all([first, latest])).resolves.toEqual([
        [{ mentionType: "user", id: "new", label: "Caio" }],
        [{ mentionType: "user", id: "new", label: "Caio" }],
      ]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("offers @all with an empty query, matching normal candidate-list behavior", async () => {
    vi.useFakeTimers();
    try {
      mocks.fetch.mockResolvedValue([]);
      const suggestion = options().suggestion;
      const editor = {
        storage: { mentionTargetContext: { target: { kind: "channel", id: "channel-1" } } },
      };

      const pending = suggestion.items({ query: "", editor });
      await vi.advanceTimersByTimeAsync(150);

      await expect(pending).resolves.toEqual([
        { mentionType: "all", id: "00000000-0000-0000-0000-000000000000", label: "all" },
      ]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("matches @all case-insensitively", async () => {
    vi.useFakeTimers();
    try {
      mocks.fetch.mockResolvedValue([]);
      const suggestion = options().suggestion;
      const editor = {
        storage: { mentionTargetContext: { target: { kind: "channel", id: "channel-1" } } },
      };

      const pending = suggestion.items({ query: "AL", editor });
      await vi.advanceTimersByTimeAsync(150);

      await expect(pending).resolves.toEqual([
        { mentionType: "all", id: "00000000-0000-0000-0000-000000000000", label: "all" },
      ]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("cancels a pending debounced fetch when the popup exits", async () => {
    vi.useFakeTimers();
    try {
      mocks.fetch.mockResolvedValue([]);
      const suggestion = options().suggestion;
      const lifecycle = suggestion.render();
      const editor = {
        storage: { mentionTargetContext: { target: { kind: "channel", id: "channel-1" } } },
      };

      suggestion.items({ query: "a", editor });
      lifecycle.onExit();
      await vi.advanceTimersByTimeAsync(150);

      expect(mocks.fetch).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });
});
