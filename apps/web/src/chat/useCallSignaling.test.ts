/**
 * useCallSignaling tests.
 *
 * Verifies: pending-guard dedup, version-based event ordering, stale-connection
 * delivery safety, active-only media requests, and reconnect/cleanup hygiene.
 *
 * WebSocket is mocked with the same controllable fake used by
 * useChatWebSocket.test.ts so tests are deterministic and do not require a
 * real server.
 */

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import { issueCallToken } from "./callApi";
import { useCallSignaling } from "./useCallSignaling";

vi.mock("./callApi", () => ({
  issueCallToken: vi.fn(),
}));

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

function callCommands(socket: FakeWebSocket): Record<string, unknown>[] {
  return socket.sentMessages
    .map((message) => JSON.parse(message) as Record<string, unknown>)
    .filter((message) => message.type !== "call.sync");
}

const baseCall = {
  call_id: "00000000-0000-4000-8000-000000000501",
  request_id: "00000000-0000-4000-8000-000000000502",
  caller_id: "00000000-0000-4000-8000-000000000503",
  callee_id: "00000000-0000-4000-8000-000000000504",
  call_type: "video" as const,
  created_at: "2026-07-30T12:00:00Z",
  occurred_at: "2026-07-30T12:00:00Z",
  expires_at: "2026-07-30T12:00:30Z",
};

function ringingEvent(version: number) {
  return {
    type: "call.ringing",
    event_id: `00000000-0000-4000-8000-00000000060${version}`,
    target_type: "user",
    target_id: baseCall.callee_id,
    call: { ...baseCall, status: "ringing", version },
  };
}

function activeEvent(version: number) {
  return {
    type: "call.accepted",
    event_id: `00000000-0000-4000-8000-00000000070${version}`,
    target_type: "user",
    target_id: baseCall.callee_id,
    call: { ...baseCall, status: "active", version },
  };
}

// ── Setup / teardown ──────────────────────────────────────────────────────────

const OriginalWebSocket = global.WebSocket;

beforeEach(() => {
  FakeWebSocket.instances = [];
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  global.WebSocket = FakeWebSocket as any;
  setTokens("test-token");
  vi.mocked(issueCallToken).mockReset();
  vi.mocked(issueCallToken).mockResolvedValue({
    token: "media-token",
    expiresAt: "2026-07-30T12:05:00Z",
  });
});

afterEach(() => {
  vi.useRealTimers();
  global.WebSocket = OriginalWebSocket;
  clearTokens();
  vi.clearAllMocks();
});

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("useCallSignaling", () => {
  it("does not double-send two commands fired before the pending state re-renders", () => {
    const { result } = renderHook(() => useCallSignaling());
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(ringingEvent(1)));

    act(() => {
      result.current.accept();
      result.current.accept();
    });

    expect(callCommands(FakeWebSocket.instances[0])).toHaveLength(1);
  });

  it("ignores an event carrying an older version than the current call state", () => {
    const { result } = renderHook(() => useCallSignaling());
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));
    expect(result.current.call?.status).toBe("active");

    act(() => FakeWebSocket.instances[0].simulateMessage(ringingEvent(1)));

    expect(result.current.call?.status).toBe("active");
    expect(result.current.call?.version).toBe(2);
  });

  it("does not let a delayed event from a stale connection overwrite newer state", () => {
    vi.useFakeTimers();
    const { result } = renderHook(() => useCallSignaling());
    const staleSocket = FakeWebSocket.instances[0];
    act(() => staleSocket.simulateOpen());
    const delayedHandler = staleSocket.onmessage;

    act(() => {
      staleSocket.close();
      vi.advanceTimersByTime(250);
    });
    const currentSocket = FakeWebSocket.instances[1];
    act(() => currentSocket.simulateOpen());
    act(() => currentSocket.simulateMessage(activeEvent(2)));
    expect(result.current.call?.version).toBe(2);

    act(() => {
      delayedHandler?.(new MessageEvent("message", { data: JSON.stringify(ringingEvent(1)) }));
    });

    expect(result.current.call?.status).toBe("active");
    expect(result.current.call?.version).toBe(2);
  });

  it("requests media only once the call becomes active", () => {
    renderHook(() => useCallSignaling());
    act(() => FakeWebSocket.instances[0].simulateOpen());

    act(() => FakeWebSocket.instances[0].simulateMessage(ringingEvent(1)));
    expect(issueCallToken).not.toHaveBeenCalled();

    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));
    expect(issueCallToken).toHaveBeenCalledExactlyOnceWith(baseCall.call_id);
  });

  it("does not create duplicate sockets or listeners for repeated close events before retry fires", () => {
    vi.useFakeTimers();
    renderHook(() => useCallSignaling());

    const ws = FakeWebSocket.instances[0];
    act(() => {
      ws.close();
      ws.onclose?.();
      ws.onclose?.();
      vi.advanceTimersByTime(2_000);
    });

    expect(FakeWebSocket.instances).toHaveLength(2);
  });

  it("does not reconnect after the hook unmounts", () => {
    vi.useFakeTimers();
    const { unmount } = renderHook(() => useCallSignaling());
    expect(FakeWebSocket.instances).toHaveLength(1);

    act(() => FakeWebSocket.instances[0].close());
    unmount();
    act(() => vi.advanceTimersByTime(5_000));

    expect(FakeWebSocket.instances).toHaveLength(1);
  });
});
