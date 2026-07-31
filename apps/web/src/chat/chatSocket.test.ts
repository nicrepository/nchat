/**
 * chatSocket tests.
 *
 * Covers the lifecycle the chat UI depends on and that issue #449 found broken:
 * one socket per tab regardless of how many hooks want it, one reconnect timer,
 * bounded jittered backoff that only resets after a stable connection, offline
 * suspension, and a stop condition so a revoked session cannot spin forever.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import {
  _resetChatSocket,
  acquireChatSocket,
  computeBackoffDelay,
  getChatSocketStatus,
  MAX_CONSECUTIVE_FAILURES,
  RECONNECT_BASE_DELAY_MS,
  RECONNECT_MAX_DELAY_MS,
  STABLE_CONNECTION_MS,
  type ChatSocketStatus,
} from "./chatSocket";

class FakeWebSocket {
  static OPEN = 1;
  static CLOSED = 3;
  static instances: FakeWebSocket[] = [];

  readyState = FakeWebSocket.OPEN;
  sent: string[] = [];
  url: string;
  protocols: string | string[];
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;

  constructor(url: string, protocols?: string | string[]) {
    this.url = url;
    this.protocols = protocols ?? [];
    FakeWebSocket.instances.push(this);
  }

  send(data: string) {
    this.sent.push(data);
  }

  close() {
    if (this.readyState === FakeWebSocket.CLOSED) return;
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.();
  }

  open() {
    this.onopen?.();
  }

  emit(data: unknown) {
    this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(data) }));
  }
}

const OriginalWebSocket = global.WebSocket;
/** random() === 0 pins every delay to the bottom of its equal-jitter window. */
const FIRST_RETRY_MS = RECONNECT_BASE_DELAY_MS / 2;

function latest(): FakeWebSocket {
  const socket = FakeWebSocket.instances.at(-1);
  if (!socket) throw new Error("no socket was created");
  return socket;
}

beforeEach(() => {
  FakeWebSocket.instances = [];
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  global.WebSocket = FakeWebSocket as any;
  setTokens("test-token");
  _resetChatSocket(() => 0);
  vi.useFakeTimers();
});

