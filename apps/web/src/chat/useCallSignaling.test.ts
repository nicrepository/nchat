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
import { _resetChatSocket, MAX_CONSECUTIVE_FAILURES, RECONNECT_BASE_DELAY_MS } from "./chatSocket";
import { issueCallToken } from "./callApi";
import { requestMediaPermission } from "./mediaPermission";
import type { CallMediaBridge } from "./useCallSignaling";
import { useCallSignaling } from "./useCallSignaling";

vi.mock("./callApi", () => ({
  issueCallToken: vi.fn(),
}));

vi.mock("./mediaPermission", () => ({
  requestMediaPermission: vi.fn(),
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

/**
 * RF-23: media only ever connects for a call this hook itself drove to
 * ringing/active via a gesture-gated accept() — never merely because an
 * event reported status "active" (see useCallSignaling's "media permission
 * gating" and "restored call" describe blocks for that contract directly).
 * Tests below that only care about behavior once media is already
 * authorized use this instead of a bare activeEvent() push, which no
 * longer authorizes anything by itself.
 */
async function authorizeActiveCall(
  result: { current: ReturnType<typeof useCallSignaling> },
  socket: FakeWebSocket,
  version: number,
) {
  act(() => socket.simulateMessage(ringingEvent(0)));
  act(() => {
    result.current.accept();
  });
  await waitFor(() => expect(callCommands(socket)).toHaveLength(1));
  act(() => socket.simulateMessage(activeEvent(version)));
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
  vi.mocked(requestMediaPermission).mockReset();
  vi.mocked(requestMediaPermission).mockResolvedValue({ ok: true });
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

  it("does not let an empty call.sync response release another pending action", async () => {
    const { result } = renderHook(() => useCallSignaling());
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    act(() => {
      expect(result.current.start(baseCall.callee_id, "audio")).toBe(true);
    });
    await waitFor(() => expect(callCommands(socket)).toHaveLength(1));

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
    await authorizeActiveCall(result, socket, 2);
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

  it("keeps the first accepted action media preparation when accept is duplicated", async () => {
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
    await waitFor(() => expect(callCommands(FakeWebSocket.instances[0])).toHaveLength(1));
    expect(requestMediaPermission).toHaveBeenCalledExactlyOnceWith("video");
    expect(media.startAudio).toHaveBeenCalledOnce();
    expect(media.stop).not.toHaveBeenCalled();
    expect(result.current.error).toBeNull();
  });

  it("keeps the first accepted action media preparation when start is duplicated", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());

    let firstStarted = false;
    let duplicateStarted = true;
    act(() => {
      firstStarted = result.current.start(baseCall.callee_id, "video");
      duplicateStarted = result.current.start(baseCall.callee_id, "video");
    });

    expect(firstStarted).toBe(true);
    expect(duplicateStarted).toBe(false);
    await waitFor(() => expect(callCommands(socket)).toHaveLength(1));
    expect(requestMediaPermission).toHaveBeenCalledExactlyOnceWith("video");
    expect(media.startAudio).toHaveBeenCalledOnce();
    expect(media.stop).not.toHaveBeenCalled();
    expect(result.current.error).toBeNull();
  });

  it("releases the reservation after a real send failure so a new start can succeed", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());

    socket.readyState = FakeWebSocket.CLOSING;
    let failedStart = false;
    act(() => {
      failedStart = result.current.start(baseCall.callee_id, "audio");
    });
    expect(failedStart).toBe(true);
    await waitFor(() => expect(result.current.pending).toBe(false));
    expect(result.current.error).toBe("Conexão em tempo real indisponível.");
    expect(media.startAudio).toHaveBeenCalledOnce();
    expect(media.stop).not.toHaveBeenCalled();

    socket.readyState = FakeWebSocket.OPEN;
    let nextStart = false;
    act(() => {
      nextStart = result.current.start(baseCall.callee_id, "audio");
    });
    expect(nextStart).toBe(true);
    await waitFor(() => expect(callCommands(socket)).toHaveLength(1));
    expect(media.startAudio).toHaveBeenCalledTimes(2);
  });

  it("accepts a new start after a server error releases the pending command", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());

    act(() => {
      expect(result.current.start(baseCall.callee_id, "audio")).toBe(true);
    });
    await waitFor(() => expect(callCommands(socket)).toHaveLength(1));
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
    await waitFor(() => expect(callCommands(socket)).toHaveLength(2));

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
    await authorizeActiveCall(result, socket, 3);
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
    expect(callCommands(socket)).toHaveLength(2);
    expect(media.stop).toHaveBeenCalledOnce();
  });

  it("keeps a media error when a stale event is rejected", async () => {
    vi.mocked(issueCallToken).mockRejectedValueOnce(new Error("token unavailable"));
    const { result } = renderHook(() => useCallSignaling(mediaBridge()));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    await authorizeActiveCall(result, socket, 3);
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
    await authorizeActiveCall(result, socket, 3);
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());
    act(() => {
      expect(result.current.end()).toBe(true);
    });

    act(() => socket.simulateMessage(endedEvent(4)));

    expect(result.current.call?.status).toBe("ended");
    expect(result.current.pending).toBe(false);
    expect(media.stop).toHaveBeenCalledOnce();
    act(() => socket.simulateMessage(endedEvent(5)));
    expect(media.stop).toHaveBeenCalledOnce();
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
    await authorizeActiveCall(result, firstSocket, 3);
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
    await act(async () => {
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

    // The disconnect raced accept()'s own completion, so pendingRef was
    // cleared before this hook could ever confirm it drove the call active
    // itself. Per RF-23 that reconnect-reported "active" is a restore, not a
    // local authorization: media stays off until an explicit activateMedia().
    expect(result.current.call?.status).toBe("active");
    expect(result.current.pending).toBe(false);
    expect(result.current.mediaActivationRequired).toBe(true);
    expect(media.connect).not.toHaveBeenCalled();
    expect(callCommands(secondSocket)).toEqual([]);

    act(() =>
      secondSocket.simulateMessage({
        type: "call.error",
        operation: "call.sync",
        code: "call_not_found",
      }),
    );

    expect(result.current.call?.status).toBe("active");
    expect(media.connect).not.toHaveBeenCalled();
  });

  it("requires an explicit activation before restoring media that a local end never confirmed", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    await authorizeActiveCall(result, firstSocket, 3);
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

    // The optimistic end() already cleared local authorization; the server
    // still reporting the call active on reconnect is a restore, so RF-23
    // requires a fresh, explicit activation before media reconnects.
    expect(result.current.call?.status).toBe("active");
    expect(result.current.pending).toBe(false);
    expect(result.current.mediaActivationRequired).toBe(true);
    expect(media.connect).toHaveBeenCalledOnce();
    expect(media.stop).toHaveBeenCalledOnce();
    expect(callCommands(secondSocket)).toEqual([]);

    await act(async () => result.current.activateMedia());

    expect(media.connect).toHaveBeenCalledTimes(2);
    expect(result.current.mediaActivationRequired).toBe(false);
  });

  it("waits for terminal cleanup before offering activation for a replacement call", async () => {
    const media = mediaBridge();
    const cleanup = deferredValue<void>();
    vi.mocked(media.stop).mockReturnValueOnce(cleanup.promise);
    const { result } = renderHook(() => useCallSignaling(media));
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    await authorizeActiveCall(result, firstSocket, 3);
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());
    act(() => {
      expect(result.current.end()).toBe(true);
    });

    vi.useFakeTimers();
    act(() => {
      firstSocket.close();
      vi.advanceTimersByTime(FIRST_RETRY_MS);
    });
    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    const replacement = {
      ...activeEvent(1),
      call: {
        ...activeEvent(1).call,
        call_id: "00000000-0000-4000-8000-000000000598",
        request_id: "00000000-0000-4000-8000-000000000597",
      },
    };
    act(() => secondSocket.simulateMessage(replacement));

    expect(result.current.call?.call_id).toBe(replacement.call.call_id);
    expect(media.stop).toHaveBeenCalledOnce();
    expect(issueCallToken).toHaveBeenCalledOnce();
    expect(media.connect).toHaveBeenCalledOnce();

    await act(async () => {
      cleanup.resolve();
      await cleanup.promise;
      await Promise.resolve();
    });

    // The replacement was never locally authorized, so it stays pending
    // activation even once the previous call's media finishes cleaning up.
    expect(result.current.mediaActivationRequired).toBe(true);
    expect(issueCallToken).toHaveBeenCalledOnce();
    expect(media.connect).toHaveBeenCalledOnce();

    await act(async () => result.current.activateMedia());

    expect(issueCallToken).toHaveBeenCalledTimes(2);
    expect(media.connect).toHaveBeenCalledTimes(2);
    expect(media.stop).toHaveBeenCalledOnce();
  });

  it("keeps replacement media recoverable when previous cleanup fails before activation", async () => {
    const media = mediaBridge();
    const cleanup = deferredValue<void>();
    vi.mocked(media.stop).mockReturnValueOnce(cleanup.promise).mockResolvedValue(undefined);
    const { result } = renderHook(() => useCallSignaling(media));
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    await authorizeActiveCall(result, firstSocket, 3);
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());
    act(() => {
      expect(result.current.end()).toBe(true);
    });

    vi.useFakeTimers();
    act(() => {
      firstSocket.close();
      vi.advanceTimersByTime(FIRST_RETRY_MS);
    });
    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    const replacement = {
      ...activeEvent(1),
      call: {
        ...activeEvent(1).call,
        call_id: "00000000-0000-4000-8000-000000000598",
      },
    };
    act(() => secondSocket.simulateMessage(replacement));
    expect(issueCallToken).toHaveBeenCalledOnce();
    expect(result.current.mediaActivationRequired).toBe(true);

    await act(async () => cleanup.reject(new Error("cleanup failed")));

    expect(issueCallToken).toHaveBeenCalledOnce();
    expect(media.connect).toHaveBeenCalledOnce();

    await act(async () => result.current.activateMedia());

    expect(issueCallToken).toHaveBeenCalledTimes(2);
    expect(media.connect).toHaveBeenCalledTimes(2);
    expect(result.current.error).toBeNull();
  });

  it("does not request replacement media when that call ends during cleanup", async () => {
    const media = mediaBridge();
    const cleanup = deferredValue<void>();
    vi.mocked(media.stop).mockReturnValueOnce(cleanup.promise);
    const { result } = renderHook(() => useCallSignaling(media));
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    await authorizeActiveCall(result, firstSocket, 3);
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());
    act(() => {
      expect(result.current.end()).toBe(true);
    });

    vi.useFakeTimers();
    act(() => {
      firstSocket.close();
      vi.advanceTimersByTime(FIRST_RETRY_MS);
    });
    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    const replacementCallId = "00000000-0000-4000-8000-000000000598";
    act(() =>
      secondSocket.simulateMessage({
        ...activeEvent(1),
        call: { ...activeEvent(1).call, call_id: replacementCallId },
      }),
    );
    act(() =>
      secondSocket.simulateMessage({
        ...endedEvent(2),
        call: { ...endedEvent(2).call, call_id: replacementCallId },
      }),
    );

    await act(async () => {
      cleanup.resolve();
      await cleanup.promise;
      await Promise.resolve();
    });

    expect(result.current.call?.status).toBe("ended");
    expect(issueCallToken).toHaveBeenCalledOnce();
    expect(media.connect).toHaveBeenCalledOnce();
    expect(media.stop).toHaveBeenCalledOnce();
  });

  it("only offers activation for the latest replacement when another call wins during cleanup", async () => {
    const media = mediaBridge();
    const cleanup = deferredValue<void>();
    vi.mocked(media.stop).mockReturnValueOnce(cleanup.promise);
    const { result } = renderHook(() => useCallSignaling(media));
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    await authorizeActiveCall(result, firstSocket, 3);
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());
    act(() => {
      expect(result.current.end()).toBe(true);
    });

    vi.useFakeTimers();
    act(() => {
      firstSocket.close();
      vi.advanceTimersByTime(FIRST_RETRY_MS);
    });
    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    const callB = "00000000-0000-4000-8000-000000000598";
    act(() =>
      secondSocket.simulateMessage({
        ...activeEvent(1),
        call: { ...activeEvent(1).call, call_id: callB },
      }),
    );
    act(() => {
      secondSocket.close();
      vi.runOnlyPendingTimers();
    });
    const thirdSocket = FakeWebSocket.instances[2];
    act(() => thirdSocket.simulateOpen());
    const callC = "00000000-0000-4000-8000-000000000597";
    act(() =>
      thirdSocket.simulateMessage({
        ...activeEvent(1),
        call: { ...activeEvent(1).call, call_id: callC },
      }),
    );

    await act(async () => {
      cleanup.resolve();
      await cleanup.promise;
      await Promise.resolve();
    });

    // Neither B nor C was locally authorized; only C remains as the current
    // call and only C is ever a target for an explicit activation.
    expect(result.current.call?.call_id).toBe(callC);
    expect(result.current.mediaActivationRequired).toBe(true);
    expect(issueCallToken).toHaveBeenCalledOnce();
    expect(media.connect).toHaveBeenCalledOnce();

    await act(async () => result.current.activateMedia());

    expect(issueCallToken).toHaveBeenCalledTimes(2);
    expect(issueCallToken).toHaveBeenLastCalledWith(callC);
    expect(media.connect).toHaveBeenCalledTimes(2);
    expect(media.stop).toHaveBeenCalledOnce();
  });

  it("does not request replacement media disabled while cleanup is pending", async () => {
    const media = mediaBridge();
    const cleanup = deferredValue<void>();
    vi.mocked(media.stop).mockReturnValueOnce(cleanup.promise);
    const { result, rerender } = renderHook(
      ({ mediaEnabled }) => useCallSignaling(media, mediaEnabled),
      { initialProps: { mediaEnabled: true } },
    );
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    await authorizeActiveCall(result, firstSocket, 3);
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());
    act(() => {
      expect(result.current.end()).toBe(true);
    });

    vi.useFakeTimers();
    act(() => {
      firstSocket.close();
      vi.advanceTimersByTime(FIRST_RETRY_MS);
    });
    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    const replacementCallId = "00000000-0000-4000-8000-000000000598";
    act(() =>
      secondSocket.simulateMessage({
        ...activeEvent(1),
        call: { ...activeEvent(1).call, call_id: replacementCallId },
      }),
    );
    rerender({ mediaEnabled: false });

    await act(async () => {
      cleanup.resolve();
      await cleanup.promise;
      await Promise.resolve();
    });

    expect(result.current.call?.call_id).toBe(replacementCallId);
    expect(issueCallToken).toHaveBeenCalledOnce();
    expect(media.connect).toHaveBeenCalledOnce();
  });

  it("keeps activation pending for a replacement call once media becomes enabled", async () => {
    const media = mediaBridge();
    const cleanup = deferredValue<void>();
    vi.mocked(media.stop).mockReturnValueOnce(cleanup.promise);
    const { result, rerender } = renderHook(
      ({ mediaEnabled }) => useCallSignaling(media, mediaEnabled),
      { initialProps: { mediaEnabled: true } },
    );
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    await authorizeActiveCall(result, firstSocket, 3);
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());
    act(() => {
      expect(result.current.end()).toBe(true);
    });

    vi.useFakeTimers();
    act(() => {
      firstSocket.close();
      vi.advanceTimersByTime(FIRST_RETRY_MS);
    });
    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    rerender({ mediaEnabled: false });
    const replacementCallId = "00000000-0000-4000-8000-000000000598";
    act(() =>
      secondSocket.simulateMessage({
        ...activeEvent(1),
        call: { ...activeEvent(1).call, call_id: replacementCallId },
      }),
    );

    rerender({ mediaEnabled: true });

    // Unauthorized regardless of the mediaEnabled gate: re-enabling never
    // auto-connects a replacement call on its own.
    expect(result.current.mediaActivationRequired).toBe(true);
    expect(issueCallToken).toHaveBeenCalledOnce();
    expect(media.connect).toHaveBeenCalledOnce();

    await act(async () => {
      cleanup.resolve();
      await cleanup.promise;
      await Promise.resolve();
    });

    expect(issueCallToken).toHaveBeenCalledOnce();
    expect(media.connect).toHaveBeenCalledOnce();

    await act(async () => result.current.activateMedia());

    expect(issueCallToken).toHaveBeenCalledTimes(2);
    expect(issueCallToken).toHaveBeenLastCalledWith(replacementCallId);
    expect(media.connect).toHaveBeenCalledTimes(2);
  });

  it("replaces a stale active call with the current generation sync result", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    await authorizeActiveCall(result, firstSocket, 3);
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());

    vi.useFakeTimers();
    act(() => {
      firstSocket.close();
      vi.advanceTimersByTime(FIRST_RETRY_MS);
    });
    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    const replacement = {
      ...activeEvent(1),
      event_id: "00000000-0000-4000-8000-000000000599",
      call: {
        ...activeEvent(1).call,
        call_id: "00000000-0000-4000-8000-000000000598",
        request_id: "00000000-0000-4000-8000-000000000597",
      },
    };

    await act(async () => secondSocket.simulateMessage(replacement));

    // The stale call's own media is torn down, but the replacement is a
    // reconciled call this hook never locally authorized: it stays pending
    // an explicit activation rather than auto-connecting.
    expect(result.current.call?.call_id).toBe(replacement.call.call_id);
    expect(media.stop).toHaveBeenCalledOnce();
    expect(result.current.mediaActivationRequired).toBe(true);
    expect(media.connect).toHaveBeenCalledOnce();
    act(() => {
      expect(result.current.end()).toBe(true);
    });
    expect(callCommands(secondSocket)).toEqual([
      { type: "call.end", call_id: replacement.call.call_id },
    ]);
  });

  it("does not request replacement media after unmount during reconciliation cleanup", async () => {
    const media = mediaBridge();
    const cleanup = deferredValue<void>();
    vi.mocked(media.stop).mockReturnValue(cleanup.promise);
    const { result, unmount } = renderHook(() => useCallSignaling(media));
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    await authorizeActiveCall(result, firstSocket, 3);
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());

    vi.useFakeTimers();
    act(() => {
      firstSocket.close();
      vi.advanceTimersByTime(FIRST_RETRY_MS);
    });
    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    act(() =>
      secondSocket.simulateMessage({
        ...activeEvent(1),
        call: {
          ...activeEvent(1).call,
          call_id: "00000000-0000-4000-8000-000000000598",
        },
      }),
    );

    unmount();
    await act(async () => cleanup.resolve());

    expect(issueCallToken).toHaveBeenCalledOnce();
    expect(media.connect).toHaveBeenCalledOnce();
  });

  it("blocks actions until reconnect sync authoritatively reports no call", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    await authorizeActiveCall(result, firstSocket, 3);
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());

    vi.useFakeTimers();
    act(() => {
      firstSocket.close();
      vi.advanceTimersByTime(FIRST_RETRY_MS);
    });
    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());

    expect(result.current.pending).toBe(true);
    act(() => {
      expect(result.current.end()).toBe(false);
    });
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
    expect(media.stop).toHaveBeenCalledOnce();
    await act(async () => {
      expect(result.current.start(baseCall.callee_id, "audio")).toBe(true);
    });
    expect(callCommands(secondSocket)).toHaveLength(1);
  });

  it("clears reconciliation pending when a reconnect drops before call.sync responds", () => {
    vi.useFakeTimers();
    const { result } = renderHook(() => useCallSignaling());
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    act(() => firstSocket.simulateMessage(activeEvent(3)));
    act(() => {
      firstSocket.close();
      vi.runOnlyPendingTimers();
    });

    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    expect(result.current.pending).toBe(true);
    expect(sentMessages(secondSocket)).toEqual([{ type: "call.sync" }]);
    const staleMessage = secondSocket.onmessage;

    act(() => secondSocket.close());

    expect(result.current.pending).toBe(false);
    act(() => vi.runOnlyPendingTimers());
    const thirdSocket = FakeWebSocket.instances[2];
    act(() => thirdSocket.simulateOpen());
    expect(result.current.pending).toBe(true);
    expect(sentMessages(thirdSocket)).toEqual([{ type: "call.sync" }]);

    act(() =>
      staleMessage?.(
        new MessageEvent("message", {
          data: JSON.stringify({
            type: "call.error",
            operation: "call.sync",
            code: "call_not_found",
          }),
        }),
      ),
    );
    expect(result.current.call?.status).toBe("active");
    expect(result.current.pending).toBe(true);

    act(() =>
      thirdSocket.simulateMessage({
        type: "call.error",
        operation: "call.sync",
        code: "call_not_found",
      }),
    );
    expect(result.current.call).toBeNull();
    expect(result.current.pending).toBe(false);
  });

  it("leaves reconciliation unlocked with an observable error after reconnect gives up", () => {
    vi.useFakeTimers();
    const { result } = renderHook(() => useCallSignaling());
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    act(() => firstSocket.simulateMessage(activeEvent(3)));
    act(() => {
      firstSocket.close();
      vi.runOnlyPendingTimers();
    });
    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    expect(result.current.pending).toBe(true);
    act(() => secondSocket.close());
    expect(result.current.pending).toBe(false);

    for (let attempt = 0; attempt < MAX_CONSECUTIVE_FAILURES; attempt += 1) {
      act(() => vi.runOnlyPendingTimers());
      const failedSocket = FakeWebSocket.instances.at(-1);
      if (!failedSocket) throw new Error("Expected reconnect socket");
      act(() => failedSocket.close());
    }

    expect(result.current.pending).toBe(false);
    expect(result.current.error).toBe("Conexão em tempo real indisponível.");
    act(() => {
      expect(result.current.end()).toBe(false);
    });
    expect(callCommands(secondSocket)).toEqual([]);
  });

  it("does not reconnect after unmount following a second drop during reconciliation", () => {
    vi.useFakeTimers();
    const { result, unmount } = renderHook(() => useCallSignaling());
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    act(() => firstSocket.simulateMessage(activeEvent(3)));
    act(() => {
      firstSocket.close();
      vi.runOnlyPendingTimers();
    });
    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    expect(result.current.pending).toBe(true);
    act(() => secondSocket.close());
    expect(result.current.pending).toBe(false);

    unmount();
    act(() => vi.runOnlyPendingTimers());

    expect(FakeWebSocket.instances).toHaveLength(2);
  });

  it("requests media only once the locally accepted call becomes active", async () => {
    const { result } = renderHook(() => useCallSignaling());
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());

    act(() => socket.simulateMessage(ringingEvent(1)));
    expect(issueCallToken).not.toHaveBeenCalled();
    act(() => {
      expect(result.current.accept()).toBe(true);
    });
    await waitFor(() => expect(callCommands(socket)).toHaveLength(1));
    expect(issueCallToken).not.toHaveBeenCalled();

    act(() => socket.simulateMessage(activeEvent(2)));
    await waitFor(() => expect(issueCallToken).toHaveBeenCalledExactlyOnceWith(baseCall.call_id));
  });

  it("waits for the identity gate before requesting active call media", async () => {
    const media = mediaBridge();
    const { result, rerender } = renderHook(
      ({ identityReady }) => useCallSignaling(media, identityReady),
      { initialProps: { identityReady: false } },
    );
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    await authorizeActiveCall(result, socket, 2);

    expect(result.current.call?.status).toBe("active");
    expect(issueCallToken).not.toHaveBeenCalled();
    expect(media.connect).not.toHaveBeenCalled();

    rerender({ identityReady: true });

    await waitFor(() => expect(issueCallToken).toHaveBeenCalledExactlyOnceWith(baseCall.call_id));
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());
  });

  it("connects the media bridge with the active call token", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());

    await authorizeActiveCall(result, socket, 2);

    await waitFor(() =>
      expect(media.connect).toHaveBeenCalledExactlyOnceWith(
        expect.objectContaining({ ...baseCall, status: "active" }),
        "media-token",
      ),
    );
    await waitFor(() => expect(result.current.mediaReady).toBe(true));
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
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    act(() => socket.simulateMessage(ringingEvent(1)));
    act(() => {
      expect(result.current.accept()).toBe(true);
    });
    await waitFor(() => expect(callCommands(socket)).toHaveLength(1));

    act(() => {
      socket.simulateMessage(activeEvent(2));
      socket.simulateMessage(activeEvent(3));
    });

    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());
    expect(issueCallToken).toHaveBeenCalledOnce();

    finishConnect?.();
  });

  it("unlocks audio from start and accept user actions", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());

    act(() => {
      result.current.start(baseCall.callee_id, "audio");
    });
    expect(media.startAudio).toHaveBeenCalledOnce();
    await waitFor(() => expect(callCommands(FakeWebSocket.instances[0])).toHaveLength(1));

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
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    await authorizeActiveCall(result, socket, 2);
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());

    act(() => {
      result.current.end();
    });
    expect(media.stop).toHaveBeenCalledOnce();

    act(() => FakeWebSocket.instances[0].simulateMessage(endedEvent(3)));
    expect(media.stop).toHaveBeenCalledOnce();
  });

  it("does not connect a late token after a locally accepted end", async () => {
    const media = mediaBridge();
    const token = deferredValue<{ token: string; expiresAt: string }>();
    vi.mocked(issueCallToken).mockImplementationOnce(() => token.promise);
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    await authorizeActiveCall(result, socket, 2);
    await waitFor(() => expect(issueCallToken).toHaveBeenCalledOnce());

    act(() => {
      expect(result.current.end()).toBe(true);
    });
    expect(media.stop).toHaveBeenCalledOnce();

    await act(async () => {
      token.resolve({ token: "late-media-token", expiresAt: "2026-07-30T12:06:00Z" });
      await token.promise;
    });

    expect(media.connect).not.toHaveBeenCalled();
    expect(result.current.error).toBeNull();
    act(() => socket.simulateMessage(endedEvent(3)));
    expect(result.current.call?.status).toBe("ended");

    // A brand new call arriving as a live "active" push (no ringing/accept
    // of its own in this hook) is exactly as unauthorized as a restored one.
    const nextCallId = "00000000-0000-4000-8000-000000000598";
    act(() =>
      socket.simulateMessage({
        ...activeEvent(1),
        call: {
          ...activeEvent(1).call,
          call_id: nextCallId,
          request_id: "00000000-0000-4000-8000-000000000597",
        },
      }),
    );
    expect(result.current.mediaActivationRequired).toBe(true);
    expect(issueCallToken).toHaveBeenCalledOnce();

    await act(async () => result.current.activateMedia());

    await waitFor(() => expect(issueCallToken).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());
    expect(media.connect).toHaveBeenLastCalledWith(
      expect.objectContaining({ call_id: nextCallId }),
      "media-token",
    );
  });

  it.each(["cancel", "decline"] as const)(
    "does not connect a late token after a locally accepted %s",
    async (action) => {
      const media = mediaBridge();
      const token = deferredValue<{ token: string; expiresAt: string }>();
      vi.mocked(issueCallToken).mockImplementationOnce(() => token.promise);
      const { result } = renderHook(() => useCallSignaling(media));
      const socket = FakeWebSocket.instances[0];
      act(() => socket.simulateOpen());
      await authorizeActiveCall(result, socket, 2);
      await waitFor(() => expect(issueCallToken).toHaveBeenCalledOnce());

      act(() => {
        expect(result.current[action]()).toBe(true);
      });
      expect(media.stop).toHaveBeenCalledOnce();

      await act(async () => {
        token.resolve({ token: "late-media-token", expiresAt: "2026-07-30T12:06:00Z" });
        await token.promise;
      });

      expect(media.connect).not.toHaveBeenCalled();
      expect(result.current.error).toBeNull();
    },
  );

  it("allows a fresh media retry after the server rejects a terminal command", async () => {
    const media = mediaBridge();
    const token = deferredValue<{ token: string; expiresAt: string }>();
    vi.mocked(issueCallToken).mockImplementationOnce(() => token.promise);
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    await authorizeActiveCall(result, socket, 2);
    await waitFor(() => expect(issueCallToken).toHaveBeenCalledOnce());
    act(() => {
      expect(result.current.end()).toBe(true);
    });

    await act(async () => {
      token.resolve({ token: "late-media-token", expiresAt: "2026-07-30T12:06:00Z" });
      await token.promise;
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
    expect(media.connect).not.toHaveBeenCalled();

    await act(async () => result.current.retryMedia());

    expect(issueCallToken).toHaveBeenCalledTimes(2);
    expect(media.connect).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({ call_id: baseCall.call_id }),
      "media-token",
    );
    expect(result.current.error).toBeNull();
  });

  it("does not connect a late token after unmount", async () => {
    const media = mediaBridge();
    const token = deferredValue<{ token: string; expiresAt: string }>();
    vi.mocked(issueCallToken).mockImplementationOnce(() => token.promise);
    const { result, unmount } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    await authorizeActiveCall(result, socket, 2);
    await waitFor(() => expect(issueCallToken).toHaveBeenCalledOnce());

    unmount();
    await act(async () => {
      token.resolve({ token: "late-media-token", expiresAt: "2026-07-30T12:06:00Z" });
      await token.promise;
    });

    expect(media.connect).not.toHaveBeenCalled();
  });

  it("ignores media side effects from a stale terminal event", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    await authorizeActiveCall(result, socket, 3);
    await waitFor(() => expect(media.connect).toHaveBeenCalledOnce());

    act(() => FakeWebSocket.instances[0].simulateMessage(endedEvent(2)));

    expect(result.current.call?.status).toBe("active");
    expect(media.stop).not.toHaveBeenCalled();
  });

  it("can retry with a fresh token after media connection fails", async () => {
    const media = mediaBridge();
    vi.mocked(media.connect).mockRejectedValueOnce(new Error("unavailable"));
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());

    await authorizeActiveCall(result, socket, 2);
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
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    await authorizeActiveCall(result, socket, 2);
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
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    await authorizeActiveCall(result, socket, 2);
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
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    await authorizeActiveCall(result, socket, 2);
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
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    await authorizeActiveCall(result, socket, 2);
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
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    await authorizeActiveCall(result, socket, 2);
    await waitFor(() => expect(result.current.error).not.toBeNull());

    const token = deferredValue<{ token: string; expiresAt: string }>();
    vi.mocked(issueCallToken).mockImplementationOnce(() => token.promise);
    const retry = result.current.retryMedia();
    await waitFor(() => expect(issueCallToken).toHaveBeenCalledTimes(2));

    act(() => socket.simulateMessage(endedEvent(3)));
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
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    await authorizeActiveCall(result, socket, 2);
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
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    await authorizeActiveCall(result, socket, 2);
    await waitFor(() => expect(issueCallToken).toHaveBeenCalledOnce());

    act(() => socket.simulateMessage(endedEvent(3)));
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

// ── RF-23: media permission preflight before call.start / call.accept ────────

describe("useCallSignaling media permission gating", () => {
  it("requests only the microphone and withholds call.start until granted (audio)", async () => {
    const permission = deferredValue<{ ok: true }>();
    vi.mocked(requestMediaPermission).mockReturnValueOnce(permission.promise);
    const { result } = renderHook(() => useCallSignaling());
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());

    act(() => {
      expect(result.current.start(baseCall.callee_id, "audio")).toBe(true);
    });

    expect(requestMediaPermission).toHaveBeenCalledExactlyOnceWith("audio");
    expect(result.current.pending).toBe(true);
    expect(callCommands(socket)).toEqual([]);

    await act(async () => permission.resolve({ ok: true }));

    expect(callCommands(socket)).toHaveLength(1);
    expect(callCommands(socket)[0]?.["type"]).toBe("call.start");
    expect(callCommands(socket)[0]?.["call_type"]).toBe("audio");
  });

  it("requests the camera and microphone before call.start for a video call", async () => {
    const { result } = renderHook(() => useCallSignaling());
    act(() => FakeWebSocket.instances[0].simulateOpen());

    act(() => {
      expect(result.current.start(baseCall.callee_id, "video")).toBe(true);
    });

    await waitFor(() => expect(callCommands(FakeWebSocket.instances[0])).toHaveLength(1));
    expect(requestMediaPermission).toHaveBeenCalledExactlyOnceWith("video");
  });

  it("does not create a call when the audio permission is denied", async () => {
    vi.mocked(requestMediaPermission).mockResolvedValueOnce({
      ok: false,
      kind: "permission_denied",
      message: "Acesso ao microfone foi negado ou bloqueado.",
    });
    const { result } = renderHook(() => useCallSignaling());
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());

    await act(async () => {
      expect(result.current.start(baseCall.callee_id, "audio")).toBe(true);
    });

    expect(callCommands(socket)).toEqual([]);
    expect(result.current.call).toBeNull();
    expect(result.current.pending).toBe(false);
    expect(result.current.error).toBe("Acesso ao microfone foi negado ou bloqueado.");
  });

  it("allows a retry after denial to request permission again and succeed", async () => {
    vi.mocked(requestMediaPermission).mockResolvedValueOnce({
      ok: false,
      kind: "permission_denied",
      message: "Acesso ao microfone foi negado ou bloqueado.",
    });
    const { result } = renderHook(() => useCallSignaling());
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    await act(async () => {
      expect(result.current.start(baseCall.callee_id, "audio")).toBe(true);
    });
    expect(result.current.error).not.toBeNull();

    await act(async () => {
      expect(result.current.start(baseCall.callee_id, "audio")).toBe(true);
    });

    expect(requestMediaPermission).toHaveBeenCalledTimes(2);
    expect(callCommands(socket)).toHaveLength(1);
    expect(result.current.error).toBeNull();
  });

  it("does not request permission twice for a double click while one is pending", async () => {
    const permission = deferredValue<{ ok: true }>();
    vi.mocked(requestMediaPermission).mockReturnValueOnce(permission.promise);
    const { result } = renderHook(() => useCallSignaling());
    act(() => FakeWebSocket.instances[0].simulateOpen());

    let first = false;
    let duplicate = true;
    act(() => {
      first = result.current.start(baseCall.callee_id, "audio");
      duplicate = result.current.start(baseCall.callee_id, "audio");
    });

    expect(first).toBe(true);
    expect(duplicate).toBe(false);
    expect(requestMediaPermission).toHaveBeenCalledOnce();
    await act(async () => permission.resolve({ ok: true }));
  });

  it("requests permission for the ringing call's type before call.accept", async () => {
    const permission = deferredValue<{ ok: true }>();
    vi.mocked(requestMediaPermission).mockReturnValueOnce(permission.promise);
    const { result } = renderHook(() => useCallSignaling());
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    act(() =>
      socket.simulateMessage({
        ...ringingEvent(1),
        call: { ...ringingEvent(1).call, call_type: "audio" },
      }),
    );

    act(() => {
      expect(result.current.accept()).toBe(true);
    });

    expect(requestMediaPermission).toHaveBeenCalledExactlyOnceWith("audio");
    expect(callCommands(socket)).toEqual([]);

    await act(async () => permission.resolve({ ok: true }));

    expect(callCommands(socket)).toEqual([{ type: "call.accept", call_id: baseCall.call_id }]);
  });

  it("keeps a ringing call recoverable and never sends call.accept when permission is denied", async () => {
    vi.mocked(requestMediaPermission).mockResolvedValueOnce({
      ok: false,
      kind: "permission_denied",
      message: "Acesso à câmera e ao microfone foi negado ou bloqueado.",
    });
    const { result } = renderHook(() => useCallSignaling());
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    act(() => socket.simulateMessage(ringingEvent(1)));

    await act(async () => {
      expect(result.current.accept()).toBe(true);
    });

    expect(callCommands(socket)).toEqual([]);
    expect(result.current.call?.status).toBe("ringing");
    expect(result.current.pending).toBe(false);
    expect(result.current.error).toBe("Acesso à câmera e ao microfone foi negado ou bloqueado.");

    act(() => {
      expect(result.current.decline()).toBe(true);
    });
    expect(callCommands(socket)).toEqual([{ type: "call.decline", call_id: baseCall.call_id }]);
  });

  it("does not send call.accept when the caller cancels while permission is pending", async () => {
    const permission = deferredValue<{ ok: true }>();
    vi.mocked(requestMediaPermission).mockReturnValueOnce(permission.promise);
    const { result } = renderHook(() => useCallSignaling());
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    act(() => socket.simulateMessage(ringingEvent(1)));
    act(() => {
      expect(result.current.accept()).toBe(true);
    });

    act(() =>
      socket.simulateMessage({
        ...endedEvent(2),
        type: "call.cancelled",
        call: { ...endedEvent(2).call, status: "cancelled" },
      }),
    );
    expect(result.current.call?.status).toBe("cancelled");

    await act(async () => permission.resolve({ ok: true }));

    expect(callCommands(socket)).toEqual([]);
  });

  it("does not dispatch call.start after the hook unmounts while permission is pending", async () => {
    const permission = deferredValue<{ ok: true }>();
    vi.mocked(requestMediaPermission).mockReturnValueOnce(permission.promise);
    const { result, unmount } = renderHook(() => useCallSignaling());
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => {
      expect(result.current.start(baseCall.callee_id, "audio")).toBe(true);
    });

    unmount();
    permission.resolve({ ok: true });
    await act(async () => permission.promise);

    expect(FakeWebSocket.instances[0].sentMessages.some((m) => m.includes("call.start"))).toBe(
      false,
    );
  });

  it("does not request permission when start has no socket at all to send to", () => {
    const { result, unmount } = renderHook(() => useCallSignaling());
    act(() => FakeWebSocket.instances[0].simulateOpen());
    unmount();

    let started = true;
    act(() => {
      started = result.current.start(baseCall.callee_id, "audio");
    });

    expect(started).toBe(false);
    expect(requestMediaPermission).not.toHaveBeenCalled();
  });

  it("reserves pending and still fails gracefully when start's socket exists but is not open", async () => {
    const { result } = renderHook(() => useCallSignaling());
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    socket.readyState = FakeWebSocket.CLOSING;

    let started = false;
    act(() => {
      started = result.current.start(baseCall.callee_id, "audio");
    });

    expect(started).toBe(true);
    await waitFor(() => expect(result.current.pending).toBe(false));
    expect(result.current.error).toBe("Conexão em tempo real indisponível.");
    expect(callCommands(socket)).toEqual([]);
  });

  it("does not leave pending stuck when start is attempted during reconnect reconciliation", () => {
    vi.useFakeTimers();
    const { result } = renderHook(() => useCallSignaling());
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    act(() => {
      firstSocket.close();
      vi.runOnlyPendingTimers();
    });
    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    expect(result.current.pending).toBe(true);

    let started = true;
    act(() => {
      started = result.current.start(baseCall.callee_id, "audio");
    });

    expect(started).toBe(false);
    expect(requestMediaPermission).not.toHaveBeenCalled();
    expect(result.current.pending).toBe(true);
    expect(callCommands(secondSocket)).toEqual([]);
  });

  it("does not leave pending stuck when accept is attempted during reconnect reconciliation", () => {
    vi.useFakeTimers();
    const { result } = renderHook(() => useCallSignaling());
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    act(() => firstSocket.simulateMessage(ringingEvent(1)));
    act(() => {
      firstSocket.close();
      vi.runOnlyPendingTimers();
    });
    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    expect(result.current.pending).toBe(true);

    let accepted = true;
    act(() => {
      accepted = result.current.accept();
    });

    expect(accepted).toBe(false);
    expect(requestMediaPermission).not.toHaveBeenCalled();
    expect(callCommands(secondSocket)).toEqual([]);
  });
});

