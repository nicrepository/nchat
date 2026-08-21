/**
 * useChatWebSocket tests.
 *
 * Verifies: event routing, dedup guard, cleanup on unmount and target change,
 * auth via Sec-WebSocket-Protocol, and filtering of events from other targets.
 *
 * WebSocket is mocked with a controllable fake so tests are deterministic and
 * do not require a real server.
 */

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import { _resetChatSocket, RECONNECT_BASE_DELAY_MS } from "./chatSocket";
import {
  useChatWebSocket,
  type WSMessageCreatedEvent,
  type WSMessageUpdatedEvent,
  type WSSubscriptionTarget,
} from "./useChatWebSocket";

// ── Mock WebSocket ────────────────────────────────────────────────────────────

type WSListener = (event: MessageEvent) => void;
type OpenListener = () => void;
type CloseListener = (event: CloseEvent) => void;

class FakeWebSocket {
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;

  readyState = FakeWebSocket.OPEN;
  sentMessages: string[] = [];
  url: string;
  protocols: string | string[];

  onopen: OpenListener | null = null;
  onmessage: WSListener | null = null;
  onerror: (() => void) | null = null;
  onclose: CloseListener | null = null;

  static instances: FakeWebSocket[] = [];

  constructor(url: string, protocols?: string | string[]) {
    this.url = url;
    this.protocols = protocols ?? [];
    FakeWebSocket.instances.push(this);
  }

  send(data: string) {
    this.sentMessages.push(data);
  }

  close() {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.(new CloseEvent("close", { code: 1006 }));
  }

  /** Test helper: simulate server pushing a message. */
  simulateMessage(data: object) {
    const event = new MessageEvent("message", { data: JSON.stringify(data) });
    this.onmessage?.(event);
  }

  /** Test helper: open the connection. */
  simulateOpen() {
    this.onopen?.();
  }
}

function subscriptionCount(socket: FakeWebSocket): number {
  return socket.sentMessages.filter((message) => {
    const parsed = JSON.parse(message) as { type?: string };
    return parsed.type === "subscribe";
  }).length;
}

function subscriptionCountFor(
  socket: FakeWebSocket,
  targetType: "channel" | "dm",
  targetId: string,
): number {
  return socket.sentMessages.filter((message) => {
    const parsed = JSON.parse(message) as {
      type?: string;
      target_type?: string;
      target_id?: string;
    };
    return (
      parsed.type === "subscribe" &&
      parsed.target_type === targetType &&
      parsed.target_id === targetId
    );
  }).length;
}

function subscribed(targetType: "channel" | "dm" = "channel", targetId = "ch-1") {
  return {
    type: "subscribed",
    operation: "subscribe",
    target_type: targetType,
    target_id: targetId,
  } as const;
}

// ── Setup / teardown ──────────────────────────────────────────────────────────

const OriginalWebSocket = global.WebSocket;

// The shared connection is module state, so every test starts from a closed
// socket with a jitter source that makes the backoff schedule exact:
// random() === 0 puts each delay at the bottom of its equal-jitter window.
const FIRST_RETRY_MS = RECONNECT_BASE_DELAY_MS / 2;

beforeEach(() => {
  FakeWebSocket.instances = [];
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  global.WebSocket = FakeWebSocket as any;
  setTokens("test-token");
  _resetChatSocket(() => 0);
});