afterEach(() => {
  _resetChatSocket();
  vi.useRealTimers();
  global.WebSocket = OriginalWebSocket;
  clearTokens();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("computeBackoffDelay", () => {
  it("doubles each attempt and stays inside its jitter window", () => {
    const floors = [0, 1, 2, 3, 4].map((attempt) => computeBackoffDelay(attempt, () => 0));
    const ceilings = [0, 1, 2, 3, 4].map((attempt) => computeBackoffDelay(attempt, () => 1));

    expect(floors).toEqual([500, 1_000, 2_000, 4_000, 8_000]);
    expect(ceilings).toEqual([1_000, 2_000, 4_000, 8_000, 16_000]);
  });

  it("caps the delay and never overflows on a long outage", () => {
    for (const attempt of [10, 40, 1_000, Number.MAX_SAFE_INTEGER]) {
      const delay = computeBackoffDelay(attempt, () => 1);
      expect(delay).toBe(RECONNECT_MAX_DELAY_MS);
      expect(Number.isFinite(delay)).toBe(true);
    }
  });

  it("spreads clients across the window instead of firing in lockstep", () => {
    const low = computeBackoffDelay(3, () => 0);
    const high = computeBackoffDelay(3, () => 1);
    expect(high).toBeGreaterThan(low);
    expect(computeBackoffDelay(3, () => 0.5)).toBeGreaterThan(low);
  });
});

describe("chatSocket connection", () => {
  it("builds ws:// with exactly one /api/chat/ws path on an http origin", () => {
    acquireChatSocket({});

    expect(latest().url).toBe(`ws://${window.location.host}/api/chat/ws`);
    expect(latest().url.match(/\/api/g)).toHaveLength(1);
    expect(latest().url).not.toMatch(/[^:]\/\//);
  });

  it("builds wss:// on an https origin", async () => {
    vi.stubGlobal("location", {
      protocol: "https:",
      host: "nchat-dev.example.test",
    } as unknown as Location);
    vi.resetModules();

    const secure = await import("./chatSocket");
    secure._resetChatSocket(() => 0);
    secure.acquireChatSocket({});

    expect(latest().url).toBe("wss://nchat-dev.example.test/api/chat/ws");
    secure._resetChatSocket();
  });

  it("carries the token as a subprotocol and never in the URL", () => {
    acquireChatSocket({});

    expect(latest().protocols).toEqual(["test-token", "nchat.v1"]);
    expect(latest().url).not.toContain("test-token");
  });

  it("opens one socket for many consumers and closes it only with the last", () => {
    const first = acquireChatSocket({});
    const second = acquireChatSocket({});
    const third = acquireChatSocket({});

    expect(FakeWebSocket.instances).toHaveLength(1);

    first.release();
    second.release();
    expect(latest().readyState).toBe(FakeWebSocket.OPEN);

    third.release();
    expect(latest().readyState).toBe(FakeWebSocket.CLOSED);
  });

  it("gives a consumer that joins an open socket its own open callback, once", async () => {
    acquireChatSocket({});
    const socket = latest();
    socket.open();

    const onOpen = vi.fn();
    acquireChatSocket({ onOpen });
    // Deferred by a microtask so the consumer already holds its handle.
    expect(onOpen).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(0);
    expect(onOpen).toHaveBeenCalledExactlyOnceWith(1);

    // The generation is unchanged, so a second open must not be announced.
    socket.open();
    expect(onOpen).toHaveBeenCalledTimes(1);
    expect(FakeWebSocket.instances).toHaveLength(1);
  });

  it("does not announce a deferred open to a consumer that released first", async () => {
    acquireChatSocket({});
    latest().open();

    const onOpen = vi.fn();
    acquireChatSocket({ onOpen }).release();
    await vi.advanceTimersByTimeAsync(0);

    expect(onOpen).not.toHaveBeenCalled();
  });

  it("announces exactly one open per generation to every consumer", () => {
    const first = vi.fn();
    const second = vi.fn();
    acquireChatSocket({ onOpen: first });
    acquireChatSocket({ onOpen: second });

    latest().open();
    expect(first).toHaveBeenCalledExactlyOnceWith(1);
    expect(second).toHaveBeenCalledExactlyOnceWith(1);

    latest().close();
    vi.advanceTimersByTime(FIRST_RETRY_MS);
    latest().open();

    expect(first).toHaveBeenLastCalledWith(2);
    expect(second).toHaveBeenLastCalledWith(2);
    expect(first).toHaveBeenCalledTimes(2);
  });

  it("fans one server message out to every consumer, parsed once", () => {
    const first = vi.fn();
    const second = vi.fn();
    acquireChatSocket({ onMessage: first });
    acquireChatSocket({ onMessage: second });
    latest().open();

    latest().emit({ type: "message.created", target_id: "ch-1" });

    expect(first).toHaveBeenCalledExactlyOnceWith(
      { type: "message.created", target_id: "ch-1" },
      1,
    );
    expect(second).toHaveBeenCalledTimes(1);
  });

  it("drops malformed frames without reaching consumers", () => {
    const onMessage = vi.fn();
    acquireChatSocket({ onMessage });
    latest().open();

    latest().onmessage?.(new MessageEvent("message", { data: "not json" }));
    latest().onmessage?.(new MessageEvent("message", { data: "null" }));
    latest().onmessage?.(new MessageEvent("message", { data: "42" }));

    expect(onMessage).not.toHaveBeenCalled();
  });

  it("reconnects after a transient close using one timer only", () => {
    acquireChatSocket({});
    latest().close();

    expect(vi.getTimerCount()).toBe(1);
    // Repeated close callbacks from the same dead socket must not pile up.
    FakeWebSocket.instances[0].onclose?.();
    FakeWebSocket.instances[0].onclose?.();
    expect(vi.getTimerCount()).toBe(1);

    vi.advanceTimersByTime(FIRST_RETRY_MS);
    expect(FakeWebSocket.instances).toHaveLength(2);
  });

  it("keeps doubling while a socket opens and drops before it is stable", () => {
    acquireChatSocket({});

    for (const expected of [FIRST_RETRY_MS, 2 * FIRST_RETRY_MS, 4 * FIRST_RETRY_MS]) {
      const before = FakeWebSocket.instances.length;
      latest().open();
      latest().close();
      vi.advanceTimersByTime(expected - 1);
      expect(FakeWebSocket.instances).toHaveLength(before);
      vi.advanceTimersByTime(1);
      expect(FakeWebSocket.instances).toHaveLength(before + 1);
    }
  });

  it("resets the backoff only after the connection has held", () => {
    acquireChatSocket({});
    latest().open();
    latest().close();
    vi.advanceTimersByTime(FIRST_RETRY_MS);

    latest().open();
    vi.advanceTimersByTime(STABLE_CONNECTION_MS);
    latest().close();

    // Forgiven: the next wait is the floor again, not the doubled one.
    vi.advanceTimersByTime(FIRST_RETRY_MS - 1);
    expect(FakeWebSocket.instances).toHaveLength(2);
    vi.advanceTimersByTime(1);
    expect(FakeWebSocket.instances).toHaveLength(3);
  });

  it("stops retrying after a bounded run of handshakes that never open", () => {
    acquireChatSocket({});

    for (let i = 0; i < MAX_CONSECUTIVE_FAILURES + 4; i += 1) {
      latest().close();
      vi.advanceTimersByTime(RECONNECT_MAX_DELAY_MS);
    }

    expect(FakeWebSocket.instances).toHaveLength(MAX_CONSECUTIVE_FAILURES);
    expect(getChatSocketStatus()).toBe("failed");
    expect(vi.getTimerCount()).toBe(0);
  });

  it("suspends while offline and resumes with a single attempt when back online", () => {
    acquireChatSocket({});
    vi.spyOn(navigator, "onLine", "get").mockReturnValue(false);

    latest().close();
    expect(vi.getTimerCount()).toBe(0);
    vi.advanceTimersByTime(RECONNECT_MAX_DELAY_MS * 4);
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(getChatSocketStatus()).toBe("disconnected");

    vi.spyOn(navigator, "onLine", "get").mockReturnValue(true);
    window.dispatchEvent(new Event("online"));

    expect(FakeWebSocket.instances).toHaveLength(2);
    window.dispatchEvent(new Event("online"));
    expect(FakeWebSocket.instances).toHaveLength(2);
  });

  it("does not connect without a session and connects once one arrives", () => {
    clearTokens();
    _resetChatSocket(() => 0);
    acquireChatSocket({});

    expect(FakeWebSocket.instances).toHaveLength(0);
    expect(getChatSocketStatus()).toBe("disconnected");

    setTokens("fresh-token");
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(latest().protocols).toEqual(["fresh-token", "nchat.v1"]);
  });

  it("stops the connection on logout and does not reconnect", () => {
    acquireChatSocket({});
    latest().open();

    clearTokens();

    expect(latest().readyState).toBe(FakeWebSocket.CLOSED);
    vi.advanceTimersByTime(RECONNECT_MAX_DELAY_MS * 4);
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(getChatSocketStatus()).toBe("disconnected");
  });

  it("replaces the socket when the session changes", () => {
    acquireChatSocket({});
    latest().open();

    setTokens("second-session-token");

    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(FakeWebSocket.instances[0].readyState).toBe(FakeWebSocket.CLOSED);
    expect(latest().protocols).toEqual(["second-session-token", "nchat.v1"]);
  });

  it("cancels the pending retry and drops late callbacks on release", () => {
    const onOpen = vi.fn();
    const onMessage = vi.fn();
    const handle = acquireChatSocket({ onOpen, onMessage });
    const socket = latest();
    socket.close();

    handle.release();

    expect(vi.getTimerCount()).toBe(0);
    vi.advanceTimersByTime(RECONNECT_MAX_DELAY_MS * 4);
    expect(FakeWebSocket.instances).toHaveLength(1);

    // A callback that was already in flight must not revive anything.
    socket.onopen?.();
    socket.onmessage?.(new MessageEvent("message", { data: JSON.stringify({ type: "x" }) }));
    expect(onOpen).not.toHaveBeenCalled();
    expect(onMessage).not.toHaveBeenCalled();
    expect(handle.send({ type: "x" })).toBe(false);
  });

  it("reports the state transitions a caller can render", () => {
    const seen: ChatSocketStatus[] = [];
    acquireChatSocket({ onStatus: (status) => seen.push(status) });

    latest().open();
    latest().close();
    vi.advanceTimersByTime(FIRST_RETRY_MS);
    latest().open();

    expect(seen).toEqual(["connecting", "connected", "reconnecting", "connected"]);
    expect(getChatSocketStatus()).toBe("connected");
  });

  it("refuses to send on a socket that is not open", () => {
    const handle = acquireChatSocket({});
    latest().readyState = FakeWebSocket.CLOSED;

    expect(handle.send({ type: "ping" })).toBe(false);
    expect(handle.isOpen()).toBe(false);
  });

  it("keeps the console quiet across a long outage", () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    acquireChatSocket({});

    for (let i = 0; i < MAX_CONSECUTIVE_FAILURES; i += 1) {
      latest().close();
      vi.advanceTimersByTime(RECONNECT_MAX_DELAY_MS);
    }

    expect(error).not.toHaveBeenCalled();
    expect(warn).not.toHaveBeenCalled();
  });
});
