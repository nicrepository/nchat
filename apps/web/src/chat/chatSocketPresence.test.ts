/**
 * chatSocket — human activity and shared subscription ownership (issue #444).
 *
 * Two properties this connection has to hold that no single hook can:
 *
 *  - presence follows a *person*, so the frame that keeps someone online is sent
 *    when they do something and never on a timer. An abandoned tab must go away;
 *    a tab being typed into must not.
 *  - a remote subscription belongs to the connection, not to whichever view
 *    happened to ask for it first. Two views wanting one conversation is one
 *    subscribe, and the first of them closing is not an unsubscribe.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearTokens, setTokens } from "../lib/authSession";
import {
  _resetChatSocket,
  acquireChatSocket,
  markPresenceActivity,
  PRESENCE_ACTIVITY_THROTTLE_MS,
  releaseConsumerSubscriptions,
  setConsumerSubscriptions,
  type ChatSocketHandle,
  type ChatSubscriptionTarget,
} from "./chatSocket";

class FakeWebSocket {
  static OPEN = 1;
  static CLOSED = 3;
  static instances: FakeWebSocket[] = [];

  readyState = FakeWebSocket.OPEN;
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;

  constructor() {
    FakeWebSocket.instances.push(this);
  }

  send(data: string) {
    this.sent.push(data);
  }

  close(code = 1006) {
    if (this.readyState === FakeWebSocket.CLOSED) return;
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.(new CloseEvent("close", { code }));
  }

  open() {
    this.onopen?.();
  }

  emit(data: unknown) {
    this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(data) }));
  }
}

const OriginalWebSocket = global.WebSocket;

function latest(): FakeWebSocket {
  const socket = FakeWebSocket.instances.at(-1);
  if (!socket) throw new Error("no socket was created");
  return socket;
}

function frames(socket: FakeWebSocket = latest()): Array<Record<string, unknown>> {
  return socket.sent.map((raw) => JSON.parse(raw) as Record<string, unknown>);
}

function framesOfType(type: string, socket: FakeWebSocket = latest()) {
  return frames(socket).filter((frame) => frame["type"] === type);
}

const channel = (targetId: string): ChatSubscriptionTarget => ({ kind: "channel", targetId });

/** Opens the shared connection and returns the handle keeping it alive. */
function openConnection(): ChatSocketHandle {
  const handle = acquireChatSocket({});
  latest().open();
  return handle;
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
  vi.restoreAllMocks();
});

// ── activity ─────────────────────────────────────────────────────────────────

describe("presence activity", () => {
  it("reports real activity to the server", () => {
    const handle = openConnection();

    window.dispatchEvent(new KeyboardEvent("keydown", { key: "a" }));

    expect(framesOfType("ping")).toHaveLength(1);
    handle.release();
  });

  it("treats keyboard, pointer, touch and wheel input as activity", () => {
    const handle = openConnection();

    for (const event of [
      new KeyboardEvent("keydown", { key: "a" }),
      new Event("pointerdown"),
      new Event("touchstart"),
      new Event("wheel"),
    ]) {
      window.dispatchEvent(event);
      vi.advanceTimersByTime(PRESENCE_ACTIVITY_THROTTLE_MS);
    }

    expect(framesOfType("ping")).toHaveLength(4);
    handle.release();
  });

  it("collapses a burst of input into one frame", () => {
    const handle = openConnection();

    for (let i = 0; i < 100; i += 1) {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "a" }));
      vi.advanceTimersByTime(100);
    }

    // Ten seconds of continuous typing, well inside one throttle window.
    expect(framesOfType("ping")).toHaveLength(1);
    handle.release();
  });

  it("reports again once the throttle window has passed", () => {
    const handle = openConnection();

    window.dispatchEvent(new KeyboardEvent("keydown", { key: "a" }));
    vi.advanceTimersByTime(PRESENCE_ACTIVITY_THROTTLE_MS + 1);
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "a" }));

    expect(framesOfType("ping")).toHaveLength(2);
    handle.release();
  });

  // The property that makes away detection work at all. An open tab is not a
  // person, and a heartbeat here would make the server unable to tell them apart.
  it("sends nothing at all while nobody touches the tab", () => {
    const handle = openConnection();

    vi.advanceTimersByTime(30 * 60 * 1_000);

    expect(framesOfType("ping")).toHaveLength(0);
    handle.release();
  });

  it("does not treat mouse movement as a person being there", () => {
    const handle = openConnection();

    for (let i = 0; i < 20; i += 1) {
      window.dispatchEvent(new MouseEvent("mousemove"));
      vi.advanceTimersByTime(PRESENCE_ACTIVITY_THROTTLE_MS + 1);
    }

    expect(framesOfType("ping")).toHaveLength(0);
    handle.release();
  });

  it("reports the tab becoming visible again, and not it being left", () => {
    const handle = openConnection();
    const visibility = vi.spyOn(document, "visibilityState", "get");

    visibility.mockReturnValue("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    expect(framesOfType("ping")).toHaveLength(0);

    visibility.mockReturnValue("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    expect(framesOfType("ping")).toHaveLength(1);

    handle.release();
  });

  it("installs one set of listeners however many consumers join", () => {
    const first = openConnection();
    const second = acquireChatSocket({});
    const third = acquireChatSocket({});

    window.dispatchEvent(new KeyboardEvent("keydown", { key: "a" }));

    expect(framesOfType("ping")).toHaveLength(1);
    first.release();
    second.release();
    third.release();
  });

  it("stops listening once the last consumer leaves", () => {
    const handle = openConnection();
    const socket = latest();
    handle.release();

    window.dispatchEvent(new KeyboardEvent("keydown", { key: "a" }));

    expect(framesOfType("ping", socket)).toHaveLength(0);
  });

  it("says nothing when there is no open connection to say it on", () => {
    expect(() => markPresenceActivity()).not.toThrow();
    expect(FakeWebSocket.instances).toHaveLength(0);
  });

  it("reports the first activity after a reconnect without waiting out a window", () => {
    const handle = openConnection();
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "a" }));
    expect(framesOfType("ping")).toHaveLength(1);

    latest().close();
    vi.advanceTimersByTime(5_000);
    latest().open();

    window.dispatchEvent(new KeyboardEvent("keydown", { key: "a" }));
    expect(framesOfType("ping")).toHaveLength(1);
    handle.release();
  });
});

