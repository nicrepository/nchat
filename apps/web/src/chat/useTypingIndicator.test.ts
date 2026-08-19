import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useTypingIndicator, type UseTypingIndicatorOptions } from "./useTypingIndicator";
import type { WSTypingUpdatedEvent } from "./useChatWebSocket";

const CURRENT_USER = "user-me";
const OTHER_USER = "user-other";

function typingEvent(
  overrides: Partial<NonNullable<WSTypingUpdatedEvent["typing"]>> = {},
): WSTypingUpdatedEvent {
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

function setup(overrides: Partial<UseTypingIndicatorOptions> = {}) {
  const sendTyping = vi.fn(() => true);
  const { result, unmount } = renderHook(() =>
    useTypingIndicator({
      kind: "channel",
      targetId: "chan-1",
      currentUserId: CURRENT_USER,
      sendTyping,
      ...overrides,
    }),
  );
  return { result, sendTyping, unmount };
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
      result.current.handleRemoteEvent(
        typingEvent({ is_typing: false, user_display_name: "Diana Reis" }),
      );
    });
    expect(result.current.typingUserIds).toEqual([]);
    expect(result.current.typingDisplayNameByUserId.has(OTHER_USER)).toBe(false);
  });

  it("ignores the local user's own echoed event", () => {
    const { result } = setup();
    act(() => {
      result.current.handleRemoteEvent(
        typingEvent({ user_id: CURRENT_USER, user_display_name: "Me" }),
      );
    });
    expect(result.current.typingUserIds).toEqual([]);
    expect(result.current.typingDisplayNameByUserId.size).toBe(0);
  });
});

// ── outbound: notifyActivity / stop / timers ────────────────────────────────

describe("useTypingIndicator — outbound activity and timers", () => {
  it("sends typing.start on first activity and does not renew within the throttle window", () => {
    vi.useFakeTimers();
    const { result, sendTyping } = setup();
    act(() => result.current.notifyActivity());
    expect(sendTyping).toHaveBeenCalledTimes(1);
    expect(sendTyping).toHaveBeenLastCalledWith(true);

    act(() => result.current.notifyActivity()); // still inside ACTIVITY_RENEW_THROTTLE_MS (2500ms)
    expect(sendTyping).toHaveBeenCalledTimes(1);
  });

  it("renews typing.start once the renewal throttle window has passed", () => {
    vi.useFakeTimers();
    const { result, sendTyping } = setup();
    act(() => result.current.notifyActivity());
    act(() => vi.advanceTimersByTime(2_500)); // ACTIVITY_RENEW_THROTTLE_MS
    act(() => result.current.notifyActivity());
    expect(sendTyping).toHaveBeenCalledTimes(2);
    expect(sendTyping).toHaveBeenNthCalledWith(2, true);
  });

  it("sends typing.stop by itself after the inactivity window elapses without renewed activity", () => {
    vi.useFakeTimers();
    const { result, sendTyping } = setup();
    act(() => result.current.notifyActivity());
    act(() => vi.advanceTimersByTime(4_000)); // STOP_AFTER_INACTIVITY_MS
    expect(sendTyping).toHaveBeenCalledTimes(2);
    expect(sendTyping).toHaveBeenNthCalledWith(1, true);
    expect(sendTyping).toHaveBeenNthCalledWith(2, false);
  });

  it("stop() is a no-op when notifyActivity was never called", () => {
    const { result, sendTyping } = setup();
    act(() => result.current.stop());
    expect(sendTyping).not.toHaveBeenCalled();
  });

  it("stop() sends typing.stop immediately and cancels the pending inactivity timer", () => {
    vi.useFakeTimers();
    const { result, sendTyping } = setup();
    act(() => result.current.notifyActivity());
    act(() => result.current.stop());
    expect(sendTyping).toHaveBeenCalledTimes(2);
    expect(sendTyping).toHaveBeenNthCalledWith(2, false);

    sendTyping.mockClear();
    act(() => vi.advanceTimersByTime(4_000)); // would have fired STOP_AFTER_INACTIVITY_MS
    expect(sendTyping).not.toHaveBeenCalled();
  });

  it("does not start a typing session while disabled", () => {
    const { result, sendTyping } = setup({ disabled: true });
    act(() => result.current.notifyActivity());
    expect(sendTyping).not.toHaveBeenCalled();
  });

  it("stops asserting typing on the previous target and clears remote state when the target changes", () => {
    vi.useFakeTimers();
    const sendTyping = vi.fn(() => true);
    const { result, rerender } = renderHook(
      ({ targetId }: { targetId: string }) =>
        useTypingIndicator({ kind: "channel", targetId, currentUserId: CURRENT_USER, sendTyping }),
      { initialProps: { targetId: "chan-1" } },
    );
    act(() => result.current.notifyActivity());
    act(() => result.current.handleRemoteEvent(typingEvent({ user_display_name: "Diana Reis" })));
    expect(result.current.typingUserIds).toEqual([OTHER_USER]);

    sendTyping.mockClear();
    act(() => rerender({ targetId: "chan-2" }));

    expect(sendTyping).toHaveBeenCalledWith(false);
    expect(result.current.typingUserIds).toEqual([]);
    expect(result.current.typingDisplayNameByUserId.size).toBe(0);
  });

  it("stops asserting typing on unmount", () => {
    const { result, sendTyping, unmount } = setup();
    act(() => result.current.notifyActivity());
    sendTyping.mockClear();
    unmount();
    expect(sendTyping).toHaveBeenCalledWith(false);
  });
});

// ── inbound: remote-typing local defensive expiry ───────────────────────────

describe("useTypingIndicator — remote typing expiry", () => {
  it("expires a remote typing entry after the local defensive window if never renewed", () => {
    vi.useFakeTimers();
    const { result } = setup();
    act(() => result.current.handleRemoteEvent(typingEvent({ user_display_name: "Diana Reis" })));
    expect(result.current.typingUserIds).toEqual([OTHER_USER]);

    act(() => vi.advanceTimersByTime(8_000)); // REMOTE_EXPIRY_MS
    expect(result.current.typingUserIds).toEqual([]);
    expect(result.current.typingDisplayNameByUserId.has(OTHER_USER)).toBe(false);
  });

  it("does not expire a remote entry that was renewed before the defensive window elapsed", () => {
    vi.useFakeTimers();
    const { result } = setup();
    act(() => result.current.handleRemoteEvent(typingEvent({ user_display_name: "Diana Reis" })));
    act(() => vi.advanceTimersByTime(5_000));
    act(() => result.current.handleRemoteEvent(typingEvent({ user_display_name: "Diana Reis" }))); // renews the 8s window from here
    act(() => vi.advanceTimersByTime(5_000)); // 10s since the first event, only 5s since the renewal
    expect(result.current.typingUserIds).toEqual([OTHER_USER]);
  });
});
