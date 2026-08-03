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

import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import { _resetChatSocket, RECONNECT_BASE_DELAY_MS } from "./chatSocket";
import { issueCallToken } from "./callApi";
import type { CallMediaBridge } from "./useCallSignaling";
import { useCallSignaling } from "./useCallSignaling";

vi.mock("./callApi", () => ({
  issueCallToken: vi.fn(),
}));

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

function endedEvent(version: number) {
  return {
    type: "call.ended",
    event_id: `00000000-0000-4000-8000-00000000080${version}`,
    target_type: "user",
    target_id: baseCall.callee_id,
    call: { ...baseCall, status: "ended", version },
  };
}

function mediaBridge(): CallMediaBridge {
  return {
    startAudio: vi.fn(async () => undefined),
    connect: vi.fn(async () => undefined),
    stop: vi.fn(async () => undefined),
  };
}

// ── Setup / teardown ──────────────────────────────────────────────────────────

const OriginalWebSocket = global.WebSocket;

// The connection is shared module state; random() === 0 pins each backoff delay
// to the bottom of its equal-jitter window so the schedule is exact.
const FIRST_RETRY_MS = RECONNECT_BASE_DELAY_MS / 2;

beforeEach(() => {
  FakeWebSocket.instances = [];
  _resetChatSocket(() => 0);
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
  _resetChatSocket();
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
      vi.advanceTimersByTime(FIRST_RETRY_MS);
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

  it("connects the media bridge with the active call token", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());

    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));

    await waitFor(() =>
      expect(media.connect).toHaveBeenCalledExactlyOnceWith(
        expect.objectContaining({ ...baseCall, status: "active" }),
        "media-token",
      ),
    );
    expect(result.current.mediaReady).toBe(true);
  });

  it("does not duplicate an in-flight media connection for repeated active events", async () => {
    let finishConnect: (() => void) | undefined;
    const media = mediaBridge();
    vi.mocked(media.connect).mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          finishConnect = resolve;
        }),
    );
    renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());

    act(() => {
      FakeWebSocket.instances[0].simulateMessage(activeEvent(2));
      FakeWebSocket.instances[0].simulateMessage(activeEvent(3));
    });

    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());
    expect(issueCallToken).toHaveBeenCalledOnce();

    finishConnect?.();
  });

  it("unlocks audio from start and accept user actions", () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());

    act(() => {
      result.current.start(baseCall.callee_id, "audio");
    });
    expect(media.startAudio).toHaveBeenCalledOnce();

    act(() => FakeWebSocket.instances[0].simulateMessage(ringingEvent(1)));
    act(() => {
      result.current.accept();
    });
    expect(media.startAudio).toHaveBeenCalledTimes(2);
  });

  it("stops capture immediately on end and on authoritative terminal events", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());

    act(() => {
      result.current.end();
    });
    expect(media.stop).toHaveBeenCalledOnce();

    act(() => FakeWebSocket.instances[0].simulateMessage(endedEvent(3)));
    expect(media.stop).toHaveBeenCalledTimes(2);
  });

  it("ignores media side effects from a stale terminal event", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(3)));
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());

    act(() => FakeWebSocket.instances[0].simulateMessage(endedEvent(2)));

    expect(result.current.call?.status).toBe("active");
    expect(media.stop).not.toHaveBeenCalled();
  });

  it("can retry with a fresh token after media connection fails", async () => {
    const media = mediaBridge();
    vi.mocked(media.connect).mockRejectedValueOnce(new Error("unavailable"));
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());

    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));
    await waitFor(() =>
      expect(result.current.error).toBe(
        "Não foi possível preparar a mídia da chamada. Tente novamente.",
      ),
    );

    act(() => result.current.retryMedia());
    await waitFor(() => expect(issueCallToken).toHaveBeenCalledTimes(2));
    expect(media.connect).toHaveBeenCalledTimes(2);
  });

  it("restarts disconnected media with a fresh token", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));
    await waitFor(() => expect(result.current.mediaReady).toBe(true));

    act(() => result.current.retryMedia());

    await waitFor(() => expect(media.connect).toHaveBeenCalledTimes(2));
    expect(media.stop).toHaveBeenCalledOnce();
    expect(issueCallToken).toHaveBeenCalledTimes(2);
  });

  it("does not surface a late media failure after the call has ended", async () => {
    let rejectToken!: (reason: unknown) => void;
    vi.mocked(issueCallToken).mockImplementationOnce(
      () =>
        new Promise((_, reject) => {
          rejectToken = reject;
        }),
    );
    const { result } = renderHook(() => useCallSignaling(mediaBridge()));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));
    await waitFor(() => expect(issueCallToken).toHaveBeenCalledOnce());

    act(() => FakeWebSocket.instances[0].simulateMessage(endedEvent(3)));
    await act(async () => rejectToken(new Error("late failure")));

    expect(result.current.call?.status).toBe("ended");
    expect(result.current.error).toBeNull();
  });

  it("does not create duplicate sockets or listeners for repeated close events before retry fires", () => {
    vi.useFakeTimers();
    renderHook(() => useCallSignaling());

    const ws = FakeWebSocket.instances[0];
    act(() => {
      ws.close();
      ws.onclose?.(new CloseEvent("close", { code: 1006 }));
      ws.onclose?.(new CloseEvent("close", { code: 1006 }));
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