// ── shared subscription ownership ────────────────────────────────────────────

describe("subscription ownership", () => {
  it("subscribes on the first consumer and unsubscribes on the last", () => {
    const handle = openConnection();

    setConsumerSubscriptions("a", [channel("x")]);
    expect(framesOfType("subscribe")).toHaveLength(1);

    // B wants the same conversation. The connection already has it.
    setConsumerSubscriptions("b", [channel("x")]);
    expect(framesOfType("subscribe")).toHaveLength(1);

    // B goes. A is still reading it, so nothing leaves the tab.
    releaseConsumerSubscriptions("b");
    expect(framesOfType("unsubscribe")).toHaveLength(0);

    // A goes. Now nobody wants it.
    releaseConsumerSubscriptions("a");
    expect(framesOfType("unsubscribe")).toEqual([
      { type: "unsubscribe", target_type: "channel", target_id: "x" },
    ]);
    handle.release();
  });

  it("keeps a target one consumer still wants when another moves off it", () => {
    const handle = openConnection();

    // The sidebar watches X; the open conversation is also X.
    setConsumerSubscriptions("sidebar", [channel("x")]);
    setConsumerSubscriptions("messages", [channel("x")]);

    // The conversation navigates to Y.
    setConsumerSubscriptions("messages", [channel("y")]);

    expect(framesOfType("unsubscribe")).toHaveLength(0);
    expect(framesOfType("subscribe")).toEqual([
      { type: "subscribe", target_type: "channel", target_id: "x" },
      { type: "subscribe", target_type: "channel", target_id: "y" },
    ]);
    handle.release();
  });

  it("survives a double cleanup without dropping a live subscription", () => {
    const handle = openConnection();

    setConsumerSubscriptions("a", [channel("x")]);
    setConsumerSubscriptions("b", [channel("x")]);

    // StrictMode runs a cleanup twice; the second must change nothing.
    releaseConsumerSubscriptions("b");
    releaseConsumerSubscriptions("b");

    expect(framesOfType("unsubscribe")).toHaveLength(0);

    releaseConsumerSubscriptions("a");
    expect(framesOfType("unsubscribe")).toHaveLength(1);
    handle.release();
  });

  it("re-declaring the same targets sends nothing", () => {
    const handle = openConnection();

    setConsumerSubscriptions("a", [channel("x"), channel("y")]);
    setConsumerSubscriptions("a", [channel("y"), channel("x")]);

    expect(framesOfType("subscribe")).toHaveLength(2);
    expect(framesOfType("unsubscribe")).toHaveLength(0);
    handle.release();
  });

  it("restores each target exactly once after a reconnect", () => {
    const handle = openConnection();
    setConsumerSubscriptions("a", [channel("x"), channel("y")]);
    setConsumerSubscriptions("b", [channel("x")]);

    latest().close();
    vi.advanceTimersByTime(5_000);
    latest().open();

    const restored = framesOfType("subscribe");
    expect(restored).toHaveLength(2);
    expect(restored.map((frame) => frame["target_id"]).sort()).toEqual(["x", "y"]);
    handle.release();
  });

  it("tells a joining consumer which of its targets are already live", () => {
    const handle = openConnection();

    expect(setConsumerSubscriptions("a", [channel("x")])).toEqual([]);
    // No acknowledgement yet: the connection cannot claim a subscription the
    // server has not confirmed.
    expect(setConsumerSubscriptions("b", [channel("x")])).toEqual([]);

    latest().emit({
      type: "subscribed",
      operation: "subscribe",
      target_type: "channel",
      target_id: "x",
    });

    // Now it can, which is what spares the joiner an acknowledgement that is
    // never coming because no frame was sent for it.
    expect(setConsumerSubscriptions("c", [channel("x")])).toEqual(["channel:x"]);
    handle.release();
  });

  it("forgets acknowledgements when the connection is replaced", () => {
    const handle = openConnection();
    setConsumerSubscriptions("a", [channel("x")]);
    latest().emit({
      type: "subscribed",
      operation: "subscribe",
      target_type: "channel",
      target_id: "x",
    });

    latest().close();
    vi.advanceTimersByTime(5_000);
    latest().open();

    expect(setConsumerSubscriptions("b", [channel("x")])).toEqual([]);
    handle.release();
  });

  it("carries nothing across a session change", () => {
    const handle = openConnection();
    setConsumerSubscriptions("a", [channel("x")]);

    setTokens("a-different-session");
    latest().open();

    // Nothing was replayed for the previous identity; the consumer declares
    // again under the new one.
    expect(framesOfType("subscribe")).toHaveLength(0);
    setConsumerSubscriptions("a", [channel("x")]);
    expect(framesOfType("subscribe")).toHaveLength(1);
    handle.release();
  });
});
