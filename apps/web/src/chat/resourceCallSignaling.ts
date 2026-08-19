import { acquireChatSocket, type ChatSocketHandle, type ChatSocketListener } from "./chatSocket";
import { parseCallEvent, type Call } from "./callState";
import type { ResourceCallKind } from "./callApi";
import { randomId } from "../lib/randomId";

interface ResourceCallStartTarget {
  kind: ResourceCallKind;
  id: string;
}

interface ResourceCallSignalingOptions {
  acquire?: typeof acquireChatSocket;
  requestId?: () => string;
  setTimeout?: (callback: () => void, delay: number) => unknown;
  clearTimeout?: (handle: unknown) => void;
  timeoutMs?: number;
}

export function startResourceCall(
  target: ResourceCallStartTarget,
  options: ResourceCallSignalingOptions = {},
): Promise<Call> {
  const acquire = options.acquire ?? acquireChatSocket;
  const requestID = (options.requestId ?? (() => randomId()))();
  const schedule =
    options.setTimeout ??
    ((callback: () => void, delay: number) => globalThis.setTimeout(callback, delay));
  const cancel =
    options.clearTimeout ??
    ((handle: unknown) =>
      globalThis.clearTimeout(handle as ReturnType<typeof globalThis.setTimeout>));

  return new Promise<Call>((resolve, reject) => {
    let handle: ChatSocketHandle | null = null;
    // Assigned after acquire() because test/socket adapters may fail synchronously.
    // eslint-disable-next-line prefer-const
    let timer: unknown;
    let settled = false;
    const finish = (result: Call | Error) => {
      if (settled) return;
      settled = true;
      cancel(timer);
      handle?.release();
      if (result instanceof Error) reject(result);
      else resolve(result);
    };
    const listener: ChatSocketListener = {
      onOpen() {
        if (
          !handle?.send({
            type: "call.start",
            request_id: requestID,
            target_type: target.kind,
            target_id: target.id,
            call_type: "audio",
          })
        ) {
          finish(new Error("resource call start failed"));
        }
      },
      onMessage(data) {
        if (data["type"] === "call.error" && data["operation"] === "call.start") {
          finish(new Error("resource call start failed"));
          return;
        }
        const event = parseCallEvent(data);
        if (
          event?.call.request_id === requestID &&
          event.call.status === "active" &&
          event.target_type === target.kind &&
          event.target_id === target.id
        ) {
          finish(event.call);
        }
      },
      onStatus(status) {
        if (status === "failed") finish(new Error("resource call start failed"));
      },
    };
    handle = acquire(listener);
    if (settled) {
      handle.release();
      return;
    }
    timer = schedule(
      () => finish(new Error("resource call start timed out")),
      options.timeoutMs ?? 10_000,
    );
  });
}

export function resolveCall(
  callID: string,
  options: Omit<ResourceCallSignalingOptions, "requestId"> = {},
): Promise<Call> {
  const acquire = options.acquire ?? acquireChatSocket;
  const schedule =
    options.setTimeout ?? ((callback, delay) => globalThis.setTimeout(callback, delay));
  const cancel = options.clearTimeout ?? ((handle) => globalThis.clearTimeout(handle as number));

  return new Promise<Call>((resolve, reject) => {
    let handle: ChatSocketHandle | null = null;
    // Assigned after acquire() because test/socket adapters may fail synchronously.
    // eslint-disable-next-line prefer-const
    let timer: unknown;
    let settled = false;
    const finish = (result: Call | Error) => {
      if (settled) return;
      settled = true;
      cancel(timer);
      handle?.release();
      if (result instanceof Error) reject(result);
      else resolve(result);
    };
    handle = acquire({
      onOpen() {
        if (!handle?.send({ type: "call.sync", call_id: callID })) {
          finish(new Error("call sync failed"));
        }
      },
      onMessage(data) {
        if (data["type"] === "call.error" && data["operation"] === "call.sync") {
          finish(new Error("call sync failed"));
          return;
        }
        const event = parseCallEvent(data);
        if (event?.call.call_id === callID) finish(event.call);
      },
      onStatus(status) {
        if (status === "failed") finish(new Error("call sync failed"));
      },
    });
    if (settled) {
      handle.release();
      return;
    }
    timer = schedule(() => finish(new Error("call sync timed out")), options.timeoutMs ?? 10_000);
  });
}
