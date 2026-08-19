import { describe, expect, it, vi } from "vitest";

import type { ChatSocketHandle, ChatSocketListener } from "./chatSocket";
import { resolveCall, startResourceCall } from "./resourceCallSignaling";

const call = {
  call_id: "00000000-0000-4000-8000-000000000546",
  request_id: "00000000-0000-4000-8000-000000000547",
  caller_id: "00000000-0000-4000-8000-000000000548",
  callee_id: "",
  target_type: "channel" as const,
  target_id: "00000000-0000-4000-8000-000000000549",
  call_type: "audio" as const,
  status: "active" as const,
  version: 1,
  created_at: "2026-08-18T12:00:00Z",
  occurred_at: "2026-08-18T12:00:00Z",
  expires_at: "2026-08-18T12:00:30Z",
};

function setup() {
  let listener!: ChatSocketListener;
  const handle: ChatSocketHandle = {
    send: vi.fn(() => true),
    isOpen: vi.fn(() => true),
    generation: vi.fn(() => 1),
    release: vi.fn(),
  };
  const acquire = vi.fn((value: ChatSocketListener) => {
    listener = value;
    return handle;
  });
  return {
    acquire,
    handle,
    get listener() {
      return listener;
    },
  };
}

describe("resource call signaling", () => {
  it("resolves a call by id through authenticated call.sync", async () => {
    const socket = setup();
    const resolving = resolveCall(call.call_id, {
      acquire: socket.acquire,
      setTimeout: vi.fn(() => 1),
      clearTimeout: vi.fn(),
    });

    socket.listener.onOpen?.(1);
    expect(socket.handle.send).toHaveBeenCalledWith({ type: "call.sync", call_id: call.call_id });
    socket.listener.onMessage?.(
      {
        type: "call.accepted",
        event_id: "event",
        target_type: "channel",
        target_id: call.target_id,
        call,
      },
      1,
    );
    socket.listener.onStatus?.("connected");
    socket.listener.onMessage?.(
      {
        type: "call.accepted",
        event_id: "duplicate",
        target_type: "channel",
        target_id: call.target_id,
        call,
      },
      1,
    );

    await expect(resolving).resolves.toEqual(call);
    expect(socket.handle.release).toHaveBeenCalledOnce();
  });

  it("resends one idempotent command on open and resolves the authoritative call event", async () => {
    const socket = setup();
    const starting = startResourceCall(
      { kind: "channel", id: call.target_id },
      {
        acquire: socket.acquire,
        requestId: () => call.request_id,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      },
    );

    socket.listener.onOpen?.(1);
    expect(socket.handle.send).toHaveBeenCalledWith({
      type: "call.start",
      request_id: call.request_id,
      target_type: "channel",
      target_id: call.target_id,
      call_type: "audio",
    });
    socket.listener.onMessage?.(
      {
        type: "call.accepted",
        event_id: "event",
        target_type: "channel",
        target_id: call.target_id,
        call,
      },
      1,
    );
    socket.listener.onStatus?.("connected");
    socket.listener.onMessage?.({}, 1);
    socket.listener.onMessage?.(
      {
        type: "call.accepted",
        event_id: "duplicate",
        target_type: "channel",
        target_id: call.target_id,
        call,
      },
      1,
    );

    await expect(starting).resolves.toEqual(call);
    expect(socket.handle.release).toHaveBeenCalledOnce();
  });

  it("fails closed on a correlated server error", async () => {
    const socket = setup();
    const starting = startResourceCall(
      { kind: "channel", id: call.target_id },
      {
        acquire: socket.acquire,
        requestId: () => call.request_id,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      },
    );

    socket.listener.onMessage?.(
      { type: "call.error", operation: "call.start", code: "call_not_found" },
      1,
    );

    await expect(starting).rejects.toThrow("resource call start failed");
    expect(socket.handle.release).toHaveBeenCalledOnce();
  });

  it("handles a synchronous socket failure during acquisition", async () => {
    const socket = setup();
    socket.acquire.mockImplementationOnce((listener) => {
      listener.onStatus?.("failed");
      return socket.handle;
    });

    await expect(
      startResourceCall(
        { kind: "channel", id: call.target_id },
        { acquire: socket.acquire, setTimeout: vi.fn(() => 1), clearTimeout: vi.fn() },
      ),
    ).rejects.toThrow("resource call start failed");
    expect(socket.handle.release).toHaveBeenCalledOnce();
  });

  it("handles a synchronous call.sync failure during acquisition", async () => {
    const socket = setup();
    socket.acquire.mockImplementationOnce((listener) => {
      listener.onStatus?.("failed");
      return socket.handle;
    });

    await expect(
      resolveCall(call.call_id, {
        acquire: socket.acquire,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      }),
    ).rejects.toThrow("call sync failed");
    expect(socket.handle.release).toHaveBeenCalledOnce();
  });

  it("fails deterministically when start cannot send, the socket fails, or its deadline fires", async () => {
    const cannotSend = setup();
    cannotSend.handle.send = vi.fn(() => false);
    const sendFailure = startResourceCall(
      { kind: "channel", id: call.target_id },
      { acquire: cannotSend.acquire, setTimeout: vi.fn(() => 1), clearTimeout: vi.fn() },
    );
    cannotSend.listener.onOpen?.(1);
    await expect(sendFailure).rejects.toThrow("resource call start failed");

    const failed = setup();
    const socketFailure = startResourceCall(
      { kind: "channel", id: call.target_id },
      { acquire: failed.acquire, setTimeout: vi.fn(() => 1), clearTimeout: vi.fn() },
    );
    failed.listener.onStatus?.("failed");
    await expect(socketFailure).rejects.toThrow("resource call start failed");

    const timed = setup();
    let expire: () => void = () => undefined;
    const timeout = startResourceCall(
      { kind: "channel", id: call.target_id },
      {
        acquire: timed.acquire,
        setTimeout: (callback) => {
          expire = callback;
          return 1;
        },
        clearTimeout: vi.fn(),
      },
    );
    expire();
    await expect(timeout).rejects.toThrow("resource call start timed out");
  });

  it("ignores unrelated events and fails closed for every sync failure path", async () => {
    const socket = setup();
    let expire: () => void = () => undefined;
    const resolving = resolveCall(call.call_id, {
      acquire: socket.acquire,
      setTimeout: (callback) => {
        expire = callback;
        return 1;
      },
      clearTimeout: vi.fn(),
    });
    socket.listener.onMessage?.(
      {
        type: "call.accepted",
        event_id: "other",
        target_type: "channel",
        target_id: call.target_id,
        call: { ...call, call_id: "other" },
      },
      1,
    );
    expire();
    await expect(resolving).rejects.toThrow("call sync timed out");

    const denied = setup();
    const deniedSync = resolveCall(call.call_id, {
      acquire: denied.acquire,
      setTimeout: vi.fn(() => 1),
      clearTimeout: vi.fn(),
    });
    denied.listener.onMessage?.({ type: "call.error", operation: "call.sync" }, 1);
    await expect(deniedSync).rejects.toThrow("call sync failed");

    const failed = setup();
    const failedSync = resolveCall(call.call_id, {
      acquire: failed.acquire,
      setTimeout: vi.fn(() => 1),
      clearTimeout: vi.fn(),
    });
    failed.listener.onStatus?.("failed");
    await expect(failedSync).rejects.toThrow("call sync failed");

    const cannotSend = setup();
    cannotSend.handle.send = vi.fn(() => false);
    const sendFailure = resolveCall(call.call_id, {
      acquire: cannotSend.acquire,
      setTimeout: vi.fn(() => 1),
      clearTimeout: vi.fn(),
    });
    cannotSend.listener.onOpen?.(1);
    await expect(sendFailure).rejects.toThrow("call sync failed");
  });
});