afterEach(() => {
  _resetChatSocket();
  vi.useRealTimers();
  global.WebSocket = OriginalWebSocket;
  clearTokens();
  vi.clearAllMocks();
});

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("useChatWebSocket", () => {
  it("connects with the access token and a safe negotiated subprotocol", () => {
    renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated: vi.fn() }),
    );

    expect(FakeWebSocket.instances).toHaveLength(1);
    const ws = FakeWebSocket.instances[0];
    expect(ws.protocols).toEqual(["test-token", "nchat.v1"]);
  });

  it("sends subscribe on open", () => {
    renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated: vi.fn() }),
    );

    act(() => {
      FakeWebSocket.instances[0].simulateOpen();
    });

    const ws = FakeWebSocket.instances[0];
    expect(ws.sentMessages).toHaveLength(1);
    expect(JSON.parse(ws.sentMessages[0])).toEqual({
      type: "subscribe",
      target_type: "channel",
      target_id: "ch-1",
    });
  });

  it("sends a strict reaction.toggle command on the active socket", () => {
    const { result } = renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated: vi.fn() }),
    );
    let sent = false;
    act(() => {
      sent = result.current.toggleReaction("msg-1", "👍");
    });
    expect(sent).toBe(true);
    expect(JSON.parse(FakeWebSocket.instances[0].sentMessages.at(-1)!)).toEqual({
      type: "reaction.toggle",
      message_id: "msg-1",
      emoji: "👍",
    });
  });

  it("reports when a reaction cannot be sent on a closed socket", () => {
    const { result } = renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated: vi.fn() }),
    );
    FakeWebSocket.instances[0].readyState = FakeWebSocket.CLOSED;

    expect(result.current.toggleReaction("msg-1", "👍")).toBe(false);
  });

  it("sends a strict typing.start/typing.stop command on the active socket", () => {
    const { result } = renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated: vi.fn() }),
    );
    let sent = false;
    act(() => {
      sent = result.current.sendTyping(true);
    });
    expect(sent).toBe(true);
    expect(JSON.parse(FakeWebSocket.instances[0].sentMessages.at(-1)!)).toEqual({
      type: "typing.start",
      target_type: "channel",
      target_id: "ch-1",
    });

    act(() => {
      sent = result.current.sendTyping(false);
    });
    expect(sent).toBe(true);
    expect(JSON.parse(FakeWebSocket.instances[0].sentMessages.at(-1)!)).toEqual({
      type: "typing.stop",
      target_type: "channel",
      target_id: "ch-1",
    });
  });

  it("reports when typing cannot be sent on a closed socket", () => {
    const { result } = renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated: vi.fn() }),
    );
    FakeWebSocket.instances[0].readyState = FakeWebSocket.CLOSED;

    expect(result.current.sendTyping(true)).toBe(false);
  });

  it("routes structured reaction errors before target filtering", () => {
    const onReactionError = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onReactionError,
      }),
    );

    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        type: "error",
        operation: "reaction",
        code: "rate_limited",
        retry_after: 60,
      }),
    );

    expect(onReactionError).toHaveBeenCalledWith(
      expect.objectContaining({ code: "rate_limited", retry_after: 60 }),
    );
  });

  it("routes subscribe access denial without treating it as a reaction error or retrying", () => {
    vi.useFakeTimers();
    const onReactionError = vi.fn();
    const onSubscriptionError = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onReactionError,
        onSubscriptionError,
      }),
    );

    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        type: "error",
        operation: "subscribe",
        code: "room_access_denied",
      }),
    );
    act(() => vi.advanceTimersByTime(30_000));

    expect(onSubscriptionError).toHaveBeenCalledWith(
      expect.objectContaining({ operation: "subscribe", code: "room_access_denied" }),
    );
    expect(onReactionError).not.toHaveBeenCalled();
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(FakeWebSocket.instances[0].readyState).toBe(FakeWebSocket.OPEN);
  });

  it("recovers a temporary subscribe failure on the current open socket", () => {
    vi.useFakeTimers();
    const onReactionError = vi.fn();
    const onSubscriptionError = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onReactionError,
        onSubscriptionError,
      }),
    );
    act(() => FakeWebSocket.instances[0].simulateOpen());

    const socket = FakeWebSocket.instances[0];
    expect(subscriptionCount(socket)).toBe(1);

    act(() =>
      socket.simulateMessage({
        type: "error",
        operation: "subscribe",
        code: "room_subscription_unavailable",
      }),
    );
    expect(FakeWebSocket.instances).toHaveLength(1);
    act(() => vi.advanceTimersByTime(249));
    expect(subscriptionCount(socket)).toBe(1);
    act(() => vi.advanceTimersByTime(1));
    expect(subscriptionCount(socket)).toBe(2);
    expect(onSubscriptionError).toHaveBeenCalledOnce();
    expect(onReactionError).not.toHaveBeenCalled();
  });

  it("limits consecutive subscribe recovery and does not schedule duplicate timers", () => {
    vi.useFakeTimers();
    renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated: vi.fn() }),
    );
    act(() => FakeWebSocket.instances[0].simulateOpen());

    const socket = FakeWebSocket.instances[0];
    const recoveryDelays = [250, 500, 1_000];
    for (const delay of recoveryDelays) {
      act(() => {
        socket.simulateMessage({
          type: "error",
          operation: "subscribe",
          code: "room_subscription_unavailable",
        });
        socket.simulateMessage({
          type: "error",
          operation: "subscribe",
          code: "room_subscription_unavailable",
        });
      });
      act(() => vi.advanceTimersByTime(delay));
    }
    expect(subscriptionCount(socket)).toBe(4);

    act(() => {
      socket.simulateMessage({
        type: "error",
        operation: "subscribe",
        code: "room_subscription_unavailable",
      });
      vi.advanceTimersByTime(30_000);
    });
    expect(subscriptionCount(socket)).toBe(4);
    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("cancels temporary subscribe recovery on target change and unmount", () => {
    vi.useFakeTimers();
    const { rerender, unmount } = renderHook(
      ({ targetId }: { targetId: string }) =>
        useChatWebSocket({ kind: "channel", targetId, onMessageCreated: vi.fn() }),
      { initialProps: { targetId: "ch-1" } },
    );
    act(() => FakeWebSocket.instances[0].simulateOpen());

    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        type: "error",
        operation: "subscribe",
        code: "room_subscription_unavailable",
      }),
    );
    act(() => rerender({ targetId: "ch-2" }));
    expect(FakeWebSocket.instances).toHaveLength(2);
    act(() => FakeWebSocket.instances[1].simulateOpen());
    act(() => vi.advanceTimersByTime(30_000));
    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(subscriptionCount(FakeWebSocket.instances[0])).toBe(1);

    act(() =>
      FakeWebSocket.instances[1].simulateMessage({
        type: "error",
        operation: "subscribe",
        code: "room_subscription_unavailable",
      }),
    );
    unmount();
    act(() => vi.advanceTimersByTime(30_000));
    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(subscriptionCount(FakeWebSocket.instances[1])).toBe(1);
  });

  it("resets the subscribe recovery budget only after a matching acknowledgement", () => {
    vi.useFakeTimers();
    const onSubscribed = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onSubscribed,
      }),
    );
    act(() => FakeWebSocket.instances[0].simulateOpen());

    const socket = FakeWebSocket.instances[0];
    for (let cycle = 0; cycle < 4; cycle += 1) {
      act(() =>
        socket.simulateMessage({
          type: "error",
          operation: "subscribe",
          code: "room_subscription_unavailable",
        }),
      );
      act(() => vi.advanceTimersByTime(250));
      act(() => socket.simulateMessage(subscribed()));
    }

    expect(subscriptionCount(socket)).toBe(5);
    expect(onSubscribed).toHaveBeenCalledTimes(4);
  });

  it("cancels pending recovery on a matching acknowledgement without sending again", () => {
    vi.useFakeTimers();
    const onReactionError = vi.fn();
    const onSubscribed = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onReactionError,
        onSubscribed,
      }),
    );
    act(() => FakeWebSocket.instances[0].simulateOpen());

    const socket = FakeWebSocket.instances[0];
    act(() => {
      socket.simulateMessage({
        type: "error",
        operation: "subscribe",
        code: "room_subscription_unavailable",
      });
      socket.simulateMessage(subscribed());
      vi.advanceTimersByTime(30_000);
    });

    expect(subscriptionCount(socket)).toBe(1);
    expect(onSubscribed).toHaveBeenCalledOnce();
    expect(onReactionError).not.toHaveBeenCalled();
  });

  it("keeps recovery pending for A when B acknowledges first", () => {
    vi.useFakeTimers();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-a",
        additionalTargets: [{ kind: "dm", targetId: "dm-b" }],
        onMessageCreated: vi.fn(),
      }),
    );
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());

    act(() => {
      socket.simulateMessage({
        type: "error",
        operation: "subscribe",
        code: "room_subscription_unavailable",
      });
      socket.simulateMessage(subscribed("dm", "dm-b"));
      vi.advanceTimersByTime(250);
    });

    expect(subscriptionCountFor(socket, "channel", "ch-a")).toBe(2);
    expect(subscriptionCountFor(socket, "dm", "dm-b")).toBe(1);

    act(() => {
      socket.simulateMessage(subscribed("channel", "ch-a"));
      vi.advanceTimersByTime(30_000);
    });
    expect(subscriptionCount(socket)).toBe(3);
  });

  it("tracks acknowledgements out of order and completes only after A", () => {
    vi.useFakeTimers();
    const onSubscribed = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-a",
        additionalTargets: [{ kind: "dm", targetId: "dm-b" }],
        onMessageCreated: vi.fn(),
        onSubscribed,
      }),
    );
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());

    act(() => {
      socket.simulateMessage({
        type: "error",
        operation: "subscribe",
        code: "room_subscription_unavailable",
      });
      socket.simulateMessage(subscribed("dm", "dm-b"));
    });
    expect(onSubscribed).not.toHaveBeenCalled();

    act(() => {
      socket.simulateMessage(subscribed("channel", "ch-a"));
      vi.advanceTimersByTime(30_000);
    });
    expect(onSubscribed).toHaveBeenCalledOnce();
    expect(subscriptionCount(socket)).toBe(2);
  });

  it("does not complete the cycle when the primary target acknowledges before B", () => {
    vi.useFakeTimers();
    const onSubscribed = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-a",
        additionalTargets: [{ kind: "dm", targetId: "dm-b" }],
        onMessageCreated: vi.fn(),
        onSubscribed,
      }),
    );
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());

    act(() => socket.simulateMessage(subscribed("channel", "ch-a")));
    expect(onSubscribed).not.toHaveBeenCalled();

    act(() => {
      socket.simulateMessage({
        type: "error",
        operation: "subscribe",
        code: "room_subscription_unavailable",
      });
      vi.advanceTimersByTime(250);
    });
    expect(subscriptionCountFor(socket, "channel", "ch-a")).toBe(1);
    expect(subscriptionCountFor(socket, "dm", "dm-b")).toBe(2);

    act(() => {
      socket.simulateMessage(subscribed("dm", "dm-b"));
      vi.advanceTimersByTime(30_000);
    });
    expect(onSubscribed).toHaveBeenCalledOnce();
    expect(onSubscribed).toHaveBeenCalledWith(subscribed("channel", "ch-a"));
    expect(subscriptionCount(socket)).toBe(3);
  });

  it("ignores a delayed acknowledgement from an older connection generation", () => {
    vi.useFakeTimers();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-a",
        additionalTargets: [{ kind: "dm", targetId: "dm-b" }],
        onMessageCreated: vi.fn(),
      }),
    );
    const oldSocket = FakeWebSocket.instances[0];
    act(() => oldSocket.simulateOpen());
    const delayedHandler = oldSocket.onmessage;

    act(() => {
      oldSocket.close();
      vi.advanceTimersByTime(FIRST_RETRY_MS);
    });
    const currentSocket = FakeWebSocket.instances[1];
    act(() => currentSocket.simulateOpen());
    act(() => {
      currentSocket.simulateMessage({
        type: "error",
        operation: "subscribe",
        code: "room_subscription_unavailable",
      });
      delayedHandler?.(
        new MessageEvent("message", {
          data: JSON.stringify(subscribed("channel", "ch-a")),
        }),
      );
      currentSocket.simulateMessage(subscribed("dm", "dm-b"));
      vi.advanceTimersByTime(250);
    });

    expect(subscriptionCountFor(currentSocket, "channel", "ch-a")).toBe(2);
    expect(subscriptionCountFor(currentSocket, "dm", "dm-b")).toBe(1);
  });

  it("updates additional targets on the current socket without duplicates or orphan timers", () => {
    vi.useFakeTimers();
    const targetB: WSSubscriptionTarget = { kind: "dm", targetId: "dm-b" };
    const targetC: WSSubscriptionTarget = { kind: "channel", targetId: "ch-c" };
    const { rerender, unmount } = renderHook(
      ({ additionalTargets }: { additionalTargets: WSSubscriptionTarget[] }) =>
        useChatWebSocket({
          kind: "channel",
          targetId: "ch-a",
          additionalTargets,
          onMessageCreated: vi.fn(),
        }),
      { initialProps: { additionalTargets: [targetB] } },
    );
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    expect(subscriptionCount(socket)).toBe(2);
    act(() => {
      socket.simulateMessage(subscribed("channel", "ch-a"));
      socket.simulateMessage(subscribed("dm", "dm-b"));
    });

    act(() => rerender({ additionalTargets: [targetB, targetC] }));
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(subscriptionCountFor(socket, "channel", "ch-a")).toBe(1);
    expect(subscriptionCountFor(socket, "dm", "dm-b")).toBe(1);
    expect(subscriptionCountFor(socket, "channel", "ch-c")).toBe(1);

    act(() => {
      socket.simulateMessage({
        type: "error",
        operation: "subscribe",
        code: "room_subscription_unavailable",
      });
      rerender({ additionalTargets: [targetB] });
    });
    act(() => vi.advanceTimersByTime(30_000));
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(
      socket.sentMessages
        .map((message) => JSON.parse(message))
        .filter(
          (message) =>
            message.type === "unsubscribe" &&
            message.target_type === "channel" &&
            message.target_id === "ch-c",
        ),
    ).toHaveLength(1);
    expect(subscriptionCount(socket)).toBe(3);

    const commandCount = socket.sentMessages.length;
    act(() => rerender({ additionalTargets: [{ ...targetB }] }));
    expect(socket.sentMessages).toHaveLength(commandCount);

    unmount();
    act(() => vi.advanceTimersByTime(30_000));
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(socket.readyState).toBe(FakeWebSocket.CLOSED);
  });

  it.each([
    ["wrong operation", { ...subscribed(), operation: "unsubscribe" }],
    ["wrong target type", subscribed("dm", "ch-1")],
    ["wrong target id", subscribed("channel", "ch-2")],
  ])("ignores a subscribe acknowledgement with %s", (_label, acknowledgement) => {
    vi.useFakeTimers();
    const onSubscribed = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onSubscribed,
      }),
    );
    act(() => FakeWebSocket.instances[0].simulateOpen());

    const socket = FakeWebSocket.instances[0];
    act(() => {
      socket.simulateMessage({
        type: "error",
        operation: "subscribe",
        code: "room_subscription_unavailable",
      });
      socket.simulateMessage(acknowledgement);
      vi.advanceTimersByTime(250);
    });

    expect(subscriptionCount(socket)).toBe(2);
    expect(onSubscribed).not.toHaveBeenCalled();
  });

  it("ignores a delayed acknowledgement from the previous target", () => {
    vi.useFakeTimers();
    const onSubscribed = vi.fn();
    const { rerender } = renderHook(
      ({ targetId }: { targetId: string }) =>
        useChatWebSocket({
          kind: "channel",
          targetId,
          onMessageCreated: vi.fn(),
          onSubscribed,
        }),
      { initialProps: { targetId: "ch-1" } },
    );
    act(() => FakeWebSocket.instances[0].simulateOpen());
    const delayedHandler = FakeWebSocket.instances[0].onmessage;

    act(() => rerender({ targetId: "ch-2" }));
    act(() => FakeWebSocket.instances[1].simulateOpen());
    act(() => {
      FakeWebSocket.instances[1].simulateMessage({
        type: "error",
        operation: "subscribe",
        code: "room_subscription_unavailable",
      });
      delayedHandler?.(
        new MessageEvent("message", { data: JSON.stringify(subscribed("channel", "ch-1")) }),
      );
      vi.advanceTimersByTime(250);
    });

    expect(subscriptionCount(FakeWebSocket.instances[1])).toBe(2);
    expect(onSubscribed).not.toHaveBeenCalled();
  });

  it("keeps legacy error envelopes compatible with reaction error handling", () => {
    const onReactionError = vi.fn();
    const onSubscriptionError = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onReactionError,
        onSubscriptionError,
      }),
    );

    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        type: "error",
        code: "temporarily_unavailable",
      }),
    );

    expect(onReactionError).toHaveBeenCalledOnce();
    expect(onSubscriptionError).not.toHaveBeenCalled();
  });

  it("routes matching reaction.updated events", () => {
    const onReactionUpdated = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onReactionUpdated,
      }),
    );
    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "ch-1",
        message_id: "msg-1",
        reaction: {
          message_id: "msg-1",
          actor_user_id: "user-1",
          emoji: "👍",
          added: true,
          reactions: [{ emoji: "👍", count: 2 }],
        },
      }),
    );
    expect(onReactionUpdated).toHaveBeenCalledWith(
      expect.objectContaining({ message_id: "msg-1" }),
    );
  });

  it("routes matching typing.updated events", () => {
    const onTypingUpdated = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onTypingUpdated,
      }),
    );
    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        type: "typing.updated",
        target_type: "channel",
        target_id: "ch-1",
        typing: {
          user_id: "user-1",
          is_typing: true,
        },
      }),
    );
    expect(onTypingUpdated).toHaveBeenCalledWith(expect.objectContaining({ target_id: "ch-1" }));
  });

  // The decisive property: it arrives for a target this client never subscribed
  // to, which is the only situation it exists for.
  it("routes conversation.available without a subscription to its target", () => {
    const onConversationAvailable = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onConversationAvailable,
      }),
    );
    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        type: "conversation.available",
        target_type: "channel",
        target_id: "ch-never-subscribed",
      }),
    );
    expect(onConversationAvailable).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "conversation.available",
        target_id: "ch-never-subscribed",
      }),
    );
  });

  it("routes conversation.available for a dm target too", () => {
    const onConversationAvailable = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onConversationAvailable,
      }),
    );
    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        type: "conversation.available",
        target_type: "dm",
        target_id: "dm-new",
      }),
    );
    expect(onConversationAvailable).toHaveBeenCalledOnce();
  });

  // A malformed event must not be routed as if it named a conversation.
  it("ignores conversation.available without a usable target", () => {
    const onConversationAvailable = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onConversationAvailable,
      }),
    );
    act(() => FakeWebSocket.instances[0].simulateMessage({ type: "conversation.available" }));
    expect(onConversationAvailable).not.toHaveBeenCalled();
  });

  it("routes matching members.added events (issue #398)", () => {
    const onMembersAdded = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onMembersAdded,
      }),
    );
    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        type: "members.added",
        target_type: "channel",
        target_id: "ch-1",
        members: { actor_user_id: "user-1", added_count: 2, member_count: 9 },
      }),
    );
    expect(onMembersAdded).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "members.added",
        members: expect.objectContaining({ added_count: 2, member_count: 9 }),
      }),
    );
  });

  // The event carries counts only. If a server ever added identities, the
  // handler must not start depending on them: the refetch is the contract.
  it("delivers members.added even with no payload at all", () => {
    const onMembersAdded = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onMembersAdded,
      }),
    );
    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        type: "members.added",
        target_type: "channel",
        target_id: "ch-1",
      }),
    );
    expect(onMembersAdded).toHaveBeenCalledOnce();
  });

  it("ignores members.added for another target", () => {
    const onMembersAdded = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onMembersAdded,
      }),
    );
    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        type: "members.added",
        target_type: "channel",
        target_id: "ch-other",
        members: { actor_user_id: "user-1", added_count: 1, member_count: 4 },
      }),
    );
    expect(onMembersAdded).not.toHaveBeenCalled();
  });

  // ── attachment.status (RF-22) ────────────────────────────────────────────

  it("routes attachment.status for the active target", () => {
    const onAttachmentStatus = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onAttachmentStatus,
      }),
    );
    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        type: "attachment.status",
        target_type: "channel",
        target_id: "ch-1",
        attachment: {
          attachment_id: "att-1",
          status: "rejected",
          updated_at: "2026-08-07T12:00:00Z",
        },
      }),
    );
    expect(onAttachmentStatus).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "attachment.status",
        attachment: expect.objectContaining({ attachment_id: "att-1", status: "rejected" }),
      }),
    );
  });

  it("ignores attachment.status for a target this hook is not subscribed to", () => {
    const onAttachmentStatus = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onAttachmentStatus,
      }),
    );
    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        type: "attachment.status",
        target_type: "channel",
        target_id: "ch-other",
        attachment: { attachment_id: "att-1", status: "clean", updated_at: "2026-08-07T12:00:00Z" },
      }),
    );
    expect(onAttachmentStatus).not.toHaveBeenCalled();
  });

  // A verdict lands seconds or minutes after the upload, quite possibly while
  // the user is looking elsewhere. Unlike the mutating actions, it is therefore
  // routed for every subscribed target rather than only the primary one — the
  // panel that has to reconcile is whichever the attachment belongs to.
  it("routes attachment.status for an additional subscribed target", () => {
    const onAttachmentStatus = vi.fn();
    const additionalTargets: WSSubscriptionTarget[] = [{ kind: "dm", targetId: "dm-9" }];
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        additionalTargets,
        onMessageCreated: vi.fn(),
        onAttachmentStatus,
      }),
    );
    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        type: "attachment.status",
        target_type: "dm",
        target_id: "dm-9",
        attachment: { attachment_id: "att-2", status: "clean", updated_at: "2026-08-07T12:00:00Z" },
      }),
    );
    expect(onAttachmentStatus).toHaveBeenCalledOnce();
  });

  it("routes a versioned link-safety correction for a subscribed target", () => {
    const onLinkSafetyChanged = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onMessageLinkSafetyChanged: onLinkSafetyChanged,
      }),
    );
    const event = {
      type: "message.link_safety_changed",
      target_type: "channel",
      target_id: "ch-1",
      message_id: "msg-1",
      link_safety: {
        message_id: "msg-1",
        state: "malicious",
        updated_at: "2026-08-18T12:00:00Z",
      },
    };

    act(() => {
      FakeWebSocket.instances[0].simulateOpen();
      FakeWebSocket.instances[0].simulateMessage(event);
    });

    expect(onLinkSafetyChanged).toHaveBeenCalledWith(event);
  });

  it("rejects an unversioned or mismatched link-safety correction", () => {
    const onLinkSafetyChanged = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onMessageLinkSafetyChanged: onLinkSafetyChanged,
      }),
    );
    const event = {
      type: "message.link_safety_changed",
      target_type: "channel",
      target_id: "ch-1",
      message_id: "msg-1",
      link_safety: { message_id: "msg-1", state: "malicious" },
    };

    act(() => {
      FakeWebSocket.instances[0].simulateOpen();
      FakeWebSocket.instances[0].simulateMessage(event);
      FakeWebSocket.instances[0].simulateMessage({
        ...event,
        link_safety: {
          ...event.link_safety,
          message_id: "msg-2",
          updated_at: "2026-08-18T12:00:00Z",
        },
      });
    });

    expect(onLinkSafetyChanged).not.toHaveBeenCalled();
  });

  it("routes every supported message.updated body format only for the active target", () => {
    const onMessageUpdated = vi.fn<(event: WSMessageUpdatedEvent) => void>();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onMessageUpdated,
      }),
    );
    const update = {
      type: "message.updated",
      target_type: "channel",
      target_id: "ch-1",
      message_update: {
        message_id: "msg-1",
        channel_id: "ch-1",
        body: "editada",
        body_format: "v1",
        edited_at: "2026-07-13T12:00:00Z",
        edit_count: 2,
        is_edited: true,
        link_safety_state: "inconclusive",
      },
    };

    act(() => {
      FakeWebSocket.instances[0].simulateMessage(update);
      FakeWebSocket.instances[0].simulateMessage({
        ...update,
        message_update: { ...update.message_update, body_format: "v2" },
      });
      FakeWebSocket.instances[0].simulateMessage({
        ...update,
        message_update: { ...update.message_update, body_format: "v3" },
      });
      FakeWebSocket.instances[0].simulateMessage({ ...update, target_id: "ch-2" });
    });

    expect(onMessageUpdated).toHaveBeenCalledTimes(3);
    expect(onMessageUpdated).toHaveBeenCalledWith(update);
  });

  it("routes sanitized deletion and route-only distributed message.updated events", () => {
    const onMessageUpdated = vi.fn<(event: WSMessageUpdatedEvent) => void>();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onMessageUpdated,
      }),
    );
    const deletion: WSMessageUpdatedEvent = {
      type: "message.updated",
      target_type: "channel",
      target_id: "ch-1",
      message_update: {
        message_id: "msg-1",
        channel_id: "ch-1",
        body: "",
        body_format: "v1",
        edited_at: "",
        edit_count: 0,
        is_edited: false,
        status: "deleted",
        is_removed: true,
        deleted_at: "2026-07-14T12:00:00Z",
        updated_at: "2026-07-14T12:00:00Z",
      },
    };
    const routeOnly: WSMessageUpdatedEvent = {
      type: "message.updated",
      target_type: "channel",
      target_id: "ch-1",
      message_id: "msg-1",
    };

    act(() => {
      FakeWebSocket.instances[0].simulateMessage(deletion);
      FakeWebSocket.instances[0].simulateMessage(routeOnly);
    });

    expect(onMessageUpdated).toHaveBeenNthCalledWith(1, deletion);
    expect(onMessageUpdated).toHaveBeenNthCalledWith(2, routeOnly);
  });

  it("ignores malformed message.updated payloads", () => {
    const onMessageUpdated = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onMessageUpdated,
      }),
    );

    const base = {
      message_id: "msg-1",
      body: "editada",
      body_format: "v3",
      edited_at: "2026-07-13T12:00:00Z",
      edit_count: 2,
      is_edited: true,
    };
    const malformedUpdates: unknown[] = [
      undefined,
      "not-an-object",
      { ...base, message_id: 1 },
      { ...base, body: null },
      { ...base, body_format: "v4" },
      { ...base, edited_at: 1 },
      { ...base, edit_count: "2" },
      { ...base, is_edited: "true" },
      { ...base, status: "removed" },
      { ...base, is_removed: "true" },
      { ...base, deleted_at: 1 },
      { ...base, updated_at: 1 },
    ];

    act(() => {
      for (const message_update of malformedUpdates) {
        FakeWebSocket.instances[0].simulateMessage({
          type: "message.updated",
          target_type: "channel",
          target_id: "ch-1",
          message_update,
        });
      }
    });

    expect(onMessageUpdated).not.toHaveBeenCalled();
  });

  it("ignores a valid message.updated when no update callback is registered", () => {
    const onMessageCreated = vi.fn();
    renderHook(() => useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated }));

    expect(() =>
      act(() =>
        FakeWebSocket.instances[0].simulateMessage({
          type: "message.updated",
          target_type: "channel",
          target_id: "ch-1",
          message_update: {
            message_id: "msg-1",
            channel_id: "ch-1",
            body: "editada",
            body_format: "v3",
            edited_at: "2026-07-13T12:00:00Z",
            edit_count: 2,
            is_edited: true,
          },
        }),
      ),
    ).not.toThrow();
    expect(onMessageCreated).not.toHaveBeenCalled();
  });

  it("calls onMessageCreated for matching message.created event", () => {
    const onMessageCreated = vi.fn();
    renderHook(() => useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated }));

    const event: WSMessageCreatedEvent = {
      type: "message.created",
      workspace_id: "ws-1",
      target_type: "channel",
      target_id: "ch-1",
      message_id: "msg-1",
      event_id: "evt-1",
      created_at: new Date().toISOString(),
    };

    act(() => {
      FakeWebSocket.instances[0].simulateMessage(event);
    });

    expect(onMessageCreated).toHaveBeenCalledOnce();
    expect(onMessageCreated).toHaveBeenCalledWith(expect.objectContaining({ message_id: "msg-1" }));
  });

  it("ignores message.created events from a different target", () => {
    const onMessageCreated = vi.fn();
    renderHook(() => useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated }));

    act(() => {
      FakeWebSocket.instances[0].simulateMessage({
        type: "message.created",
        workspace_id: "ws-1",
        target_type: "channel",
        target_id: "ch-OTHER", // different channel
        message_id: "msg-x",
        event_id: "evt-x",
        created_at: new Date().toISOString(),
      });
    });

    expect(onMessageCreated).not.toHaveBeenCalled();
  });

  it("ignores events with unknown type", () => {
    const onMessageCreated = vi.fn();
    renderHook(() => useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated }));

    act(() => {
      FakeWebSocket.instances[0].simulateMessage({
        type: "presence.updated",
        target_type: "channel",
        target_id: "ch-1",
      });
    });

    expect(onMessageCreated).not.toHaveBeenCalled();
  });

  it("sends unsubscribe and closes on unmount", () => {
    const { unmount } = renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated: vi.fn() }),
    );

    act(() => {
      FakeWebSocket.instances[0].simulateOpen();
    });

    unmount();

    const ws = FakeWebSocket.instances[0];
    // Should have sent subscribe + unsubscribe
    expect(ws.sentMessages.length).toBeGreaterThanOrEqual(1);
    const unsubMsg = ws.sentMessages.find((m) => {
      const p = JSON.parse(m) as { type: string };
      return p.type === "unsubscribe";
    });
    expect(unsubMsg).toBeDefined();
    expect(ws.readyState).toBe(FakeWebSocket.CLOSED);
  });

  it("opens a new connection and subscribes when targetId changes", () => {
    const { rerender } = renderHook(
      ({ targetId }: { targetId: string }) =>
        useChatWebSocket({ kind: "channel", targetId, onMessageCreated: vi.fn() }),
      { initialProps: { targetId: "ch-1" } },
    );

    expect(FakeWebSocket.instances).toHaveLength(1);

    act(() => {
      rerender({ targetId: "ch-2" });
    });

    // Old connection closed, new one opened
    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(FakeWebSocket.instances[0].readyState).toBe(FakeWebSocket.CLOSED);
  });

  it("reconnects with bounded backoff after unexpected close", () => {
    vi.useFakeTimers();
    renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated: vi.fn() }),
    );

    expect(FakeWebSocket.instances).toHaveLength(1);

    act(() => {
      FakeWebSocket.instances[0].close();
    });
    expect(FakeWebSocket.instances).toHaveLength(1);

    act(() => {
      vi.advanceTimersByTime(FIRST_RETRY_MS - 1);
    });
    expect(FakeWebSocket.instances).toHaveLength(1);

    act(() => {
      vi.advanceTimersByTime(1);
    });

    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(FakeWebSocket.instances[1].protocols).toEqual(["test-token", "nchat.v1"]);
    expect(FakeWebSocket.instances[0].url).not.toContain("test-token");
    expect(FakeWebSocket.instances[1].url).not.toContain("test-token");
  });

  it("keeps backing off when a socket opens and drops before it is stable", () => {
    vi.useFakeTimers();
    renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated: vi.fn() }),
    );

    act(() => FakeWebSocket.instances[0].close());
    act(() => vi.advanceTimersByTime(FIRST_RETRY_MS));
    const recoveredSocket = FakeWebSocket.instances[1];

    // Opens, then drops well before STABLE_CONNECTION_MS: the attempt counter
    // must not be forgiven, so the next wait is the doubled one.
    act(() => recoveredSocket.simulateOpen());
    act(() => recoveredSocket.close());

    act(() => vi.advanceTimersByTime(2 * FIRST_RETRY_MS - 1));
    expect(FakeWebSocket.instances).toHaveLength(2);
    act(() => vi.advanceTimersByTime(1));
    expect(FakeWebSocket.instances).toHaveLength(3);
  });

  it("retries subscribe recovery on its own budget, independently of network backoff", () => {
    vi.useFakeTimers();
    renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated: vi.fn() }),
    );

    const socket = FakeWebSocket.instances[0];
    act(() => {
      socket.simulateOpen();
      socket.simulateMessage(subscribed());
    });
    for (const delay of [250, 500, 1_000]) {
      act(() =>
        socket.simulateMessage({
          type: "error",
          operation: "subscribe",
          code: "room_subscription_unavailable",
        }),
      );
      act(() => vi.advanceTimersByTime(delay));
    }
    expect(subscriptionCount(socket)).toBe(4);
  });

  it("does not reconnect after cleanup", () => {
    vi.useFakeTimers();
    const { unmount } = renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated: vi.fn() }),
    );

    expect(FakeWebSocket.instances).toHaveLength(1);

    unmount();

    act(() => {
      vi.advanceTimersByTime(5_000);
    });

    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("does not create duplicate sockets for repeated close events before retry fires", () => {
    vi.useFakeTimers();
    renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated: vi.fn() }),
    );

    const ws = FakeWebSocket.instances[0];
    act(() => {
      ws.close();
      ws.onclose?.(new CloseEvent("close", { code: 1006 }));
      ws.onclose?.(new CloseEvent("close", { code: 1006 }));
      vi.advanceTimersByTime(FIRST_RETRY_MS);
    });

    expect(FakeWebSocket.instances).toHaveLength(2);
  });

  it("does nothing when no access token is available", () => {
    clearTokens();
    renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated: vi.fn() }),
    );

    expect(FakeWebSocket.instances).toHaveLength(0);
  });

  it("does nothing when targetId is empty", () => {
    renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "", onMessageCreated: vi.fn() }),
    );

    expect(FakeWebSocket.instances).toHaveLength(0);
  });

  it("ignores malformed (non-JSON) server messages", () => {
    const onMessageCreated = vi.fn();
    renderHook(() => useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated }));

    act(() => {
      const ws = FakeWebSocket.instances[0];
      const event = new MessageEvent("message", { data: "not-json{{{{" });
      ws.onmessage?.(event);
    });

    expect(onMessageCreated).not.toHaveBeenCalled();
  });

  it("cancels a pending reconnect timer when the hook unmounts", () => {
    vi.useFakeTimers();
    const { unmount } = renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated: vi.fn() }),
    );

    // Close the socket to trigger reconnect scheduling (readyState becomes CLOSED).
    act(() => {
      FakeWebSocket.instances[0].close();
    });

    // Unmount before the timer fires — cleanup should cancel the pending timer.
    // Because readyState is now CLOSED (not OPEN), the cleanup skips the unsubscribe send.
    unmount();

    act(() => {
      vi.advanceTimersByTime(5_000);
    });

    // No new socket should have been created after unmount.
    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("schedules a reconnect when the WebSocket constructor throws", () => {
    vi.useFakeTimers();

    // Replace with a WebSocket that always throws on construction.
    global.WebSocket = class {
      constructor() {
        throw new Error("Connection refused");
      }
    } as unknown as typeof WebSocket;

    // The hook must not propagate the constructor exception.
    renderHook(() =>
      useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated: vi.fn() }),
    );

    // Constructor threw — no socket was created yet.
    expect(FakeWebSocket.instances).toHaveLength(0);

    // Restore the working FakeWebSocket so the pending reconnect can succeed.
    global.WebSocket = FakeWebSocket as unknown as typeof WebSocket;

    act(() => {
      vi.advanceTimersByTime(FIRST_RETRY_MS);
    });

    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("ignores valid JSON that is not an object (e.g. null or a number)", () => {
    const onMessageCreated = vi.fn();
    renderHook(() => useChatWebSocket({ kind: "channel", targetId: "ch-1", onMessageCreated }));

    act(() => {
      const ws = FakeWebSocket.instances[0];
      // JSON.parse("null") → null; the hook checks !data && returns.
      ws.onmessage?.(new MessageEvent("message", { data: "null" }));
      // JSON.parse("42") → 42; typeof 42 !== "object".
      ws.onmessage?.(new MessageEvent("message", { data: "42" }));
    });

    expect(onMessageCreated).not.toHaveBeenCalled();
  });

  it("uses one canonical UUID for subscribe, ACK, events, equivalent rerenders, and cleanup", () => {
    vi.useFakeTimers();
    const uppercase = "550E8400-E29B-41D4-A716-446655440000";
    const canonical = "550e8400-e29b-41d4-a716-446655440000";
    const onMessageCreated = vi.fn();
    const onSubscribed = vi.fn();
    const { rerender, unmount } = renderHook(
      ({ targetId }) =>
        useChatWebSocket({
          kind: "channel",
          targetId,
          onMessageCreated,
          onSubscribed,
        }),
      { initialProps: { targetId: uppercase } },
    );

    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    expect(JSON.parse(socket.sentMessages[0])).toEqual({
      type: "subscribe",
      target_type: "channel",
      target_id: canonical,
    });

    act(() => {
      socket.simulateMessage({
        type: "error",
        operation: "subscribe",
        code: "room_subscription_unavailable",
      });
      socket.simulateMessage(subscribed("channel", canonical));
      socket.simulateMessage({
        type: "message.created",
        target_type: "channel",
        target_id: canonical,
        message_id: "message-1",
        workspace_id: "workspace-1",
        event_id: "event-1",
        created_at: "2026-01-01T00:00:00Z",
      });
    });

    expect(onSubscribed).toHaveBeenCalledOnce();
    expect(onMessageCreated).toHaveBeenCalledOnce();
    act(() => vi.advanceTimersByTime(5_000));
    expect(subscriptionCount(socket)).toBe(1);

    act(() => rerender({ targetId: canonical }));
    expect(FakeWebSocket.instances).toHaveLength(1);

    unmount();
    const unsubscribe = socket.sentMessages
      .map((message) => JSON.parse(message) as Record<string, unknown>)
      .find((message) => message["type"] === "unsubscribe");
    expect(unsubscribe).toMatchObject({ target_type: "channel", target_id: canonical });
  });

  // ── RF-21 sender-scoped refusal ─────────────────────────────────────────────

  // message.blocked is addressed to a user rather than to a conversation, so it
  // carries no target to match against and is routed ahead of the subscription
  // guard. Without a message id there is nothing for the author's composer to
  // stop waiting on, so the event is dropped rather than forwarded half-formed.
  it.each([
    { name: "no message id at all", body: { reason: "malicious_link" } },
    { name: "a numeric message id", body: { message_id: 42, reason: "malicious_link" } },
    { name: "a null message id", body: { message_id: null, reason: "malicious_link" } },
    {
      name: "an object message id",
      body: { message_id: { id: "msg-1" }, reason: "malicious_link" },
    },
  ])("ignores a message.blocked event with $name", ({ body }) => {
    const onMessageBlocked = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onMessageBlocked,
      }),
    );

    act(() => {
      FakeWebSocket.instances[0].simulateOpen();
      FakeWebSocket.instances[0].simulateMessage({ type: "message.blocked", ...body });
    });

    expect(onMessageBlocked).not.toHaveBeenCalled();
  });

  it("routes message.blocked to its author before the subscription guard", () => {
    const onMessageBlocked = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onMessageBlocked,
      }),
    );
    const event = { type: "message.blocked", message_id: "msg-1", reason: "malicious_link" };

    act(() => {
      FakeWebSocket.instances[0].simulateOpen();
      // Deliberately no acknowledgement first: the author must be told even
      // though this client has confirmed no target, which is exactly why the
      // event is routed before the subscription guard.
      FakeWebSocket.instances[0].simulateMessage(event);
    });

    expect(onMessageBlocked).toHaveBeenCalledOnce();
    expect(onMessageBlocked).toHaveBeenCalledWith(event);
  });

  // A reaction is a mutating action on the conversation the user has open. A
  // subscribed-but-background target still receives corrections (link safety,
  // attachment status), but not this.
  it("keeps mutating events scoped to the primary target", () => {
    const onReactionUpdated = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-a",
        additionalTargets: [{ kind: "dm", targetId: "dm-b" }],
        onMessageCreated: vi.fn(),
        onReactionUpdated,
      }),
    );
    const socket = FakeWebSocket.instances[0];
    act(() => {
      socket.simulateOpen();
      socket.simulateMessage(subscribed("channel", "ch-a"));
      socket.simulateMessage(subscribed("dm", "dm-b"));
    });

    act(() =>
      socket.simulateMessage({
        type: "reaction.updated",
        target_type: "dm",
        target_id: "dm-b",
        message_id: "msg-1",
      }),
    );
    expect(onReactionUpdated).not.toHaveBeenCalled();

    act(() =>
      socket.simulateMessage({
        type: "reaction.updated",
        target_type: "channel",
        target_id: "ch-a",
        message_id: "msg-1",
      }),
    );
    expect(onReactionUpdated).toHaveBeenCalledOnce();
  });

  it("routes pin.updated for the primary target", () => {
    const onPinUpdated = vi.fn();
    renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-1",
        onMessageCreated: vi.fn(),
        onPinUpdated,
      }),
    );
    const socket = FakeWebSocket.instances[0];
    const event = {
      type: "pin.updated",
      target_type: "channel",
      target_id: "ch-1",
      message_id: "msg-1",
      pinned: true,
    };

    act(() => {
      socket.simulateOpen();
      socket.simulateMessage(subscribed("channel", "ch-1"));
      socket.simulateMessage(event);
    });

    expect(onPinUpdated).toHaveBeenCalledOnce();
    expect(onPinUpdated).toHaveBeenCalledWith(event);
  });

  // A second consumer joining a socket that already holds the subscriptions it
  // wants is acknowledged from what the connection knows, without re-asking the
  // server. Re-subscribing would be both a wasted round trip and a way for a
  // background panel to reset the recovery state of the conversation on screen.
  it("adopts subscriptions the shared socket already holds for a second consumer", async () => {
    const first = renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-a",
        additionalTargets: [{ kind: "dm", targetId: "dm-b" }],
        onMessageCreated: vi.fn(),
      }),
    );
    const socket = FakeWebSocket.instances[0];
    act(() => {
      socket.simulateOpen();
      socket.simulateMessage(subscribed("channel", "ch-a"));
      socket.simulateMessage(subscribed("dm", "dm-b"));
    });
    const framesBefore = subscriptionCount(socket);

    const onSubscribed = vi.fn();
    const second = renderHook(() =>
      useChatWebSocket({
        kind: "channel",
        targetId: "ch-a",
        additionalTargets: [{ kind: "dm", targetId: "dm-b" }],
        onMessageCreated: vi.fn(),
        onSubscribed,
      }),
    );
    // Joining an open socket defers the open callback by one microtask, so the
    // consumer has its handle before it is asked to subscribe.
    await act(async () => {
      await Promise.resolve();
    });

    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(subscriptionCount(socket)).toBe(framesBefore);
    // Both the join callback and the subscription-declaration effect resolve the
    // same already-live targets, so the consumer can be notified more than once.
    // What must hold is that every notification is the primary acknowledgement:
    // a consumer told it is subscribed to the wrong conversation would render
    // that conversation's history under the one the user actually opened.
    expect(onSubscribed).toHaveBeenCalled();
    for (const [acknowledgement] of onSubscribed.mock.calls) {
      expect(acknowledgement).toMatchObject({
        type: "subscribed",
        operation: "subscribe",
        target_type: "channel",
        target_id: "ch-a",
      });
    }

    second.unmount();
    first.unmount();
  });
});
