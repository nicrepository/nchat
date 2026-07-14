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
import {
  useChatWebSocket,
  type WSMessageCreatedEvent,
  type WSMessageUpdatedEvent,
} from "./useChatWebSocket";

// ── Mock WebSocket ────────────────────────────────────────────────────────────

type WSListener = (event: MessageEvent) => void;
type OpenListener = () => void;
type CloseListener = () => void;

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
    this.onclose?.();
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

// ── Setup / teardown ──────────────────────────────────────────────────────────

const OriginalWebSocket = global.WebSocket;

beforeEach(() => {
  FakeWebSocket.instances = [];
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  global.WebSocket = FakeWebSocket as any;
  setTokens("test-token");
});

afterEach(() => {
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
        code: "rate_limited",
        retry_after: 60,
      }),
    );

    expect(onReactionError).toHaveBeenCalledWith(
      expect.objectContaining({ code: "rate_limited", retry_after: 60 }),
    );
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
      vi.advanceTimersByTime(250);
    });

    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(FakeWebSocket.instances[1].protocols).toEqual(["test-token", "nchat.v1"]);
    expect(FakeWebSocket.instances[0].url).not.toContain("test-token");
    expect(FakeWebSocket.instances[1].url).not.toContain("test-token");
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
      ws.onclose?.();
      ws.onclose?.();
      vi.advanceTimersByTime(250);
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
      vi.advanceTimersByTime(300);
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
});
