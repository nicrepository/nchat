import { describe, expect, it, vi } from "vitest";

import type { ChatSocketHandle, ChatSocketListener } from "./chatSocket";
import {
  joinResourceCall,
  leaveResourceCall,
  resolveCall,
  ResourceCallSignalingError,
  startResourceCall,
  syncResourceCall,
} from "./resourceCallSignaling";

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

const target = { kind: "channel" as const, id: call.target_id };
const requestID = "00000000-0000-4000-8000-000000000601";
const participationID = "00000000-0000-4000-8000-000000000602";

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

function admitted(overrides: Record<string, unknown> = {}) {
  return {
    type: "call.admitted",
    operation: "call.start",
    response_to: requestID,
    participation_id: participationID,
    call,
    ...overrides,
  };
}

describe("resource call signaling", () => {
  describe("startResourceCall", () => {
    it("sends call.start with the command's own request_id", async () => {
      const socket = setup();
      void startResourceCall(target, {
        acquire: socket.acquire,
        requestId: () => requestID,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      socket.listener.onOpen?.(1);
      expect(socket.handle.send).toHaveBeenCalledWith({
        type: "call.start",
        request_id: requestID,
        target_type: "channel",
        target_id: call.target_id,
        call_type: "audio",
      });
    });

    it("resolves solely on call.admitted correlated by response_to === request_id", async () => {
      const socket = setup();
      const starting = startResourceCall(target, {
        acquire: socket.acquire,
        requestId: () => requestID,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      socket.listener.onMessage?.(admitted(), 1);
      await expect(starting).resolves.toEqual({ call, participationId: participationID });
      expect(socket.handle.release).toHaveBeenCalledOnce();
    });

    it("never depends on admitted.call.request_id — B reusing A's call still resolves with A's historical request_id intact", async () => {
      const socket = setup();
      const starting = startResourceCall(target, {
        acquire: socket.acquire,
        requestId: () => requestID, // B's own current request_id
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      const historicalCall = { ...call, request_id: "00000000-0000-4000-8000-0000000009aa" }; // A's original
      socket.listener.onMessage?.(admitted({ response_to: requestID, call: historicalCall }), 1);
      await expect(starting).resolves.toEqual({
        call: historicalCall,
        participationId: participationID,
      });
    });

    it("ignores an admitted response for an unrelated request_id or operation", async () => {
      const socket = setup();
      const starting = startResourceCall(target, {
        acquire: socket.acquire,
        requestId: () => requestID,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      socket.listener.onMessage?.(admitted({ response_to: "some-other-request" }), 1);
      socket.listener.onMessage?.(admitted({ operation: "call.join" }), 1);
      socket.listener.onMessage?.(admitted(), 1);
      await expect(starting).resolves.toEqual({ call, participationId: participationID });
    });

    it("ignores an unrelated call.error (different request_id/operation) without rejecting", async () => {
      const socket = setup();
      const starting = startResourceCall(target, {
        acquire: socket.acquire,
        requestId: () => requestID,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      socket.listener.onMessage?.(
        { type: "call.error", operation: "call.start", code: "x", response_to: "other" },
        1,
      );
      socket.listener.onMessage?.(
        { type: "call.error", operation: "call.join", code: "x", response_to: requestID },
        1,
      );
      socket.listener.onMessage?.(admitted(), 1);
      await expect(starting).resolves.toEqual({ call, participationId: participationID });
    });

    it("rejects with the correlated call.error's code", async () => {
      const socket = setup();
      const starting = startResourceCall(target, {
        acquire: socket.acquire,
        requestId: () => requestID,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      socket.listener.onMessage?.(
        {
          type: "call.error",
          operation: "call.start",
          code: "call_participant_busy",
          response_to: requestID,
        },
        1,
      );
      await expect(starting).rejects.toMatchObject(
        new ResourceCallSignalingError("call.start", "call_participant_busy"),
      );
      expect(socket.handle.release).toHaveBeenCalledOnce();
    });

    it("never resolves from the call.accepted lifecycle broadcast — only call.admitted is the ACK", async () => {
      const socket = setup();
      let expire: () => void = () => undefined;
      const starting = startResourceCall(target, {
        acquire: socket.acquire,
        requestId: () => requestID,
        setTimeout: (callback) => {
          expire = callback;
          return 1;
        },
        clearTimeout: vi.fn(),
      });
      socket.listener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "broadcast",
          target_type: "channel",
          target_id: call.target_id,
          call,
        },
        1,
      );
      expire();
      await expect(starting).rejects.toThrow("resource call start timed out");
    });

    it("rejects a malformed admitted.call payload as if nothing arrived", async () => {
      const socket = setup();
      let expire: () => void = () => undefined;
      const starting = startResourceCall(target, {
        acquire: socket.acquire,
        requestId: () => requestID,
        setTimeout: (callback) => {
          expire = callback;
          return 1;
        },
        clearTimeout: vi.fn(),
      });
      socket.listener.onMessage?.(admitted({ call: { ...call, status: "ringing" } }), 1);
      socket.listener.onMessage?.(admitted({ call: { ...call, target_id: "wrong" } }), 1);
      socket.listener.onMessage?.(admitted({ participation_id: undefined }), 1);
      socket.listener.onMessage?.(
        admitted({ participation_id: "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA" }),
        1,
      );
      expire();
      await expect(starting).rejects.toThrow("resource call start timed out");
    });

    it("fails deterministically when start cannot send, the socket fails, or its deadline fires", async () => {
      const cannotSend = setup();
      cannotSend.handle.send = vi.fn(() => false);
      const sendFailure = startResourceCall(target, {
        acquire: cannotSend.acquire,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      cannotSend.listener.onOpen?.(1);
      await expect(sendFailure).rejects.toThrow("resource call start failed");
      expect(cannotSend.handle.release).toHaveBeenCalledOnce();

      const failed = setup();
      const socketFailure = startResourceCall(target, {
        acquire: failed.acquire,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      failed.listener.onStatus?.("failed");
      await expect(socketFailure).rejects.toThrow("resource call start failed");
      expect(failed.handle.release).toHaveBeenCalledOnce();

      const timed = setup();
      let expire: () => void = () => undefined;
      const timeout = startResourceCall(target, {
        acquire: timed.acquire,
        setTimeout: (callback) => {
          expire = callback;
          return 1;
        },
        clearTimeout: vi.fn(),
      });
      expire();
      await expect(timeout).rejects.toThrow("resource call start timed out");
      expect(timed.handle.release).toHaveBeenCalledOnce();
    });

    // Issue #622 round 2 adversarial audit (section 2): two independent
    // startResourceCall() calls sharing one real underlying socket (every
    // acquireChatSocket consumer fans out from the SAME dispatch, unlike
    // this file's setup() which only ever tracks one listener at a time) —
    // an error or admitted response addressed to one request must never
    // resolve or reject the other, since correlation is by response_to
    // alone, checked against each call's own closed-over request_id.
    it("two concurrent starts sharing one socket never cross-resolve or cross-reject each other", async () => {
      const listeners: ChatSocketListener[] = [];
      const handle: ChatSocketHandle = {
        send: vi.fn(() => true),
        isOpen: vi.fn(() => true),
        generation: vi.fn(() => 1),
        release: vi.fn(),
      };
      const acquire = vi.fn((value: ChatSocketListener) => {
        listeners.push(value);
        return handle;
      });
      const broadcast = (data: Record<string, unknown>) => {
        for (const listener of listeners) listener.onMessage?.(data, 1);
      };

      const startingA = startResourceCall(target, { acquire, requestId: () => "request-a" });
      const startingB = startResourceCall(target, { acquire, requestId: () => "request-b" });
      let settledB: "pending" | "settled" = "pending";
      void startingB.then(
        () => (settledB = "settled"),
        () => (settledB = "settled"),
      );

      // An error addressed to A must never resolve or reject B.
      broadcast({
        type: "call.error",
        operation: "call.start",
        code: "call_participant_busy",
        response_to: "request-a",
      });
      await Promise.resolve();
      await Promise.resolve();
      expect(settledB).toBe("pending");
      await expect(startingA).rejects.toBeInstanceOf(ResourceCallSignalingError);

      // B's own admitted response still resolves B, unaffected by A having
      // already settled.
      const callB = { ...call, call_id: "00000000-0000-4000-8000-0000000009bb" };
      broadcast({
        type: "call.admitted",
        operation: "call.start",
        response_to: "request-b",
        participation_id: participationID,
        call: callB,
      });
      await expect(startingB).resolves.toEqual({ call: callB, participationId: participationID });
    });
  });

  describe("joinResourceCall", () => {
    it("sends call.join with call_id, target and request_id", () => {
      const socket = setup();
      void joinResourceCall(call.call_id, target, {
        acquire: socket.acquire,
        requestId: () => requestID,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      socket.listener.onOpen?.(1);
      expect(socket.handle.send).toHaveBeenCalledWith({
        type: "call.join",
        request_id: requestID,
        call_id: call.call_id,
        target_type: "channel",
        target_id: call.target_id,
      });
    });

    it("resolves solely on call.admitted operation=call.join correlated by response_to", async () => {
      const socket = setup();
      const joining = joinResourceCall(call.call_id, target, {
        acquire: socket.acquire,
        requestId: () => requestID,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      socket.listener.onMessage?.(admitted({ operation: "call.start" }), 1); // wrong operation, ignored
      socket.listener.onMessage?.(admitted({ operation: "call.join" }), 1);
      await expect(joining).resolves.toEqual({ call, participationId: participationID });
    });

    it("validates admitted.call.call_id against the requested call_id", async () => {
      const socket = setup();
      let expire: () => void = () => undefined;
      const joining = joinResourceCall(call.call_id, target, {
        acquire: socket.acquire,
        requestId: () => requestID,
        setTimeout: (callback) => {
          expire = callback;
          return 1;
        },
        clearTimeout: vi.fn(),
      });
      socket.listener.onMessage?.(
        admitted({ operation: "call.join", call: { ...call, call_id: "some-other-call" } }),
        1,
      );
      expire();
      await expect(joining).rejects.toThrow("resource call join timed out");
    });

    it("does not resolve on a target mismatch", async () => {
      const socket = setup();
      let expire: () => void = () => undefined;
      const joining = joinResourceCall(call.call_id, target, {
        acquire: socket.acquire,
        requestId: () => requestID,
        setTimeout: (callback) => {
          expire = callback;
          return 1;
        },
        clearTimeout: vi.fn(),
      });
      socket.listener.onMessage?.(
        admitted({ operation: "call.join", call: { ...call, target_id: "wrong-target" } }),
        1,
      );
      expire();
      await expect(joining).rejects.toThrow("resource call join timed out");
    });

    it("rejects with the correlated call.error's code, ignoring unrelated ones", async () => {
      const socket = setup();
      const joining = joinResourceCall(call.call_id, target, {
        acquire: socket.acquire,
        requestId: () => requestID,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      socket.listener.onMessage?.(
        { type: "call.error", operation: "call.join", code: "x", response_to: "other" },
        1,
      );
      socket.listener.onMessage?.(
        {
          type: "call.error",
          operation: "call.join",
          code: "call_invalid_state",
          response_to: requestID,
        },
        1,
      );
      await expect(joining).rejects.toMatchObject(
        new ResourceCallSignalingError("call.join", "call_invalid_state"),
      );
    });

    it("fails deterministically when send fails, the socket fails, or its deadline fires", async () => {
      const cannotSend = setup();
      cannotSend.handle.send = vi.fn(() => false);
      const sendFailure = joinResourceCall(call.call_id, target, {
        acquire: cannotSend.acquire,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      cannotSend.listener.onOpen?.(1);
      await expect(sendFailure).rejects.toThrow("resource call join failed");
      expect(cannotSend.handle.release).toHaveBeenCalledOnce();

      const failed = setup();
      const socketFailure = joinResourceCall(call.call_id, target, {
        acquire: failed.acquire,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      failed.listener.onStatus?.("failed");
      await expect(socketFailure).rejects.toThrow("resource call join failed");
      expect(failed.handle.release).toHaveBeenCalledOnce();
    });
  });

  describe("syncResourceCall", () => {
    const syncId = "00000000-0000-4000-8000-000000000602";

    it("sends call.resource.sync with sync_id and target", () => {
      const socket = setup();
      void syncResourceCall(target, {
        acquire: socket.acquire,
        syncId: () => syncId,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      socket.listener.onOpen?.(1);
      expect(socket.handle.send).toHaveBeenCalledWith({
        type: "call.resource.sync",
        sync_id: syncId,
        target_type: "channel",
        target_id: call.target_id,
      });
    });

    it("resolves with the active call when found", async () => {
      const socket = setup();
      const syncing = syncResourceCall(target, {
        acquire: socket.acquire,
        syncId: () => syncId,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      socket.listener.onMessage?.(
        {
          type: "call.resource.synced",
          sync_id: syncId,
          target_type: "channel",
          target_id: call.target_id,
          call,
          observed_at: "2026-08-21T12:00:00.000000Z",
        },
        1,
      );
      await expect(syncing).resolves.toEqual({
        call,
        observedAt: "2026-08-21T12:00:00.000000Z",
      });
    });

    it("resolves with call: null — a valid, expected discovery result", async () => {
      const socket = setup();
      const syncing = syncResourceCall(target, {
        acquire: socket.acquire,
        syncId: () => syncId,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      socket.listener.onMessage?.(
        {
          type: "call.resource.synced",
          sync_id: syncId,
          target_type: "channel",
          target_id: call.target_id,
          call: null,
          observed_at: "2026-08-21T12:00:00.000000Z",
        },
        1,
      );
      await expect(syncing).resolves.toEqual({
        call: null,
        observedAt: "2026-08-21T12:00:00.000000Z",
      });
    });

    it("ignores a synced response with the wrong sync_id", async () => {
      const socket = setup();
      let expire: () => void = () => undefined;
      const syncing = syncResourceCall(target, {
        acquire: socket.acquire,
        syncId: () => syncId,
        setTimeout: (callback) => {
          expire = callback;
          return 1;
        },
        clearTimeout: vi.fn(),
      });
      socket.listener.onMessage?.(
        {
          type: "call.resource.synced",
          sync_id: "wrong-sync-id",
          target_type: "channel",
          target_id: call.target_id,
          call: null,
          observed_at: "2026-08-21T12:00:00.000000Z",
        },
        1,
      );
      expire();
      await expect(syncing).rejects.toThrow("resource call sync timed out");
    });

    it("ignores a synced response for the wrong target", async () => {
      const socket = setup();
      let expire: () => void = () => undefined;
      const syncing = syncResourceCall(target, {
        acquire: socket.acquire,
        syncId: () => syncId,
        setTimeout: (callback) => {
          expire = callback;
          return 1;
        },
        clearTimeout: vi.fn(),
      });
      socket.listener.onMessage?.(
        {
          type: "call.resource.synced",
          sync_id: syncId,
          target_type: "dm",
          target_id: call.target_id,
          call: null,
          observed_at: "2026-08-21T12:00:00.000000Z",
        },
        1,
      );
      expire();
      await expect(syncing).rejects.toThrow("resource call sync timed out");
    });

    it("fails deterministically when send fails, the socket fails, or its deadline fires", async () => {
      const cannotSend = setup();
      cannotSend.handle.send = vi.fn(() => false);
      const sendFailure = syncResourceCall(target, {
        acquire: cannotSend.acquire,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      cannotSend.listener.onOpen?.(1);
      await expect(sendFailure).rejects.toThrow("resource call sync failed");
      expect(cannotSend.handle.release).toHaveBeenCalledOnce();

      const failed = setup();
      const socketFailure = syncResourceCall(target, {
        acquire: failed.acquire,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      failed.listener.onStatus?.("failed");
      await expect(socketFailure).rejects.toThrow("resource call sync failed");
      expect(failed.handle.release).toHaveBeenCalledOnce();
    });
  });

  describe("resolveCall (authenticated call.sync by call_id, correlated by sync_id — issue #614 blocker follow-up)", () => {
    const syncId = "00000000-0000-4000-8000-000000000603";

    it("sends call.sync with call_id and its own sync_id", () => {
      const socket = setup();
      void resolveCall(call.call_id, {
        acquire: socket.acquire,
        syncId: () => syncId,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      socket.listener.onOpen?.(1);
      expect(socket.handle.send).toHaveBeenCalledWith({
        type: "call.sync",
        call_id: call.call_id,
        sync_id: syncId,
      });
    });

    it("resolves solely on call.synced correlated by sync_id", async () => {
      const socket = setup();
      const resolving = resolveCall(call.call_id, {
        acquire: socket.acquire,
        syncId: () => syncId,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });

      socket.listener.onOpen?.(1);
      socket.listener.onMessage?.({ type: "call.synced", sync_id: syncId, call }, 1);

      await expect(resolving).resolves.toEqual(call);
      expect(socket.handle.release).toHaveBeenCalledOnce();
    });

    it("ignores a call.synced response with the wrong sync_id or a foreign call_id, and every lifecycle broadcast", async () => {
      const socket = setup();
      let expire: () => void = () => undefined;
      const resolving = resolveCall(call.call_id, {
        acquire: socket.acquire,
        syncId: () => syncId,
        setTimeout: (callback) => {
          expire = callback;
          return 1;
        },
        clearTimeout: vi.fn(),
      });
      // A stale/foreign call.synced (wrong sync_id — e.g. an abandoned
      // earlier attempt's own reply) must never resolve this one.
      socket.listener.onMessage?.({ type: "call.synced", sync_id: "wrong-sync-id", call }, 1);
      // A malformed/foreign call.synced with the RIGHT sync_id but the wrong
      // call payload must not resolve either.
      socket.listener.onMessage?.(
        { type: "call.synced", sync_id: syncId, call: { ...call, call_id: "some-other-call" } },
        1,
      );
      // A real lifecycle broadcast for the exact same call_id — the shape a
      // legacy (sync_id-less) call.sync reply used to be indistinguishable
      // from — must never be mistaken for this correlated reply either.
      socket.listener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "broadcast",
          target_type: "channel",
          target_id: call.target_id,
          call,
        },
        1,
      );
      expire();
      await expect(resolving).rejects.toThrow("call sync timed out");
    });

    // The #614 blocker's exact race: useCallSignaling's own reconnect
    // call.sync (A, no sync_id — see useCallSignaling.ts's onOpen) races a
    // later, independent recovery resolveCall (B) for the SAME call_id
    // sharing the one underlying socket. The old resolveCall correlated
    // solely by call_id, so A's stale "active" reply — a real answer to A,
    // just observed before the call actually ended — could be mistaken for
    // B's own answer. With sync_id correlation, B's own attempt can only
    // ever be satisfied by ITS OWN sync_id, so A's reply (whatever shape it
    // takes) can never settle B; B settles only once ITS OWN terminal
    // call.synced reply arrives.
    it("two concurrent direct call.syncs sharing one socket never cross-resolve — a stale reply for A must never settle B", async () => {
      const listeners: ChatSocketListener[] = [];
      const handle: ChatSocketHandle = {
        send: vi.fn(() => true),
        isOpen: vi.fn(() => true),
        generation: vi.fn(() => 1),
        release: vi.fn(),
      };
      const acquire = vi.fn((value: ChatSocketListener) => {
        listeners.push(value);
        return handle;
      });
      const broadcast = (data: Record<string, unknown>) => {
        for (const listener of listeners) listener.onMessage?.(data, 1);
      };

      const resolvingB = resolveCall(call.call_id, { acquire, syncId: () => "sync-b" });
      let settledB: "pending" | "settled" = "pending";
      void resolvingB.then(
        () => (settledB = "settled"),
        () => (settledB = "settled"),
      );

      // A's own reply (unrelated sync_id) arrives on the shared socket while
      // B is still in flight — every consumer attached to it, including B's,
      // receives this onMessage call.
      broadcast({ type: "call.synced", sync_id: "sync-a", call });
      await Promise.resolve();
      await Promise.resolve();
      expect(settledB).toBe("pending");

      // B's own terminal reply — the call ended between A's read and B's —
      // is the only thing that may settle B, and it must reflect B's own
      // observation, never get short-circuited by A's already-delivered one.
      const endedCall = { ...call, status: "ended" as const, version: call.version + 1 };
      broadcast({ type: "call.synced", sync_id: "sync-b", call: endedCall });
      await expect(resolvingB).resolves.toEqual(endedCall);
    });

    it("rejects with the correlated call.error, ignoring one for an unrelated sync_id", async () => {
      const socket = setup();
      const resolving = resolveCall(call.call_id, {
        acquire: socket.acquire,
        syncId: () => syncId,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      socket.listener.onMessage?.(
        { type: "call.error", operation: "call.sync", response_to: "other-sync" },
        1,
      );
      socket.listener.onMessage?.(
        { type: "call.error", operation: "call.sync", response_to: syncId },
        1,
      );
      await expect(resolving).rejects.toThrow("call sync failed");
    });

    it("fails deterministically when send fails, the socket fails, or its deadline fires", async () => {
      const denied = setup();
      const deniedSync = resolveCall(call.call_id, {
        acquire: denied.acquire,
        syncId: () => syncId,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      denied.listener.onMessage?.(
        { type: "call.error", operation: "call.sync", response_to: syncId },
        1,
      );
      await expect(deniedSync).rejects.toThrow("call sync failed");

      const failed = setup();
      const failedSync = resolveCall(call.call_id, {
        acquire: failed.acquire,
        syncId: () => syncId,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      failed.listener.onStatus?.("failed");
      await expect(failedSync).rejects.toThrow("call sync failed");

      const cannotSend = setup();
      cannotSend.handle.send = vi.fn(() => false);
      const sendFailure = resolveCall(call.call_id, {
        acquire: cannotSend.acquire,
        syncId: () => syncId,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      cannotSend.listener.onOpen?.(1);
      await expect(sendFailure).rejects.toThrow("call sync failed");

      const timed = setup();
      let expire: () => void = () => undefined;
      const timeout = resolveCall(call.call_id, {
        acquire: timed.acquire,
        syncId: () => syncId,
        setTimeout: (callback) => {
          expire = callback;
          return 1;
        },
        clearTimeout: vi.fn(),
      });
      expire();
      await expect(timeout).rejects.toThrow("call sync timed out");
    });
  });

  describe("leaveResourceCall", () => {
    it("sends the fence and resolves only its requester ACK", async () => {
      const socket = setup();
      const leaving = leaveResourceCall(call.call_id, participationID, {
        acquire: socket.acquire,
        requestId: () => requestID,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });

      socket.listener.onOpen?.(1);
      expect(socket.handle.send).toHaveBeenCalledWith({
        type: "call.leave",
        request_id: requestID,
        call_id: call.call_id,
        participation_id: participationID,
      });
      socket.listener.onMessage?.(
        {
          type: "call.left",
          operation: "call.leave",
          response_to: requestID,
          released: true,
          call: { ...call, status: "ended" },
        },
        1,
      );

      await expect(leaving).resolves.toEqual({
        released: true,
        call: { ...call, status: "ended" },
      });
      expect(socket.handle.release).toHaveBeenCalledOnce();
    });

    it("fails closed when call.leave is refused by the server", async () => {
      const socket = setup();
      const leaving = leaveResourceCall(call.call_id, participationID, {
        acquire: socket.acquire,
        requestId: () => requestID,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });

      socket.listener.onMessage?.(
        {
          type: "call.error",
          operation: "call.leave",
          response_to: requestID,
          code: "call_not_found",
        },
        1,
      );

      await expect(leaving).rejects.toMatchObject({
        operation: "call.leave",
        code: "call_not_found",
      });
      expect(socket.handle.release).toHaveBeenCalledOnce();
    });

    it("returns released=false for a stale fence and ignores unrelated errors", async () => {
      const socket = setup();
      const leaving = leaveResourceCall(call.call_id, participationID, {
        acquire: socket.acquire,
        requestId: () => requestID,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      socket.listener.onMessage?.(
        {
          type: "call.error",
          operation: "call.leave",
          response_to: "other",
          code: "call_participation_stale",
        },
        1,
      );
      socket.listener.onMessage?.(
        {
          type: "call.error",
          operation: "call.leave",
          response_to: requestID,
          code: "call_participation_stale",
        },
        1,
      );
      await expect(leaving).resolves.toEqual({ released: false });
    });

    it("fails deterministically when call.leave cannot send, the socket fails, or its deadline fires", async () => {
      const cannotSend = setup();
      cannotSend.handle.send = vi.fn(() => false);
      const sendFailure = leaveResourceCall(call.call_id, participationID, {
        acquire: cannotSend.acquire,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      cannotSend.listener.onOpen?.(1);
      await expect(sendFailure).rejects.toThrow("call leave failed");

      const failed = setup();
      const socketFailure = leaveResourceCall(call.call_id, participationID, {
        acquire: failed.acquire,
        setTimeout: vi.fn(() => 1),
        clearTimeout: vi.fn(),
      });
      failed.listener.onStatus?.("failed");
      await expect(socketFailure).rejects.toThrow("call leave failed");

      const timed = setup();
      let expire: () => void = () => undefined;
      const timeout = leaveResourceCall(call.call_id, participationID, {
        acquire: timed.acquire,
        setTimeout: (callback) => {
          expire = callback;
          return 1;
        },
        clearTimeout: vi.fn(),
      });
      expire();
      await expect(timeout).rejects.toThrow("call leave timed out");
    });
  });
});
