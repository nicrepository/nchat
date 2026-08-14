import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useTypingIndicator } from "./useTypingIndicator";
import type { WSTypingUpdatedEvent } from "./useChatWebSocket";

const CURRENT_USER = "user-me";
const OTHER_USER = "user-other";

function typingEvent(overrides: Partial<NonNullable<WSTypingUpdatedEvent["typing"]>> = {}): WSTypingUpdatedEvent {
  return {
    type: "typing.updated",
    target_type: "channel",
    target_id: "chan-1",
    typing: {
      user_id: OTHER_USER,
      is_typing: true,
      ...overrides,
    },
  };
}

function setup() {
  const sendTyping = vi.fn(() => true);
  const { result } = renderHook(() =>
    useTypingIndicator({
      kind: "channel",
      targetId: "chan-1",
      currentUserId: CURRENT_USER,
      sendTyping,
    }),
  );
  return { result, sendTyping };
}

beforeEach(() => {
  vi.useRealTimers();
});

describe("useTypingIndicator — server-resolved display names", () => {
  it("starts with an empty typingDisplayNameByUserId map", () => {
    const { result } = setup();
    expect(result.current.typingDisplayNameByUserId.size).toBe(0);
  });

  it("populates the name from a typing.updated event carrying user_display_name", () => {
    const { result } = setup();
    act(() => {
      result.current.handleRemoteEvent(typingEvent({ user_display_name: "Diana Reis" }));
    });
    expect(result.current.typingUserIds).toEqual([OTHER_USER]);
    expect(result.current.typingDisplayNameByUserId.get(OTHER_USER)).toBe("Diana Reis");
  });

  it("leaves the user out of the name map when the server sent no name", () => {
    const { result } = setup();
    act(() => {
      result.current.handleRemoteEvent(typingEvent({ user_display_name: undefined }));
    });
    expect(result.current.typingUserIds).toEqual([OTHER_USER]);
    expect(result.current.typingDisplayNameByUserId.has(OTHER_USER)).toBe(false);
  });

  it("removes the name when the user stops typing", () => {
    const { result } = setup();
    act(() => {
      result.current.handleRemoteEvent(typingEvent({ user_display_name: "Diana Reis" }));
    });
    act(() => {
      result.current.handleRemoteEvent(typingEvent({ is_typing: false, user_display_name: "Diana Reis" }));
    });
    expect(result.current.typingUserIds).toEqual([]);
    expect(result.current.typingDisplayNameByUserId.has(OTHER_USER)).toBe(false);
  });

  it("ignores the local user's own echoed event", () => {
    const { result } = setup();
    act(() => {
      result.current.handleRemoteEvent(typingEvent({ user_id: CURRENT_USER, user_display_name: "Me" }));
    });
    expect(result.current.typingUserIds).toEqual([]);
    expect(result.current.typingDisplayNameByUserId.size).toBe(0);
  });
});
