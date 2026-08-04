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

function sentMessages(socket: FakeWebSocket): Record<string, unknown>[] {
  return socket.sentMessages.map((message) => JSON.parse(message) as Record<string, unknown>);
}

function callCommands(socket: FakeWebSocket): Record<string, unknown>[] {
  return sentMessages(socket).filter((message) => message.type !== "call.sync");
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

function deferredValue<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
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
  it("treats call.sync call_not_found as an empty normal state", () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];

    act(() => socket.simulateOpen());
    expect(sentMessages(socket)).toContainEqual({ type: "call.sync" });
    act(() =>
      socket.simulateMessage({
        type: "call.error",
        operation: "call.sync",
        code: "call_not_found",
      }),
    );

    expect(result.current.call).toBeNull();
    expect(result.current.error).toBeNull();
    expect(result.current.pending).toBe(false);
    expect(media.stop).not.toHaveBeenCalled();
  });

  it("does not let an empty call.sync response release another pending action", () => {
    const { result } = renderHook(() => useCallSignaling());
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    act(() => {
      expect(result.current.start(baseCall.callee_id, "audio")).toBe(true);
    });

    act(() =>
      socket.simulateMessage({
        type: "call.error",
        operation: "call.sync",
        code: "call_not_found",
      }),
    );

    expect(result.current.pending).toBe(true);
    expect(result.current.error).toBeNull();
    act(() => {
      expect(result.current.start(baseCall.callee_id, "audio")).toBe(false);
    });
    expect(callCommands(socket)).toHaveLength(1);
  });

  it.each([
    ["call.accept", "call_not_found"],
    ["call.sync", "call_unavailable"],
  ])("keeps %s/%s as an actionable error", (operation, code) => {
    const { result } = renderHook(() => useCallSignaling());
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());

    act(() => socket.simulateMessage({ type: "call.error", operation, code }));

    expect(result.current.error).toBe("Não foi possível concluir a ação da chamada.");
  });

  it("does not alter an active call for an empty call.sync response", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    act(() => socket.simulateMessage(activeEvent(2)));
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());

    act(() =>
      socket.simulateMessage({
        type: "call.error",
        operation: "call.sync",
        code: "call_not_found",
      }),
    );

    expect(result.current.call?.status).toBe("active");
    expect(result.current.call?.version).toBe(2);
    expect(result.current.error).toBeNull();
    expect(media.stop).not.toHaveBeenCalled();
  });

  it("keeps the first accepted action media preparation when accept is duplicated", () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(ringingEvent(1)));

    let firstAccepted = false;
    let duplicateAccepted = true;
    act(() => {
      firstAccepted = result.current.accept();
      duplicateAccepted = result.current.accept();
    });

    expect(firstAccepted).toBe(true);
    expect(duplicateAccepted).toBe(false);
    expect(callCommands(FakeWebSocket.instances[0])).toHaveLength(1);
    expect(media.startAudio).toHaveBeenCalledOnce();
    expect(media.stop).not.toHaveBeenCalled();
    expect(result.current.error).toBeNull();
  });

  it("keeps the first accepted action media preparation when start is duplicated", () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    vi.mocked(media.startAudio).mockImplementation(async () => {
      expect(callCommands(socket)).toHaveLength(1);
    });

    let firstStarted = false;
    let duplicateStarted = true;
    act(() => {
      firstStarted = result.current.start(baseCall.callee_id, "video");
      duplicateStarted = result.current.start(baseCall.callee_id, "video");
    });

    expect(firstStarted).toBe(true);
    expect(duplicateStarted).toBe(false);
    expect(callCommands(socket)).toHaveLength(1);
    expect(media.startAudio).toHaveBeenCalledOnce();
    expect(media.stop).not.toHaveBeenCalled();
    expect(result.current.error).toBeNull();
  });

  it("releases the reservation after a real send failure so a new start can succeed", () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());

    socket.readyState = FakeWebSocket.CLOSING;
    let failedStart = true;
    act(() => {
      failedStart = result.current.start(baseCall.callee_id, "audio");
    });
    expect(failedStart).toBe(false);
    expect(result.current.pending).toBe(false);
    expect(result.current.error).toBe("Conexão em tempo real indisponível.");
    expect(media.startAudio).not.toHaveBeenCalled();
    expect(media.stop).not.toHaveBeenCalled();

    socket.readyState = FakeWebSocket.OPEN;
    let nextStart = false;
    act(() => {
      nextStart = result.current.start(baseCall.callee_id, "audio");
    });
    expect(nextStart).toBe(true);
    expect(callCommands(socket)).toHaveLength(1);
    expect(media.startAudio).toHaveBeenCalledOnce();
  });

  it("accepts a new start after a server error releases the pending command", () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());

    act(() => {
      expect(result.current.start(baseCall.callee_id, "audio")).toBe(true);
    });
    act(() =>
      socket.simulateMessage({
        type: "call.error",
        operation: "call.start",
        code: "call_invalid_state",
      }),
    );
    act(() => {
      expect(result.current.start(baseCall.callee_id, "audio")).toBe(true);
    });

    expect(callCommands(socket)).toHaveLength(2);
    expect(media.startAudio).toHaveBeenCalledTimes(2);
    expect(media.stop).not.toHaveBeenCalled();
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

  it.each([
    ["older", () => endedEvent(2)],
    [
      "another call",
      () => ({
        ...endedEvent(4),
        call: {
          ...endedEvent(4).call,
          call_id: "00000000-0000-4000-8000-000000000599",
        },
      }),
    ],
    ["duplicate", () => activeEvent(3)],
  ])("does not release a pending end for an %s event", async (_, staleEvent) => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    act(() => socket.simulateMessage(activeEvent(3)));
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());

    act(() => {
      expect(result.current.end()).toBe(true);
    });
    expect(result.current.pending).toBe(true);
    expect(media.stop).toHaveBeenCalledOnce();

    act(() => socket.simulateMessage(staleEvent()));

    expect(result.current.call?.status).toBe("active");
    expect(result.current.pending).toBe(true);
    act(() => {
      expect(result.current.end()).toBe(false);
    });
    expect(callCommands(socket)).toHaveLength(1);
    expect(media.stop).toHaveBeenCalledOnce();
  });

  it("keeps a media error when a stale event is rejected", async () => {
    vi.mocked(issueCallToken).mockRejectedValueOnce(new Error("token unavailable"));
    const { result } = renderHook(() => useCallSignaling(mediaBridge()));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    act(() => socket.simulateMessage(activeEvent(3)));
    await waitFor(() =>
      expect(result.current.error).toBe(
        "Não foi possível preparar a mídia da chamada. Tente novamente.",
      ),
    );

    act(() => socket.simulateMessage(ringingEvent(2)));

    expect(result.current.error).toBe(
      "Não foi possível preparar a mídia da chamada. Tente novamente.",
    );
  });

  it("releases a pending end only for the current terminal event", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    act(() => socket.simulateMessage(activeEvent(3)));
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());
    act(() => {
      expect(result.current.end()).toBe(true);
    });

    act(() => socket.simulateMessage(endedEvent(4)));

    expect(result.current.call?.status).toBe("ended");
    expect(result.current.pending).toBe(false);
    expect(media.stop).toHaveBeenCalledTimes(2);
    act(() => socket.simulateMessage(endedEvent(5)));
    expect(media.stop).toHaveBeenCalledTimes(2);
  });

  it("releases pending for a correlated call.error", async () => {
    const { result } = renderHook(() => useCallSignaling());
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    act(() => socket.simulateMessage(activeEvent(3)));
    await waitFor(() => expect(result.current.call?.status).toBe("active"));
    act(() => {
      expect(result.current.end()).toBe(true);
    });

    act(() =>
      socket.simulateMessage({
        type: "call.error",
        operation: "call.end",
        call_id: baseCall.call_id,
        code: "call_invalid_state",
      }),
    );

    expect(result.current.pending).toBe(false);
    expect(result.current.error).toBe("A chamada já mudou de estado.");
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

  it("reconciles a pending end as no active call after reconnect", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    act(() => firstSocket.simulateMessage(activeEvent(3)));
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());

    act(() => {
      expect(result.current.end()).toBe(true);
    });
    expect(result.current.pending).toBe(true);
    expect(media.stop).toHaveBeenCalledOnce();

    vi.useFakeTimers();
    act(() => firstSocket.close());
    expect(result.current.pending).toBe(false);
    act(() => vi.advanceTimersByTime(FIRST_RETRY_MS));

    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    expect(sentMessages(secondSocket)).toEqual([{ type: "call.sync" }]);
    expect(callCommands(secondSocket)).toEqual([]);

    act(() =>
      secondSocket.simulateMessage({
        type: "call.error",
        operation: "call.sync",
        code: "call_not_found",
      }),
    );

    expect(result.current.call).toBeNull();
    expect(result.current.pending).toBe(false);
    expect(result.current.error).toBeNull();
    expect(media.stop).toHaveBeenCalledOnce();
    act(() => {
      expect(result.current.start(baseCall.callee_id, "audio")).toBe(true);
    });
    expect(callCommands(secondSocket)).toHaveLength(1);
    expect(callCommands(secondSocket)[0]?.["type"]).toBe("call.start");
  });

  it("keeps a newer active event when reconnect sync later reports no call", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    act(() => firstSocket.simulateMessage(ringingEvent(1)));
    act(() => {
      expect(result.current.accept()).toBe(true);
    });

    vi.useFakeTimers();
    act(() => firstSocket.close());
    expect(result.current.pending).toBe(false);
    act(() => vi.advanceTimersByTime(FIRST_RETRY_MS));

    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    expect(sentMessages(secondSocket)).toEqual([{ type: "call.sync" }]);
    await act(async () => secondSocket.simulateMessage(activeEvent(2)));

    expect(result.current.call?.status).toBe("active");
    expect(result.current.pending).toBe(false);
    expect(media.connect).toHaveBeenCalledOnce();
    expect(callCommands(secondSocket)).toEqual([]);

    act(() =>
      secondSocket.simulateMessage({
        type: "call.error",
        operation: "call.sync",
        code: "call_not_found",
      }),
    );

    expect(result.current.call?.status).toBe("active");
    expect(media.connect).toHaveBeenCalledOnce();
  });

  it("restores media only after reconnect confirms the same active call", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    act(() => firstSocket.simulateMessage(activeEvent(3)));
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());
    act(() => {
      expect(result.current.end()).toBe(true);
    });

    vi.useFakeTimers();
    act(() => firstSocket.close());
    expect(media.connect).toHaveBeenCalledOnce();
    act(() => vi.advanceTimersByTime(FIRST_RETRY_MS));
    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());

    await act(async () => secondSocket.simulateMessage(activeEvent(3)));

    expect(result.current.call?.status).toBe("active");
    expect(result.current.pending).toBe(false);
    expect(media.connect).toHaveBeenCalledTimes(2);
    expect(media.stop).toHaveBeenCalledOnce();
    expect(callCommands(secondSocket)).toEqual([]);
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

    const requestId = callCommands(FakeWebSocket.instances[0])[0]?.["request_id"];
    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        ...ringingEvent(1),
        call: { ...ringingEvent(1).call, request_id: requestId },
      }),
    );
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

    await act(async () => result.current.retryMedia());
    expect(issueCallToken).toHaveBeenCalledTimes(2);
    expect(media.connect).toHaveBeenCalledTimes(2);
  });

  it("restarts disconnected media with a fresh token", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));
    await waitFor(() => expect(result.current.mediaReady).toBe(true));

    await act(async () => result.current.retryMedia());
    expect(media.connect).toHaveBeenCalledTimes(2);
    expect(media.stop).toHaveBeenCalledOnce();
    expect(issueCallToken).toHaveBeenCalledTimes(2);
  });

  it("completes a failed token retry and allows another retry with a fresh token", async () => {
    const media = mediaBridge();
    vi.mocked(media.connect).mockRejectedValueOnce(new Error("unavailable"));
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));
    await waitFor(() => expect(result.current.error).not.toBeNull());

    vi.mocked(issueCallToken).mockRejectedValueOnce(new Error("token unavailable"));
    const failedRetry = result.current.retryMedia();

    expect(failedRetry).toBeInstanceOf(Promise);
    await act(async () => failedRetry);
    expect(media.stop).toHaveBeenCalledOnce();
    expect(media.connect).toHaveBeenCalledOnce();
    expect(result.current.error).toBe(
      "Não foi possível preparar a mídia da chamada. Tente novamente.",
    );

    vi.mocked(issueCallToken).mockResolvedValueOnce({
      token: "fresh-media-token",
      expiresAt: "2026-07-30T12:06:00Z",
    });
    await act(async () => result.current.retryMedia());

    expect(issueCallToken).toHaveBeenCalledTimes(3);
    expect(media.connect).toHaveBeenLastCalledWith(
      expect.objectContaining({ call_id: baseCall.call_id }),
      "fresh-media-token",
    );
    expect(result.current.error).toBeNull();
    expect(result.current.mediaReady).toBe(true);
  });

  it("shares one pending media retry between concurrent callers", async () => {
    const media = mediaBridge();
    vi.mocked(media.connect).mockRejectedValueOnce(new Error("unavailable"));
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));
    await waitFor(() => expect(result.current.error).not.toBeNull());

    const token = deferredValue<{ token: string; expiresAt: string }>();
    vi.mocked(issueCallToken).mockImplementationOnce(() => token.promise);
    const firstRetry = result.current.retryMedia();
    const duplicateRetry = result.current.retryMedia();

    expect(firstRetry).toBeInstanceOf(Promise);
    expect(duplicateRetry).toBe(firstRetry);
    await waitFor(() => expect(issueCallToken).toHaveBeenCalledTimes(2));
    expect(media.stop).toHaveBeenCalledOnce();

    token.resolve({
      token: "fresh-media-token",
      expiresAt: "2026-07-30T12:06:00Z",
    });
    await act(async () => firstRetry);
    expect(media.connect).toHaveBeenCalledTimes(2);
  });

  it("completes a failed connection retry and permits the next attempt", async () => {
    const media = mediaBridge();
    vi.mocked(media.connect)
      .mockRejectedValueOnce(new Error("initial failure"))
      .mockRejectedValueOnce(new Error("retry failure"));
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));
    await waitFor(() => expect(result.current.error).not.toBeNull());

    await act(async () => result.current.retryMedia());
    expect(media.connect).toHaveBeenCalledTimes(2);
    expect(result.current.error).toBe(
      "Não foi possível preparar a mídia da chamada. Tente novamente.",
    );

    await act(async () => result.current.retryMedia());
    expect(media.connect).toHaveBeenCalledTimes(3);
    expect(result.current.error).toBeNull();
    expect(result.current.mediaReady).toBe(true);
  });

  it("ignores a late token failure from a retry after the call ends", async () => {
    const media = mediaBridge();
    vi.mocked(media.connect).mockRejectedValueOnce(new Error("unavailable"));
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));
    await waitFor(() => expect(result.current.error).not.toBeNull());

    const token = deferredValue<{ token: string; expiresAt: string }>();
    vi.mocked(issueCallToken).mockImplementationOnce(() => token.promise);
    const retry = result.current.retryMedia();
    await waitFor(() => expect(issueCallToken).toHaveBeenCalledTimes(2));

    act(() => FakeWebSocket.instances[0].simulateMessage(endedEvent(3)));
    token.reject(new Error("late token failure"));
    await act(async () => retry);

    expect(result.current.call?.status).toBe("ended");
    expect(result.current.error).toBeNull();
    expect(media.connect).toHaveBeenCalledOnce();
  });

  it("does not connect or update after unmount during a media retry", async () => {
    const media = mediaBridge();
    vi.mocked(media.connect).mockRejectedValueOnce(new Error("unavailable"));
    const { result, unmount } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));
    await waitFor(() => expect(result.current.error).not.toBeNull());

    const token = deferredValue<{ token: string; expiresAt: string }>();
    vi.mocked(issueCallToken).mockImplementationOnce(() => token.promise);
    const retry = result.current.retryMedia();
    await waitFor(() => expect(issueCallToken).toHaveBeenCalledTimes(2));

    unmount();
    token.reject(new Error("late token failure"));
    await retry;

    expect(media.connect).toHaveBeenCalledOnce();
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
