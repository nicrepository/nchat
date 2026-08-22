import { acquireChatSocket, type ChatSocketHandle, type ChatSocketListener } from "./chatSocket";
import { parseCall, parseCallEvent, type Call } from "./callState";
import type { ResourceCallKind } from "./callApi";
import { randomId } from "../lib/randomId";

export interface ResourceCallTargetRef {
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

export type ResourceCallSignalingOperation =
  | "call.start"
  | "call.join"
  | "call.leave"
  | "call.resource.sync";

export class ResourceCallSignalingError extends Error {
  readonly operation: ResourceCallSignalingOperation;
  readonly code: string;

  constructor(operation: ResourceCallSignalingOperation, code: string) {
    super("resource call signaling failed");
    this.name = "ResourceCallSignalingError";
    this.operation = operation;
    this.code = code;
  }
}

/**
 * Shared request/response plumbing for every command in this module: acquire
 * the shared socket, send exactly one payload once it is open (resending
 * nothing — the shared socket itself owns reconnect), settle on whichever of
 * onMessage/onStatus/timeout fires first, and always release the handle and
 * cancel the timer exactly once. What differs per command lives entirely in
 * `onMessage` and `sendFailureMessage`/`timeoutMessage`.
 */
function runSocketRequest<T>(
  options: ResourceCallSignalingOptions,
  send: (handle: ChatSocketHandle) => boolean,
  onMessage: (data: Record<string, unknown>, finish: (result: T | Error) => void) => void,
  sendFailureMessage: string,
  timeoutMessage: string,
): Promise<T> {
  const acquire = options.acquire ?? acquireChatSocket;
  const schedule =
    options.setTimeout ??
    ((callback: () => void, delay: number) => globalThis.setTimeout(callback, delay));
  const cancel =
    options.clearTimeout ??
    ((handle: unknown) =>
      globalThis.clearTimeout(handle as ReturnType<typeof globalThis.setTimeout>));

  return new Promise<T>((resolve, reject) => {
    let handle: ChatSocketHandle | null = null;
    // Assigned after acquire() because test/socket adapters may fail synchronously.
    // eslint-disable-next-line prefer-const
    let timer: unknown;
    let settled = false;
    const finish = (result: T | Error) => {
      if (settled) return;
      settled = true;
      cancel(timer);
      handle?.release();
      if (result instanceof Error) reject(result);
      else resolve(result);
    };
    const listener: ChatSocketListener = {
      onOpen() {
        if (!handle || !send(handle)) finish(new Error(sendFailureMessage));
      },
      onMessage(data) {
        onMessage(data, finish);
      },
      onStatus(status) {
        if (status === "failed") finish(new Error(sendFailureMessage));
      },
    };
    handle = acquire(listener);
    if (settled) {
      handle.release();
      return;
    }
    timer = schedule(() => finish(new Error(timeoutMessage)), options.timeoutMs ?? 10_000);
  });
}

function validateAdmittedCall(
  call: Call,
  target: ResourceCallTargetRef,
  expectedCallId?: string,
): boolean {
  return (
    call.status === "active" &&
    call.target_type === target.kind &&
    call.target_id === target.id &&
    (expectedCallId === undefined || call.call_id === expectedCallId)
  );
}

const canonicalUUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

export interface ResourceCallAdmission {
  call: Call;
  participationId: string;
}

function parseAdmission(
  data: Record<string, unknown>,
  target: ResourceCallTargetRef,
  expectedCallId?: string,
): ResourceCallAdmission | null {
  const call = parseCall(data["call"]);
  const participationId = data["participation_id"];
  return call &&
    validateAdmittedCall(call, target, expectedCallId) &&
    typeof participationId === "string" &&
    canonicalUUID.test(participationId)
    ? { call, participationId }
    : null;
}

/**
 * Starts (or, canonically, reuses the already-active) resource call for a
 * channel or group DM — issue #622 round 1's call.start contract.
 *
 * Resolves SOLELY on this requester's own call.admitted, correlated by
 * response_to === this command's request_id — never by matching
 * admitted.call.request_id, which is the call's own historical request_id
 * (the original starter's, persisted forever on the row) and is NOT
 * rewritten when a later caller reuses the same active call. The
 * call.accepted lifecycle broadcast every subscriber of the target receives
 * is a completely separate, unrelated message and is ignored here — it is
 * never this command's ACK.
 */
export function startResourceCall(
  target: ResourceCallTargetRef,
  options: ResourceCallSignalingOptions = {},
): Promise<ResourceCallAdmission> {
  const requestID = (options.requestId ?? (() => randomId()))();
  return runSocketRequest<ResourceCallAdmission>(
    options,
    (handle) =>
      handle.send({
        type: "call.start",
        request_id: requestID,
        target_type: target.kind,
        target_id: target.id,
        call_type: "audio",
      }),
    (data, finish) => {
      if (data["type"] === "call.error") {
        if (data["operation"] !== "call.start" || data["response_to"] !== requestID) return;
        finish(
          new ResourceCallSignalingError(
            "call.start",
            typeof data["code"] === "string" ? data["code"] : "call_error",
          ),
        );
        return;
      }
      if (data["type"] !== "call.admitted") return;
      if (data["operation"] !== "call.start" || data["response_to"] !== requestID) return;
      const admission = parseAdmission(data, target);
      if (admission) finish(admission);
    },
    "resource call start failed",
    "resource call start timed out",
  );
}

/**
 * Joins an already-known, active resource call — issue #622 round 2's
 * call.join contract. Never creates a call: the server rejects an unknown or
 * no-longer-active call_id, and this never falls back to call.start.
 *
 * Resolves SOLELY on this requester's own call.admitted, correlated the same
 * way as startResourceCall — by response_to, never by admitted.call.request_id
 * (which stays the call's original historical request_id when this join
 * reuses an existing call started by someone else) — and additionally
 * validated against the call_id actually requested, so a mismatched
 * admission (which the server itself should never produce) is never
 * mistaken for success.
 */
export function joinResourceCall(
  callId: string,
  target: ResourceCallTargetRef,
  options: ResourceCallSignalingOptions = {},
): Promise<ResourceCallAdmission> {
  const requestID = (options.requestId ?? (() => randomId()))();
  return runSocketRequest<ResourceCallAdmission>(
    options,
    (handle) =>
      handle.send({
        type: "call.join",
        request_id: requestID,
        call_id: callId,
        target_type: target.kind,
        target_id: target.id,
      }),
    (data, finish) => {
      if (data["type"] === "call.error") {
        if (data["operation"] !== "call.join" || data["response_to"] !== requestID) return;
        finish(
          new ResourceCallSignalingError(
            "call.join",
            typeof data["code"] === "string" ? data["code"] : "call_error",
          ),
        );
        return;
      }
      if (data["type"] !== "call.admitted") return;
      if (data["operation"] !== "call.join" || data["response_to"] !== requestID) return;
      const admission = parseAdmission(data, target, callId);
      if (admission) finish(admission);
    },
    "resource call join failed",
    "resource call join timed out",
  );
}

export interface ResourceCallSyncResult {
  call: Call | null;
  /** Server-derived snapshot instant — never the browser clock. See resourceCallDiscovery.ts. */
  observedAt: string;
}

/**
 * Discovers the authoritative active call (if any) of one channel/group-DM
 * target — issue #622 round 1's call.resource.sync contract. Never creates a
 * lease, issues a token, or otherwise participates: this is read-only
 * discovery, safe to call for a target the caller merely observes.
 *
 * Resolves on this requester's own call.resource.synced, correlated by
 * sync_id AND by target_type/target_id (the server never broadcasts this —
 * it is always a direct, requester-only reply, but the extra check costs
 * nothing and guards against a malformed/foreign payload all the same).
 * call: null is a valid, expected result — not an error — for both "no
 * active call" and "not authorized", which the server deliberately keeps
 * indistinguishable on the wire.
 */
export function syncResourceCall(
  target: ResourceCallTargetRef,
  options: ResourceCallSignalingOptions & { syncId?: () => string } = {},
): Promise<ResourceCallSyncResult> {
  const syncID = (options.syncId ?? options.requestId ?? (() => randomId()))();
  return runSocketRequest<ResourceCallSyncResult>(
    options,
    (handle) =>
      handle.send({
        type: "call.resource.sync",
        sync_id: syncID,
        target_type: target.kind,
        target_id: target.id,
      }),
    (data, finish) => {
      if (data["type"] !== "call.resource.synced") return;
      if (
        data["sync_id"] !== syncID ||
        data["target_type"] !== target.kind ||
        data["target_id"] !== target.id ||
        typeof data["observed_at"] !== "string" ||
        data["observed_at"] === ""
      ) {
        return;
      }
      if (data["call"] === null) {
        finish({ call: null, observedAt: data["observed_at"] });
        return;
      }
      const call = parseCall(data["call"]);
      if (call && call.target_type === target.kind && call.target_id === target.id) {
        finish({ call, observedAt: data["observed_at"] });
      }
    },
    "resource call sync failed",
    "resource call sync timed out",
  );
}

// leaveResourceCall releases this participant's own presence in a resource
// (channel/group-DM) call immediately, without ending it for anyone else
// (issue #569). It resolves with the authoritative post-leave call — still
// "active" if other participants remain, "ended" if this was the last one —
// so a caller can safely start a new call the instant this promise settles,
// with no lease-expiry timeout to wait out.
export type ResourceCallLeaveResult = { released: true; call: Call } | { released: false };

export function leaveResourceCall(
  callID: string,
  participationID: string,
  options: ResourceCallSignalingOptions = {},
): Promise<ResourceCallLeaveResult> {
  const requestID = (options.requestId ?? (() => randomId()))();
  return runSocketRequest<ResourceCallLeaveResult>(
    options,
    (handle) =>
      handle.send({
        type: "call.leave",
        request_id: requestID,
        call_id: callID,
        participation_id: participationID,
      }),
    (data, finish) => {
      if (
        data["type"] === "call.error" &&
        data["operation"] === "call.leave" &&
        data["response_to"] === requestID
      ) {
        const code = typeof data["code"] === "string" ? data["code"] : "call_error";
        if (code === "call_participation_stale") finish({ released: false });
        else finish(new ResourceCallSignalingError("call.leave", code));
        return;
      }
      if (
        data["type"] !== "call.left" ||
        data["operation"] !== "call.leave" ||
        data["response_to"] !== requestID ||
        data["released"] !== true
      ) {
        return;
      }
      const call = parseCall(data["call"]);
      if (call?.call_id === callID) finish({ released: true, call });
    },
    "call leave failed",
    "call leave timed out",
  );
}

export function resolveCall(
  callID: string,
  options: Omit<ResourceCallSignalingOptions, "requestId"> = {},
): Promise<Call> {
  return runSocketRequest<Call>(
    options,
    (handle) => handle.send({ type: "call.sync", call_id: callID }),
    (data, finish) => {
      if (data["type"] === "call.error" && data["operation"] === "call.sync") {
        finish(new Error("call sync failed"));
        return;
      }
      const event = parseCallEvent(data);
      if (event?.call.call_id === callID) finish(event.call);
    },
    "call sync failed",
    "call sync timed out",
  );
}
