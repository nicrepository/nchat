/**
 * useMessageJump — when a `?message=` deep link is followed (issue #896).
 *
 * The scroll authority is replaced by spies: what is under test is only the
 * rule deciding whether a render is a new request to travel or the same one
 * seen again.
 */

import { renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { Message } from "../../chatTypes";
import { useMessageJump } from "./useMessageJump";

const noMessages: Message[] = [];

function renderJump(initial: { messageId: string; request: string }) {
  const commands = {
    scrollToMessage: vi.fn(() => true),
    hasRow: vi.fn(() => true),
    beginJump: vi.fn(),
    endJump: vi.fn(),
    jumpRef: { current: null },
    listRef: { current: null },
    messageRefs: { current: new Map<string, HTMLElement>() },
  };
  const hook = renderHook(
    ({ messageId, request }) => useMessageJump(commands, noMessages, messageId, request),
    { initialProps: initial },
  );
  return { ...hook, scrollToMessage: commands.scrollToMessage };
}

describe("useMessageJump — following a deep link", () => {
  it("follows an external link once, however often it re-renders", () => {
    const { rerender, scrollToMessage } = renderJump({ messageId: "m1", request: "" });

    rerender({ messageId: "m1", request: "" });
    rerender({ messageId: "m1", request: "" });

    expect(scrollToMessage).toHaveBeenCalledTimes(1);
    expect(scrollToMessage).toHaveBeenCalledWith("m1");
  });

  it("travels again when the reader asks for the same message again", () => {
    const { rerender, scrollToMessage } = renderJump({ messageId: "m1", request: "entry-1" });

    rerender({ messageId: "m1", request: "entry-1" });
    expect(scrollToMessage).toHaveBeenCalledTimes(1);

    rerender({ messageId: "m1", request: "entry-2" });
    expect(scrollToMessage).toHaveBeenCalledTimes(2);
    expect(scrollToMessage).toHaveBeenLastCalledWith("m1");
  });

  it("does not travel when a navigation only keeps the query", () => {
    const { rerender, scrollToMessage } = renderJump({ messageId: "m1", request: "entry-1" });

    // Sending a message replaces the entry without the jump mark.
    rerender({ messageId: "m1", request: "" });

    expect(scrollToMessage).toHaveBeenCalledTimes(1);
  });

  it("follows a different message whatever the request says", () => {
    const { rerender, scrollToMessage } = renderJump({ messageId: "m1", request: "entry-1" });

    rerender({ messageId: "m2", request: "" });

    expect(scrollToMessage).toHaveBeenLastCalledWith("m2");
    expect(scrollToMessage).toHaveBeenCalledTimes(2);
  });

  it("follows the same link again after it was left", () => {
    const { rerender, scrollToMessage } = renderJump({ messageId: "m1", request: "" });

    rerender({ messageId: "", request: "" });
    rerender({ messageId: "m1", request: "" });

    expect(scrollToMessage).toHaveBeenCalledTimes(2);
  });
});