// ── RF-23: a restored/reconciled active call requires explicit activation ────

describe("useCallSignaling restored call activation", () => {
  it("does not call getUserMedia or media.connect for a call.sync active on first mount", () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());

    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));

    expect(result.current.call?.status).toBe("active");
    expect(result.current.mediaActivationRequired).toBe(true);
    expect(requestMediaPermission).not.toHaveBeenCalled();
    expect(media.connect).not.toHaveBeenCalled();
  });

  it("does not call getUserMedia when a reconnect confirms the call is still active", () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    act(() => firstSocket.simulateMessage(activeEvent(2)));

    vi.useFakeTimers();
    act(() => {
      firstSocket.close();
      vi.advanceTimersByTime(FIRST_RETRY_MS);
    });
    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    act(() => secondSocket.simulateMessage(activeEvent(2)));

    expect(result.current.mediaActivationRequired).toBe(true);
    expect(requestMediaPermission).not.toHaveBeenCalled();
    expect(media.connect).not.toHaveBeenCalled();
  });

  it("requests permission and connects exactly once when the user explicitly activates media", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));
    expect(result.current.mediaActivationRequired).toBe(true);

    await act(async () => result.current.activateMedia());

    expect(requestMediaPermission).toHaveBeenCalledExactlyOnceWith("video");
    expect(media.connect).toHaveBeenCalledOnce();
    expect(result.current.mediaActivationRequired).toBe(false);
  });

  it("requests only the microphone for a restored audio call", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        ...activeEvent(2),
        call: { ...activeEvent(2).call, call_type: "audio" },
      }),
    );

    await act(async () => result.current.activateMedia());

    expect(requestMediaPermission).toHaveBeenCalledExactlyOnceWith("audio");
  });

  it("requests the camera and microphone for a restored video call", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() =>
      FakeWebSocket.instances[0].simulateMessage({
        ...activeEvent(2),
        call: { ...activeEvent(2).call, call_type: "video" },
      }),
    );

    await act(async () => result.current.activateMedia());

    expect(requestMediaPermission).toHaveBeenCalledExactlyOnceWith("video");
  });

  it("keeps a restored call active and recoverable when activation permission is denied", async () => {
    vi.mocked(requestMediaPermission).mockResolvedValueOnce({
      ok: false,
      kind: "permission_denied",
      message: "Acesso à câmera e ao microfone foi negado ou bloqueado.",
    });
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));

    await act(async () => result.current.activateMedia());

    expect(result.current.call?.status).toBe("active");
    expect(result.current.mediaActivationRequired).toBe(true);
    expect(result.current.error).toBe("Acesso à câmera e ao microfone foi negado ou bloqueado.");
    expect(media.connect).not.toHaveBeenCalled();
  });

  it("requires a new click to retry activation after denial", async () => {
    vi.mocked(requestMediaPermission).mockResolvedValueOnce({
      ok: false,
      kind: "permission_denied",
      message: "Acesso à câmera e ao microfone foi negado ou bloqueado.",
    });
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));
    await act(async () => result.current.activateMedia());
    expect(result.current.error).not.toBeNull();

    await act(async () => result.current.activateMedia());

    expect(requestMediaPermission).toHaveBeenCalledTimes(2);
    expect(media.connect).toHaveBeenCalledOnce();
    expect(result.current.mediaActivationRequired).toBe(false);
    expect(result.current.error).toBeNull();
  });

  it("does not duplicate getUserMedia on a double click of activate media", async () => {
    const permission = deferredValue<{ ok: true }>();
    vi.mocked(requestMediaPermission).mockReturnValueOnce(permission.promise);
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));

    let first!: Promise<void>;
    let duplicate!: Promise<void>;
    act(() => {
      first = result.current.activateMedia();
      duplicate = result.current.activateMedia();
    });

    expect(requestMediaPermission).toHaveBeenCalledOnce();
    expect(duplicate).toBe(first);
    await act(async () => permission.resolve({ ok: true }));
  });

  it("invalidates a pending activation for call A when call B replaces it", async () => {
    const permission = deferredValue<{ ok: true }>();
    vi.mocked(requestMediaPermission).mockReturnValueOnce(permission.promise);
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const firstSocket = FakeWebSocket.instances[0];
    act(() => firstSocket.simulateOpen());
    act(() => firstSocket.simulateMessage(activeEvent(2)));

    act(() => {
      void result.current.activateMedia();
    });
    expect(requestMediaPermission).toHaveBeenCalledOnce();

    vi.useFakeTimers();
    act(() => {
      firstSocket.close();
      vi.advanceTimersByTime(FIRST_RETRY_MS);
    });
    const secondSocket = FakeWebSocket.instances[1];
    act(() => secondSocket.simulateOpen());
    const callB = {
      ...activeEvent(1),
      call: { ...activeEvent(1).call, call_id: "00000000-0000-4000-8000-000000000598" },
    };
    act(() => secondSocket.simulateMessage(callB));

    await act(async () => permission.resolve({ ok: true }));

    // Call A's late permission grant must not authorize or connect call B.
    expect(media.connect).not.toHaveBeenCalled();
    expect(result.current.call?.call_id).toBe(callB.call.call_id);
    expect(result.current.mediaActivationRequired).toBe(true);
  });

  it("does not connect when the call ends while the activation prompt is pending", async () => {
    const permission = deferredValue<{ ok: true }>();
    vi.mocked(requestMediaPermission).mockReturnValueOnce(permission.promise);
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    const socket = FakeWebSocket.instances[0];
    act(() => socket.simulateOpen());
    act(() => socket.simulateMessage(activeEvent(2)));

    act(() => {
      void result.current.activateMedia();
    });
    act(() => socket.simulateMessage(endedEvent(3)));
    expect(result.current.call?.status).toBe("ended");

    await act(async () => permission.resolve({ ok: true }));

    expect(media.connect).not.toHaveBeenCalled();
  });

  it("only finalizes the preflight track when the hook unmounts during the activation prompt", async () => {
    const permission = deferredValue<{ ok: true }>();
    vi.mocked(requestMediaPermission).mockReturnValueOnce(permission.promise);
    const media = mediaBridge();
    const { result, unmount } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());
    act(() => FakeWebSocket.instances[0].simulateMessage(activeEvent(2)));

    act(() => {
      void result.current.activateMedia();
    });
    unmount();

    await act(async () => permission.resolve({ ok: true }));

    // requestMediaPermission itself always stops its temporary tracks
    // (see mediaPermission.test.ts); this only asserts the hook never
    // reacts to a permission grant that resolves after unmount.
    expect(media.connect).not.toHaveBeenCalled();
  });

  it("does not duplicate media.connect for a duplicate active event on a restored call", async () => {
    const media = mediaBridge();
    const { result } = renderHook(() => useCallSignaling(media));
    act(() => FakeWebSocket.instances[0].simulateOpen());

    act(() => {
      FakeWebSocket.instances[0].simulateMessage(activeEvent(2));
      FakeWebSocket.instances[0].simulateMessage(activeEvent(2));
    });
    expect(result.current.mediaActivationRequired).toBe(true);

    await act(async () => result.current.activateMedia());

    expect(media.connect).toHaveBeenCalledOnce();
  });
});
